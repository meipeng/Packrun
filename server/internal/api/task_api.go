package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/packrun/packrun/internal/deploy"
	"github.com/packrun/packrun/internal/model"
)

// ---------- 任务取消登记 ----------

type sessionRegistry struct {
	mu    sync.Mutex
	items map[uint]context.CancelFunc
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{items: map[uint]context.CancelFunc{}}
}

func (s *sessionRegistry) add(id uint, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[id] = cancel
}

func (s *sessionRegistry) remove(id uint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, id)
}

func (s *sessionRegistry) cancel(id uint) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fn, ok := s.items[id]; ok {
		fn()
		return true
	}
	return false
}

// ---------- 创建任务 ----------

type createTaskReq struct {
	ServerID    uint   `json:"serverId" binding:"required"`
	DirectoryID uint   `json:"directoryId" binding:"required"`
	PackageID   uint   `json:"packageId" binding:"required"`
	PreScriptID *uint  `json:"preScriptId"`
	ScriptID    *uint  `json:"scriptId"`
	OTP         string `json:"otp"`
}

func (a *App) createTask(c *gin.Context) {
	u := currentUser(c)
	if u.Role == model.RoleAuditor {
		forbidden(c, "审计角色不可发起部署")
		return
	}

	var req createTaskReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请选择服务器、目录与安装包")
		return
	}
	if req.OTP == "" {
		req.OTP = c.GetHeader("X-OTP")
	}

	var srv model.Server
	if err := a.DB.First(&srv, req.ServerID).Error; err != nil {
		badRequest(c, "目标服务器不存在")
		return
	}
	if !checkServerScope(u, srv.ID) {
		forbidden(c, "无权操作该服务器")
		return
	}

	var dir model.Directory
	if err := a.DB.First(&dir, req.DirectoryID).Error; err != nil {
		badRequest(c, "部署目录不存在")
		return
	}
	if dir.ServerID != srv.ID {
		badRequest(c, "目录与服务器不匹配")
		return
	}

	var pkg model.Package
	if err := a.DB.First(&pkg, req.PackageID).Error; err != nil {
		badRequest(c, "安装包不存在")
		return
	}
	if !canUsePackage(u, &pkg) {
		forbidden(c, "只能使用自己上传的安装包")
		return
	}

	var preScript *model.Script
	if req.PreScriptID != nil && *req.PreScriptID > 0 {
		var s model.Script
		if err := a.DB.First(&s, *req.PreScriptID).Error; err != nil {
			badRequest(c, "前置校验脚本不存在")
			return
		}
		if s.Phase != model.PhasePreCheck {
			badRequest(c, "前置脚本阶段必须为 precheck")
			return
		}
		preScript = &s
	}
	var script *model.Script
	if req.ScriptID != nil && *req.ScriptID > 0 {
		var s model.Script
		if err := a.DB.First(&s, *req.ScriptID).Error; err != nil {
			badRequest(c, "部署脚本不存在")
			return
		}
		if s.Phase != model.PhasePostDeploy {
			badRequest(c, "后置脚本阶段必须为 postdeploy")
			return
		}
		script = &s
	}

	// 高危操作再认证：生产环境必须校验动态口令
	otpVerified := false
	if srv.Env == model.EnvProd {
		passed, msg := a.verifyStepUpOTP(u, req.OTP)
		if !passed {
			a.audit(c, "task.create", srv.Name, "fail", "", msg, false)
			fail(c, 401, CodeOTPRequired, msg)
			return
		}
		otpVerified = true
	}

	// 资源滥用防护：单用户未终态任务数受配置上限约束。
	if msg, pass := a.checkPendingTaskLimit(u, 1); !pass {
		a.audit(c, "task.create", srv.Name+":"+dir.Path, "fail", "", msg, otpVerified)
		fail(c, http.StatusTooManyRequests, CodeDeployFailed, msg)
		return
	}

	task := model.Task{
		TaskNo:      newTaskNo(),
		ServerID:    srv.ID,
		ServerName:  srv.Name,
		DirectoryID: dir.ID,
		TargetPath:  dir.Path,
		PackageID:   pkg.ID,
		PackageName: pkg.Name,
		PreScriptID: req.PreScriptID,
		ScriptID:    req.ScriptID,
		OperatorID:  u.ID,
		Operator:    u.Username,
		Status:      model.TaskPending,
		Stage:       "queued",
		Progress:    0,
		Message:     "任务已受理，等待执行",
		OTPVerified: otpVerified,
	}
	if preScript != nil {
		task.PreScriptName = preScript.Name
		task.PreScriptVersion = preScript.Version
	}
	if script != nil {
		task.ScriptName = script.Name
		task.ScriptVersion = script.Version
	}
	if err := a.DB.Create(&task).Error; err != nil {
		internal(c, "创建任务失败")
		return
	}

	a.audit(c, "task.create", srv.Name+":"+dir.Path, "ok", task.TaskNo,
		fmt.Sprintf("包=%s 前置脚本=%s(v%d) 后置脚本=%s(v%d) 环境=%s",
			pkg.Name, orNone(task.PreScriptName), task.PreScriptVersion,
			orNone(task.ScriptName), task.ScriptVersion, srv.Env), otpVerified)

	go a.runTask(task.ID)
	ok(c, task)
}

