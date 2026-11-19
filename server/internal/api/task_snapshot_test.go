package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/packrun/packrun/internal/model"
)

// mustCreateScript 直接落库一个脚本记录。
func mustCreateScript(t *testing.T, db *gorm.DB, name, phase, content string) *model.Script {
	t.Helper()
	s := model.Script{Name: name, Phase: phase, Content: content, TimeoutSec: 60, Version: 1}
	if err := db.Create(&s).Error; err != nil {
		t.Fatalf("创建脚本失败: %v", err)
	}
	return &s
}

// mustCreateTaskWithScripts 创建任务并按生产路径固化脚本快照。
func mustCreateTaskWithScripts(t *testing.T, app *App, db *gorm.DB, pre, post *model.Script) *model.Task {
	t.Helper()
	task := model.Task{
		TaskNo: "T-SNAPSHOT-0001", ServerName: "srv-a", TargetPath: "/opt/a",
		Status: model.TaskPending, Stage: "queued",
	}
	if pre != nil {
		id := pre.ID
		task.PreScriptID = &id
		task.PreScriptName = pre.Name
		task.PreScriptVersion = pre.Version
	}
	if post != nil {
		id := post.ID
		task.ScriptID = &id
		task.ScriptName = post.Name
		task.ScriptVersion = post.Version
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	app.snapshotTaskScripts(&task, pre, post)
	return &task
}

// 脚本快照（收口手册 §21.7）：受理后编辑或删除脚本，任务执行与详情展示仍用受理时内容。
func TestTaskScriptSnapshotIsolatedFromEdits(t *testing.T) {
	app, _, db := newSecurityTestEnv(t)
	pre := mustCreateScript(t, db, "校验包结构", model.PhasePreCheck, "echo OLD_PRE")
	post := mustCreateScript(t, db, "重启应用", model.PhasePostDeploy, "echo OLD_POST")
	task := mustCreateTaskWithScripts(t, app, db, pre, post)

	// 受理后编辑后置脚本内容并递增版本，同时删除前置脚本
	db.Model(post).Updates(map[string]any{"content": "echo NEW_POST", "version": post.Version + 1})
	db.Delete(pre)

	gotPre, gotPost, reason := app.loadTaskScripts(task)
	if reason != "" {
		t.Fatalf("快照存在时不应回退失败，reason = %q", reason)
	}
	if gotPre == nil || gotPre.Content != "echo OLD_PRE" {
		t.Fatalf("前置脚本应使用受理时快照，实际 %+v", gotPre)
	}
	if gotPost == nil || gotPost.Content != "echo OLD_POST" || gotPost.Version != 1 {
		t.Fatalf("后置脚本应使用受理时快照（v1 旧内容），实际 %+v", gotPost)
	}
}

// 存量任务（快照功能上线前受理）保持按 ID 现读的回退行为。
func TestTaskScriptLegacyFallback(t *testing.T) {
	app, _, db := newSecurityTestEnv(t)
	post := mustCreateScript(t, db, "重启应用", model.PhasePostDeploy, "echo LIVE")
	id := post.ID
	task := model.Task{TaskNo: "T-LEGACY-0001", ScriptID: &id, Status: model.TaskPending}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 注意返回值顺序：第一返回值是前置脚本，第二是后置脚本；本任务只绑定了后置脚本。
	_, script, reason := app.loadTaskScripts(&task)
	if reason != "" {
		t.Fatalf("存量回退不应失败，reason = %q", reason)
	}
	if script == nil || script.Content != "echo LIVE" {
		t.Fatalf("存量任务应现读脚本内容，实际 %+v", script)
	}

	// 脚本被删除时给出失败原因
	db.Delete(post)
	_, _, reason = app.loadTaskScripts(&task)
	if reason != "部署后脚本已被删除" {
		t.Fatalf("脚本被删除应返回失败原因，实际 %q", reason)
	}
}

// 脚本仍被未完成任务引用时，编辑接口返回提示（前端展示影响面）。
func TestUpdateScriptWarnsWhenReferencedByUnfinishedTasks(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	token := mustToken(t, app, admin)
	post := mustCreateScript(t, db, "重启应用", model.PhasePostDeploy, "echo V1")

	task := model.Task{TaskNo: "T-REF-0001", ScriptID: &post.ID, Status: model.TaskExecuting}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	payload := map[string]any{
		"name": post.Name, "phase": model.PhasePostDeploy, "content": "echo V2",
		"timeoutSec": 60,
	}
	path := fmt.Sprintf("/api/v1/scripts/%d", post.ID)
	rec, resp := doJSON(t, router, http.MethodPut, path, payload, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("更新脚本应成功，实际 %d / %s", rec.Code, resp.Message)
	}
	raw, err := json.Marshal(resp.Data)
	if err != nil {
		t.Fatalf("序列化响应失败: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "快照") || !strings.Contains(body, `"warning"`) {
		t.Fatalf("脚本仍被未完成任务引用时应返回快照提示，实际 %s", body)
	}
}
