package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/packrun/packrun/internal/config"
	"github.com/packrun/packrun/internal/model"
)

func newBackupTestDB(t *testing.T, dbPath string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	return db
}

func TestBackupCopiesDatabaseAndPackages(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "packrun.db")
	db := newBackupTestDB(t, dbPath)
	if err := db.Create(&model.User{
		Username: "alice", PasswordHash: "hash", Role: model.RoleAdmin,
		ServerScope: "*", Status: "active",
	}).Error; err != nil {
		t.Fatalf("写入测试用户失败: %v", err)
	}

	pkgDir := filepath.Join(dir, "packages")
	if err := os.MkdirAll(filepath.Join(pkgDir, "2026"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "2026", "app.zip"), []byte("zip-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Storage: config.StorageConfig{DBPath: dbPath, PackageDir: pkgDir}}
	dest, err := Backup(db, cfg, filepath.Join(dir, "backups"), 7)
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	var manifest BackupManifest
	data, err := os.ReadFile(filepath.Join(dest, "manifest.json"))
	if err != nil {
		t.Fatalf("读取备份清单失败: %v", err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("解析备份清单失败: %v", err)
	}
	if manifest.DatabaseBytes == 0 || manifest.DatabaseSHA256 == "" {
		t.Fatalf("清单缺少数据库摘要: %+v", manifest)
	}
	if manifest.PackageFiles != 1 || manifest.PackageBytes != int64(len("zip-data")) {
		t.Fatalf("包仓库统计不符: %+v", manifest)
	}

	backupDB, err := gorm.Open(sqlite.Open(filepath.Join(dest, "packrun.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开备份数据库失败: %v", err)
	}
	var count int64
	if err := backupDB.Model(&model.User{}).Count(&count).Error; err != nil {
		t.Fatalf("查询备份数据库失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("备份数据库用户数 = %d，期望 1", count)
	}

	pkgData, err := os.ReadFile(filepath.Join(dest, "packages", "2026", "app.zip"))
	if err != nil {
		t.Fatalf("备份包文件缺失: %v", err)
	}
	if string(pkgData) != "zip-data" {
		t.Fatalf("备份包内容不一致: %q", pkgData)
	}
}

func TestBackupPrunesOldCopies(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "packrun.db")
	db := newBackupTestDB(t, dbPath)
	out := filepath.Join(dir, "backups")

	old := []string{
		backupPrefix + "20200101-000000",
		backupPrefix + "20200102-000000",
		backupPrefix + "20200103-000000",
	}
	for _, name := range old {
		if err := os.MkdirAll(filepath.Join(out, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{Storage: config.StorageConfig{DBPath: dbPath, PackageDir: filepath.Join(dir, "packages")}}
	dest, err := Backup(db, cfg, out, 2)
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("最新备份应保留: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, old[0])); !os.IsNotExist(err) {
		t.Fatalf("最旧备份应被清理")
	}
	if _, err := os.Stat(filepath.Join(out, old[1])); !os.IsNotExist(err) {
		t.Fatalf("第二旧备份应被清理")
	}
	if _, err := os.Stat(filepath.Join(out, old[2])); err != nil {
		t.Fatalf("较新的历史备份应保留: %v", err)
	}
}

func TestVerifyDatabaseRejectsBrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.db")
	if err := os.WriteFile(path, []byte("not-a-sqlite-database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyDatabase(path); err == nil {
		t.Fatal("损坏的数据库文件应校验失败")
	}
}
