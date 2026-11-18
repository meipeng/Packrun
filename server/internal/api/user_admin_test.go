package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/packrun/packrun/internal/model"
)

func TestUpdateUserClearsParentID(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	token := mustToken(t, app, admin)
	parent := mustCreateRoleUser(t, db, "boss", model.RoleDeployer, "*", "active")
	child := mustCreateRoleUser(t, db, "kid", model.RoleDeployer, "*", "active")

	base := map[string]any{"role": model.RoleDeployer, "serverScope": "*", "status": "active"}

	// 先建立归属关系
	rec, _ := doJSON(t, router, http.MethodPut, fmt.Sprintf("/api/v1/users/%d", child.ID), mergeMap(base, map[string]any{
		"parentId": parent.ID,
	}), token)
	if rec.Code != http.StatusOK {
		t.Fatalf("设置归属失败: %d / %s", rec.Code, rec.Body.String())
	}
	var got model.User
	if err := db.First(&got, child.ID).Error; err != nil || got.ParentID == nil || *got.ParentID != parent.ID {
		t.Fatalf("归属未写入: %+v（err=%v）", got.ParentID, err)
	}

	// 显式传 null 应真正清空，而不是被当作「未提供」
	rec, resp := doJSON(t, router, http.MethodPut, fmt.Sprintf("/api/v1/users/%d", child.ID), mergeMap(base, map[string]any{
		"parentId": nil,
	}), token)
	if rec.Code != http.StatusOK {
		t.Fatalf("清空归属失败: %d / %s", rec.Code, rec.Body.String())
	}
	if err := db.First(&got, child.ID).Error; err != nil {
		t.Fatalf("回读用户失败: %v", err)
	}
	if got.ParentID != nil {
		t.Fatalf("parentId 应被清空，实际 %v", *got.ParentID)
	}
	var body model.User
	if err := json.Unmarshal(resp.Data, &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if body.ParentID != nil {
		t.Fatalf("响应里的 parentId 应为空，实际 %v", *body.ParentID)
	}
}

func TestUpdateUserRejectsInvalidParent(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	token := mustToken(t, app, admin)
	top := mustCreateRoleUser(t, db, "top", model.RoleDeployer, "*", "active")
	mid := mustCreateRoleUser(t, db, "mid", model.RoleDeployer, "*", "active")
	leaf := mustCreateRoleUser(t, db, "leaf", model.RoleDeployer, "*", "active")

	// mid 先挂到 top 下，形成一层归属
	if err := db.Model(mid).Update("parent_id", top.ID).Error; err != nil {
		t.Fatalf("准备归属失败: %v", err)
	}

	base := map[string]any{"role": model.RoleDeployer, "serverScope": "*", "status": "active"}

	// 不能把自己设为自己的上级
	rec, resp := doJSON(t, router, http.MethodPut, fmt.Sprintf("/api/v1/users/%d", leaf.ID), mergeMap(base, map[string]any{
		"parentId": leaf.ID,
	}), token)
	if rec.Code != http.StatusBadRequest || resp.Message != "归属主账号不能是自己" {
		t.Fatalf("自引用应 400，实际 %d / %q", rec.Code, resp.Message)
	}

	// 不允许挂到子账号下（只允许一层归属）
	rec, resp = doJSON(t, router, http.MethodPut, fmt.Sprintf("/api/v1/users/%d", leaf.ID), mergeMap(base, map[string]any{
		"parentId": mid.ID,
	}), token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("链式归属应 400，实际 %d / %q", rec.Code, resp.Message)
	}

	// 父账号不存在
	rec, resp = doJSON(t, router, http.MethodPut, fmt.Sprintf("/api/v1/users/%d", leaf.ID), mergeMap(base, map[string]any{
		"parentId": 99999,
	}), token)
	if rec.Code != http.StatusBadRequest || resp.Message != "归属主账号不存在" {
		t.Fatalf("不存在的父账号应 400，实际 %d / %q", rec.Code, resp.Message)
	}
}

func TestDeleteUserSoftDeletesAndInvalidatesSession(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	token := mustToken(t, app, admin)
	victim := mustCreateRoleUser(t, db, "temp", model.RoleDeployer, "*", "active")
	victimToken := mustToken(t, app, victim)

	rec, _ := doJSON(t, router, http.MethodDelete, fmt.Sprintf("/api/v1/users/%d", victim.ID), nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除用户失败: %d / %s", rec.Code, rec.Body.String())
	}

	// 会话应立即失效（token 版本递增 + 软删除后查询不到）
	rec, _ = doJSON(t, router, http.MethodGet, "/api/v1/auth/me", nil, victimToken)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("被删账号的会话应失效，实际 %d", rec.Code)
	}

	// 列表不再出现
	var list []model.User
	if err := db.Find(&list).Error; err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	for _, u := range list {
		if u.ID == victim.ID {
			t.Fatalf("已删除用户仍出现在列表里")
		}
	}

	// 账号名释放，可重建同名账号
	rec, _ = doJSON(t, router, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "temp", "password": "Correct@123",
		"role": model.RoleDeployer, "serverScope": "*", "status": "active",
	}, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("重建同名账号失败: %d / %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteUserGuards(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	token := mustToken(t, app, admin)
	deployer := mustCreateRoleUser(t, db, "dep", model.RoleDeployer, "*", "active")

	// 不能删除当前登录账号
	rec, resp := doJSON(t, router, http.MethodDelete, fmt.Sprintf("/api/v1/users/%d", admin.ID), nil, token)
	if rec.Code != http.StatusBadRequest || resp.Message != "不能删除当前登录账号" {
		t.Fatalf("删除自己应 400，实际 %d / %q", rec.Code, resp.Message)
	}

	// 仍是项目创建者时拒绝删除
	project := model.Project{Name: "订单服务", OwnerID: deployer.ID}
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("创建项目失败: %v", err)
	}
	rec, resp = doJSON(t, router, http.MethodDelete, fmt.Sprintf("/api/v1/users/%d", deployer.ID), nil, token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("项目 Owner 应拒绝删除，实际 %d / %q", rec.Code, resp.Message)
	}

	// 还有未完成任务时拒绝删除
	if err := db.Delete(&project).Error; err != nil {
		t.Fatalf("清理项目失败: %v", err)
	}
	task := model.Task{TaskNo: "T-1", Status: model.TaskPending, OperatorID: deployer.ID}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	rec, resp = doJSON(t, router, http.MethodDelete, fmt.Sprintf("/api/v1/users/%d", deployer.ID), nil, token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("有未完成任务时应拒绝删除，实际 %d / %q", rec.Code, resp.Message)
	}

	// 任务结束后可以删除
	if err := db.Model(&task).Update("status", model.TaskSuccess).Error; err != nil {
		t.Fatalf("更新任务状态失败: %v", err)
	}
	rec, _ = doJSON(t, router, http.MethodDelete, fmt.Sprintf("/api/v1/users/%d", deployer.ID), nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("任务结束后应可删除，实际 %d / %s", rec.Code, rec.Body.String())
	}
}

