package api

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"gorm.io/gorm"

	"github.com/packrun/packrun/internal/model"
)

// gcDecision 是单个包的回收决策，Reason 用于 dry-run 输出与日志。
type gcDecision struct {
	Pkg    model.Package
	Keep   bool
	Reason string
}

// planPackageGC 计算包仓库回收清单。默认保守，任一命中即保留：
//  1. 未被任何任务引用，且上传未超过 3 天（可能刚传还没发）；
//  2. 被任务引用，且属于某目录最近 keepPerDir 个版本的包（保证最近 N 版可回滚）；
//  3. 被任务引用，且上传未超过 keepDays 天（刚发的版本还没验证完）。
//
// keepPerDir <= 0 表示关闭版本回收：所有被引用的包一律保留。
func planPackageGC(db *gorm.DB, keepPerDir, keepDays int, now time.Time) ([]gcDecision, error) {
	var pkgs []model.Package
	if err := db.Order("id asc").Find(&pkgs).Error; err != nil {
		return nil, fmt.Errorf("查询安装包失败: %w", err)
	}
	if len(pkgs) == 0 {
		return nil, nil
	}

	// 只取三列：任务表可能很大，不需要整行。
	var refs []struct {
		ID          uint
		PackageID   uint
		DirectoryID uint
	}
	if err := db.Model(&model.Task{}).
		Select("id, package_id, directory_id").
		Order("id desc").Scan(&refs).Error; err != nil {
		return nil, fmt.Errorf("查询任务引用失败: %w", err)
	}

	referenced := make(map[uint]bool, len(refs))
	for _, r := range refs {
		if r.PackageID != 0 {
			referenced[r.PackageID] = true
		}
	}

	// 每个目录按任务倒序保留最近 keepPerDir 个不同版本的包。
	keptByWindow := map[uint]bool{}
	if keepPerDir > 0 {
		perDir := make(map[uint]map[uint]bool)
		for _, r := range refs {
			if r.PackageID == 0 {
				continue
			}
			set := perDir[r.DirectoryID]
			if set == nil {
				set = make(map[uint]bool, keepPerDir)
				perDir[r.DirectoryID] = set
			}
			if len(set) >= keepPerDir {
				continue
			}
			set[r.PackageID] = true
		}
		for _, set := range perDir {
			for id := range set {
				keptByWindow[id] = true
			}
		}
	}

	uploadCutoff := packageRetentionCutoff(now)
	freshCutoff := now.AddDate(0, 0, -keepDays)

	out := make([]gcDecision, 0, len(pkgs))
	for _, p := range pkgs {
		switch {
		case referenced[p.ID] && keepPerDir <= 0:
			out = append(out, gcDecision{Pkg: p, Keep: true, Reason: "版本回收已关闭"})
		case referenced[p.ID] && keptByWindow[p.ID]:
			out = append(out, gcDecision{Pkg: p, Keep: true, Reason: "目录最近版本"})
		case referenced[p.ID] && keepDays > 0 && !p.CreatedAt.Before(freshCutoff):
			out = append(out, gcDecision{Pkg: p, Keep: true, Reason: "上传时间在保留窗口内"})
		case referenced[p.ID]:
			out = append(out, gcDecision{Pkg: p, Keep: false, Reason: "超出目录保留版本数"})
		case !p.CreatedAt.Before(uploadCutoff):
			out = append(out, gcDecision{Pkg: p, Keep: true, Reason: "上传未满 3 天"})
		default:
			out = append(out, gcDecision{Pkg: p, Keep: false, Reason: "未被任何任务引用"})
		}
	}
	return out, nil
}

// applyPackageGC 执行回收：先删文件再删记录。文件删除失败时保留记录，
// 避免出现「记录还在但文件没了」的悬空包（这类记录下次 GC 会再次尝试）。
func applyPackageGC(db *gorm.DB, packageDir string, decisions []gcDecision) (removed int, freedBytes int64, failed int) {
	for _, d := range decisions {
		if d.Keep {
			continue
		}
		if d.Pkg.StorageKey != "" {
			path := filepath.Join(packageDir, d.Pkg.StorageKey)
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				log.Printf("[gc] 删除包文件失败 id=%d key=%s: %v", d.Pkg.ID, d.Pkg.StorageKey, err)
				failed++
				continue
			}
		}
		if err := db.Delete(&model.Package{}, d.Pkg.ID).Error; err != nil {
			log.Printf("[gc] 删除包记录失败 id=%d: %v", d.Pkg.ID, err)
			failed++
			continue
		}
		removed++
		freedBytes += d.Pkg.Size
	}
	return removed, freedBytes, failed
}

