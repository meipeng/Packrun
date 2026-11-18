package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// recorder 记录收到的 webhook 请求体，便于断言格式与次数。
type recorder struct {
	mu     sync.Mutex
	bodies [][]byte
	status int
}

func (r *recorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.bodies = append(r.bodies, body)
		r.mu.Unlock()
		status := r.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
	}
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

func (r *recorder) last() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bodies) == 0 {
		return nil
	}
	return r.bodies[len(r.bodies)-1]
}

func TestWebhookDeduplicatesWithinWindow(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()

	wh := NewWebhook(srv.URL, "wecom", time.Minute)
	msg := Message{Key: "disk:free", Title: "磁盘剩余空间不足", Body: "剩余 100 MB", Level: LevelError}
	for i := 0; i < 3; i++ {
		if err := wh.Send(context.Background(), msg); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
	}
	if rec.count() != 1 {
		t.Fatalf("去重窗口内只应发送 1 次，实际 %d 次", rec.count())
	}

	// 不同 Key 不受影响
	other := Message{Key: "task:1", Title: "部署任务失败", Level: LevelError}
	if err := wh.Send(context.Background(), other); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if rec.count() != 2 {
		t.Fatalf("不同 Key 应各发一次，实际 %d 次", rec.count())
	}
}

func TestWebhookWecomFormat(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()

	wh := NewWebhook(srv.URL, "wecom", time.Minute)
	if err := wh.Send(context.Background(), Message{Key: "k", Title: "标题", Body: "正文", Level: LevelWarn}); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	var payload struct {
		MsgType string `json:"msgtype"`
		Text    struct {
			Content string `json:"content"`
		} `json:"text"`
	}
	if err := json.Unmarshal(rec.last(), &payload); err != nil {
		t.Fatalf("解析请求体失败: %v", err)
	}
	if payload.MsgType != "text" || payload.Text.Content == "" {
		t.Fatalf("企业微信格式不符: %s", rec.last())
	}
	if want := "[Packrun] 标题\n正文"; payload.Text.Content != want {
		t.Fatalf("内容 = %q，期望 %q", payload.Text.Content, want)
	}
}

func TestWebhookGenericFormat(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()

	wh := NewWebhook(srv.URL, "generic", time.Minute)
	if err := wh.Send(context.Background(), Message{Key: "k", Title: "标题", Level: LevelInfo}); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal(rec.last(), &payload); err != nil {
		t.Fatalf("解析请求体失败: %v", err)
	}
	if payload["source"] != "packrun" || payload["level"] != "info" || payload["title"] != "标题" {
		t.Fatalf("通用格式不符: %v", payload)
	}
}

func TestWebhookReportsNon2xx(t *testing.T) {
	rec := &recorder{status: http.StatusInternalServerError}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()

	wh := NewWebhook(srv.URL, "wecom", time.Minute)
	if err := wh.Send(context.Background(), Message{Key: "k", Title: "t"}); err == nil {
		t.Fatalf("非 2xx 应返回错误")
	}
}

func TestWebhookWithoutURLIsNoop(t *testing.T) {
	wh := NewWebhook("", "wecom", time.Minute)
	if err := wh.Send(context.Background(), Message{Key: "k", Title: "t"}); err != nil {
		t.Fatalf("未配置地址时不应报错: %v", err)
	}
}
