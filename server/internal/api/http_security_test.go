package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/packrun/packrun/internal/model"
)

// mustInsertPendingTask 直接写入一条待执行任务，用于构造配额边界场景。
func mustInsertPendingTask(t *testing.T, db *gorm.DB, u *model.User) {
	t.Helper()
	task := model.Task{
		TaskNo:     fmt.Sprintf("D%s%d", strings.ToUpper(u.Username), time.Now().UnixNano()),
		OperatorID: u.ID,
		Operator:   u.Username,
		Status:     model.TaskPending,
		Stage:      "queued",
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("插入待执行任务失败: %v", err)
	}
}

func TestHealthzReturnsMinimalPayload(t *testing.T) {
	_, router, _ := newSecurityTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/healthz 状态码 = %d，期望 200", w.Code)
	}
	if strings.Contains(w.Body.String(), "mode") {
		t.Fatalf("/healthz 不应暴露运行模式，实际响应 = %s", w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析 /healthz 响应失败: %v", err)
	}
	if payload["status"] != "ok" {
		t.Fatalf("/healthz status = %v，期望 ok", payload["status"])
	}
}

func TestSecurityHeadersOnAPIResponse(t *testing.T) {
	_, router, _ := newSecurityTestEnv(t)

	rec, _ := doJSON(t, router, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "ghost", "password": "whatever"}, "")

	want := map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "no-referrer",
		"Permissions-Policy":           "geolocation=(), microphone=(), camera=()",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Cache-Control":                "no-store",
	}
	for header, expected := range want {
		if got := rec.Header().Get(header); got != expected {
			t.Fatalf("%s = %q，期望 %q", header, got, expected)
		}
	}
}

func TestOversizedJSONBodyRejected(t *testing.T) {
	_, router, _ := newSecurityTestEnv(t)

	huge := strings.Repeat("a", (1<<20)+1024)
	rec, body := doJSON(t, router, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": huge, "password": "whatever"}, "")

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大请求体状态码 = %d，期望 413", rec.Code)
	}
	if body.Code != CodeBadRequest || body.Message != "请求体过大" {
		t.Fatalf("超大请求体响应 = %+v，期望 413「请求体过大」", body)
	}
}

func TestMultipartBodyLimitAppliesToNonUploadEndpoints(t *testing.T) {
	_, router, _ := newSecurityTestEnv(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("username", strings.Repeat("a", (1<<20)+1024)); err != nil {
		t.Fatalf("构造 multipart 表单失败: %v", err)
	}
	if err := mw.WriteField("password", "whatever"); err != nil {
		t.Fatalf("构造 multipart 表单失败: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("关闭 multipart 表单失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("登录接口超大 multipart 状态码 = %d，期望 413，响应 = %s", w.Code, w.Body.String())
	}
}

func TestCORSCredentialsOnlyForExactOriginMatch(t *testing.T) {
	app, router, _ := newSecurityTestEnv(t)

	newRequest := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// 精确匹配：回显来源并允许凭据。
	exact := newRequest("http://localhost:5173")
	if got := exact.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("精确匹配 Allow-Origin = %q，期望回显来源", got)
	}
	if got := exact.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("精确匹配 Allow-Credentials = %q，期望 true", got)
	}

	// 通配配置：回显来源但不得发放凭据。
	app.Cfg.Server.AllowedOrigins = []string{"*"}
	wildRouter := NewRouter(app)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://any.example")
	w := httptest.NewRecorder()
	wildRouter.ServeHTTP(w, req)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://any.example" {
		t.Fatalf("通配配置 Allow-Origin = %q，期望回显来源", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got == "true" {
		t.Fatalf("通配配置不应设置 Allow-Credentials: true，实际 %q", got)
	}

	// 未知来源不回显。
	unknown := newRequest("https://unknown.example")
	if got := unknown.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("未知来源 Allow-Origin = %q，期望为空", got)
	}
}