func newTaskNo() string {
	return "D" + time.Now().Format("20060102150405") + strings.ToUpper(randomHex(3))
}

// ---------- 执行 ----------

func (a *App) runTask(id uint) {
	var task model.Task
	if err := a.DB.First(&task, id).Error; err != nil {
		return
	}
	if task.Status != model.TaskPending {
		return
	}

	var srv model.Server
	if err := a.DB.First(&srv, task.ServerID).Error; err != nil {
		a.finishTask(id, model.TaskFailed, "目标服务器已被删除", nil)
		return
	}

	// 同一目录先串行，再按服务器并发上限排队，最后进入全局闸门，避免占槽空等
	releaseDir := a.lockDirectory(task.DirectoryID)
	defer releaseDir()

	releaseServer := a.acquireServerGate(srv.ID, srv.MaxConcurrent)
	defer releaseServer()

	a.Sem <- struct{}{}
	defer func() { <-a.Sem }()

	// 等待期间任务可能已被取消
	if err := a.DB.First(&task, id).Error; err != nil || task.Status != model.TaskPending {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.Sessions.add(id, cancel)
	defer a.Sessions.remove(id)

	var dir model.Directory
	var pkg model.Package
	if err := a.DB.First(&dir, task.DirectoryID).Error; err != nil {
		a.finishTask(id, model.TaskFailed, "部署目录已被删除", nil)
		return
	}
	if dir.Path != task.TargetPath {
		a.finishTask(id, model.TaskFailed, "部署目录配置已变更，请重新发起任务", nil)
		return
	}
	if err := a.DB.First(&pkg, task.PackageID).Error; err != nil {
		a.finishTask(id, model.TaskFailed, "安装包记录已被删除", nil)
		return
	}

	var preScript *model.Script
	if task.PreScriptID != nil {
		var s model.Script
		if err := a.DB.First(&s, *task.PreScriptID).Error; err != nil {
			a.finishTask(id, model.TaskFailed, "前置校验脚本已被删除", nil)
			return
		}
		preScript = &s
	}
	var script *model.Script
	if task.ScriptID != nil {
		var s model.Script
		if err := a.DB.First(&s, *task.ScriptID).Error; err != nil {
			a.finishTask(id, model.TaskFailed, "部署后脚本已被删除", nil)
			return
		}
		script = &s
	}

	now := time.Now()
	a.DB.Model(&model.Task{}).Where("id = ?", id).Updates(map[string]any{
		"status": model.TaskUploading, "stage": "prepare", "started_at": now, "progress": 5,
		"message": "开始执行",
	})

	var mu sync.Mutex
	var current *model.TaskStep
	var currentLog *stepLog
	seq := 1

	hooks := deploy.Hooks{
		OnStep: func(name string) {
			mu.Lock()
			defer mu.Unlock()
			ts := time.Now()
			if current != nil {
				a.DB.Model(current).Updates(map[string]any{"status": "success", "finished_at": ts})
			}
			step := model.TaskStep{TaskID: id, Seq: seq, Name: name, Status: "running", StartedAt: &ts}
			a.DB.Create(&step)
			current = &step
			currentLog = &stepLog{}
			seq++
		},
		OnLog: func(line string) {
			mu.Lock()
			defer mu.Unlock()
			if current == nil || currentLog == nil {
				return
			}
			// 单步日志有界保留（头 8KB + 尾 24KB），超长输出不会把数据库撑爆。
			currentLog.add(line)
			current.Output = currentLog.String()
			a.DB.Model(current).Update("output", current.Output)
		},
		OnStage: func(stage string, pct int) {
			status := model.TaskUploading
			switch stage {
			case "verify":
				status = model.TaskVerifying
			case "precheck", "unpack", "script", "done":
				status = model.TaskExecuting
			}
			a.DB.Model(&model.Task{}).Where("id = ?", id).Updates(map[string]any{
				"stage": stage, "progress": pct, "status": status,
			})
		},
	}

	req := deploy.Request{
		Server: srv, Directory: dir, Package: pkg, PreScript: preScript, Script: script,
		SourcePath: filepath.Join(a.Cfg.Storage.PackageDir, pkg.StorageKey),
		Hooks:      hooks,
	}

	err := a.Deployer.Deploy(ctx, req)

	mu.Lock()
	if current != nil {
		ts := time.Now()
		status := "success"
		if err != nil {
			status = "failed"
		}
		a.DB.Model(current).Updates(map[string]any{"status": status, "finished_at": ts})
	}
	mu.Unlock()

	switch {
	case err == nil:
		a.finishTask(id, model.TaskSuccess, "部署完成", nil)
	case ctx.Err() != nil:
		a.finishTask(id, model.TaskCanceled, "任务已取消", nil)
	default:
		log.Printf("[task] %s 执行失败: %v", task.TaskNo, err)
		a.finishTask(id, model.TaskFailed, err.Error(), &srv)
	}
}

func (a *App) finishTask(id uint, status, message string, srv *model.Server) {
	now := time.Now()
	updates := map[string]any{
		"status": status, "message": message, "finished_at": now,
	}
	if status == model.TaskSuccess {
		updates["progress"] = 100
		updates["stage"] = "done"
	}
	a.DB.Model(&model.Task{}).Where("id = ?", id).Updates(updates)

	var task model.Task
	if err := a.DB.First(&task, id).Error; err != nil {
		return
	}
	result := map[string]string{model.TaskSuccess: "ok", model.TaskCanceled: "ok"}[status]
	if result == "" {
		result = "fail"
	}
	a.DB.Create(&model.AuditLog{
		ActorID: task.OperatorID, Actor: task.Operator,
		Action: "task.finish", Target: task.ServerName + ":" + task.TargetPath,
		Result: result, TaskNo: task.TaskNo, OTPVerified: task.OTPVerified,
		Detail: fmt.Sprintf("包=%s 状态=%s %s", task.PackageName, status, message),
	})

	// 项目发布批次：全部任务终态后结算项目版本
	if task.ProjectID > 0 {
		a.settleProjectBatch(task.ProjectID, task.BatchNo)
	}
}

// ---------- 查询与取消 ----------

func (a *App) listTasks(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}

	q := applyServerScope(a.DB.Model(&model.Task{}), currentUser(c), "server_id")
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("serverId"); v != "" {
		q = q.Where("server_id = ?", v)
	}
	if v := c.Query("projectId"); v != "" {
		q = q.Where("project_id = ?", v)
	}
	if v := c.Query("batchNo"); v != "" {
		q = q.Where("batch_no = ?", v)
	}
	if v := c.Query("keyword"); v != "" {
		like := "%" + v + "%"
		q = q.Where("task_no LIKE ? OR package_name LIKE ? OR operator LIKE ?", like, like, like)
	}

	var total int64
	q.Count(&total)

	var list []model.Task
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		internal(c, "查询任务失败")
		return
	}
	ok(c, gin.H{"items": list, "total": total, "page": page, "pageSize": size})
}

