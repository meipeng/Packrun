package api

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/packrun/packrun/internal/model"
	"github.com/packrun/packrun/internal/notify"
)

// watchdog 定时巡检运维风险：磁盘余量、包仓库配额、失败任务、证书到期。
// 告警通过 notify 渠道推送，同类消息由 notifier 自己去重。
type watchdog struct {
	app      *App
	notifier notify.Notifier
	interval time.Duration

	mu           sync.Mutex
	alertedTasks map[uint]bool
	alertedCert  map[string]bool
}

// StartWatchdog 启动后台巡检；interval<=0 表示关闭。
func StartWatchdog(app *App, notifier notify.Notifier, interval time.Duration) {
	if interval <= 0 {
		log.Printf("[alert] alert.check_interval_seconds=0，后台巡检未启动")
		return
	}
	if notifier == nil {
		notifier = notify.Nop{}
	}
	w := &watchdog{
		app:          app,
		notifier:     notifier,
		interval:     interval,
		alertedTasks: map[uint]bool{},
		alertedCert:  map[string]bool{},
	}
	target := "仅写服务日志"
	if app.Cfg.Alert.WebhookURL != "" {
		target = "推送到 " + app.Cfg.Alert.Format + " webhook"
	}
	log.Printf("[alert] 巡检已启动，间隔 %s，告警%s", interval, target)
	go func() {
		w.checkOnce()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			w.checkOnce()
		}
	}()
}

func (w *watchdog) checkOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	w.checkDisk(ctx)
	w.checkFailedTasks(ctx)
	w.checkCerts(ctx)
}

// send 推送一条告警；未配置渠道时退化为服务日志，保证信息不丢。
func (w *watchdog) send(ctx context.Context, m notify.Message) {
	if err := w.notifier.Send(ctx, m); err != nil {
		log.Printf("[alert] 推送失败 key=%s: %v", m.Key, err)
		return
	}
	if w.app.Cfg.Alert.WebhookURL == "" {
		log.Printf("[alert] %s：%s %s", m.Level, m.Title, m.Body)
	}
}

// checkDisk 检查包目录所在磁盘剩余空间与包仓库配额占用。
func (w *watchdog) checkDisk(ctx context.Context) {
	dir := w.app.Cfg.Storage.PackageDir
	freeMB, totalMB, err := diskSpaceMB(dir)
	if err != nil {
		w.send(ctx, notify.Message{
			Key: "disk:unreadable", Level: notify.LevelWarn,
			Title: "包目录磁盘空间读取失败", Body: dir,
		})
		return
	}
	if min := w.app.Cfg.Alert.DiskMinFreeMB; min > 0 && freeMB < int64(min) {
		w.send(ctx, notify.Message{
			Key: "disk:free", Level: notify.LevelError,
			Title: "磁盘剩余空间不足",
			Body:  fmt.Sprintf("%s 剩余 %d MB（阈值 %d MB，总容量 %d MB）", dir, freeMB, min, totalMB),
		})
	}
	if max := w.app.Cfg.Storage.MaxTotalMB; max > 0 {
		used, err := packageDirBytes(dir)
		if err != nil {
			return
		}
		pct := float64(used) * 100 / (float64(max) * 1024 * 1024)
		if pct >= 90 {
			w.send(ctx, notify.Message{
				Key: "disk:quota", Level: notify.LevelWarn,
				Title: "包仓库接近配额上限",
				Body:  fmt.Sprintf("已用 %d MB / 上限 %d MB（%.0f%%），可清理历史安装包或调整 storage.max_total_mb", used>>20, max, pct),
			})
		}
	}
}

