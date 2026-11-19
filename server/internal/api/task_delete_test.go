package api

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"github.com/packrun/packrun/internal/model"
)

var taskSeq int64

// mustCreateTask 直接落库一条任务记录，按需附带一条步骤。
func mustCreateTask(t *testing.T, db *gorm.DB, serverID uint, operator *model.User, status string) *model.Task {
	t.Helper()
	task := model.Task{
		TaskNo:     fmt.Sprintf("T20261119%08d", atomic.AddInt64(&taskSeq, 1)),
		ServerID:   serverID,
		PackageID:  1,
		OperatorID: operator.ID,
		Operator:   operator.Username,
		Status:     status,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	step := model.TaskStep{TaskID: task.ID, Seq: 1, Name: "上传", Status: "success"}
	if err := db.Create(&step).Error; err != nil {
		t.Fatalf("创建任务步骤失败: %v", err)
	}
	return &task
}

func taskDeletePath(id uint) string {
	return fmt.Sprintf("/api/v1/tasks/%d", id)
}

func TestDeleteFinishedTask(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	adminToken := mustToken(t, app, admin)
	alice := mustCreateRoleUser(t, db, "alice", model.RoleDeployer, "*", "active")
	aliceToken := mustToken(t, app, alice)

	srv := mustCreateServer(t, db, "s1", "test")
	task := mustCreateTask(t, db, srv.ID, alice, model.TaskSuccess)

	// 发布者移除自己已结束的任务：连步骤一起删除
	if rec, resp := doJSON(t, router, http.MethodDelete, taskDeletePath(task.ID), nil, aliceToken); rec.Code != http.StatusOK {
		t.Fatalf("移除已结束任务失败: %d / %s", rec.Code, resp.Message)
	}
	var steps int64
	db.Model(&model.TaskStep{}).Where("task_id = ?", task.ID).Count(&steps)
	if steps != 0 {
		t.Fatalf("任务步骤应一并删除，剩余 %d", steps)
	}
	var tasks int64
	db.Model(&model.Task{}).Where("id = ?", task.ID).Count(&tasks)
	if tasks != 0 {
		t.Fatalf("任务记录应已删除，剩余 %d", tasks)
	}

	// 管理员可移除他人的已结束任务
	other := mustCreateTask(t, db, srv.ID, alice, model.TaskFailed)
	if rec, _ := doJSON(t, router, http.MethodDelete, taskDeletePath(other.ID), nil, adminToken); rec.Code != http.StatusOK {
		t.Fatalf("管理员移除任务失败: %d", rec.Code)
	}
}

func TestDeleteTaskGuardrails(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	adminToken := mustToken(t, app, admin)
	alice := mustCreateRoleUser(t, db, "alice", model.RoleDeployer, "*", "active")
	bob := mustCreateRoleUser(t, db, "bob", model.RoleDeployer, "*", "active")
	auditor := mustCreateRoleUser(t, db, "aud", model.RoleAuditor, "*", "active")
	bobToken := mustToken(t, app, bob)
	auditorToken := mustToken(t, app, auditor)

	srv := mustCreateServer(t, db, "s1", "test")

	// 进行中的任务不可移除
	running := mustCreateTask(t, db, srv.ID, alice, model.TaskExecuting)
	if rec, resp := doJSON(t, router, http.MethodDelete, taskDeletePath(running.ID), nil, adminToken); rec.Code != http.StatusBadRequest {
		t.Fatalf("进行中任务应拒绝移除: %d / %s", rec.Code, resp.Message)
	}

	// 发布者不能移除他人发起的任务
	finished := mustCreateTask(t, db, srv.ID, alice, model.TaskCanceled)
	if rec, _ := doJSON(t, router, http.MethodDelete, taskDeletePath(finished.ID), nil, bobToken); rec.Code != http.StatusForbidden {
		t.Fatalf("移除他人任务应 403: %d", rec.Code)
	}

	// 审计角色不可移除任务
	if rec, _ := doJSON(t, router, http.MethodDelete, taskDeletePath(finished.ID), nil, auditorToken); rec.Code != http.StatusForbidden {
		t.Fatalf("审计角色移除任务应 403: %d", rec.Code)
	}

	// 任务删除写入审计日志（上面进行中/越权失败的不算，成功删除一条）
	var audits int64
	db.Model(&model.AuditLog{}).Where("action = ?", "task.delete").Count(&audits)
	if audits != 0 {
		t.Fatalf("失败请求不应写 task.delete 审计，实际 %d 条", audits)
	}

	// 管理员成功删除后写入审计
	if rec, _ := doJSON(t, router, http.MethodDelete, taskDeletePath(finished.ID), nil, adminToken); rec.Code != http.StatusOK {
		t.Fatalf("管理员移除任务失败: %d", rec.Code)
	}
	db.Model(&model.AuditLog{}).Where("action = ?", "task.delete").Count(&audits)
	if audits != 1 {
		t.Fatalf("任务删除应写入审计日志，实际 %d 条", audits)
	}
}