func (a *App) getTask(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var task model.Task
	if err := a.DB.First(&task, id).Error; err != nil {
		notFound(c, "任务不存在")
		return
	}
	if !checkServerScope(currentUser(c), task.ServerID) {
		forbidden(c, "无权查看该任务")
		return
	}
	var steps []model.TaskStep
	a.DB.Where("task_id = ?", id).Order("seq asc").Find(&steps)
	ok(c, gin.H{"task": task, "steps": steps})
}

func (a *App) cancelTask(c *gin.Context) {
	u := currentUser(c)
	if u.Role == model.RoleAuditor {
		forbidden(c, "审计角色不可取消任务")
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	var task model.Task
	if err := a.DB.First(&task, id).Error; err != nil {
		notFound(c, "任务不存在")
		return
	}
	if !checkServerScope(u, task.ServerID) {
		forbidden(c, "无权取消该任务")
		return
	}
	if task.Status == model.TaskSuccess || task.Status == model.TaskFailed || task.Status == model.TaskCanceled {
		badRequest(c, "任务已结束，无法取消")
		return
	}
	if task.Status == model.TaskPending {
		a.finishTask(task.ID, model.TaskCanceled, "任务在排队阶段被取消", nil)
		ok(c, gin.H{"canceled": true})
		return
	}
	if a.Sessions.cancel(task.ID) {
		a.audit(c, "task.cancel", task.TaskNo, "ok", task.TaskNo, "执行中被取消", task.OTPVerified)
		ok(c, gin.H{"canceled": true})
		return
	}
	badRequest(c, "任务进程已结束")
}

// ---------- 概览 ----------

func (a *App) overview(c *gin.Context) {
	u := currentUser(c)
	var serverTotal, serverOnline int64
	applyServerScope(a.DB.Model(&model.Server{}), u, "id").Count(&serverTotal)
	applyServerScope(a.DB.Model(&model.Server{}).Where("status = ?", "online"), u, "id").Count(&serverOnline)

	var pkgCount int64
	a.DB.Model(&model.Package{}).Count(&pkgCount)

	// 服务器资源摘要：scope 内服务器及其最新指标快照。
	var scoped []model.Server
	applyServerScope(a.DB.Select("id, name, env, status"), u, "id").Order("env asc, id asc").Find(&scoped)
	var allMetrics []model.ServerMetric
	a.DB.Find(&allMetrics)
	metricByID := make(map[uint]model.ServerMetric, len(allMetrics))
	for _, m := range allMetrics {
		metricByID[m.ServerID] = m
	}
	type serverMetricView struct {
		ServerName string              `json:"serverName"`
		Env        string              `json:"env"`
		Status     string              `json:"status"`
		Metric     *model.ServerMetric `json:"metric"`
	}
	serverMetrics := make([]serverMetricView, 0, len(scoped))
	for _, s := range scoped {
		view := serverMetricView{ServerName: s.Name, Env: s.Env, Status: s.Status}
		if m, exists := metricByID[s.ID]; exists {
			metric := m
			view.Metric = &metric
		}
		serverMetrics = append(serverMetrics, view)
	}

	// 概览页以「中控机健康 + 服务器资源」为主，任务数据交给任务页，避免首页堆指标。
	ok(c, gin.H{
		"serverTotal":   serverTotal,
		"serverOnline":  serverOnline,
		"packageTotal":  pkgCount,
		"deployMode":    a.Cfg.Deploy.Mode,
		"serverMetrics": serverMetrics,
		"health":        a.buildHealth(),
	})
}

// RecoverInterruptedTasks 把上次进程中断时未完成的任务收口为失败，
// 避免重启后任务永久停留在 uploading / executing 状态。
func RecoverInterruptedTasks(db *gorm.DB) int {
	running := []string{model.TaskPending, model.TaskUploading, model.TaskVerifying, model.TaskExecuting}
	var tasks []model.Task
	if err := db.Where("status IN ?", running).Find(&tasks).Error; err != nil {
		log.Printf("[recover] 查询未完成任务失败: %v", err)
		return 0
	}

	now := time.Now()
	for _, task := range tasks {
		db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
			"status": model.TaskFailed, "message": "服务重启，任务执行中断", "finished_at": now,
		})
		db.Model(&model.TaskStep{}).Where("task_id = ? AND status = ?", task.ID, "running").
			Updates(map[string]any{"status": "failed", "finished_at": now})
		db.Create(&model.AuditLog{
			Actor: "system", Action: "task.recover", Target: task.ServerName + ":" + task.TargetPath,
			Result: "fail", TaskNo: task.TaskNo, Detail: "服务重启，任务被标记为中断失败",
		})
	}
	return len(tasks)
}

func orNone(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}
