package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/packrun/packrun/internal/model"
)

// listPackageNames 以指定令牌拉取安装包列表并返回名称集合。
func listPackageNames(t *testing.T, r http.Handler, token string) map[string]bool {
	t.Helper()
	rec, resp := doJSON(t, r, http.MethodGet, "/api/v1/packages", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("查询包列表失败: %d / %s", rec.Code, rec.Body.String())
	}
	var list []model.Package
	if err := json.Unmarshal(resp.Data, &list); err != nil {
		t.Fatalf("解析包列表失败: %v", err)
	}
	names := make(map[string]bool, len(list))
	for _, p := range list {
		names[p.Name] = true
	}
	return names
}

func TestPackageListIsolatedByUploader(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	adminToken := mustToken(t, app, admin)
	alice := mustCreateRoleUser(t, db, "alice", model.RoleDeployer, "*", "active")
	bob := mustCreateRoleUser(t, db, "bob", model.RoleDeployer, "*", "active")
	aliceToken := mustToken(t, app, alice)
	bobToken := mustToken(t, app, bob)

	if rec, _ := doUpload(t, router, "alice.zip", makeStoredZip(t, "a.txt", 8), aliceToken); rec.Code != http.StatusOK {
		t.Fatalf("alice 上传失败: %d", rec.Code)
	}
	if rec, _ := doUpload(t, router, "bob.zip", makeStoredZip(t, "b.txt", 8), bobToken); rec.Code != http.StatusOK {
		t.Fatalf("bob 上传失败: %d", rec.Code)
	}

	aliceNames := listPackageNames(t, router, aliceToken)
	if !aliceNames["alice.zip"] || aliceNames["bob.zip"] {
		t.Fatalf("alice 只应看到自己的包，实际 %v", aliceNames)
	}
	bobNames := listPackageNames(t, router, bobToken)
	if !bobNames["bob.zip"] || bobNames["alice.zip"] {
		t.Fatalf("bob 只应看到自己的包，实际 %v", bobNames)
	}
	adminNames := listPackageNames(t, router, adminToken)
	if !adminNames["alice.zip"] || !adminNames["bob.zip"] {
		t.Fatalf("管理员应看到全部包，实际 %v", adminNames)
	}
}

func TestDeployerCannotUseOthersPackage(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	adminToken := mustToken(t, app, admin)
	alice := mustCreateRoleUser(t, db, "alice", model.RoleDeployer, "*", "active")
	bob := mustCreateRoleUser(t, db, "bob", model.RoleDeployer, "*", "active")
	aliceToken := mustToken(t, app, alice)
	bobToken := mustToken(t, app, bob)

	srv := mustCreateServer(t, db, "s1", "test")
	dir := mustCreateDirectory(t, db, srv.ID, "/opt/app")

	_, resp := doUpload(t, router, "bob.zip", makeStoredZip(t, "b.txt", 8), bobToken)
	var pkg model.Package
	if err := json.Unmarshal(resp.Data, &pkg); err != nil {
		t.Fatalf("解析包记录失败: %v", err)
	}

	rec, resp2 := doJSON(t, router, http.MethodPost, "/api/v1/tasks", map[string]any{
		"serverId": srv.ID, "directoryId": dir.ID, "packageId": pkg.ID,
	}, aliceToken)
	if rec.Code != http.StatusForbidden || resp2.Message != "只能使用自己上传的安装包" {
		t.Fatalf("使用他人安装包应 403，实际 %d / %q", rec.Code, resp2.Message)
	}

	// 自己上传的包可以正常发起任务（管理员代发也一样）
	_, ownResp := doUpload(t, router, "alice.zip", makeStoredZip(t, "a.txt", 8), aliceToken)
	var ownPkg model.Package
	if err := json.Unmarshal(ownResp.Data, &ownPkg); err != nil {
		t.Fatalf("解析包记录失败: %v", err)
	}
	rec, resp3 := doJSON(t, router, http.MethodPost, "/api/v1/tasks", map[string]any{
		"serverId": srv.ID, "directoryId": dir.ID, "packageId": ownPkg.ID,
	}, aliceToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("使用自己的包应成功，实际 %d / %q", rec.Code, resp3.Message)
	}

	// 管理员不受上传者限制
	rec, resp4 := doJSON(t, router, http.MethodPost, "/api/v1/tasks", map[string]any{
		"serverId": srv.ID, "directoryId": dir.ID, "packageId": pkg.ID,
	}, adminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("管理员应可用任意包，实际 %d / %q", rec.Code, resp4.Message)
	}
}
