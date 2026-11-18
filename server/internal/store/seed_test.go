package store

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/packrun/packrun/internal/config"
	"github.com/packrun/packrun/internal/model"
)

// openStoreTestDB 打开独立的内存库并迁移 Seed 涉及的全部表。
func openStoreTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
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
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := db.AutoMigrate(
		&model.User{}, &model.RecoveryCode{}, &model.Server{}, &model.Directory{}, &model.Script{},
		&model.Package{}, &model.Task{}, &model.TaskStep{}, &model.AuditLog{},
		&model.Project{}, &model.ProjectDirectory{}, &model.ProjectMember{},
	); err != nil {
		t.Fatalf("迁移表结构失败: %v", err)
	}
	return db
}

func newSeedConfig(mode string) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{Mode: mode},
		Storage: config.StorageConfig{
			MaxTotalMB: 20480,
		},
		Bootstrap: config.BootstrapConfig{
			AdminUsername: "admin",
			AdminPassword: "Admin@12345",
		},
	}
}

func TestSeedOnlyCreatesDemoDataInDebug(t *testing.T) {
	t.Run("release", func(t *testing.T) {
		db := openStoreTestDB(t, "seed_release")
		if err := Seed(db, newSeedConfig("release")); err != nil {
			t.Fatalf("Seed 执行失败: %v", err)
		}

		var users, servers, auditors int64
		if err := db.Model(&model.User{}).Count(&users).Error; err != nil {
			t.Fatalf("统计用户失败: %v", err)
		}
		if err := db.Model(&model.User{}).Where("username = ?", "auditor").Count(&auditors).Error; err != nil {
			t.Fatalf("统计演示账号失败: %v", err)
		}
		if err := db.Model(&model.Server{}).Count(&servers).Error; err != nil {
			t.Fatalf("统计服务器失败: %v", err)
		}

		if users != 1 {
			t.Fatalf("release 模式用户数 = %d，期望 1（仅管理员）", users)
		}
		if auditors != 0 {
			t.Fatalf("release 模式不应创建演示账号 auditor，实际 %d 个", auditors)
		}
		if servers != 0 {
			t.Fatalf("release 模式服务器数 = %d，期望 0", servers)
		}
	})

	t.Run("debug", func(t *testing.T) {
		db := openStoreTestDB(t, "seed_debug")
		if err := Seed(db, newSeedConfig("debug")); err != nil {
			t.Fatalf("Seed 执行失败: %v", err)
		}

		var users, servers int64
		if err := db.Model(&model.User{}).Count(&users).Error; err != nil {
			t.Fatalf("统计用户失败: %v", err)
		}
		if err := db.Model(&model.Server{}).Count(&servers).Error; err != nil {
			t.Fatalf("统计服务器失败: %v", err)
		}
		if users != 2 {
			t.Fatalf("debug 模式用户数 = %d，期望 2（admin + auditor）", users)
		}
		if servers != 3 {
			t.Fatalf("debug 模式服务器数 = %d，期望 3", servers)
		}
	})
}
