package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/packrun/packrun/internal/model"
)

// doHealth 直接请求 /healthz 并解析报告。
func doHealth(t *testing.T, h http.Handler) (int, healthReport) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var rep healthReport
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatalf("解析健康检查响应失败: %v（body=%s）", err, w.Body.String())
	}
	return w.Code, rep
}

func TestHealthzReportsAllChecks(t *testing.T) {
	app, router, _ := newSecurityTestEnv(t)
	// 数据库目录用临时目录，避免测试环境缺省路径不存在导致检查失败。
	app.Cfg.Storage.DBPath = filepath.Join(t.TempDir(), "packrun.db")

	code, rep := doHealth(t, router)
	if code != http.StatusOK || rep.Status != "ok" {
		t.Fatalf("健康检查应为 ok，实际 %d / %s（%+v）", code, rep.Status, rep.Checks)
	}
	for _, name := range []string{"database", "packageDir", "disk", "tasks"} {
		ch, ok := rep.Checks[name]
		if !ok {
			t.Fatalf("缺少检查项 %s", name)
		}
		if ch.Status == "" {
			t.Fatalf("检查项 %s 未返回状态", name)
		}
	}
	if rep.Checks["database"].Status != "ok" || rep.Checks["packageDir"].Status != "ok" {
		t.Fatalf("核心检查应通过: %+v", rep.Checks)
	}
	if rep.Version == "" || rep.UptimeSec < 0 {
		t.Fatalf("版本与运行时长为空: %+v", rep)
	}
}

func TestHealthzDegradedWhenPackageDirNotWritable(t *testing.T) {
	app, router, _ := newSecurityTestEnv(t)
	app.Cfg.Storage.DBPath = filepath.Join(t.TempDir(), "packrun.db")
	dir := t.TempDir()
	app.Cfg.Storage.PackageDir = dir

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("调整目录权限失败: %v", err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()

	code, rep := doHealth(t, router)
	if code != http.StatusServiceUnavailable || rep.Status != "degraded" {
		t.Fatalf("包目录不可写应 503，实际 %d / %s", code, rep.Status)
	}
	if rep.Checks["packageDir"].Status != "fail" {
		t.Fatalf("packageDir 应标记 fail，实际 %+v", rep.Checks["packageDir"])
	}
}

func TestHealthzWarnsOnLowDiskSpace(t *testing.T) {
	app, router, _ := newSecurityTestEnv(t)
	app.Cfg.Storage.DBPath = filepath.Join(t.TempDir(), "packrun.db")
	// 阈值设成不可能达到的剩余空间，触发磁盘检查失败。
	app.Cfg.Alert.DiskMinFreeMB = 1 << 30

	code, rep := doHealth(t, router)
	if code != http.StatusServiceUnavailable || rep.Checks["disk"].Status != "fail" {
		t.Fatalf("磁盘余量不足应 503，实际 %d / %+v", code, rep.Checks["disk"])
	}
}

// selfSignedPEM 生成一张自签证书，返回 PEM 编码。
func selfSignedPEM(t *testing.T, cn string, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("签发证书失败: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestCertExpiryPicksEarliest(t *testing.T) {
	dir := t.TempDir()
	early := time.Now().Add(5 * 24 * time.Hour).UTC().Truncate(time.Second)
	late := time.Now().Add(200 * 24 * time.Hour).UTC().Truncate(time.Second)

	path := filepath.Join(dir, "chain.pem")
	content := append(selfSignedPEM(t, "late.example.com", late), selfSignedPEM(t, "early.example.com", early)...)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("写入证书失败: %v", err)
	}

	got, cn, err := certExpiry(path)
	if err != nil {
		t.Fatalf("解析证书失败: %v", err)
	}
	if !got.Equal(early) {
		t.Fatalf("应返回最早到期的证书，实际 %s", got)
	}
	if cn != "early.example.com" {
		t.Fatalf("证书主体不匹配: %s", cn)
	}
}

func TestCertExpiryRejectsInvalidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.pem")
	if err := os.WriteFile(path, []byte("not a cert"), 0o600); err != nil {
		t.Fatalf("写入文件失败: %v", err)
	}
	if _, _, err := certExpiry(path); err == nil {
		t.Fatalf("无效证书应报错")
	}
}

func TestOverviewCarriesHealthAndDropsTaskStats(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	token := mustToken(t, app, admin)
	app.Cfg.Storage.DBPath = filepath.Join(t.TempDir(), "packrun.db")

	rec, resp := doJSON(t, router, http.MethodGet, "/api/v1/overview", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("概览接口失败: %d / %s", rec.Code, rec.Body.String())
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(resp.Data, &payload); err != nil {
		t.Fatalf("解析概览响应失败: %v", err)
	}
	for _, gone := range []string{"taskToday", "taskFailed", "taskRunning", "recentTasks"} {
		if _, ok := payload[gone]; ok {
			t.Fatalf("概览响应不应再包含任务字段 %s", gone)
		}
	}
	var rep healthReport
	if err := json.Unmarshal(payload["health"], &rep); err != nil {
		t.Fatalf("解析 health 字段失败: %v", err)
	}
	if rep.Status != "ok" || len(rep.Checks) == 0 {
		t.Fatalf("health 字段不完整: %+v", rep)
	}
	if rep.Checks["packageDir"].Metrics == nil || rep.Checks["disk"].Metrics == nil {
		t.Fatalf("包仓库与磁盘应带结构化指标: %+v", rep.Checks)
	}
}

func TestHealthCachesWithinWindow(t *testing.T) {
	app, router, _ := newSecurityTestEnv(t)
	app.Cfg.Storage.DBPath = filepath.Join(t.TempDir(), "packrun.db")
	app.healthCacheFor = time.Hour

	if code, _ := doHealth(t, router); code != http.StatusOK {
		t.Fatalf("首次检查应通过，实际 %d", code)
	}
	// 缓存窗口内即使目录变得不可写，也应复用上一次结果。
	dir := app.Cfg.Storage.PackageDir
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("调整目录权限失败: %v", err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()

	if code, rep := doHealth(t, router); code != http.StatusOK || rep.Checks["packageDir"].Status != "ok" {
		t.Fatalf("缓存窗口内应复用结果，实际 %d / %+v", code, rep.Checks["packageDir"])
	}

	// 关闭缓存后应重新检查并返回 503。
	app.healthCacheFor = 0
	if code, rep := doHealth(t, router); code != http.StatusServiceUnavailable {
		t.Fatalf("关闭缓存后应重新检查，实际 %d / %+v", code, rep.Checks["packageDir"])
	}
}
