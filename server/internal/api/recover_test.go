package api

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/packrun/packrun/internal/model"
)

func newRecoverTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "_")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", name)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.Task{}, &model.TaskStep{}, &model.AuditLog{}, &model.TaskScript{}); err != nil {
		t.Fatalf("迁移表结构失败: %v", err)
	}
	return db
}

// 重启收口：已开始执行的任务进入「待恢复」，排队任务保持排队并返回待入队 ID。
func TestRecoverInterruptedTasks(t *testing.T) {
	db := newRecoverTestDB(t)

	tasks := []model.Task{
		{TaskNo: "R-EXEC", ServerName: "srv-a", TargetPath: "/opt/a", Status: model.TaskExecuting, Stage: "upload"},
		{TaskNo: "R-PENDING", ServerName: "srv-b", TargetPath: "/opt/b", Status: model.TaskPending, Stage: "queued"},
		{TaskNo: "R-OK", ServerName: "srv-c", TargetPath: "/opt/c", Status: model.TaskSuccess, Stage: "done"},
	}
	if err := db.Create(&tasks).Error; err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	if err := db.Create(&model.TaskStep{
		TaskID: tasks[0].ID, Seq: 1, Name: "上传安装包", Status: "running", StartedAt: ptrTime(time.Now()),
	}).Error; err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}

	pendingIDs, interrupted := RecoverInterruptedTasks(db)
	if interrupted != 1 {
		t.Fatalf("应中断收口 1 个任务，实际 %d", interrupted)
	}
	if len(pendingIDs) != 1 || pendingIDs[0] != tasks[1].ID {
		t.Fatalf("排队任务应返回待入队 ID，实际 %v", pendingIDs)
	}

	for _, want := range []struct {
		taskNo string
		status string
	}{
		{"R-EXEC", model.TaskInterrupted},
		{"R-PENDING", model.TaskPending},
		{"R-OK", model.TaskSuccess},
	} {
		var task model.Task
		if err := db.Where("task_no = ?", want.taskNo).First(&task).Error; err != nil {
			t.Fatalf("查询任务 %s 失败: %v", want.taskNo, err)
		}
		if task.Status != want.status {
			t.Fatalf("任务 %s 状态 = %s，期望 %s", want.taskNo, task.Status, want.status)
		}
	}
	var interruptedTask model.Task
	db.Where("task_no = ?", "R-EXEC").First(&interruptedTask)
	if interruptedTask.FinishedAt != nil {
		t.Fatal("待恢复任务不应写 finished_at（尚未结束）")
	}

	var step model.TaskStep
	if err := db.Where("task_id = ?", tasks[0].ID).First(&step).Error; err != nil {
		t.Fatalf("查询步骤失败: %v", err)
	}
	if step.Status != "failed" {
		t.Fatalf("运行中步骤应标记为 failed，实际 %s", step.Status)
	}

	var auditCount int64
	db.Model(&model.AuditLog{}).Where("action = ?", "task.recover").Count(&auditCount)
	if auditCount != 1 {
		t.Fatalf("应写入 1 条 task.recover 审计（仅中断任务），实际 %d", auditCount)
	}
}

func TestRecoverInterruptedTasksNoop(t *testing.T) {
	db := newRecoverTestDB(t)
	pendingIDs, interrupted := RecoverInterruptedTasks(db)
	if interrupted != 0 || len(pendingIDs) != 0 {
		t.Fatalf("无未完成任务时应返回空，实际 pendingIDs=%v interrupted=%d", pendingIDs, interrupted)
	}
}

func ptrTime(t time.Time) *time.Time {
	return &t
}
