package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/packrun/packrun/internal/config"
)

const backupPrefix = "packrun-backup-"

// BackupManifest 记录一次数据备份的内容摘要，恢复前可先核对。
type BackupManifest struct {
	CreatedAt      string `json:"createdAt"`
	Database       string `json:"database"`
	DatabaseSHA256 string `json:"databaseSha256"`
	DatabaseBytes  int64  `json:"databaseBytes"`
	PackageDir     string `json:"packageDir"`
	PackageFiles   int    `json:"packageFiles"`
	PackageBytes   int64  `json:"packageBytes"`
}

// OpenForBackup 打开现有数据库但不执行迁移，供备份命令使用。
func OpenForBackup(cfg *config.Config) (*gorm.DB, error) {
	if _, err := os.Stat(cfg.Storage.DBPath); err != nil {
		return nil, fmt.Errorf("数据库文件不可用: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", cfg.Storage.DBPath)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	// 备份是单连接快照，避免多个连接看到不同时间点。
	sqlDB.SetMaxOpenConns(1)
	return db, nil
}

// Backup 在线备份 SQLite 与包仓库到 outRoot 下的时间戳目录。
// SQLite 使用 VACUUM INTO 生成一致性快照，不会复制运行中的 -wal/-shm。
func Backup(db *gorm.DB, cfg *config.Config, outRoot string, keep int) (string, error) {
	if strings.TrimSpace(outRoot) == "" {
		return "", fmt.Errorf("备份目录不能为空")
	}
	if keep < 1 {
		keep = 1
	}
	if err := os.MkdirAll(outRoot, 0o700); err != nil {
		return "", fmt.Errorf("创建备份目录失败: %w", err)
	}

	dest := filepath.Join(outRoot, backupPrefix+time.Now().Format("20060102-150405"))
	if _, err := os.Stat(dest); err == nil {
		dest += "-" + fmt.Sprintf("%d", time.Now().UnixNano())
	}
	if isSubPath(cfg.Storage.PackageDir, dest) {
		return "", fmt.Errorf("备份目录不能位于包仓库内: %s", dest)
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return "", fmt.Errorf("创建本次备份目录失败: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(dest)
		}
	}()

	dbDest := filepath.Join(dest, filepath.Base(cfg.Storage.DBPath))
	if err := db.Exec("VACUUM INTO " + quoteSQL(dbDest)).Error; err != nil {
		return "", fmt.Errorf("备份数据库失败: %w", err)
	}
	if err := VerifyDatabase(dbDest); err != nil {
		return "", err
	}
	dbSum, dbBytes, err := fileDigest(dbDest)
	if err != nil {
		return "", fmt.Errorf("计算数据库摘要失败: %w", err)
	}

	pkgFiles, pkgBytes, err := copyTree(cfg.Storage.PackageDir, filepath.Join(dest, "packages"))
	if err != nil {
		return "", fmt.Errorf("备份包仓库失败: %w", err)
	}

	manifest := BackupManifest{
		CreatedAt:      time.Now().Format(time.RFC3339),
		Database:       filepath.Base(dbDest),
		DatabaseSHA256: dbSum,
		DatabaseBytes:  dbBytes,
		PackageDir:     cfg.Storage.PackageDir,
		PackageFiles:   pkgFiles,
		PackageBytes:   pkgBytes,
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dest, "manifest.json"), append(manifestData, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("写入备份清单失败: %w", err)
	}

	if err := pruneBackups(outRoot, keep); err != nil {
		log.Printf("[backup] 清理旧备份失败: %v", err)
	}
	cleanup = false
	return dest, nil
}

// VerifyDatabase 用只读连接对备份库做完整性检查。
func VerifyDatabase(path string) error {
	db, err := gorm.Open(sqlite.Open("file:"+path+"?mode=ro"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return fmt.Errorf("打开备份数据库失败: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	var result string
	if err := sqlDB.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("检查备份数据库失败: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("备份数据库完整性检查未通过: %s", result)
	}
	return nil
}

func fileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func copyTree(src, dst string) (int, int64, error) {
	info, err := os.Stat(src)
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	if !info.IsDir() {
		return 0, 0, fmt.Errorf("包仓库路径不是目录: %s", src)
	}

	var files int
	var bytes int64
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("包仓库包含不支持的文件类型: %s", path)
		}
		if err := copyFileWithMode(path, target, info.Mode().Perm()); err != nil {
			return err
		}
		files++
		bytes += info.Size()
		return nil
	})
	return files, bytes, err
}

func copyFileWithMode(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func pruneBackups(root string, keep int) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), backupPrefix) {
			dirs = append(dirs, entry.Name())
		}
	}
	sort.Strings(dirs)
	for len(dirs) > keep {
		if err := os.RemoveAll(filepath.Join(root, dirs[0])); err != nil {
			return err
		}
		dirs = dirs[1:]
	}
	return nil
}

func quoteSQL(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func isSubPath(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == "." || parent == string(filepath.Separator) {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
