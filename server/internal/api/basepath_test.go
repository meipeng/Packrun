package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWithBasePathStripsPrefix(t *testing.T) {
	var gotPath, gotQuery string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	})

	h := WithBasePath(next, "/packrun/")
	req := httptest.NewRequest(http.MethodGet, "/packrun/api/v1/tasks?status=running", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("状态码 = %d，期望 204", w.Code)
	}
	if gotPath != "/api/v1/tasks" {
		t.Fatalf("剥离后路径 = %q，期望 /api/v1/tasks", gotPath)
	}
	if gotQuery != "status=running" {
		t.Fatalf("查询参数 = %q，期望保留", gotQuery)
	}
}

func TestWithBasePathRedirectsRootAndBase(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("重定向请求不应到达下游: %s", r.URL.Path)
	})
	h := WithBasePath(next, "/packrun")

	for _, path := range []string{"/", "/packrun"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != http.StatusMovedPermanently {
			t.Fatalf("%s 状态码 = %d，期望 301", path, w.Code)
		}
		if loc := w.Header().Get("Location"); loc != "/packrun/" {
			t.Fatalf("%s Location = %q，期望 /packrun/", path, loc)
		}
	}
}

func TestWithBasePathAllowsHealthzAndRejectsOutside(t *testing.T) {
	var gotPath string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})
	h := WithBasePath(next, "/packrun")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK || gotPath != "/healthz" {
		t.Fatalf("/healthz 应原样透传，状态码 = %d，路径 = %q", w.Code, gotPath)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/other", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("子路径外请求状态码 = %d，期望 404", w.Code)
	}
}
