package store

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/packrun/packrun/internal/auth"
	"github.com/packrun/packrun/internal/config"
	"github.com/packrun/packrun/internal/model"
)

// Open 建立本地持久化连接（中控机单机场景，无需外部数据库）。
func Open(cfg *config.Config) (*gorm.DB, error) {
	for _, dir := range []string{
		filepath.Dir(cfg.Storage.DBPath),
		cfg.Storage.PackageDir,
		cfg.Storage.SandboxRoot,
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", cfg.Storage.DBPath)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}

	// 数据库文件仅允许当前用户读写。
	if err := os.Chmod(cfg.Storage.DBPath, 0o600); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("设置数据库文件权限失败: %w", err)
	}

	if err := db.AutoMigrate(
		&model.User{}, &model.RecoveryCode{}, &model.Server{}, &model.Directory{}, &model.Script{},
		&model.Package{}, &model.Task{}, &model.TaskStep{}, &model.TaskScript{}, &model.AuditLog{},
		&model.Project{}, &model.ProjectDirectory{}, &model.ProjectMember{},
		&model.ServerMetric{}, &model.AlertDedup{},
	); err != nil {
		return nil, err
	}

	return db, nil
}

// Seed 首次启动写入管理员与一批演示配置，便于控制台直接可用。
func Seed(db *gorm.DB, cfg *config.Config) error {
	var userCount int64
	if err := db.Model(&model.User{}).Count(&userCount).Error; err != nil {
		return err
	}
	if userCount == 0 {
		hash, err := auth.HashPassword(cfg.Bootstrap.AdminPassword)
		if err != nil {
			return err
		}
		admin := model.User{
			Username:     cfg.Bootstrap.AdminUsername,
			DisplayName:  "平台管理员",
			PasswordHash: hash,
			Role:         model.RoleAdmin,
			ServerScope:  "*",
			Status:       "active",
		}
		if err := db.Create(&admin).Error; err != nil {
			return err
		}
		log.Printf("[bootstrap] 已创建管理员账号 %s（请登录后立即绑定动态口令）", admin.Username)

		// 演示账号仅在 debug 模式创建，避免非调试环境出现固定口令账号。
		if cfg.Server.Mode == "debug" {
			auditorHash, _ := auth.HashPassword("Audit@12345")
			db.Create(&model.User{
				Username:     "auditor",
				DisplayName:  "审计员",
				PasswordHash: auditorHash,
				Role:         model.RoleAuditor,
				ServerScope:  "*",
				Status:       "active",
			})
		}
	}

	// 演示服务器、目录与脚本仅在 debug 模式创建。
	if cfg.Server.Mode == "debug" {
		var serverCount int64
		if err := db.Model(&model.Server{}).Count(&serverCount).Error; err != nil {
			return err
		}
		if serverCount == 0 {
			servers := []model.Server{
				{Name: "app-prod-01", Env: model.EnvProd, Host: "10.0.10.11", Port: 22, Username: "deploy",
					AuthType: "key", CredentialRef: "file:~/.ssh/id_ed25519", MaxConcurrent: 1, Remark: "生产应用节点"},
				{Name: "app-test-01", Env: model.EnvTest, Host: "10.0.20.21", Port: 22, Username: "deploy",
					AuthType: "key", CredentialRef: "env:PACKRUN_TEST_SSH_KEY", MaxConcurrent: 2, Remark: "测试环境"},
				{Name: "job-dev-01", Env: model.EnvDev, Host: "127.0.0.1", Port: 22, Username: "dev",
					AuthType: "password", CredentialRef: "env:PACKRUN_DEV_SSH_PASSWORD", MaxConcurrent: 2, Remark: "开发机"},
			}
			if err := db.Create(&servers).Error; err != nil {
				return err
			}
			dirs := []model.Directory{
				{ServerID: servers[0].ID, Path: "/opt/apps/order-service", Owner: "app", AllowOverwrite: true, BackupBeforeDeploy: true, KeepReleases: 5, MaxSizeMB: 512},
				{ServerID: servers[1].ID, Path: "/opt/apps/order-service", Owner: "app", AllowOverwrite: true, BackupBeforeDeploy: true, KeepReleases: 3, MaxSizeMB: 512},
				{ServerID: servers[2].ID, Path: "/opt/apps/demo", Owner: "dev", AllowOverwrite: true, BackupBeforeDeploy: false, KeepReleases: 1, MaxSizeMB: 256},
			}
			db.Create(&dirs)

			scripts := []model.Script{
				{Name: "重启应用", Phase: model.PhasePostDeploy, TimeoutSec: 60, RunAs: "app",
					Description: "部署完成后重启服务并等待健康检查",
					Content:     "systemctl restart order-service\nfor i in $(seq 1 30); do curl -fsS http://127.0.0.1:8081/healthz && exit 0; sleep 1; done\nexit 1"},
				{Name: "校验包结构", Phase: model.PhasePreCheck, TimeoutSec: 30,
					Description: "确认目录内存在可执行入口",
					Content:     "test -f ./bin/start.sh || { echo '缺少 bin/start.sh'; exit 2; }"},
			}
			db.Create(&scripts)
		}
	}
	return nil
}