func TestAdminResetsUserOTP(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	token := mustToken(t, app, admin)
	victim := mustCreateRoleUser(t, db, "lost", model.RoleDeployer, "*", "active")
	victimToken := mustToken(t, app, victim)

	if err := db.Model(victim).Updates(map[string]any{
		"otp_enabled": true, "otp_secret": "JBSWY3DPEHPK3PXP",
	}).Error; err != nil {
		t.Fatalf("准备动态口令失败: %v", err)
	}
	if err := db.Create(&model.RecoveryCode{UserID: victim.ID, CodeHash: "hash-1"}).Error; err != nil {
		t.Fatalf("准备恢复码失败: %v", err)
	}

	rec, _ := doJSON(t, router, http.MethodPost, fmt.Sprintf("/api/v1/users/%d/otp/reset", victim.ID), nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("重置动态口令失败: %d / %s", rec.Code, rec.Body.String())
	}

	var got model.User
	if err := db.First(&got, victim.ID).Error; err != nil {
		t.Fatalf("回读用户失败: %v", err)
	}
	if got.OTPEnabled || got.OTPSecret != "" || got.PendingOTPSecret != "" {
		t.Fatalf("动态口令未清空: enabled=%v secret=%q", got.OTPEnabled, got.OTPSecret)
	}
	var codes int64
	db.Model(&model.RecoveryCode{}).Where("user_id = ?", victim.ID).Count(&codes)
	if codes != 0 {
		t.Fatalf("恢复码应一并清除，实际剩 %d 条", codes)
	}

	rec, _ = doJSON(t, router, http.MethodGet, "/api/v1/auth/me", nil, victimToken)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("重置后旧会话应失效，实际 %d", rec.Code)
	}
}

// mergeMap 合并两份请求体，后者覆盖前者。
func mergeMap(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
