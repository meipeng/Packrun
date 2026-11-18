package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/packrun/packrun/internal/auth"
	"github.com/packrun/packrun/internal/config"
	"github.com/packrun/packrun/internal/deploy"
	"github.com/packrun/packrun/internal/model"
)

type stubDeployer struct{}

func (stubDeployer) Deploy(ctx context.Context, req deploy.Request) error { return nil }

// resetAuthFailureCounters 清理包级限流状态，避免用例之间互相污染。
func resetAuthFailureCounters() {
	for _, counter := range []*failureCounter{loginFailures, stepUpFailures} {
		counter.mu.Lock()
		counter.items = map[string]*failureItem{}
		counter.mu.Unlock()
	}
}

func newAuthTestEnv(t *testing.T) (*App, *gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	resetAuthFailureCounters()

	name := strings.ReplaceAll(t.Name(), "/", "_")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", name)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.User{}, &model.RecoveryCode{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移表结构失败: %v", err)
	}

	cfg := &config.Config{
		Server: config.ServerConfig{
			Addr:           "127.0.0.1:8080",
			Mode:           "debug",
			AllowedOrigins: []string{"http://localhost:5173"},
			TrustedProxies: []string{"127.0.0.1/32", "::1/128"},
		},
		Auth: config.AuthConfig{
			JWTSecret:      "packrun-test-secret-0123456789abcdef",
			TokenTTLMinute: 60,
			OTPIssuer:      "Packrun",
			OTPPeriod:      30,
			OTPSkew:        1,
		},
		Deploy: config.DeployConfig{Mode: "local", MaxConcurrent: 3, MaxPendingPerUser: 10},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("测试配置不合法: %v", err)
	}

	app := NewApp(cfg, db, stubDeployer{})
	return app, NewRouter(app), db
}

func mustCreateUser(t *testing.T, db *gorm.DB, username, password, status string, otpEnabled bool) *model.User {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("口令哈希失败: %v", err)
	}
	u := model.User{
		Username:     username,
		PasswordHash: hash,
		Role:         model.RoleAdmin,
		ServerScope:  "*",
		Status:       status,
		TokenVersion: 1,
	}
	if otpEnabled {
		secret, err := auth.GenerateSecret()
		if err != nil {
			t.Fatalf("生成动态口令种子失败: %v", err)
		}
		u.OTPSecret = secret
		u.OTPEnabled = true
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	return &u
}

type apiResp struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func doJSON(t *testing.T, r http.Handler, method, path string, payload any, token string) (*httptest.ResponseRecorder, apiResp) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("序列化请求失败: %v", err)
		}
		body = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp apiResp
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w, resp
}

func TestLoginErrorsAreUniform(t *testing.T) {
	_, router, db := newAuthTestEnv(t)
	mustCreateUser(t, db, "alice", "Correct@123", "active", false)
	mustCreateUser(t, db, "banned", "Correct@123", "disabled", false)

	ghost, ghostBody := doJSON(t, router, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "ghost", "password": "whatever"}, "")
	wrong, wrongBody := doJSON(t, router, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "alice", "password": "wrong-password"}, "")
	disabled, disabledBody := doJSON(t, router, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "banned", "password": "Correct@123"}, "")

	for name, rec := range map[string]*httptest.ResponseRecorder{"ghost": ghost, "wrong": wrong, "disabled": disabled} {
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: 状态码 = %d，期望 401", name, rec.Code)
		}
	}
	for name, b := range map[string]apiResp{"ghost": ghostBody, "wrong": wrongBody, "disabled": disabledBody} {
		if b.Message != "账号或密码错误" || b.Code != CodeAuthFailed {
			t.Fatalf("%s: 响应 = %+v，期望统一 401「账号或密码错误」", name, b)
		}
	}
}

