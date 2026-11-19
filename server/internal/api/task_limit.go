package api

import (
	"fmt"

	"github.com/packrun/packrun/internal/model"
)

// pendingTaskFallbackLimit 配置缺失或非正数时使用的单用户待执行任务上限。
const pendingTaskFallbackLimit = 10

// pendingTaskStates 未终态任务集合：配额、项目批次运行中判断与脚本引用提示共用。
// 重启中断的「待恢复」任务同样占用未终态配额，需人工继续或标记失败后释放。
var pendingTaskStates = []string{
	model.TaskPending, model.TaskUploading, model.TaskVerifying, model.TaskExecuting, model.TaskInterrupted,
}

// unfinishedTaskStates 与 pendingTaskStates 同源，语义化别名。
var unfinishedTaskStates = pendingTaskStates

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
