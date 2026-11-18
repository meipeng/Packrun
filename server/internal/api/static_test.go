package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

func newStaticTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	mountWebUI(r, fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte("<!doctype html><html><head><base href=\"/\" /></head><body><title>packrun</title></body></html>")},
		"assets/app.js": &fstest.MapFile{Data: []byte("console.log('packrun')")},
	}, "/")
	return r
}

func TestStaticServesSPAFallback(t *testing.T) {
	r := newStaticTestRouter()

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/projects/1", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("SPA 路由状态码 = %d，期望 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "packrun") {
		t.Fatalf("SPA 路由未返回 index.html，实际响应 = %s", w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("index.html Cache-Control = %q，期望 no-cache", got)
	}
	if got := w.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") {
		t.Fatalf("index.html 缺少 CSP，实际 = %q", got)
	}
}

func TestStaticServesHashedAsset(t *testing.T) {
	r := newStaticTestRouter()

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("静态资源状态码 = %d，期望 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "console.log") {
		t.Fatalf("静态资源内容错误，实际响应 = %s", w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("assets Cache-Control = %q，期望长缓存", got)
	}
}

func TestStaticMissingAssetNotFound(t *testing.T) {
	r := newStaticTestRouter()

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("缺失静态资源状态码 = %d，期望 404", w.Code)
	}
}

func TestStaticUnknownAPIReturnsJSON404(t *testing.T) {
	r := newStaticTestRouter()

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/missing", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("未知 API 状态码 = %d，期望 404", w.Code)
	}
	var payload struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("未知 API 响应不是 JSON: %v", err)
	}
	if payload.Code != CodeNotFound {
		t.Fatalf("未知 API 错误码 = %d，期望 %d", payload.Code, CodeNotFound)
	}
}

func TestStaticInjectsConfiguredBasePath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	mountWebUI(r, fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><html><head><base href=\"/\" /></head><body>packrun</body></html>")},
	}, "/packrun/")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/projects", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("SPA 路由状态码 = %d，期望 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `<base href="/packrun/" />`) {
		t.Fatalf("index.html 未注入子路径 base，实际响应 = %s", w.Body.String())
	}
}