func TestCreateTaskRejectsWhenPendingLimitReached(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	user := mustCreateRoleUser(t, db, "dep", model.RoleDeployer, "*", "active")
	srv := mustCreateServer(t, db, "dev-srv", model.EnvDev)
	dir := mustCreateDirectory(t, db, srv.ID, "/opt/app")
	pkg := model.Package{Name: "app.zip", Kind: "zip", Size: 10, StorageKey: "app.zip", UploaderID: user.ID}
	if err := db.Create(&pkg).Error; err != nil {
		t.Fatalf("创建安装包失败: %v", err)
	}
	token := mustToken(t, app, user)
	payload := map[string]any{"serverId": srv.ID, "directoryId": dir.ID, "packageId": pkg.ID}

	for i := 0; i < 10; i++ {
		mustInsertPendingTask(t, db, user)
	}

	rec, body := doJSON(t, router, http.MethodPost, "/api/v1/tasks", payload, token)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("达到待执行上限时状态码 = %d，期望 429，响应 = %s", rec.Code, rec.Body.String())
	}
	if body.Code != CodeDeployFailed || !strings.Contains(body.Message, "待执行任务已达上限") {
		t.Fatalf("响应 = %+v，期望包含「待执行任务已达上限」", body)
	}
}

func TestCreateTaskAllowedBelowPendingLimit(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	user := mustCreateRoleUser(t, db, "dep", model.RoleDeployer, "*", "active")
	srv := mustCreateServer(t, db, "dev-srv", model.EnvDev)
	dir := mustCreateDirectory(t, db, srv.ID, "/opt/app")
	pkg := model.Package{Name: "app.zip", Kind: "zip", Size: 10, StorageKey: "app.zip", UploaderID: user.ID}
	if err := db.Create(&pkg).Error; err != nil {
		t.Fatalf("创建安装包失败: %v", err)
	}
	token := mustToken(t, app, user)
	payload := map[string]any{"serverId": srv.ID, "directoryId": dir.ID, "packageId": pkg.ID}

	for i := 0; i < 9; i++ {
		mustInsertPendingTask(t, db, user)
	}

	rec, _ := doJSON(t, router, http.MethodPost, "/api/v1/tasks", payload, token)
	if rec.Code == http.StatusTooManyRequests {
		t.Fatalf("9 条待执行任务时不应触发配额限制，响应 = %s", rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("配额内的合法请求应创建成功，状态码 = %d，响应 = %s", rec.Code, rec.Body.String())
	}
}

func TestPublishProjectRejectsWhenPendingLimitReached(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
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
		t.Fatalf("绑定项目目录失败: %v", err)
	}

	pkg := model.Package{Name: "app.zip", Kind: "zip", Size: 10, StorageKey: "app.zip", UploaderID: user.ID}
	if err := db.Create(&pkg).Error; err != nil {
		t.Fatalf("创建安装包失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(app.Cfg.Storage.PackageDir, pkg.StorageKey), []byte("zip"), 0o600); err != nil {
		t.Fatalf("写入安装包文件失败: %v", err)
	}

	token := mustToken(t, app, user)
	// 已满配额的任务不属于该项目，避免命中「项目仍有发布任务进行中」的前置校验。
	for i := 0; i < 10; i++ {
		mustInsertPendingTask(t, db, user)
	}

	rec, body := doJSON(t, router, http.MethodPost,
		fmt.Sprintf("/api/v1/projects/%d/publish", p.ID), map[string]any{"packageId": pkg.ID}, token)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("项目发布达到配额时状态码 = %d，期望 429，响应 = %s", rec.Code, rec.Body.String())
	}
	if body.Code != CodeDeployFailed || !strings.Contains(body.Message, "待执行任务已达上限") {
		t.Fatalf("响应 = %+v，期望包含「待执行任务已达上限」", body)
	}

	var created int64
	db.Model(&model.Task{}).Where("project_id = ?", p.ID).Count(&created)
	if created != 0 {
		t.Fatalf("超限请求不应创建任何项目任务，实际创建 %d 条", created)
	}
}
