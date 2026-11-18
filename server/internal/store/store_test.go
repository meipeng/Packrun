package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/packrun/packrun/internal/config"
)

func TestOpenSetsPrivatePermissions(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			DBPath:      filepath.Join(root, "data", "packrun.db"),
			PackageDir:  filepath.Join(root, "packages"),
			SandboxRoot: filepath.Join(root, "sandbox"),
			MaxTotalMB:  20480,
		},
	}

	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}

	for _, dir := range []string{
		filepath.Dir(cfg.Storage.DBPath),
		cfg.Storage.PackageDir,
		cfg.Storage.SandboxRoot,
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("目录 %s 不存在: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s 不是目录", dir)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("目录 %s 权限 = %o，期望 700", dir, perm)
		}
	}

	info, err := os.Stat(cfg.Storage.DBPath)
	if err != nil {
		t.Fatalf("数据库文件不存在: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("数据库文件权限 = %o，期望 600", perm)
	}
}
