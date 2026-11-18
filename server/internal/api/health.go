package api

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/packrun/packrun/internal/model"
)

// Version 由构建时注入（Makefile 的 -ldflags "-X ...api.Version=..."），默认 dev。
var Version = "dev"

// processStart 用于在健康检查里汇报进程运行时长。
var processStart = time.Now()

// healthCheck 是单项检查结果：ok / warn 不会导致整体不可用，fail 会返回 503。
// Detail 面向人读，Metrics 面向页面渲染（单位统一为 MB 或百分比），两者并存。
type healthCheck struct {
	Status    string           `json:"status"`
	Detail    string           `json:"detail,omitempty"`
	LatencyMS int64            `json:"latencyMs,omitempty"`
	Metrics   map[string]int64 `json:"metrics,omitempty"`
}

// healthReport 是对外暴露的探针结果：只保留可用性信息，
// 不透露运行模式与部署方式，避免未鉴权接口泄露部署拓扑。
type healthReport struct {
	Status    string                 `json:"status"`
	Version   string                 `json:"version"`
	UptimeSec int64                  `json:"uptimeSec"`
	Time      string                 `json:"time"`
	Checks    map[string]healthCheck `json:"checks"`
}

// healthz 深检查：不仅证明进程活着，还要证明数据库可用可写、包目录可写、磁盘尚有空间。
// 任一硬性检查失败返回 503，便于负载均衡与监控直接判定不可用。
func (a *App) healthz(c *gin.Context) {
	report := a.buildHealth()
	status := http.StatusOK
	if report.Status != "ok" {
		status = http.StatusServiceUnavailable
	}
	c.JSON(status, report)
}

// buildHealth 带短缓存的健康检查：负载均衡与概览页可能几秒一次轮询，
// 每次都跑 PRAGMA quick_check 与目录遍历代价过高，缓存窗口内直接复用上次结果。
func (a *App) buildHealth() healthReport {
	a.healthMu.Lock()
	defer a.healthMu.Unlock()
	if a.healthCacheFor > 0 && !a.healthAt.IsZero() && time.Since(a.healthAt) < a.healthCacheFor {
		return a.healthCache
	}
	report := a.buildHealthReport()
	a.healthCache = report
	a.healthAt = time.Now()
	return report
}

func (a *App) buildHealthReport() healthReport {
	checks := map[string]healthCheck{
		"database":   a.checkDatabase(),
		"packageDir": a.checkPackageDir(),
		"disk":       a.checkDisk(),
		"tasks":      a.checkRecentTasks(),
	}
	status := "ok"
	for _, ch := range checks {
		if ch.Status == "fail" {
			status = "degraded"
			break
		}
	}
	return healthReport{
		Status:    status,
		Version:   Version,
		UptimeSec: int64(time.Since(processStart).Seconds()),
		Time:      time.Now().Format(time.RFC3339),
		Checks:    checks,
	}
}

// checkDatabase 验证连接可用、SQLite 完整性检查通过、库文件所在目录可写。
func (a *App) checkDatabase() healthCheck {
	start := time.Now()
	ch := healthCheck{Status: "ok"}
	defer func() { ch.LatencyMS = time.Since(start).Milliseconds() }()

	sqlDB, err := a.DB.DB()
	if err != nil {
		ch.Status, ch.Detail = "fail", "无法获取数据库连接"
		return ch
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		ch.Status, ch.Detail = "fail", "数据库连接不可用"
		return ch
	}
	// quick_check 只读，不会改写数据；损坏时能第一时间暴露，而不是等到部署失败。
	var result string
	if err := a.DB.Raw("PRAGMA quick_check(1)").Row().Scan(&result); err != nil {
		ch.Status, ch.Detail = "fail", "数据库完整性检查失败"
		return ch
	}
	if result != "ok" {
		ch.Status, ch.Detail = "fail", "数据库完整性检查未通过："+result
		return ch
	}
	if dir := filepath.Dir(a.Cfg.Storage.DBPath); dir != "" {
		if err := probeWritable(dir); err != nil {
			ch.Status, ch.Detail = "fail", "数据库目录不可写："+dir
			return ch
		}
	}
	return ch
}

