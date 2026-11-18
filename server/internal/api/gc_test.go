package api

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/packrun/packrun/internal/model"
)

func TestPlanPackageGCKeepsRecentReleasesPerDirectory(t *testing.T) {
	_, _, db := newSecurityTestEnv(t)

	// 两个目录各 12 个版本，保留窗口 10 → 各淘汰最旧 2 个。
	type entry struct {
		dir  uint
		pkgs []*model.Package
	}
	entries := []entry{{dir: 1}, {dir: 2}}
	seq := 0
	for i := range entries {
		for v := 1; v <= 12; v++ {
			seq++
			pkg := model.Package{
				Name: fmt.Sprintf("d%d-v%d.zip", entries[i].dir, v), Kind: "zip",
				Size: 1024, StorageKey: fmt.Sprintf("d%d/v%d.zip", entries[i].dir, v), UploaderID: 1,
			}
			if err := db.Create(&pkg).Error; err != nil {
				t.Fatalf("创建包失败: %v", err)
			}
			// 30 天前上传，排除「最近上传一律保留」的影响。
			if err := db.Model(&pkg).UpdateColumn("created_at", time.Now().AddDate(0, 0, -30)).Error; err != nil {
				t.Fatalf("调整上传时间失败: %v", err)
			}
			task := model.Task{
				TaskNo: fmt.Sprintf("T-%d", seq), DirectoryID: entries[i].dir,
				PackageID: pkg.ID, Status: model.TaskSuccess,
			}
			if err := db.Create(&task).Error; err != nil {
				t.Fatalf("创建任务失败: %v", err)
			}
			entries[i].pkgs = append(entries[i].pkgs, &pkg)
		}
	}

	decisions, err := planPackageGC(db, 10, 7, time.Now())
	if err != nil {
		t.Fatalf("计算回收清单失败: %v", err)
	}
	keep := map[string]bool{}
	for _, d := range decisions {
		keep[d.Pkg.Name] = d.Keep
	}
	for _, e := range entries {
		for v := 1; v <= 12; v++ {
			name := fmt.Sprintf("d%d-v%d.zip", e.dir, v)
			want := v > 2 // 最旧 2 个应被回收
			if keep[name] != want {
				t.Fatalf("%s 保留=%v，期望 %v", name, keep[name], want)
			}
		}
	}
}

func TestPlanPackageGCKeepsFreshUploadEvenOutsideWindow(t *testing.T) {
	_, _, db := newSecurityTestEnv(t)

	oldPkg := model.Package{Name: "old.zip", Kind: "zip", Size: 10, StorageKey: "x/old.zip", UploaderID: 1}
	recentPkg := model.Package{Name: "recent.zip", Kind: "zip", Size: 10, StorageKey: "x/recent.zip", UploaderID: 1}
	for _, p := range []*model.Package{&oldPkg, &recentPkg} {
		if err := db.Create(p).Error; err != nil {
			t.Fatalf("创建包失败: %v", err)
		}
	}
	db.Model(&oldPkg).UpdateColumn("created_at", time.Now().AddDate(0, 0, -30))
	db.Model(&recentPkg).UpdateColumn("created_at", time.Now().AddDate(0, 0, -1))

	// 同一目录两个版本，窗口只保留 1 个（最新的 recent）。
	for i, p := range []*model.Package{&oldPkg, &recentPkg} {
		task := model.Task{TaskNo: fmt.Sprintf("T-%d", i), DirectoryID: 7, PackageID: p.ID, Status: model.TaskSuccess}
		if err := db.Create(&task).Error; err != nil {
			t.Fatalf("创建任务失败: %v", err)
		}
	}

	decisions, err := planPackageGC(db, 1, 7, time.Now())
	if err != nil {
		t.Fatalf("计算回收清单失败: %v", err)
	}
	for _, d := range decisions {
		if d.Pkg.Name == "old.zip" && d.Keep {
			t.Fatalf("超出窗口的旧包应被回收，实际保留（原因：%s）", d.Reason)
		}
		if d.Pkg.Name == "recent.zip" && !d.Keep {
			t.Fatalf("最近上传的包应保留，实际被回收（原因：%s）", d.Reason)
		}
	}
}

func TestPlanPackageGCDropsUnreferencedAfterRetention(t *testing.T) {
	_, _, db := newSecurityTestEnv(t)

	stale := model.Package{Name: "stale.zip", Kind: "zip", Size: 10, StorageKey: "x/stale.zip", UploaderID: 1}
	fresh := model.Package{Name: "fresh.zip", Kind: "zip", Size: 10, StorageKey: "x/fresh.zip", UploaderID: 1}
	db.Create(&stale)
	db.Create(&fresh)
	db.Model(&stale).UpdateColumn("created_at", time.Now().AddDate(0, 0, -10))

	decisions, err := planPackageGC(db, 10, 7, time.Now())
	if err != nil {
		t.Fatalf("计算回收清单失败: %v", err)
	}
	for _, d := range decisions {
		switch d.Pkg.Name {
		case "stale.zip":
			if d.Keep {
				t.Fatalf("未被引用且超期的包应回收，原因=%s", d.Reason)
			}
		case "fresh.zip":
			if !d.Keep {
				t.Fatalf("刚上传的包应保留，原因=%s", d.Reason)
			}
		}
	}
}

