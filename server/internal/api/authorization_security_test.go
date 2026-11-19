package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/packrun/packrun/internal/auth"
	"github.com/packrun/packrun/internal/model"
)

// newSecurityTestEnv 在认证测试环境基础上补齐授权/上传相关的业务表。
func newSecurityTestEnv(t *testing.T) (*App, *gin.Engine, *gorm.DB) {
	t.Helper()
	app, router, db := newAuthTestEnv(t)
	if err := db.AutoMigrate(
		&model.Server{}, &model.Directory{}, &model.Script{}, &model.Package{},
		&model.Project{}, &model.ProjectDirectory{}, &model.ProjectMember{},
		&model.Task{}, &model.TaskStep{}, &model.TaskScript{},
	); err != nil {
		t.Fatalf("迁移业务表失败: %v", err)
	}
	app.Cfg.Storage.PackageDir = t.TempDir()
	app.Cfg.Storage.MaxTotalMB = 20480
	return app, router, db
}

func mustCreateRoleUser(t *testing.T, db *gorm.DB, username, role, scope, status string) *model.User {
	t.Helper()
	hash, err := auth.HashPassword("Correct@123")
	if err != nil {
		t.Fatalf("口令哈希失败: %v", err)
	}
	u := model.User{
		Username:     username,
		PasswordHash: hash,
		Role:         role,
		ServerScope:  scope,
		Status:       status,
		TokenVersion: 1,
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	return &u
}

func mustToken(t *testing.T, app *App, u *model.User) string {
	t.Helper()
	token, err := auth.IssueToken(app.Cfg.Auth.JWTSecret, u.ID, u.Username, u.Role,
		effectiveTokenVersion(u.TokenVersion), time.Hour)
	if err != nil {
		t.Fatalf("签发令牌失败: %v", err)
	}
	return token
}

func mustCreateServer(t *testing.T, db *gorm.DB, name, env string) *model.Server {
	t.Helper()
	srv := model.Server{
		Name: name, Env: env, Host: "127.0.0.1", Port: 22,
		AuthType: "key", CredentialRef: "env:PACKRUN_SECRET", Status: "unknown",
	}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatalf("创建服务器失败: %v", err)
	}
	return &srv
}

func mustCreateDirectory(t *testing.T, db *gorm.DB, serverID uint, path string) *model.Directory {
	t.Helper()
	dir := model.Directory{ServerID: serverID, Path: path, MaxSizeMB: 1024}
	if err := db.Create(&dir).Error; err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	return &dir
}

func TestUpdateProjectDirectoryScope(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)

	inSrv := mustCreateServer(t, db, "srv-in", model.EnvDev)
	outSrv := mustCreateServer(t, db, "srv-out", model.EnvDev)
	inDir := mustCreateDirectory(t, db, inSrv.ID, "/opt/app")
	inDir2 := mustCreateDirectory(t, db, inSrv.ID, "/opt/app2")
	outDir := mustCreateDirectory(t, db, outSrv.ID, "/opt/out")

	deployer := mustCreateRoleUser(t, db, "dep", model.RoleDeployer, strconv.Itoa(int(inSrv.ID)), "active")
	p := model.Project{Name: "项目", Code: "p", OwnerID: deployer.ID, Version: "v0.0.0", Status: "active"}
	if err := db.Create(&p).Error; err != nil {
		t.Fatalf("创建项目失败: %v", err)
	}
	if err := db.Create(&model.ProjectDirectory{ProjectID: p.ID, DirectoryID: inDir.ID}).Error; err != nil {
		t.Fatalf("初始绑定失败: %v", err)
	}
	token := mustToken(t, app, deployer)
	path := fmt.Sprintf("/api/v1/projects/%d", p.ID)

	// 绑定范围外目录：403，既有绑定保持原样。
	rec, resp := doJSON(t, router, http.MethodPut, path, map[string]any{
		"directoryIds": []uint{outDir.ID},
	}, token)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("绑定范围外目录状态码 = %d，期望 403，响应 = %s", rec.Code, rec.Body.String())
	}
	if resp.Message != "目录不存在或无权访问" {
		t.Fatalf("响应消息 = %q，期望不区分目录不存在与无权限", resp.Message)
	}
	var bindings []model.ProjectDirectory
	db.Where("project_id = ?", p.ID).Order("directory_id asc").Find(&bindings)
	if len(bindings) != 1 || bindings[0].DirectoryID != inDir.ID {
		t.Fatalf("越权请求后绑定应保持原样，实际 %+v", bindings)
	}

	// 范围内目录：允许整体替换。
	rec, _ = doJSON(t, router, http.MethodPut, path, map[string]any{
		"directoryIds": []uint{inDir.ID, inDir2.ID},
	}, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("绑定范围内目录状态码 = %d，期望 200，响应 = %s", rec.Code, rec.Body.String())
	}
	bindings = nil
	db.Where("project_id = ?", p.ID).Order("directory_id asc").Find(&bindings)
	if len(bindings) != 2 {
		t.Fatalf("期望绑定 2 个目录，实际 %+v", bindings)
	}
}