// checkPackageDir 验证包仓库可写，并汇报配额占用；超过 90% 记为 warn。
func (a *App) checkPackageDir() healthCheck {
	dir := a.Cfg.Storage.PackageDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return healthCheck{Status: "fail", Detail: "包目录不可用：" + dir}
	}
	if err := probeWritable(dir); err != nil {
		return healthCheck{Status: "fail", Detail: "包目录不可写：" + dir}
	}
	used, err := packageDirBytes(dir)
	if err != nil {
		return healthCheck{Status: "fail", Detail: "包目录统计失败"}
	}
	usedMB := used >> 20
	ch := healthCheck{Status: "ok", Detail: fmt.Sprintf("已用 %d MB", usedMB)}
	if a.Cfg.Storage.MaxTotalMB > 0 {
		pct := float64(used) * 100 / (float64(a.Cfg.Storage.MaxTotalMB) * 1024 * 1024)
		ch.Detail = fmt.Sprintf("已用 %d MB / 上限 %d MB（%.0f%%）", usedMB, a.Cfg.Storage.MaxTotalMB, pct)
		ch.Metrics = map[string]int64{
			"usedMB": usedMB,
			"maxMB":  int64(a.Cfg.Storage.MaxTotalMB),
			"pct":    int64(pct + 0.5),
		}
		if pct >= 90 {
			ch.Status = "warn"
			ch.Detail += "，接近配额上限"
		}
	}
	return ch
}

// checkDisk 检查包目录所在磁盘的剩余空间，低于配置阈值记为 fail。
func (a *App) checkDisk() healthCheck {
	dir := a.Cfg.Storage.PackageDir
	if _, err := os.Stat(dir); err != nil {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return healthCheck{Status: "fail", Detail: "无法访问包目录"}
		}
	}
	freeMB, totalMB, err := diskSpaceMB(dir)
	if err != nil {
		return healthCheck{Status: "warn", Detail: "磁盘空间读取失败"}
	}
	ch := healthCheck{Status: "ok", Detail: fmt.Sprintf("剩余 %d MB / 共 %d MB", freeMB, totalMB)}
	if totalMB > 0 {
		usedPct := int64((float64(totalMB-freeMB)*100)/float64(totalMB) + 0.5)
		ch.Metrics = map[string]int64{"freeMB": freeMB, "totalMB": totalMB, "usedPct": usedPct}
	}
	if min := a.Cfg.Alert.DiskMinFreeMB; min > 0 && freeMB < int64(min) {
		ch.Status = "fail"
		ch.Detail = fmt.Sprintf("剩余 %d MB，低于阈值 %d MB", freeMB, min)
	}
	return ch
}

// checkRecentTasks 统计最近一小时失败的任务数，只作为 warn 提示，不影响可用性判定。
func (a *App) checkRecentTasks() healthCheck {
	var failed int64
	since := time.Now().Add(-time.Hour)
	if err := a.DB.Model(&model.Task{}).
		Where("status = ? AND COALESCE(finished_at, created_at) >= ?", model.TaskFailed, since).
		Count(&failed).Error; err != nil {
		return healthCheck{Status: "warn", Detail: "任务统计失败"}
	}
	if failed > 0 {
		return healthCheck{Status: "warn", Detail: fmt.Sprintf("最近 1 小时 %d 个任务失败", failed)}
	}
	return healthCheck{Status: "ok", Detail: "最近 1 小时无失败任务"}
}

// probeWritable 通过创建并删除临时文件验证目录可写。
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".packrun-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

// diskSpaceMB 返回指定路径所在文件系统的剩余与总容量（MB）。
func diskSpaceMB(path string) (freeMB, totalMB int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	block := int64(st.Bsize)
	totalMB = int64(st.Blocks) * block >> 20
	freeMB = int64(st.Bavail) * block >> 20
	return freeMB, totalMB, nil
}

// certExpiry 读取 PEM 证书文件，返回最早到期的那张证书的到期时间与主体。
func certExpiry(path string) (time.Time, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, "", err
	}
	var earliest time.Time
	var subject string
	for {
		var block *pem.Block
		block, raw = pem.Decode(raw)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return time.Time{}, "", err
		}
		if earliest.IsZero() || cert.NotAfter.Before(earliest) {
			earliest = cert.NotAfter
			subject = cert.Subject.CommonName
		}
	}
	if earliest.IsZero() {
		return time.Time{}, "", fmt.Errorf("未在 %s 中找到证书", path)
	}
	return earliest, subject, nil
}
