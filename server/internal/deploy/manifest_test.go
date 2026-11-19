package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/packrun/packrun/internal/model"
)

func TestDiffManifest(t *testing.T) {
	added, overwritten, removed := diffManifest(
		[]string{"a.jar", "lib/b.jar", "conf/old.properties", "./c.txt"},
		[]string{"a.jar", "lib/b.jar", "d/new.txt"},
	)
	if added != 1 || overwritten != 2 {
		t.Fatalf("added=%d overwritten=%d，期望 1/2", added, overwritten)
	}
	if len(removed) != 2 || removed[0] != "c.txt" || removed[1] != "conf/old.properties" {
		t.Fatalf("removed 应为排序后的旧文件清单，实际 %v", removed)
	}
}

func TestApplyManifestLimits(t *testing.T) {
	m := &Manifest{}
	big := make([]string, manifestRemovedLimit+10)
	for i := range big {
		big[i] = "f.txt"
	}
	m.applyManifestLimits(big)
	if len(m.Removed) != manifestRemovedLimit || !m.Truncated {
		t.Fatalf("超限清单应截断并标注，实际 %d 条 truncated=%v", len(m.Removed), m.Truncated)
	}

	m2 := &Manifest{}
	m2.applyManifestLimits([]string{"only.txt"})
	if len(m2.Removed) != 1 || m2.Truncated {
		t.Fatalf("未超限清单应全量保留，实际 %d 条 truncated=%v", len(m2.Removed), m2.Truncated)
	}
}

func TestSplitRemoteEntriesAndTraversal(t *testing.T) {
	entries := splitRemoteEntries("a.jar\r\nlib/\n\nsub/b.txt\n")
	if len(entries) != 2 || entries[0] != "a.jar" || entries[1] != "sub/b.txt" {
		t.Fatalf("应剔除目录条目与空行，实际 %v", entries)
	}
	if !containsPathTraversal("a/../../etc/passwd") || containsPathTraversal("a/b..c.txt") {
		t.Fatal(".. 穿越判断不符合预期")
	}
	if got := sanitizeRef("D20261119ABC/def:xyz", "fallback"); got != "D20261119ABCdefxyz" {
		t.Fatalf("sanitizeRef 应剔除非法字符，实际 %q", got)
	}
}

// 干净替换：zip 解包时清理包内不存在的旧文件，且不影响部署目录之外的路径。
func TestLocalDeployCleanReplace(t *testing.T) {
	root := t.TempDir()
	pkgDir := t.TempDir()
	zipPath := filepath.Join(pkgDir, "app.zip")
	newZipWithFiles(t, zipPath, []string{"index.html"}, 16)
	sum, err := fileSHA256(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(zipPath)
	if err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(root, "srv-clean", "app")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(target, "legacy.old.jar")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(root, "srv-clean", "app.bak-keep")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	manifest := &Manifest{}
	d := &LocalDeployer{SandboxRoot: root}
	err = d.Deploy(context.Background(), Request{
		Server: model.Server{Name: "srv-clean"},
		Directory: model.Directory{
			Path: "/app", AllowOverwrite: true, CleanReplace: true,
			PackageMode: model.PackageModeUnpack, MaxSizeMB: 16,
		},
		Package:    model.Package{Name: "app.zip", Kind: "zip", Size: info.Size(), SHA256: sum},
		SourcePath: zipPath,
		Manifest:   manifest,
	})
	if err != nil {
		t.Fatalf("部署失败: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("干净替换应清理包内不存在的旧文件，stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(sibling, "keep.txt")); err != nil {
		t.Fatalf("部署目录之外的备份内容不应受影响: %v", err)
	}
	if manifest.TotalEntries != 1 || manifest.Added != 1 || manifest.Overwritten != 0 {
		t.Fatalf("manifest 统计不符，实际 %+v", manifest)
	}
	if len(manifest.Removed) != 1 || manifest.Removed[0] != "legacy.old.jar" {
		t.Fatalf("manifest 应记录清理清单，实际 %v", manifest.Removed)
	}
}

// 「仅新增」策略对 zip 同样生效：包内任一文件与目标目录现有文件冲突即拒绝。
func TestLocalDeployRejectsOverwriteConflict(t *testing.T) {
	root := t.TempDir()
	pkgDir := t.TempDir()
	zipPath := filepath.Join(pkgDir, "app.zip")
	newZipWithFiles(t, zipPath, []string{"conf/a.properties"}, 16)
	sum, err := fileSHA256(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(zipPath)
	if err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(root, "srv-no", "app")
	if err := os.MkdirAll(filepath.Join(target, "conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "conf", "a.properties"), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := &LocalDeployer{SandboxRoot: root}
	err = d.Deploy(context.Background(), Request{
		Server: model.Server{Name: "srv-no"},
		Directory: model.Directory{
			Path: "/app", AllowOverwrite: false,
			PackageMode: model.PackageModeUnpack, MaxSizeMB: 16,
		},
		Package:    model.Package{Name: "app.zip", Kind: "zip", Size: info.Size(), SHA256: sum},
		SourcePath: zipPath,
	})
	if err == nil || !strings.Contains(err.Error(), "仅新增") {
		t.Fatalf("仅新增策略应拒绝覆盖冲突，实际 err=%v", err)
	}
}

// 续跑：跳过的步骤不再执行（不产生新的时间线步骤），备份可沿用最近一次，任务仍然成功。
func TestLocalDeployResumeSkipsCompletedSteps(t *testing.T) {
	root := t.TempDir()
	pkgDir := t.TempDir()
	zipPath := filepath.Join(pkgDir, "app.zip")
	newZipWithFiles(t, zipPath, []string{"index.html"}, 16)
	sum, err := fileSHA256(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(zipPath)
	if err != nil {
		t.Fatal(err)
	}

	d := &LocalDeployer{SandboxRoot: root}
	base := Request{
		Server: model.Server{Name: "srv-resume"},
		Directory: model.Directory{
			Path: "/app", AllowOverwrite: true, BackupBeforeDeploy: true,
			KeepReleases: 3, PackageMode: model.PackageModeUnpack, MaxSizeMB: 16,
		},
		Package:    model.Package{Name: "app.zip", Kind: "zip", Size: info.Size(), SHA256: sum},
		SourcePath: zipPath,
	}

	// 第一次完整部署：产出备份目录
	if err := d.Deploy(context.Background(), base); err != nil {
		t.Fatalf("首次部署失败: %v", err)
	}

	// 模拟中断后续跑：除「校验安装包」外全部跳过
	var steps []string
	resume := base
	resume.SkipSteps = map[string]bool{
		StepPrepare: true, StepPreCheck: true, StepBackup: true,
		StepUpload: true, StepUnpack: true, StepScript: true,
	}
	resume.Hooks = Hooks{OnStep: func(name string) { steps = append(steps, name) }}
	if err := d.Deploy(context.Background(), resume); err != nil {
		t.Fatalf("续跑部署失败: %v", err)
	}
	// 跳过的步骤不产生时间线行；上传临时包在首次部署后已被清理，
	// 续跑时自动回退重新上传（校验步骤始终执行）。
	if len(steps) != 2 || steps[0] != StepUpload || steps[1] != StepVerify {
		t.Fatalf("续跑应只执行上传回退与校验步骤，实际 %v", steps)
	}
}