func TestUpdateProjectRejectsNonAdminMemberAndPolicy(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	deployer := mustCreateRoleUser(t, db, "dep", model.RoleDeployer, "*", "active")
	p := model.Project{Name: "项目", Code: "p", OwnerID: deployer.ID, Version: "v0.0.0", Status: "active"}
	if err := db.Create(&p).Error; err != nil {
		t.Fatalf("创建项目失败: %v", err)
	}
	token := mustToken(t, app, deployer)
	path := fmt.Sprintf("/api/v1/projects/%d", p.ID)

	rec, _ := doJSON(t, router, http.MethodPut, path, map[string]any{
		"memberIds": []uint{deployer.ID},
	}, token)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("非管理员提交 memberIds 状态码 = %d，期望 403", rec.Code)
	}

	rec, _ = doJSON(t, router, http.MethodPut, path, map[string]any{
		"policy": map[string]any{"allowOverwrite": true},
	}, token)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("非管理员提交 policy 状态码 = %d，期望 403", rec.Code)
	}
}

func TestServerCredentialMaskedForNonAdmin(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	deployer := mustCreateRoleUser(t, db, "dep", model.RoleDeployer, "*", "active")
	srv := mustCreateServer(t, db, "srv-a", model.EnvDev)
	adminToken := mustToken(t, app, admin)
	deployerToken := mustToken(t, app, deployer)

	rec, body := doJSON(t, router, http.MethodGet, "/api/v1/servers", nil, deployerToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("部署者查询服务器状态码 = %d，期望 200", rec.Code)
	}
	var list []model.Server
	if err := json.Unmarshal(body.Data, &list); err != nil || len(list) != 1 {
		t.Fatalf("解析服务器列表失败: %v，数据 = %s", err, string(body.Data))
	}
	if list[0].CredentialRef != "***" {
		t.Fatalf("部署者列表中的 credentialRef = %q，期望 ***", list[0].CredentialRef)
	}

	rec, body = doJSON(t, router, http.MethodGet, fmt.Sprintf("/api/v1/servers/%d", srv.ID), nil, deployerToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("部署者查询服务器详情状态码 = %d，期望 200", rec.Code)
	}
	var one model.Server
	if err := json.Unmarshal(body.Data, &one); err != nil {
		t.Fatalf("解析服务器详情失败: %v", err)
	}
	if one.CredentialRef != "***" {
		t.Fatalf("部署者详情中的 credentialRef = %q，期望 ***", one.CredentialRef)
	}

	rec, body = doJSON(t, router, http.MethodGet, fmt.Sprintf("/api/v1/servers/%d", srv.ID), nil, adminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("管理员查询服务器详情状态码 = %d，期望 200", rec.Code)
	}
	if err := json.Unmarshal(body.Data, &one); err != nil {
		t.Fatalf("解析管理员视图失败: %v", err)
	}
	if one.CredentialRef != "env:PACKRUN_SECRET" {
		t.Fatalf("管理员应看到原始 credentialRef，实际 %q", one.CredentialRef)
	}
}

