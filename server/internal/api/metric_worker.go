package api

import (
	"context"
	"log"
	"time"

	"github.com/packrun/packrun/internal/deploy"
	"github.com/packrun/packrun/internal/model"
)

// StartMetricWorker 启动后台指标采集：按 interval 轮询全部服务器并落库最新快照。
// 后台采集面向系统而非用户数据范围，不写审计日志避免噪音；interval<=0 或
// 执行器不支持采集时不开启。
func StartMetricWorker(app *App, interval time.Duration) {
	if interval <= 0 {
		return
	}
	prober, ok := app.Deployer.(deploy.Prober)
	if !ok {
		return
	}
	go func() {
		log.Printf("[metrics] 后台指标采集已启动，间隔 %s", interval)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			collectAllServers(app, prober)
		}
	}()
}

// collectAllServers 顺序采集全部服务器，避免并发压库；单台 30 秒超时。
func collectAllServers(app *App, prober deploy.Prober) {
	var servers []model.Server
	if err := app.DB.Find(&servers).Error; err != nil {
		log.Printf("[metrics] 查询服务器失败: %v", err)
		return
	}
	for _, srv := range servers {
		collectOneServer(app, prober, srv)
	}
}

func collectOneServer(app *App, prober deploy.Prober, srv model.Server) {
	var dirs []string
	app.DB.Model(&model.Directory{}).Where("server_id = ?", srv.ID).Pluck("path", &dirs)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	metric, err := prober.Probe(ctx, srv, dirs)

	status := "online"
	if err != nil {
		status = "offline"
	}
	app.DB.Model(&model.Server{}).Where("id = ?", srv.ID).Update("status", status)

	if metric == nil {
		metric = &model.ServerMetric{
			ServerID: srv.ID, Status: status, Message: err.Error(),
			ProbeMode: app.Cfg.Deploy.Mode, ProbedAt: time.Now(),
		}
	}
	upsertServerMetric(app.DB, metric)
}
