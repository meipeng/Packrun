// Package notify 把运维告警推送到外部渠道：企业微信群机器人或通用 webhook。
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Level 表示告警级别，仅用于展示与通用格式的字段。
type Level string

const (
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Message 是一条告警。Key 相同的消息在去重窗口内只发送一次，避免故障期间刷屏。
type Message struct {
	Key   string
	Title string
	Body  string
	Level Level
}

// Notifier 是告警发送端抽象，便于替换实现与测试。
type Notifier interface {
	Send(ctx context.Context, m Message) error
}

// Nop 在未配置外部渠道时使用：调用方仍会写服务日志。
type Nop struct{}

func (Nop) Send(context.Context, Message) error { return nil }

// Webhook 通用 webhook 推送，支持企业微信群机器人（wecom）与通用 JSON（generic）两种格式。
type Webhook struct {
	URL         string
	Format      string // wecom | generic
	Client      *http.Client
	MinInterval time.Duration // 同一 Key 的去重窗口

	mu     sync.Mutex
	lastAt map[string]time.Time
}

func NewWebhook(url, format string, minInterval time.Duration) *Webhook {
	if format == "" {
		format = "wecom"
	}
	if minInterval <= 0 {
		minInterval = 10 * time.Minute
	}
	return &Webhook{
		URL:         url,
		Format:      format,
		MinInterval: minInterval,
		Client:      &http.Client{Timeout: 10 * time.Second},
		lastAt:      map[string]time.Time{},
	}
}

// allow 判断该 Key 是否已过去重窗口；窗口内重复消息直接丢弃。
func (w *Webhook) allow(key string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lastAt == nil {
		w.lastAt = map[string]time.Time{}
	}
	if at, ok := w.lastAt[key]; ok && now.Sub(at) < w.MinInterval {
		return false
	}
	w.lastAt[key] = now
	return true
}

func (w *Webhook) Send(ctx context.Context, m Message) error {
	if w.URL == "" {
		return nil
	}
	if !w.allow(m.Key, time.Now()) {
		return nil
	}
	payload, err := w.encode(m)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("webhook 返回状态码 %d", resp.StatusCode)
	}
	return nil
}

// encode 按配置格式生成请求体：企业微信机器人要求 msgtype，通用格式保持扁平结构。
func (w *Webhook) encode(m Message) ([]byte, error) {
	text := m.Title
	if m.Body != "" {
		text += "\n" + m.Body
	}
	if w.Format == "generic" {
		return json.Marshal(map[string]string{
			"source": "packrun",
			"level":  string(m.Level),
			"title":  m.Title,
			"text":   text,
		})
	}
	return json.Marshal(map[string]any{
		"msgtype": "text",
		"text":    map[string]string{"content": "[Packrun] " + text},
	})
}