func TestUpdateUserProtectsLastAdmin(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	token := mustToken(t, app, admin)
	path := fmt.Sprintf("/api/v1/users/%d", admin.ID)

	rec, body := doJSON(t, router, http.MethodPut, path, map[string]any{
		"role": model.RoleDeployer, "serverScope": "*", "status": "active",
	}, token)
	if rec.Code != http.StatusBadRequest || body.Message != "系统必须保留至少一个启用状态的管理员" {
		t.Fatalf("降级最后一个管理员应 400，实际 %d / %q", rec.Code, body.Message)
	}

	rec, body = doJSON(t, router, http.MethodPut, path, map[string]any{
		"role": model.RoleAdmin, "serverScope": "*", "status": "disabled",
	}, token)
	if rec.Code != http.StatusBadRequest || body.Message != "系统必须保留至少一个启用状态的管理员" {
		t.Fatalf("禁用最后一个管理员应 400，实际 %d / %q", rec.Code, body.Message)
	}

	other := mustCreateRoleUser(t, db, "admin2", model.RoleAdmin, "*", "active")
	rec, _ = doJSON(t, router, http.MethodPut, fmt.Sprintf("/api/v1/users/%d", other.ID), map[string]any{
		"role": model.RoleAdmin, "serverScope": "*", "status": "disabled",
	}, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("存在两个管理员时应允许禁用其中一个，实际 %d，响应 = %s", rec.Code, rec.Body.String())
	}
}

func TestAdminUpdatesUserProjectGrants(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	dep := mustCreateRoleUser(t, db, "dep", model.RoleDeployer, "*", "active")
	p1 := model.Project{Name: "标准版", Code: "std", OwnerID: admin.ID, Version: "v0.0.0", Status: "active"}
	p2 := model.Project{Name: "企业版", Code: "ent", OwnerID: admin.ID, Version: "v0.0.0", Status: "active"}
	for _, p := range []*model.Project{&p1, &p2} {
		if err := db.Create(p).Error; err != nil {
			t.Fatalf("创建项目失败: %v", err)
		}
	}
	adminToken := mustToken(t, app, admin)
	userPath := fmt.Sprintf("/api/v1/users/%d", dep.ID)

	// 管理员为用户勾选两个项目：整体写入项目成员关系。
	rec, resp := doJSON(t, router, http.MethodPut, userPath, map[string]any{
		"role": model.RoleDeployer, "serverScope": "*", "status": "active",
		"projectIds": []uint{p1.ID, p2.ID},
	}, adminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("设置项目授权状态码 = %d，期望 200，响应 = %s", rec.Code, rec.Body.String())
	}
	var members []model.ProjectMember
	db.Where("user_id = ?", dep.ID).Order("project_id asc").Find(&members)
	if len(members) != 2 || members[0].ProjectID != p1.ID || members[1].ProjectID != p2.ID {
		t.Fatalf("项目授权不符合预期: %+v", members)
	}

	// 子账户列表只返回被授权项目。
	rec, body := doJSON(t, router, http.MethodGet, "/api/v1/projects", nil, mustToken(t, app, dep))
	if rec.Code != http.StatusOK {
		t.Fatalf("子账户查询项目状态码 = %d，期望 200", rec.Code)
	}
	var projects []ProjectView
	if err := json.Unmarshal(body.Data, &projects); err != nil || len(projects) != 2 {
		t.Fatalf("子账户应看到 2 个授权项目，err = %v，数据 = %s", err, string(body.Data))
	}

	// 取消其中一个授权。
	rec, _ = doJSON(t, router, http.MethodPut, userPath, map[string]any{
		"role": model.RoleDeployer, "serverScope": "*", "status": "active",
		"projectIds": []uint{p2.ID},
	}, adminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("更新项目授权状态码 = %d，期望 200，响应 = %s", rec.Code, rec.Body.String())
	}
	members = nil
	db.Where("user_id = ?", dep.ID).Find(&members)
	if len(members) != 1 || members[0].ProjectID != p2.ID {
		t.Fatalf("取消授权后应仅剩 1 个项目，实际 %+v", members)
	}

	// 含不存在项目的授权整批拒绝，既有授权保持不变。
	rec, resp = doJSON(t, router, http.MethodPut, userPath, map[string]any{
		"role": model.RoleDeployer, "serverScope": "*", "status": "active",
		"projectIds": []uint{p1.ID, 99999},
	}, adminToken)
	if rec.Code != http.StatusBadRequest || resp.Message != "项目不存在：99999" {
		t.Fatalf("非法项目应 400，实际 %d / %q", rec.Code, resp.Message)
	}
	members = nil
	db.Where("user_id = ?", dep.ID).Find(&members)
	if len(members) != 1 || members[0].ProjectID != p2.ID {
		t.Fatalf("非法请求后授权应保持不变，实际 %+v", members)
	}
}