// checkFailedTasks 把新出现的失败任务各推送一次，按任务 ID 去重。
func (w *watchdog) checkFailedTasks(ctx context.Context) {
	since := time.Now().Add(-2 * w.interval)
	var tasks []model.Task
	if err := w.app.DB.
		Where("status = ? AND COALESCE(finished_at, created_at) >= ?", model.TaskFailed, since).
		Order("id asc").Limit(20).Find(&tasks).Error; err != nil {
		log.Printf("[alert] 查询失败任务出错: %v", err)
		return
	}
	for _, t := range tasks {
		w.mu.Lock()
		seen := w.alertedTasks[t.ID]
		if !seen {
			w.alertedTasks[t.ID] = true
		}
		w.mu.Unlock()
		if seen {
			continue
		}
		body := fmt.Sprintf("任务 %s｜%s → %s｜操作人 %s", t.TaskNo, t.PackageName, t.TargetPath, t.Operator)
		if t.Message != "" {
			body += "\n原因：" + t.Message
		}
		w.send(ctx, notify.Message{
			Key: fmt.Sprintf("task:%d", t.ID), Level: notify.LevelError,
			Title: "部署任务失败", Body: body,
		})
	}
	w.pruneAlertedTasks()
}

// pruneAlertedTasks 避免去重集合无限增长：只保留仍可能被查询到的任务。
func (w *watchdog) pruneAlertedTasks() {
	if len(w.alertedTasks) <= 512 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.alertedTasks = map[uint]bool{}
}

// checkCerts 检查配置里的证书到期时间，剩余天数低于阈值时告警。
func (w *watchdog) checkCerts(ctx context.Context) {
	days := w.app.Cfg.Alert.CertExpireDays
	if days <= 0 {
		return
	}
	for _, path := range w.app.Cfg.Alert.CertFiles {
		notAfter, commonName, err := certExpiry(path)
		if err != nil {
			w.send(ctx, notify.Message{
				Key: "cert:read:" + path, Level: notify.LevelWarn,
				Title: "证书文件读取失败", Body: path + "：" + err.Error(),
			})
			continue
		}
		left := time.Until(notAfter)
		if left > time.Duration(days)*24*time.Hour {
			continue
		}
		w.mu.Lock()
		seen := w.alertedCert[path]
		w.mu.Unlock()
		if seen {
			continue
		}
		w.send(ctx, notify.Message{
			Key: "cert:" + path, Level: notify.LevelError,
			Title: "证书即将到期",
			Body:  fmt.Sprintf("%s（%s）剩余 %d 天，到期时间 %s", path, commonName, int(left.Hours()/24), notAfter.Format("2006-01-02")),
		})
		w.mu.Lock()
		w.alertedCert[path] = true
		w.mu.Unlock()
	}
}

// purgeTaskStepOutputs 清空超过保留天数的任务步骤正文，只保留步骤时间线。
// 步骤输出是最容易让数据库无限增长的部分，历史排障价值随时间快速衰减。
func purgeTaskStepOutputs(app *App, days int) int {
	if days <= 0 {
		return 0
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	var taskIDs []uint
	if err := app.DB.Model(&model.Task{}).
		Where("created_at < ?", cutoff).Limit(5000).Pluck("id", &taskIDs).Error; err != nil {
		log.Printf("[tasks] 查询过期任务失败: %v", err)
		return 0
	}
	if len(taskIDs) == 0 {
		return 0
	}
	res := app.DB.Model(&model.TaskStep{}).
		Where("task_id IN ? AND output <> ?", taskIDs, "").
		Update("output", "")
	if res.Error != nil {
		log.Printf("[tasks] 清理过期步骤日志失败: %v", res.Error)
		return 0
	}
	return int(res.RowsAffected)
}

// StartTaskLogRetentionWorker 启动后清理一次，之后每 24 小时清理一次过期步骤正文。
func StartTaskLogRetentionWorker(app *App, days int) {
	if days <= 0 {
		log.Printf("[tasks] storage.task_log_retention_days=0，任务日志不自动清理")
		return
	}
	go func() {
		if n := purgeTaskStepOutputs(app, days); n > 0 {
			log.Printf("[tasks] 已清空 %d 条超过 %d 天的步骤日志正文", n, days)
		}
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if n := purgeTaskStepOutputs(app, days); n > 0 {
				log.Printf("[tasks] 已清空 %d 条超过 %d 天的步骤日志正文", n, days)
			}
		}
	}()
}