func TestApplyPackageGCDeletesFileAndRecord(t *testing.T) {
	app, _, db := newSecurityTestEnv(t)
	dir := app.Cfg.Storage.PackageDir
	if err := os.MkdirAll(filepath.Join(dir, "200601"), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	pkg := model.Package{Name: "gone.zip", Kind: "zip", Size: 2048, StorageKey: "200601/gone.zip", UploaderID: 1}
	if err := db.Create(&pkg).Error; err != nil {
		t.Fatalf("创建包失败: %v", err)
	}
	path := filepath.Join(dir, pkg.StorageKey)
	if err := os.WriteFile(path, []byte("zip"), 0o600); err != nil {
		t.Fatalf("写入包文件失败: %v", err)
	}

	removed, freed, failed := applyPackageGC(db, dir, []gcDecision{{Pkg: pkg, Keep: false, Reason: "测试"}})
	if removed != 1 || failed != 0 || freed != 2048 {
		t.Fatalf("回收结果不符: removed=%d freed=%d failed=%d", removed, freed, failed)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("包文件应被删除")
	}
	var count int64
	db.Model(&model.Package{}).Where("id = ?", pkg.ID).Count(&count)
	if count != 0 {
		t.Fatalf("包记录应被删除")
	}
}

func TestApplyPackageGCToleratesMissingFile(t *testing.T) {
	app, _, db := newSecurityTestEnv(t)
	pkg := model.Package{Name: "lost.zip", Kind: "zip", Size: 10, StorageKey: "200601/lost.zip", UploaderID: 1}
	db.Create(&pkg)

	removed, _, failed := applyPackageGC(db, app.Cfg.Storage.PackageDir, []gcDecision{{Pkg: pkg, Keep: false}})
	if removed != 1 || failed != 0 {
		t.Fatalf("文件已不存在时应正常删除记录: removed=%d failed=%d", removed, failed)
	}
}

func TestPurgeExpiredTasksKeepsRunningAndRecent(t *testing.T) {
	app, _, db := newSecurityTestEnv(t)
	old := time.Now().AddDate(0, 0, -200)

	cases := []struct {
		no     string
		status string
		age    time.Time
		keep   bool
	}{
		{"T-OLD-SUCCESS", model.TaskSuccess, old, false},
		{"T-OLD-FAILED", model.TaskFailed, old, false},
		{"T-OLD-CANCELED", model.TaskCanceled, old, false},
		{"T-OLD-RUNNING", model.TaskExecuting, old, true},
		{"T-NEW-SUCCESS", model.TaskSuccess, time.Now(), true},
	}
	created := make([]model.Task, 0, len(cases))
	for _, c := range cases {
		task := model.Task{TaskNo: c.no, Status: c.status}
		if err := db.Create(&task).Error; err != nil {
			t.Fatalf("创建任务失败: %v", err)
		}
		if err := db.Model(&task).UpdateColumn("created_at", c.age).Error; err != nil {
			t.Fatalf("调整任务时间失败: %v", err)
		}
		step := model.TaskStep{TaskID: task.ID, Seq: 1, Name: "s", Status: "success", Output: "log"}
		if err := db.Create(&step).Error; err != nil {
			t.Fatalf("创建步骤失败: %v", err)
		}
		created = append(created, task)
	}

	if n := purgeExpiredTasks(app, 180); n != 3 {
		t.Fatalf("应删除 3 个过期终态任务，实际 %d", n)
	}
	for i, c := range cases {
		var count int64
		db.Model(&model.Task{}).Where("id = ?", created[i].ID).Count(&count)
		if c.keep && count != 1 {
			t.Fatalf("%s 不应被删除", c.no)
		}
		if !c.keep && count != 0 {
			t.Fatalf("%s 应被删除", c.no)
		}
		var steps int64
		db.Model(&model.TaskStep{}).Where("task_id = ?", created[i].ID).Count(&steps)
		if !c.keep && steps != 0 {
			t.Fatalf("%s 的步骤应一并删除", c.no)
		}
	}

	if n := purgeExpiredTasks(app, 0); n != 0 {
		t.Fatalf("关闭保留期时不应删除，实际 %d", n)
	}
}

func TestPurgeExpiredAuditLogs(t *testing.T) {
	app, _, db := newSecurityTestEnv(t)
	oldLog := model.AuditLog{Actor: "alice", Action: "user.update", Target: "x", Result: "ok"}
	newLog := model.AuditLog{Actor: "bob", Action: "user.update", Target: "y", Result: "ok"}
	if err := db.Create(&oldLog).Error; err != nil {
		t.Fatalf("创建审计日志失败: %v", err)
	}
	if err := db.Create(&newLog).Error; err != nil {
		t.Fatalf("创建审计日志失败: %v", err)
	}
	db.Model(&oldLog).UpdateColumn("created_at", time.Now().AddDate(0, 0, -365))

	if n := purgeExpiredAuditLogs(app, 180); n != 1 {
		t.Fatalf("应删除 1 条过期审计日志，实际 %d", n)
	}
	var count int64
	db.Model(&model.AuditLog{}).Where("actor = ?", "bob").Count(&count)
	if count != 1 {
		t.Fatalf("未过期的审计日志不应被删除")
	}
	if n := purgeExpiredAuditLogs(app, 0); n != 0 {
		t.Fatalf("关闭保留期时不应删除，实际 %d", n)
	}
}