func TestLoginLocksAccountAfterFiveFailures(t *testing.T) {
	_, router, db := newAuthTestEnv(t)
	mustCreateUser(t, db, "alice", "Correct@123", "active", false)

	payload := map[string]string{"username": "alice", "password": "wrong-password"}
	for i := 0; i < 5; i++ {
		rec, _ := doJSON(t, router, http.MethodPost, "/api/v1/auth/login", payload, "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次失败: 状态码 = %d，期望 401", i+1, rec.Code)
		}
	}
	rec, body := doJSON(t, router, http.MethodPost, "/api/v1/auth/login", payload, "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("第 6 次尝试: 状态码 = %d，期望 429", rec.Code)
	}
	if body.Code != CodeAuthFailed {
		t.Fatalf("第 6 次尝试: 业务码 = %d，期望 %d", body.Code, CodeAuthFailed)
	}
}

func TestTokenVersionInvalidatesSession(t *testing.T) {
	_, router, db := newAuthTestEnv(t)
	u := mustCreateUser(t, db, "alice", "Correct@123", "active", false)

	rec, body := doJSON(t, router, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "alice", "password": "Correct@123"}, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("登录失败: 状态码 = %d，响应 = %s", rec.Code, rec.Body.String())
	}
	var loginData struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body.Data, &loginData); err != nil || loginData.Token == "" {
		t.Fatalf("解析登录令牌失败: %v", err)
	}

	if rec, _ := doJSON(t, router, http.MethodGet, "/api/v1/auth/me", nil, loginData.Token); rec.Code != http.StatusOK {
		t.Fatalf("改密前 /auth/me 状态码 = %d，期望 200", rec.Code)
	}

	if err := db.Model(&model.User{}).Where("id = ?", u.ID).
		UpdateColumn("token_version", gorm.Expr("token_version + 1")).Error; err != nil {
		t.Fatalf("递增 token_version 失败: %v", err)
	}

	rec, body = doJSON(t, router, http.MethodGet, "/api/v1/auth/me", nil, loginData.Token)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("token_version 变更后 /auth/me 状态码 = %d，期望 401", rec.Code)
	}
	if body.Code != CodeTokenInvalid || body.Message != "登录已失效，请重新登录" {
		t.Fatalf("响应 = %+v，期望登录失效提示", body)
	}
}

func TestOTPReplayIsRejected(t *testing.T) {
	app, router, db := newAuthTestEnv(t)
	u := mustCreateUser(t, db, "alice", "Correct@123", "active", true)

	code, err := auth.GenerateOTP(u.OTPSecret, time.Now(), app.Cfg.Auth.OTPPeriod)
	if err != nil {
		t.Fatalf("生成动态口令失败: %v", err)
	}

	payload := map[string]string{"username": "alice", "password": "Correct@123", "otp": code}
	if rec, _ := doJSON(t, router, http.MethodPost, "/api/v1/auth/login", payload, ""); rec.Code != http.StatusOK {
		t.Fatalf("首次携带动态口令登录应成功，状态码 = %d，响应 = %s", rec.Code, rec.Body.String())
	}
	if rec, _ := doJSON(t, router, http.MethodPost, "/api/v1/auth/login", payload, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("重复使用同一动态口令登录应被拒绝，状态码 = %d", rec.Code)
	}

	// step-up 场景：重置已消费步进后，同一口令只能通过一次。
	if err := db.Model(&model.User{}).Where("id = ?", u.ID).UpdateColumn("last_otp_step", 0).Error; err != nil {
		t.Fatalf("重置 last_otp_step 失败: %v", err)
	}
	var fresh model.User
	if err := db.First(&fresh, u.ID).Error; err != nil {
		t.Fatalf("重新加载用户失败: %v", err)
	}
	stepUpCode, err := auth.GenerateOTP(fresh.OTPSecret, time.Now(), app.Cfg.Auth.OTPPeriod)
	if err != nil {
		t.Fatalf("生成动态口令失败: %v", err)
	}
	if passed, msg := app.verifyStepUpOTP(&fresh, stepUpCode); !passed {
		t.Fatalf("首次 step-up 应通过，实际失败：%s", msg)
	}
	if passed, msg := app.verifyStepUpOTP(&fresh, stepUpCode); passed || msg != "动态口令已使用" {
		t.Fatalf("重复 step-up 应被拒绝并提示已使用，passed=%v msg=%q", passed, msg)
	}
}