// RunPackageGC 计算并执行一次回收，供后台维护与 CLI 共用；dryRun 只打印清单。
func RunPackageGC(db *gorm.DB, packageDir string, keepPerDir, keepDays int, dryRun bool) (removed int, freedBytes int64, failed int, err error) {
	decisions, err := planPackageGC(db, keepPerDir, keepDays, time.Now())
	if err != nil {
		return 0, 0, 0, err
	}
	if dryRun {
		for _, d := range decisions {
			if d.Keep {
				continue
			}
			fmt.Printf("将回收 %-28s %8d KB  %s\n", d.Pkg.Name, d.Pkg.Size>>10, d.Reason)
		}
		for _, d := range decisions {
			if !d.Keep {
				removed++
				freedBytes += d.Pkg.Size
			}
		}
		return removed, freedBytes, 0, nil
	}
	removed, freedBytes, failed = applyPackageGC(db, packageDir, decisions)
	if removed > 0 || failed > 0 {
		log.Printf("[gc] 回收历史安装包 %d 个，释放 %d MB，失败 %d 个", removed, freedBytes>>20, failed)
	}
	return removed, freedBytes, failed, nil
}

// purgeExpiredTasks 删除超过保留期的终态任务及其步骤；未完成任务一律不动。
func purgeExpiredTasks(app *App, days int) int {
	if days <= 0 {
		return 0
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	terminal := []string{model.TaskSuccess, model.TaskFailed, model.TaskCanceled}
	total := 0
	for {
		var ids []uint
		if err := app.DB.Model(&model.Task{}).
			Where("status IN ? AND created_at < ?", terminal, cutoff).
			Limit(500).Pluck("id", &ids).Error; err != nil {
			log.Printf("[tasks] 查询过期任务失败: %v", err)
			return total
		}
		if len(ids) == 0 {
			break
		}
		if err := app.DB.Where("task_id IN ?", ids).Delete(&model.TaskStep{}).Error; err != nil {
			log.Printf("[tasks] 删除过期任务步骤失败: %v", err)
			return total
		}
		res := app.DB.Delete(&model.Task{}, ids)
		if res.Error != nil {
			log.Printf("[tasks] 删除过期任务失败: %v", res.Error)
			return total
		}
		total += int(res.RowsAffected)
		if len(ids) < 500 {
			break
		}
	}
	return total
}

// purgeExpiredAuditLogs 按保留期分批删除审计日志，避免单条 DELETE 长时间锁库。
func purgeExpiredAuditLogs(app *App, days int) int {
	if days <= 0 {
		return 0
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	total := 0
	for {
		var ids []uint
		if err := app.DB.Model(&model.AuditLog{}).
			Where("created_at < ?", cutoff).
			Limit(1000).Pluck("id", &ids).Error; err != nil {
			log.Printf("[audit] 查询过期日志失败: %v", err)
			return total
		}
		if len(ids) == 0 {
			break
		}
		res := app.DB.Delete(&model.AuditLog{}, ids)
		if res.Error != nil {
			log.Printf("[audit] 删除过期日志失败: %v", res.Error)
			return total
		}
		total += int(res.RowsAffected)
		if len(ids) < 1000 {
			break
		}
	}
	return total
}

// maintenanceOnce 执行一轮维护：上传回收、残留临时文件、任务日志、任务与审计留存，
// 包仓库版本回收较重，由调用方控制频率（默认每天一次）。
func maintenanceOnce(app *App, withGC bool) {
	if withGC {
		if _, _, _, err := RunPackageGC(app.DB, app.Cfg.Storage.PackageDir,
			app.Cfg.Storage.PackageKeepReleases, app.Cfg.Storage.PackageKeepDays, false); err != nil {
			log.Printf("[gc] 包仓库回收失败: %v", err)
		}
	}
	// 未被任务引用且超过 3 天的上传记录，靠 GC 的默认规则顺带清理；
	// 这里只负责异常退出残留的临时文件。
	if n := sweepStaleUploads(app.Cfg.Storage.PackageDir, time.Hour); n > 0 {
		log.Printf("[packages] 已清理 %d 个残留的上传临时文件", n)
	}
	if n := purgeTaskStepOutputs(app, app.Cfg.Storage.TaskLogRetentionDays); n > 0 {
		log.Printf("[tasks] 已清空 %d 条超过 %d 天的步骤日志正文", n, app.Cfg.Storage.TaskLogRetentionDays)
	}
	if n := purgeExpiredTasks(app, app.Cfg.Storage.TaskRetentionDays); n > 0 {
		log.Printf("[tasks] 已删除 %d 个超过 %d 天的历史任务", n, app.Cfg.Storage.TaskRetentionDays)
	}
	if n := purgeExpiredAuditLogs(app, app.Cfg.Storage.AuditRetentionDays); n > 0 {
		log.Printf("[audit] 已删除 %d 条超过 %d 天的审计日志", n, app.Cfg.Storage.AuditRetentionDays)
	}
	if n := purgeAlertDedup(app, 7 * 24 * time.Hour); n > 0 {
		log.Printf("[alert] 已清理 %d 条过期的告警去重记录", n)
	}
}

// StartMaintenanceWorker 启动后立即维护一次，之后每小时一次；包仓库回收每天一次。
func StartMaintenanceWorker(app *App, interval time.Duration) {
	if interval <= 0 {
		return
	}
	go func() {
		log.Printf("[maintenance] 维护任务已启动，间隔 %s（包仓库回收每天一次）", interval)
		maintenanceOnce(app, true)
		lastGC := time.Now()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			withGC := time.Since(lastGC) >= 24*time.Hour
			if withGC {
				lastGC = time.Now()
			}
			maintenanceOnce(app, withGC)
		}
	}()
}
