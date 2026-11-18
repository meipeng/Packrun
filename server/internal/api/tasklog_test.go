package api

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/packrun/packrun/internal/model"
	"github.com/packrun/packrun/internal/notify"
)

func TestStepLogKeepsHeadAndTail(t *testing.T) {
	var l stepLog
	l.add("first-line")
	for i := 0; i < 5000; i++ {
		l.add(fmt.Sprintf("line-%05d-%s", i, strings.Repeat("x", 40)))
	}
	out := l.String()

	if !strings.Contains(out, "first-line") {
		t.Fatalf("头部日志应保留")
	}
	if !strings.Contains(out, "line-04999") {
		t.Fatalf("最新日志应保留")
	}
	if !strings.Contains(out, "已省略") {
		t.Fatalf("被截断时应标注省略量")
	}
	// 头部 8KB + 尾部 24KB + 一行说明，允许 1KB 余量。
	if limit := stepLogHeadBytes + stepLogTailBytes + 1024; len(out) > limit {
		t.Fatalf("日志体积 %d 超过上限 %d", len(out), limit)
	}
}

func TestStepLogClampsLongLine(t *testing.T) {
	var l stepLog
	l.add(strings.Repeat("汉", 5000)) // 15000 字节，超过单行上限
	line := l.head[0]
	if len(line) > stepLogLineBytes+64 {
		t.Fatalf("单行未被截断，长度 %d", len(line))
	}
	if !utf8.ValidString(line) {
		t.Fatalf("截断后出现非法 UTF-8")
	}
	if !strings.Contains(line, "本行过长已截断") {
		t.Fatalf("缺少截断提示")
	}
}

func TestPurgeTaskStepOutputs(t *testing.T) {
	app, _, db := newSecurityTestEnv(t)

	oldTask := model.Task{TaskNo: "T-OLD", Status: model.TaskSuccess}
	if err := db.Create(&oldTask).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	// CreatedAt 由 GORM 自动写入，这里改成 120 天前。
	if err := db.Model(&oldTask).UpdateColumn("created_at", time.Now().AddDate(0, 0, -120)).Error; err != nil {
		t.Fatalf("调整创建时间失败: %v", err)
	}
	step := model.TaskStep{TaskID: oldTask.ID, Seq: 1, Name: "上传安装包", Status: "success", Output: "大量日志"}
	if err := db.Create(&step).Error; err != nil {
		t.Fatalf("创建步骤失败: %v", err)
	}

	freshTask := model.Task{TaskNo: "T-NEW", Status: model.TaskSuccess}
	if err := db.Create(&freshTask).Error; err != nil {
		t.Fatalf("创建新任务失败: %v", err)
	}
	freshStep := model.TaskStep{TaskID: freshTask.ID, Seq: 1, Name: "上传安装包", Status: "success", Output: "保留我"}
	if err := db.Create(&freshStep).Error; err != nil {
		t.Fatalf("创建新步骤失败: %v", err)
	}

	if n := purgeTaskStepOutputs(app, 90); n != 1 {
		t.Fatalf("应只清理 1 条过期步骤正文，实际 %d", n)
	}

	var gotOld, gotFresh model.TaskStep
	db.First(&gotOld, step.ID)
	if gotOld.Output != "" {
		t.Fatalf("过期步骤正文应清空，实际 %q", gotOld.Output)
	}
	if gotOld.Name != "上传安装包" {
		t.Fatalf("步骤记录本身应保留")
	}
	db.First(&gotFresh, freshStep.ID)
	if gotFresh.Output != "保留我" {
		t.Fatalf("未过期步骤不应被清理")
	}

	// 关闭清理（0 天）时不做任何修改
	if n := purgeTaskStepOutputs(app, 0); n != 0 {
		t.Fatalf("关闭清理时不应改动数据，实际 %d", n)
	}
}

// fakeNotifier 记录被推送的消息，用于验证巡检逻辑与去重。
type fakeNotifier struct {
	msgs []notify.Message
	err  error
}

func (f *fakeNotifier) Send(_ context.Context, m notify.Message) error {
	f.msgs = append(f.msgs, m)
	return f.err
}

func newTestWatchdog(app *App, notifier notify.Notifier) *watchdog {
	return &watchdog{
		app:          app,
		notifier:     notifier,
		interval:     time.Minute,
		alertedTasks: map[uint]bool{},
		alertedCert:  map[string]bool{},
	}
}

func TestWatchdogAlertsFailedTaskOnce(t *testing.T) {
	app, _, db := newSecurityTestEnv(t)
	task := model.Task{
		TaskNo: "T-FAIL", Status: model.TaskFailed, Operator: "alice",
		PackageName: "app.zip", TargetPath: "/opt/app", Message: "目标目录不可写",
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	fake := &fakeNotifier{}
	w := newTestWatchdog(app, fake)
	w.checkFailedTasks(context.Background())
	w.checkFailedTasks(context.Background())

	if len(fake.msgs) != 1 {
		t.Fatalf("同一任务只应告警一次，实际 %d 次", len(fake.msgs))
	}
	if !strings.Contains(fake.msgs[0].Body, "T-FAIL") || !strings.Contains(fake.msgs[0].Body, "目标目录不可写") {
		t.Fatalf("告警内容缺少任务信息: %+v", fake.msgs[0])
	}
}

func TestWatchdogAlertsExpiringCertOnce(t *testing.T) {
	app, _, _ := newSecurityTestEnv(t)
	path := writeTestCert(t, time.Now().Add(3*24*time.Hour))
	app.Cfg.Alert.CertFiles = []string{path}
	app.Cfg.Alert.CertExpireDays = 14

	fake := &fakeNotifier{}
	w := newTestWatchdog(app, fake)
	w.checkCerts(context.Background())
	w.checkCerts(context.Background())

	if len(fake.msgs) != 1 {
		t.Fatalf("即将到期的证书应只告警一次，实际 %d 次", len(fake.msgs))
	}
	if !strings.Contains(fake.msgs[0].Body, "剩余") {
		t.Fatalf("告警应包含剩余天数: %+v", fake.msgs[0])
	}

	// 到期天数足够远时不告警
	app.Cfg.Alert.CertFiles = []string{writeTestCert(t, time.Now().Add(200*24*time.Hour))}
	fake2 := &fakeNotifier{}
	newTestWatchdog(app, fake2).checkCerts(context.Background())
	if len(fake2.msgs) != 0 {
		t.Fatalf("远期证书不应告警，实际 %+v", fake2.msgs)
	}
}

func writeTestCert(t *testing.T, notAfter time.Time) string {
	t.Helper()
	path := t.TempDir() + "/cert.pem"
	if err := os.WriteFile(path, selfSignedPEM(t, "deploy.example.com", notAfter), 0o600); err != nil {
		t.Fatalf("写入证书失败: %v", err)
	}
	return path
}
