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
	"github.com/packrun/packrun/internal/notify"
)

func newDedupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "_")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", name)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.AlertDedup{}); err != nil {
		t.Fatalf("迁移表结构失败: %v", err)
	}
	return db
}

func TestGormDedupStoreWindow(t *testing.T) {
	store := &GormDedupStore{DB: newDedupTestDB(t)}
	now := time.Now()

	if !store.Allow("disk:free", now, 10*time.Minute) {
		t.Fatal("首次发送应放行")
	}
	if store.Allow("disk:free", now.Add(5*time.Minute), 10*time.Minute) {
		t.Fatal("窗口内重复发送应被拦截")
	}
	if !store.Allow("disk:free", now.Add(11*time.Minute), 10*time.Minute) {
		t.Fatal("过窗口后应放行")
	}
}

func TestGormDedupStoreOnce(t *testing.T) {
	store := &GormDedupStore{DB: newDedupTestDB(t)}
	now := time.Now()

	if !store.Allow("task:7", now, 0) {
		t.Fatal("首次发送应放行")
	}
	// window<=0：记录存在即拒绝，跨多久都不再发送（一次性告警）
	if store.Allow("task:7", now.Add(72*time.Hour), 0) {
		t.Fatal("一次性去重不应放行重复发送")
	}
	if !store.Allow("task:8", now, 0) {
		t.Fatal("不同 Key 应互不影响")
	}
}

func TestGormDedupStoreSurvivesReopen(t *testing.T) {
	db := newDedupTestDB(t)
	store := &GormDedupStore{DB: db}
	now := time.Now()
	store.Allow("cert:/etc/nginx/fullchain.pem", now, 0)

	// 模拟进程重启：同一个库重新构造存储实例
	reopened := &GormDedupStore{DB: db}
	if reopened.Allow("cert:/etc/nginx/fullchain.pem", now.Add(time.Hour), 0) {
		t.Fatal("重启后同一事件的告警应仍被去重拦截")
	}
}

func TestPurgeAlertDedup(t *testing.T) {
	app := &App{DB: newDedupTestDB(t)}
	old := time.Now().Add(-8 * 24 * time.Hour)
	app.DB.Create(&model.AlertDedup{Key: "task:1", LastAt: old})
	app.DB.Create(&model.AlertDedup{Key: "task:2", LastAt: time.Now()})

	if n := purgeAlertDedup(app, 7*24*time.Hour); n != 1 {
		t.Fatalf("应清理 1 条过期去重记录，实际 %d", n)
	}
	var left int64
	app.DB.Model(&model.AlertDedup{}).Count(&left)
	if left != 1 {
		t.Fatalf("清理后应剩余 1 条，实际 %d", left)
	}
}

func TestNotifyMemoryDedup(t *testing.T) {
	d := notify.NewMemoryDedup()
	now := time.Now()
	if !d.Allow("k", now, time.Minute) || d.Allow("k", now.Add(30*time.Second), time.Minute) {
		t.Fatal("进程内去重窗口判定不符合预期")
	}
	if !d.Allow("k", now.Add(2*time.Minute), time.Minute) {
		t.Fatal("过窗口后应放行")
	}
}