// mustCreateOTPUser 创建已绑定动态口令的启用账号，并直接签发令牌。
func mustCreateOTPUser(t *testing.T, app *App, db *gorm.DB, username, password string) (*model.User, string) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("口令哈希失败: %v", err)
	}
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatalf("生成动态口令种子失败: %v", err)
	}
	u := model.User{
		Username:     username,
		PasswordHash: hash,
		Role:         model.RoleAdmin,
		ServerScope:  "*",
		Status:       "active",
		OTPEnabled:   true,
		OTPSecret:    secret,
		TokenVersion: 1,
		LastOTPStep:  0,
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	token, err := auth.IssueToken(app.Cfg.Auth.JWTSecret, u.ID, u.Username, u.Role, u.TokenVersion, time.Hour)
	if err != nil {
		t.Fatalf("签发令牌失败: %v", err)
	}
	return &u, token
}

func mustPasswordHash(t *testing.T, db *gorm.DB, userID uint) string {
	t.Helper()
	var u model.User
	if err := db.First(&u, userID).Error; err != nil {
		t.Fatalf("加载用户失败: %v", err)
	}
	return u.PasswordHash
}

func TestChangePasswordRequiresOTPAndInvalidatesToken(t *testing.T) {
	app, router, db := newAuthTestEnv(t)
	u, token := mustCreateOTPUser(t, app, db, "alice", "Correct@123")
	resetAuthFailureCounters()

	originHash := mustPasswordHash(t, db, u.ID)
	payload := map[string]string{"oldPassword": "Correct@123", "newPassword": "Next@12345"}

	// 未携带动态口令：拒绝且口令不变。
	rec, _ := doJSON(t, router, http.MethodPost, "/api/v1/auth/password", payload, token)
	if rec.Code == http.StatusOK {
		t.Fatalf("缺少动态口令时改密不应成功，响应 = %s", rec.Body.String())
	}
	if got := mustPasswordHash(t, db, u.ID); got != originHash {
		t.Fatal("缺少动态口令时不应更新 password_hash")
	}

	// 错误动态口令：拒绝且口令不变。
	code, err := auth.GenerateOTP(u.OTPSecret, time.Now(), app.Cfg.Auth.OTPPeriod)
	if err != nil {
		t.Fatalf("生成动态口令失败: %v", err)
	}
	wrongCode := "000000"
	if wrongCode == code {
		wrongCode = "111111"
	}
	wrongPayload := map[string]string{"oldPassword": "Correct@123", "newPassword": "Next@12345", "otp": wrongCode}
	rec, _ = doJSON(t, router, http.MethodPost, "/api/v1/auth/password", wrongPayload, token)
	if rec.Code == http.StatusOK {
		t.Fatalf("动态口令错误时改密不应成功，响应 = %s", rec.Body.String())
	}
	if got := mustPasswordHash(t, db, u.ID); got != originHash {
		t.Fatal("动态口令错误时不应更新 password_hash")
	}

	// 正确动态口令：改密成功。
	okPayload := map[string]string{"oldPassword": "Correct@123", "newPassword": "Next@12345", "otp": code}
	rec, body := doJSON(t, router, http.MethodPost, "/api/v1/auth/password", okPayload, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("携带正确动态口令改密应成功，状态码 = %d，响应 = %s", rec.Code, rec.Body.String())
	}
	if got := mustPasswordHash(t, db, u.ID); got == originHash {
		t.Fatal("改密成功后 password_hash 应已更新")
	}
	if !auth.VerifyPassword(mustPasswordHash(t, db, u.ID), "Next@12345") {
		t.Fatal("改密后新口令应可通过校验")
	}

	// 旧令牌因 token_version 递增而立即失效。
	rec, body = doJSON(t, router, http.MethodGet, "/api/v1/auth/me", nil, token)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("改密后旧令牌访问 /auth/me 状态码 = %d，期望 401，响应 = %s", rec.Code, rec.Body.String())
	}
	if body.Code != CodeTokenInvalid {
		t.Fatalf("改密后旧令牌业务码 = %d，期望 %d", body.Code, CodeTokenInvalid)
	}

	var fresh model.User
	if err := db.First(&fresh, u.ID).Error; err != nil {
		t.Fatalf("重新加载用户失败: %v", err)
	}
	if fresh.TokenVersion != 2 {
		t.Fatalf("改密后 token_version = %d，期望 2", fresh.TokenVersion)
	}
}

