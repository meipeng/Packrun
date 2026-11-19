package api

import (
	"fmt"

	"github.com/packrun/packrun/internal/model"
)

// pendingTaskFallbackLimit 配置缺失或非正数时使用的单用户待执行任务上限。
const pendingTaskFallbackLimit = 10

// pendingTaskStates 视为「待执行」的任务状态。
var pendingTaskStates = []string{
	model.TaskPending, model.TaskUploading, model.TaskVerifying, model.TaskExecuting,
}

// unfinishedTaskStates 所有未终态状态：配额、项目批次运行中判断与脚本引用提示共用。
var unfinishedTaskStates = append(pendingTaskStates, model.TaskInterrupted)

func (a *App) pendingTaskLimit() int {
	if a.Cfg == nil || a.Cfg.Deploy.MaxPendingPerUser <= 0 {
		return pendingTaskFallbackLimit
	}
	return a.Cfg.Deploy.MaxPendingPerUser
}

// pendingTaskCount 统计用户当前处于未终态的任务数量。
func (a *App) pendingTaskCount(userID uint) int64 {
	var n int64
	a.DB.Model(&model.Task{}).
		Where("operator_id = ? AND status IN ?", userID, pendingTaskStates).Count(&n)
	return n
}

// checkPendingTaskLimit 校验追加 additional 个任务后是否仍在配额内，返回提示与是否通过。
// 并发场景下允许短窗口内轻微超限，不做分布式加锁。
func (a *App) checkPendingTaskLimit(u *model.User, additional int) (string, bool) {
	if u == nil || additional <= 0 {
		return "", true
	}
	limit := a.pendingTaskLimit()
	if a.pendingTaskCount(u.ID)+int64(additional) > int64(limit) {
		return fmt.Sprintf("待执行任务已达上限（%d 个），请等待现有任务完成后再试", limit), false
	}
	return "", true
}