func TestCheckPublishPrerequisitesOTPFlag(t *testing.T) {
	app, _, db := newSecurityTestEnv(t)
	user := mustCreateRoleUser(t, db, "dep", model.RoleDeployer, "*", "active")
	srv := mustCreateServer(t, db, "dev-srv", model.EnvDev)
	dir := mustCreateDirectory(t, db, srv.ID, "/opt/app")
	p := model.Project{Name: "项目", Code: "p", OwnerID: user.ID, Version: "v0.0.0", Status: "active"}
	if err := db.Create(&p).Error; err != nil {
		t.Fatalf("创建项目失败: %v", err)
	}
	if err := db.Create(&model.ProjectMember{ProjectID: p.ID, UserID: user.ID}).Error; err != nil {
		t.Fatalf("添加项目成员失败: %v", err)
	}
	if err := db.Create(&model.ProjectDirectory{ProjectID: p.ID, DirectoryID: dir.ID}).Error; err != nil {
		t.Fatalf("绑定目录失败: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	dirs, otpVerified, pass := app.checkPublishPrerequisites(c, user, p, "")
	if !pass {
		t.Fatalf("非生产目录的项目应通过前置校验，响应 = %s", w.Body.String())
	}
	if otpVerified {
		t.Fatal("未绑定生产目录时 otpVerified 必须为 false")
	}
	if len(dirs) != 1 {
		t.Fatalf("期望返回 1 个目录，实际 %d", len(dirs))
	}
}

func TestInvalidUserStatusRejected(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	token := mustToken(t, app, admin)

	rec, _ := doJSON(t, router, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "newbie", "password": "Correct@123",
		"role": model.RoleDeployer, "serverScope": "*", "status": "root",
	}, token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("创建用户时非法 status 状态码 = %d，期望 400", rec.Code)
	}

	target := mustCreateRoleUser(t, db, "dep", model.RoleDeployer, "*", "active")
	rec, _ = doJSON(t, router, http.MethodPut, fmt.Sprintf("/api/v1/users/%d", target.ID), map[string]any{
		"role": model.RoleDeployer, "serverScope": "*", "status": "root",
	}, token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("更新用户时非法 status 状态码 = %d，期望 400", rec.Code)
	}
}

func doUpload(t *testing.T, r http.Handler, filename string, content []byte, token string) (*httptest.ResponseRecorder, apiResp) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("构造上传表单失败: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("写入上传内容失败: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("关闭上传表单失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/packages", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp apiResp
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w, resp
}

func makeStoredZip(t *testing.T, name string, size int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
	if err != nil {
		t.Fatalf("创建 zip 条目失败: %v", err)
	}
	if _, err := w.Write(make([]byte, size)); err != nil {
		t.Fatalf("写入 zip 内容失败: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	return buf.Bytes()
}

func TestUploadRejectsNonZipContent(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	token := mustToken(t, app, admin)

	rec, resp := doUpload(t, router, "app.zip", []byte("definitely not a zip"), token)
	if rec.Code != http.StatusBadRequest || resp.Message != "文件内容不是有效的 ZIP/JAR" {
		t.Fatalf("非 ZIP 内容应 400，实际 %d / %q", rec.Code, resp.Message)
	}
}

func TestUploadRejectsWhenPackageQuotaExceeded(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	token := mustToken(t, app, admin)
	app.Cfg.Storage.MaxTotalMB = 1

	content := makeStoredZip(t, "big.bin", 2<<20)
	rec, resp := doUpload(t, router, "app.zip", content, token)
	if rec.Code != http.StatusInsufficientStorage || resp.Message != "包仓库容量已满" {
		t.Fatalf("超过仓库容量应 507，实际 %d / %q", rec.Code, resp.Message)
	}
}

func TestUploadValidZipSucceeds(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	token := mustToken(t, app, admin)

	content := makeStoredZip(t, "hello.txt", 16)
	rec, resp := doUpload(t, router, "测试包.zip", content, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("正常 ZIP 上传状态码 = %d，期望 200，响应 = %s", rec.Code, rec.Body.String())
	}
	var pkg model.Package
	if err := json.Unmarshal(resp.Data, &pkg); err != nil {
		t.Fatalf("解析包记录失败: %v", err)
	}
	if pkg.Name != "测试包.zip" || pkg.Kind != "zip" || pkg.Size != int64(len(content)) {
		t.Fatalf("包记录不符合预期: %+v（期望大小 %d）", pkg, len(content))
	}

	var stored string
	err := filepath.WalkDir(app.Cfg.Storage.PackageDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			stored = path
		}
		return nil
	})
	if err != nil || stored == "" {
		t.Fatalf("未找到落盘的包文件，err = %v", err)
	}
	info, err := os.Stat(stored)
	if err != nil {
		t.Fatalf("读取落盘文件失败: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("包文件权限 = %o，期望 600", perm)
	}
}

func TestProjectOwnerCanSeeOwnProject(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	owner := mustCreateRoleUser(t, db, "owner", model.RoleDeployer, "*", "active")
	other := mustCreateRoleUser(t, db, "other", model.RoleDeployer, "*", "active")
	token := mustToken(t, app, owner)

	rec, body := doJSON(t, router, http.MethodPost, "/api/v1/projects", map[string]any{
		"name": "我的项目", "code": "mine",
	}, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("创建项目状态码 = %d，期望 200，响应 = %s", rec.Code, rec.Body.String())
	}
	var created ProjectView
	if err := json.Unmarshal(body.Data, &created); err != nil {
		t.Fatalf("解析创建结果失败: %v，数据 = %s", err, string(body.Data))
	}
	if created.ID == 0 {
		t.Fatal("创建结果缺少项目 ID")
	}
	if len(created.Members) != 1 || created.Members[0].ID != owner.ID {
		t.Fatalf("创建者应作为隐式成员回显，实际 %+v", created.Members)
	}

	// 列表：未写入 project_members 也应看到自己创建的项目。
	rec, body = doJSON(t, router, http.MethodGet, "/api/v1/projects", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("项目列表状态码 = %d，期望 200", rec.Code)
	}
	var list []ProjectView
	if err := json.Unmarshal(body.Data, &list); err != nil || len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("创建者项目列表不符: err=%v list=%+v", err, list)
	}

	// 详情与历史：Owner 直接可读。
	path := fmt.Sprintf("/api/v1/projects/%d", created.ID)
	if rec, _ := doJSON(t, router, http.MethodGet, path, nil, token); rec.Code != http.StatusOK {
		t.Fatalf("创建者读取项目详情状态码 = %d，期望 200", rec.Code)
	}
	if rec, _ := doJSON(t, router, http.MethodGet, path+"/history", nil, token); rec.Code != http.StatusOK {
		t.Fatalf("创建者读取项目历史状态码 = %d，期望 200", rec.Code)
	}

	// 未被授权的其他发布者仍然不可见。
	otherToken := mustToken(t, app, other)
	if rec, _ := doJSON(t, router, http.MethodGet, path, nil, otherToken); rec.Code != http.StatusForbidden {
		t.Fatalf("未授权用户读取详情状态码 = %d，期望 403", rec.Code)
	}
	rec, body = doJSON(t, router, http.MethodGet, "/api/v1/projects", nil, otherToken)
	var otherList []ProjectView
	if err := json.Unmarshal(body.Data, &otherList); err != nil {
		t.Fatalf("解析其他用户项目列表失败: %v", err)
	}
	if len(otherList) != 0 {
		t.Fatalf("未授权用户不应看到项目，实际 %+v", otherList)
	}
}