func TestStepUpOTPLockoutAfterFiveFailures(t *testing.T) {
	app, router, db := newAuthTestEnv(t)
	u, token := mustCreateOTPUser(t, app, db, "bob", "Correct@123")
	resetAuthFailureCounters()

	code, err := auth.GenerateOTP(u.OTPSecret, time.Now(), app.Cfg.Auth.OTPPeriod)
	if err != nil {
		t.Fatalf("生成动态口令失败: %v", err)
	}
	wrongCode := "000000"
	if wrongCode == code {
		wrongCode = "111111"
	}

	for i := 0; i < 5; i++ {
		rec, _ := doJSON(t, router, http.MethodPost, "/api/v1/auth/recovery-codes/regenerate",
			map[string]string{"code": wrongCode}, token)
		if rec.Code == http.StatusOK {
			t.Fatalf("第 %d 次错误动态口令不应通过，响应 = %s", i+1, rec.Body.String())
		}
	}

	// 第 6 次即使动态口令正确，也应被 step-up 限流拦截。
	rec, body := doJSON(t, router, http.MethodPost, "/api/v1/auth/recovery-codes/regenerate",
		map[string]string{"code": code}, token)
	if rec.Code == http.StatusOK {
		t.Fatalf("连续失败 5 次后正确口令仍被放行，响应 = %s", rec.Body.String())
	}
	if !strings.Contains(body.Message, "次数过多") && !strings.Contains(body.Message, "锁定") {
		t.Fatalf("第 6 次响应消息 = %q，期望包含「次数过多」或「锁定」", body.Message)
	}
}

func TestClientIPOnlyTrustsConfiguredProxies(t *testing.T) {
	app, _, _ := newAuthTestEnv(t)

	newContext := func(remoteAddr string, headers map[string]string) *gin.Context {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.RemoteAddr = remoteAddr
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		c.Request = req
		return c
	}

	if got := app.clientIP(newContext("203.0.113.9:4321", map[string]string{
		"X-Forwarded-For": "198.51.100.7",
		"X-Real-IP":       "198.51.100.8",
	})); got != "203.0.113.9" {
		t.Fatalf("非受信对端应返回 TCP 地址，实际 %q", got)
	}

	if got := app.clientIP(newContext("127.0.0.1:4321", map[string]string{
		"X-Forwarded-For": "198.51.100.7, 10.1.2.3",
	})); got != "198.51.100.7" {
		t.Fatalf("受信代理应返回 XFF 链首，实际 %q", got)
	}

	if got := app.clientIP(newContext("127.0.0.1:4321", map[string]string{
		"X-Forwarded-For": "not-an-ip",
		"X-Real-IP":       "198.51.100.8",
	})); got != "198.51.100.8" {
		t.Fatalf("XFF 非法时应回落到 X-Real-IP，实际 %q", got)
	}

	if got := app.clientIP(newContext("127.0.0.1:4321", map[string]string{
		"X-Forwarded-For": "not-an-ip",
		"X-Real-IP":       "also-bad",
	})); got != "127.0.0.1" {
		t.Fatalf("转发头均非法时应回落到 TCP 地址，实际 %q", got)
	}
}
