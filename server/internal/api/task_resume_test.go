package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/packrun/packrun/internal/model"
)

func taskActionPath(id uint, action string) string {
	return fmt.Sprintf("/api/v1/tasks/%d/%s", id, action)
}

// 等待任务进入终态（stub 执行器同步完成，轮询兜底）。
func waitTaskTerminal(t *testing.T, db *gorm.DB, id uint) model.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var task model.Task
		if err := db.First(&task, id).Error; err == nil {
			switch task.Status {
			case model.TaskSuccess, model.TaskFailed, model.TaskCanceled:
				return task
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("任务未在期限内进入终态")
	return model.Task{}
}

// 重启中断的任务可人工恢复执行：状态回到终态且写入 task.resume 审计。
func TestResumeInterruptedTask(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	alice := mustCreateRoleUser(t, db, "alice", model.RoleDeployer, "*", "active")
	aliceToken := mustToken(t, app, alice)
	srv := mustCreateServer(t, db, "s-resume", model.EnvTest)
	dir := model.Directory{ServerID: srv.ID, Path: "/opt/a", AllowOverwrite: true}
	if err := db.Create(&dir).Error; err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	pkg := model.Package{Name: "app.zip", Kind: "zip", Size: 10, SHA256: "deadbeef", StorageKey: "unused"}
	if err := db.Create(&pkg).Error; err != nil {
		t.Fatalf("创建安装包失败: %v", err)
	}

	task := model.Task{
		TaskNo: "T-RESUME-0001", ServerID: srv.ID, ServerName: srv.Name,
		DirectoryID: dir.ID, TargetPath: dir.Path, PackageID: pkg.ID, PackageName: pkg.Name,
		Status: model.TaskInterrupted, Stage: "unpack", OperatorID: alice.ID, Operator: alice.Username,
		OTPVerified: true,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	rec, resp := doJSON(t, router, http.MethodPost, taskActionPath(task.ID, "resume"), nil, aliceToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("恢复中断任务应成功，实际 %d / %s", rec.Code, resp.Message)
	}

	done := waitTaskTerminal(t, db, task.ID)
	if done.Status != model.TaskSuccess {
		t.Fatalf("恢复后任务应执行成功（stub 执行器），实际 %s：%s", done.Status, done.Message)
	}

	var audits int64
	db.Model(&model.AuditLog{}).Where("action = ?", "task.resume").Count(&audits)
	if audits != 1 {
		t.Fatalf("应写入 1 条 task.resume 审计，实际 %d", audits)
	}
}

// 中断任务可人工标记失败收口：终态、审计与项目批次结算同 finishTask 口径。
func TestMarkFailedInterruptedTask(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	alice := mustCreateRoleUser(t, db, "alice", model.RoleDeployer, "*", "active")
	aliceToken := mustToken(t, app, alice)
	srv := mustCreateServer(t, db, "s-mark", model.EnvTest)

	task := model.Task{
		TaskNo: "T-MARK-0001", ServerID: srv.ID, ServerName: srv.Name, TargetPath: "/opt/b",
		Status: model.TaskInterrupted, OperatorID: alice.ID, Operator: alice.Username,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	rec, resp := doJSON(t, router, http.MethodPost, taskActionPath(task.ID, "mark-failed"), nil, aliceToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("标记失败应成功，实际 %d / %s", rec.Code, resp.Message)
	}
	var got model.Task
	db.First(&got, task.ID)
	if got.Status != model.TaskFailed {
		t.Fatalf("任务应变为 failed，实际 %s", got.Status)
	}
	if got.FinishedAt == nil {
		t.Fatal("标记失败应写 finished_at")
	}

	var audits int64
	db.Model(&model.AuditLog{}).Where("action = ?", "task.markfailed").Count(&audits)
	if audits != 1 {
		t.Fatalf("应写入 1 条 task.markfailed 审计，实际 %d", audits)
	}
}

// 非中断状态的任务不可恢复/标记失败；审计员不可处置；越权范围拒绝。
func TestResumeTaskGuardrails(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	adminToken := mustToken(t, app, admin)
	alice := mustCreateRoleUser(t, db, "alice", model.RoleDeployer, "*", "active")
	aliceToken := mustToken(t, app, alice)
	auditor := mustCreateRoleUser(t, db, "aud", model.RoleAuditor, "*", "active")
	auditorToken := mustToken(t, app, auditor)
	bob := mustCreateRoleUser(t, db, "bob", model.RoleDeployer, "999", "active")
	bobToken := mustToken(t, app, bob)

	srv := mustCreateServer(t, db, "s-guard", model.EnvTest)
	success := model.Task{TaskNo: "T-GUARD-OK", ServerID: srv.ID, Status: model.TaskSuccess}
	interrupted := model.Task{
		TaskNo: "T-GUARD-INT", ServerID: srv.ID, Status: model.TaskInterrupted,
		OperatorID: alice.ID, Operator: alice.Username,
	}
	if err := db.Create(&success).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := db.Create(&interrupted).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 非中断任务不可恢复
	if rec, _ := doJSON(t, router, http.MethodPost, taskActionPath(success.ID, "resume"), nil, adminToken); rec.Code != http.StatusBadRequest {
		t.Fatalf("恢复非中断任务应 400，实际 %d", rec.Code)
	}
	// 审计员不可处置
	if rec, _ := doJSON(t, router, http.MethodPost, taskActionPath(interrupted.ID, "resume"), nil, auditorToken); rec.Code != http.StatusForbidden {
		t.Fatalf("审计员恢复任务应 403，实际 %d", rec.Code)
	}
	if rec, _ := doJSON(t, router, http.MethodPost, taskActionPath(interrupted.ID, "mark-failed"), nil, auditorToken); rec.Code != http.StatusForbidden {
		t.Fatalf("审计员标记失败应 403，实际 %d", rec.Code)
	}
	// 服务器范围外不可处置
	if rec, _ := doJSON(t, router, http.MethodPost, taskActionPath(interrupted.ID, "resume"), nil, bobToken); rec.Code != http.StatusForbidden {
		t.Fatalf("范围外恢复任务应 403，实际 %d", rec.Code)
	}
	// 范围内的发布者可以标记失败
	if rec, _ := doJSON(t, router, http.MethodPost, taskActionPath(interrupted.ID, "mark-failed"), nil, aliceToken); rec.Code != http.StatusOK {
		t.Fatalf("范围内标记失败应 200，实际 %d", rec.Code)
	}
	// 中断任务可直接移除（视为放弃恢复）
	if rec, _ := doJSON(t, router, http.MethodDelete, fmt.Sprintf("/api/v1/tasks/%d", interrupted.ID), nil, aliceToken); rec.Code != http.StatusOK {
		t.Fatalf("移除中断任务应 200，实际 %d", rec.Code)
	}
}
