package api

import (
	"context"
	"encoding/json"
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
	// 脚本内容随任务固化：执行与展示均以快照为准，脚本后续编辑/删除不影响已受理任务。
	a.snapshotTaskScripts(&task, preScript, script)

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

// ---------- 脚本快照 ----------

// snapshotTaskScripts 把任务引用的脚本内容固化为快照（收口手册 §21.7）：
// 排队/执行期间脚本被编辑或删除，都不影响已受理任务。
func (a *App) snapshotTaskScripts(task *model.Task, preScript, script *model.Script) {
	for phase, s := range map[string]*model.Script{
		model.ScriptPhasePre:  preScript,
		model.ScriptPhasePost: script,
	} {
		if s == nil {
			continue
		}
		if err := a.DB.Create(&model.TaskScript{
			TaskID: task.ID, Phase: phase, ScriptID: s.ID, Name: s.Name,
			Version: s.Version, Content: s.Content, TimeoutSec: s.TimeoutSec, RunAs: s.RunAs,
		}).Error; err != nil {
			log.Printf("[task] %s 脚本快照写入失败 phase=%s: %v", task.TaskNo, phase, err)
		}
	}
}

// loadTaskScripts 读取任务的脚本：优先用受理时固化的快照；
// 无快照的存量任务（功能上线前受理）回退按 ID 现读。
// reason 非空表示引用的脚本已不可用，任务应失败收口。
func (a *App) loadTaskScripts(task *model.Task) (preScript, script *model.Script, reason string) {
	var snaps []model.TaskScript
	a.DB.Where("task_id = ?", task.ID).Order("id asc").Find(&snaps)
	if len(snaps) == 0 {
		if task.PreScriptID != nil {
			var s model.Script
			if err := a.DB.First(&s, *task.PreScriptID).Error; err != nil {
				return nil, nil, "前置校验脚本已被删除"
			}
			preScript = &s
		}
		if task.ScriptID != nil {
			var s model.Script
			if err := a.DB.First(&s, *task.ScriptID).Error; err != nil {
				return nil, nil, "部署后脚本已被删除"
			}
			script = &s
		}
		return preScript, script, ""
	}
	for i := range snaps {
		s := &model.Script{
			ID: snaps[i].ScriptID, Name: snaps[i].Name, Phase: snaps[i].Phase,
			Content: snaps[i].Content, TimeoutSec: snaps[i].TimeoutSec,
			RunAs: snaps[i].RunAs, Version: snaps[i].Version,
		}
		if snaps[i].Phase == model.ScriptPhasePre {
			preScript = s
		} else {
			script = s
		}
	}
	return preScript, script, ""
}

// countUnfinishedScriptRefs 统计未完成任务对该脚本的引用数，用于编辑提示。
func (a *App) countUnfinishedScriptRefs(scriptID uint) int64 {
	var n int64
	a.DB.Model(&model.Task{}).
		Where("(pre_script_id = ? OR script_id = ?) AND status IN ?",
			scriptID, scriptID, unfinishedTaskStates).
		Count(&n)
	return n
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

	// 续跑识别：已有步骤记录说明是中断后的恢复执行（收口手册 §21.6）。
	// 「上次已成功」的步骤本次跳过；校验类步骤始终重新执行。
	var skipSteps map[string]bool
	var stepCount int64
	a.DB.Model(&model.TaskStep{}).Where("task_id = ?", id).Count(&stepCount)
	seq := int(stepCount) + 1
	if stepCount > 0 {
		skipSteps = map[string]bool{}
		var done []model.TaskStep
		a.DB.Where("task_id = ? AND status = ?", id, "success").Order("seq asc").Find(&done)
		skipNames := make([]string, 0, len(done))
		for _, st := range done {
			if _, seen := skipSteps[st.Name]; !seen {
				skipSteps[st.Name] = true
				skipNames = append(skipNames, st.Name)
			}
		}
		// 续跑说明落进时间线：跳过清单可见
		ts := time.Now()
		note := model.TaskStep{TaskID: id, Seq: seq, Name: "续跑校验", Status: "success", StartedAt: &ts, FinishedAt: &ts}
		note.Output = fmt.Sprintf("检测到上次执行中断，续跑跳过已完成步骤：%s", strings.Join(skipNames, "、"))
		a.DB.Create(&note)
		seq++
		a.DB.Model(&model.Task{}).Where("id = ?", id).
			Update("message", "人工恢复执行，跳过已完成步骤："+strings.Join(skipNames, "、"))
	}

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

	// 脚本以受理时快照为准；无快照的存量任务回退按 ID 现读。
	preScript, script, snapReason := a.loadTaskScripts(&task)
	if snapReason != "" {
		a.finishTask(id, model.TaskFailed, snapReason, nil)
		return
	}

	now := time.Now()
	a.DB.Model(&model.Task{}).Where("id = ?", id).Updates(map[string]any{
		"status": model.TaskUploading, "stage": "prepare", "started_at": now, "progress": 5,
		"message": "开始执行",
	})

	var mu sync.Mutex
	var current *model.TaskStep
	var currentLog *stepLog

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

	var manifest deploy.Manifest
	req := deploy.Request{
		Server: srv, Directory: dir, Package: pkg, PreScript: preScript, Script: script,
		SourcePath: filepath.Join(a.Cfg.Storage.PackageDir, pkg.StorageKey),
		Hooks:      hooks,
		TaskRef:    task.TaskNo,
		SkipSteps:  skipSteps,
		Manifest:   &manifest,
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
		if manifest.TotalEntries > 0 {
			if b, mErr := json.Marshal(manifest); mErr == nil {
				a.DB.Model(&model.Task{}).Where("id = ?", id).Update("manifest", string(b))
			}
		}
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
	var scripts []model.TaskScript
	a.DB.Where("task_id = ?", id).Order("id asc").Find(&scripts)
	ok(c, gin.H{"task": task, "steps": steps, "scripts": scripts})
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
	if task.Status == model.TaskInterrupted {
		badRequest(c, "任务已因服务重启中断待恢复，请选择「继续执行」或「标记失败」")
		return
	}
	if a.Sessions.cancel(task.ID) {
		a.audit(c, "task.cancel", task.TaskNo, "ok", task.TaskNo, "执行中被取消", task.OTPVerified)
		ok(c, gin.H{"canceled": true})
		return
	}
	badRequest(c, "任务进程已结束")
}

// deleteTask 移除一条已结束的任务记录（连同步骤日志）。
// 与保留期清理同一口径：只允许删除终态任务，进行中的任务必须先取消；
// 权限与包移除一致：审计员不可删，发布者只能删自己发起的任务。
func (a *App) deleteTask(c *gin.Context) {
	u := currentUser(c)
	if u.Role == model.RoleAuditor {
		forbidden(c, "审计角色不可移除任务")
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	var task model.Task
	if err := a.DB.First(&task, id).Error; err != nil {
		notFound(c, "任务不存在")
		return
	}
	if !checkServerScope(u, task.ServerID) {
		forbidden(c, "无权移除该任务")
		return
	}
	if u.Role != model.RoleAdmin && task.OperatorID != u.ID {
		forbidden(c, "只能移除自己发起的任务")
		return
	}
	switch task.Status {
	case model.TaskSuccess, model.TaskFailed, model.TaskCanceled, model.TaskInterrupted:
	default:
		badRequest(c, "任务仍在进行中，请先取消或恢复处理再移除")
		return
	}
	if err := a.DB.Where("task_id = ?", task.ID).Delete(&model.TaskStep{}).Error; err != nil {
		internal(c, "删除任务步骤失败")
		return
	}
	if err := a.DB.Where("task_id = ?", task.ID).Delete(&model.TaskScript{}).Error; err != nil {
		internal(c, "删除任务脚本快照失败")
		return
	}
	if err := a.DB.Delete(&model.Task{}, task.ID).Error; err != nil {
		internal(c, "移除任务失败")
		return
	}
	a.audit(c, "task.delete", task.TaskNo, "ok", task.TaskNo, "移除已结束任务记录", task.OTPVerified)
	ok(c, gin.H{"deleted": true})
}

// ---------- 中断恢复 ----------

// requireInterruptionHandler 校验任务处于「待恢复」状态且调用者有权处置。
// 返回任务与是否通过校验；审计角色与越权范围直接拒绝。
func (a *App) requireInterruptionHandler(c *gin.Context) (*model.Task, *model.User, bool) {
	u := currentUser(c)
	if u.Role == model.RoleAuditor {
		forbidden(c, "审计角色不可处置中断任务")
		return nil, nil, false
	}
	id, _ := strconv.Atoi(c.Param("id"))
	var task model.Task
	if err := a.DB.First(&task, id).Error; err != nil {
		notFound(c, "任务不存在")
		return nil, nil, false
	}
	if !checkServerScope(u, task.ServerID) {
		forbidden(c, "无权处置该任务")
		return nil, nil, false
	}
	if task.Status != model.TaskInterrupted {
		badRequest(c, "只有「待恢复」状态的任务可以继续执行或标记失败")
		return nil, nil, false
	}
	return &task, u, true
}

// resumeTask 人工恢复一个重启中断的任务：重新入队执行，
// 上次已成功的步骤自动跳过，校验类步骤重新执行。
func (a *App) resumeTask(c *gin.Context) {
	task, _, pass := a.requireInterruptionHandler(c)
	if !pass {
		return
	}
	a.DB.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status": model.TaskPending, "stage": "queued", "progress": 0,
		"message": "人工恢复，重新排队执行（已完成步骤自动跳过）", "finished_at": nil,
	})
	a.audit(c, "task.resume", task.TaskNo, "ok", task.TaskNo, "人工恢复中断任务，重新入队", task.OTPVerified)
	go a.runTask(task.ID)
	ok(c, gin.H{"resumed": true})
}

// markTaskFailed 把重启中断的任务人工标记为失败收口，用于放弃续跑的场景。
func (a *App) markTaskFailed(c *gin.Context) {
	task, _, pass := a.requireInterruptionHandler(c)
	if !pass {
		return
	}
	a.finishTask(task.ID, model.TaskFailed, "人工确认：中断任务不再续跑，标记为失败", nil)
	a.audit(c, "task.markfailed", task.TaskNo, "ok", task.TaskNo, "中断任务人工标记失败", task.OTPVerified)
	ok(c, gin.H{"marked": true})
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

// RecoverInterruptedTasks 上次进程中断的启动收口：
//   - 已开始执行（uploading/verifying/executing）→ interrupted 待恢复，
//     等待人工「继续执行」或「标记失败」，不再一律判失败；
//   - 仍在排队（pending）→ 保持不变，返回 ID 列表由装配层在服务就绪后重新入队。
//
// 返回（待重新入队的任务 ID 列表, 中断收口条数）。
func RecoverInterruptedTasks(db *gorm.DB) ([]uint, int) {
	var tasks []model.Task
	if err := db.Where("status IN ?", []string{model.TaskUploading, model.TaskVerifying, model.TaskExecuting}).
		Find(&tasks).Error; err != nil {
		log.Printf("[recover] 查询未完成任务失败: %v", err)
		return nil, 0
	}

	var pendingIDs []uint
	if err := db.Model(&model.Task{}).Where("status = ?", model.TaskPending).
		Order("id asc").Pluck("id", &pendingIDs).Error; err != nil {
		log.Printf("[recover] 查询排队任务失败: %v", err)
		pendingIDs = nil
	}

	now := time.Now()
	for _, task := range tasks {
		db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
			"status": model.TaskInterrupted,
			"message": "服务重启，任务执行中断；可在任务详情选择「继续执行」或「标记失败」",
		})
		db.Model(&model.TaskStep{}).Where("task_id = ? AND status = ?", task.ID, "running").
			Updates(map[string]any{"status": "failed", "finished_at": now})
		db.Create(&model.AuditLog{
			Actor: "system", Action: "task.recover", Target: task.ServerName + ":" + task.TargetPath,
			Result: "fail", TaskNo: task.TaskNo, Detail: "服务重启，任务进入待恢复状态",
		})
	}
	return pendingIDs, len(tasks)
}

// RequeuePendingTasks 进程重启后重新入队仍在排队的任务（从未开始执行，无恢复语义）。
func RequeuePendingTasks(app *App, ids []uint) {
	for _, id := range ids {
		go app.runTask(id)
	}
}

func orNone(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}
