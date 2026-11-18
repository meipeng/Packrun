package deploy

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/packrun/packrun/internal/model"
)

func TestNormalizePath(t *testing.T) {
	valid := map[string]string{
		"/opt/apps/demo":      "/opt/apps/demo",
		"/opt/apps/demo/":     "/opt/apps/demo",
		"/opt/apps/./demo":    "/opt/apps/demo",
		"/opt//apps/demo":     "/opt/apps/demo",
		"/opt/apps/demo data": "/opt/apps/demo data",
	}
	for input, want := range valid {
		got, err := NormalizePath(input)
		if err != nil {
			t.Fatalf("NormalizePath(%q) 返回错误: %v", input, err)
		}
		if got != want {
			t.Fatalf("NormalizePath(%q) = %q，期望 %q", input, got, want)
		}
	}

	invalid := []string{
		"",
		"   ",
		"opt/apps/demo",
		"/",
		"/opt/../etc",
		"/opt/apps/../../etc",
	}
	for _, input := range invalid {
		if _, err := NormalizePath(input); err == nil {
			t.Fatalf("NormalizePath(%q) 应返回错误", input)
		}
	}
}

func TestDirSizeMB(t *testing.T) {
	dir := t.TempDir()
	if mb, err := dirSizeMB(dir); err != nil || mb != 0 {
		t.Fatalf("空目录占用应为 0 MB，实际 %d，err=%v", mb, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "a.bin"), make([]byte, 10), 0o644); err != nil {
		t.Fatal(err)
	}
	if mb, err := dirSizeMB(dir); err != nil || mb != 1 {
		t.Fatalf("非空目录应向上取整为 1 MB，实际 %d，err=%v", mb, err)
	}

	if mb, err := dirSizeMB(filepath.Join(dir, "missing")); err != nil || mb != 0 {
		t.Fatalf("不存在目录应返回 0，实际 %d，err=%v", mb, err)
	}
}

func TestPruneBackups(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "app")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	stamps := []string{"20260101010101", "20260102020202", "20260103030303"}
	for _, stamp := range stamps {
		if err := os.MkdirAll(filepath.Join(root, "app.bak-"+stamp), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "other.bak-20260101010101"), 0o755); err != nil {
		t.Fatal(err)
	}

	pruned, err := pruneBackups(target, 2)
	if err != nil {
		t.Fatalf("清理备份失败: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("期望清理 1 个备份，实际 %d", pruned)
	}
	if _, err := os.Stat(filepath.Join(root, "app.bak-"+stamps[0])); !os.IsNotExist(err) {
		t.Fatalf("最旧备份应被删除")
	}
	if _, err := os.Stat(filepath.Join(root, "app.bak-"+stamps[2])); err != nil {
		t.Fatalf("最新备份应保留: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "other.bak-20260101010101")); err != nil {
		t.Fatalf("其它目录的备份不应被误删: %v", err)
	}

	if pruned, _ := pruneBackups(target, 2); pruned != 0 {
		t.Fatalf("未超出保留数量时不应清理，实际 %d", pruned)
	}
	if pruned, _ := pruneBackups(target, 0); pruned != 2 {
		t.Fatalf("保留 0 版时应清理全部备份，实际 %d", pruned)
	}
}

func TestUnzipRejectsUnsafeEntries(t *testing.T) {
	cases := []struct {
		name  string
		entry string
		link  bool
	}{
		{name: "路径穿越", entry: "../escape.txt"},
		{name: "绝对路径", entry: "/tmp/escape.txt"},
		{name: "符号链接", entry: "link", link: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			zipPath := filepath.Join(dir, "evil.zip")
			f, err := os.Create(zipPath)
			if err != nil {
				t.Fatal(err)
			}
			zw := zip.NewWriter(f)
			if tc.link {
				header := &zip.FileHeader{Name: tc.entry, Method: zip.Store}
				header.SetMode(os.ModeSymlink | 0o777)
				w, err := zw.CreateHeader(header)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = w.Write([]byte("/etc/passwd"))
			} else {
				w, err := zw.Create(tc.entry)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = w.Write([]byte("evil"))
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}

			target := filepath.Join(dir, "target")
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := unzip(zipPath, target, 1<<30, 20000); err == nil {
				t.Fatalf("压缩包包含危险条目 %q，应拒绝解压", tc.entry)
			}
		})
	}
}

func TestZipUncompressedSize(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "size.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, size := range []int{1024, 2048} {
		header := &zip.FileHeader{Name: "f.bin", Method: zip.Store}
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(make([]byte, size)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := zipUncompressedSize(zipPath)
	if err != nil {
		t.Fatalf("统计解压大小失败: %v", err)
	}
	if got != 3072 {
		t.Fatalf("解压大小 = %d，期望 3072", got)
	}
}

func TestCheckUnpackedSize(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "big.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	header := &zip.FileHeader{Name: "blob.bin", Method: zip.Store}
	w, err := zw.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(make([]byte, 2<<20)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	req := Request{
		Package:    model.Package{Kind: "zip"},
		Directory:  model.Directory{MaxSizeMB: 1},
		SourcePath: zipPath,
	}
	if err := checkUnpackedSize(req); err == nil {
		t.Fatal("解压后 2MB 超过 1MB 阈值，应拒绝")
	}

	req.Directory.MaxSizeMB = 3
	if err := checkUnpackedSize(req); err != nil {
		t.Fatalf("解压后 2MB 未超过 3MB 阈值，不应拒绝: %v", err)
	}

	req.Package.Kind = "jar"
	if err := checkUnpackedSize(req); err != nil {
		t.Fatalf("非 zip 包不应做解压体积检查: %v", err)
	}

	req.Package.Kind = "zip"
	req.Directory.PackageMode = model.PackageModeUpload
	if err := checkUnpackedSize(req); err != nil {
		t.Fatalf("仅上传模式不应做解压体积检查: %v", err)
	}
}

// 仅上传模式：zip 保留为压缩包，交由部署后脚本处理，不自动解压。
func TestLocalDeployZipUploadMode(t *testing.T) {
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
	err = d.Deploy(context.Background(), Request{
		Server: model.Server{Name: "srv-1"},
		Directory: model.Directory{
			Path: "/app", PackageMode: model.PackageModeUpload,
			AllowOverwrite: true, MaxSizeMB: 16,
		},
		Package:    model.Package{Name: "app.zip", Kind: "zip", Size: info.Size(), SHA256: sum},
		SourcePath: zipPath,
	})
	if err != nil {
		t.Fatalf("部署失败: %v", err)
	}

	target := filepath.Join(root, "srv-1", "app")
	if _, err := os.Stat(filepath.Join(target, "app.zip")); err != nil {
		t.Fatalf("压缩包未保留在目标目录: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "index.html")); !os.IsNotExist(err) {
		t.Fatalf("仅上传模式不应解压出 index.html")
	}
}

func TestLocalDeployRestoresBackupOnFailure(t *testing.T) {
	root := t.TempDir()
	pkgDir := t.TempDir()
	zipPath := filepath.Join(pkgDir, "app.zip")
	newZipWithFiles(t, zipPath, []string{"index.html"}, 16)
	info, err := os.Stat(zipPath)
	if err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(root, "srv-1", "app")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(target, "old.txt")
	if err := os.WriteFile(oldFile, []byte("old-version"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := &LocalDeployer{SandboxRoot: root}
	err = d.Deploy(context.Background(), Request{
		Server: model.Server{Name: "srv-1"},
		Directory: model.Directory{
			Path: "/app", AllowOverwrite: true, BackupBeforeDeploy: true,
			KeepReleases: 3, MaxSizeMB: 16, PackageMode: model.PackageModeUnpack,
		},
		// 故意给错误的 SHA256，让部署在备份之后失败。
		Package:    model.Package{Name: "app.zip", Kind: "zip", Size: info.Size(), SHA256: strings.Repeat("0", 64)},
		SourcePath: zipPath,
	})
	if err == nil {
		t.Fatal("SHA256 不匹配时应部署失败")
	}
	if !strings.Contains(err.Error(), "已自动恢复部署前版本") {
		t.Fatalf("失败原因应包含自动恢复标记，实际: %v", err)
	}

	data, err := os.ReadFile(oldFile)
	if err != nil {
		t.Fatalf("旧版本文件未恢复: %v", err)
	}
	if string(data) != "old-version" {
		t.Fatalf("旧版本内容不一致: %q", data)
	}
	if _, err := os.Stat(filepath.Join(target, "index.html")); !os.IsNotExist(err) {
		t.Fatalf("失败版本的文件不应留在目标目录")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".incoming-") {
			t.Fatalf("失败后不应残留上传临时文件: %s", entry.Name())
		}
	}
}

func TestUnzipNormal(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "app.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("bin/start.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("#!/bin/sh\necho ok\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(dir, "target")
	count, err := unzip(zipPath, target, 1<<30, 20000)
	if err != nil {
		t.Fatalf("正常压缩包解压失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("期望解压 1 个文件，实际 %d", count)
	}
	data, err := os.ReadFile(filepath.Join(target, "bin", "start.sh"))
	if err != nil {
		t.Fatalf("解压文件缺失: %v", err)
	}
	if string(data) != "#!/bin/sh\necho ok\n" {
		t.Fatalf("解压内容不一致: %q", data)
	}
}

func TestValidateServerName(t *testing.T) {
	valid := []string{"app", "app-1", "web.prod_01", "A1"}
	for _, name := range valid {
		if err := ValidateServerName(name); err != nil {
			t.Fatalf("ValidateServerName(%q) 应通过，实际 %v", name, err)
		}
	}

	invalid := []string{"", ".", "..", "../etc", "a/b", "a b", "服务器", strings.Repeat("a", 65)}
	for _, name := range invalid {
		if err := ValidateServerName(name); err == nil {
			t.Fatalf("ValidateServerName(%q) 应失败", name)
		}
	}
}

func TestResolveTargetRejectsUnsafeServerName(t *testing.T) {
	d := &LocalDeployer{SandboxRoot: t.TempDir()}
	req := Request{
		Server:    model.Server{Name: "../etc"},
		Directory: model.Directory{Path: "/app"},
	}
	if _, err := d.resolveTarget(req); err == nil {
		t.Fatal("非法服务器名应拒绝解析目标路径")
	}
}

func newZipWithFiles(t *testing.T, zipPath string, names []string, size int) {
	t.Helper()
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, name := range names {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			if _, err := w.Write(make([]byte, size)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUnzipEnforcesByteBudget(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "big.zip")
	newZipWithFiles(t, zipPath, []string{"blob.bin"}, 2<<20)

	target := filepath.Join(dir, "target")
	if _, err := unzip(zipPath, target, 1<<20, 20000); err == nil {
		t.Fatal("实际写入 2MB 超过 1MB 预算，应失败")
	}
	if _, err := os.Stat(filepath.Join(target, "blob.bin")); !os.IsNotExist(err) {
		t.Fatalf("超限后不应在目标目录残留文件，stat err = %v", err)
	}
}

func TestUnzipRejectsTooManyEntries(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "many.zip")
	newZipWithFiles(t, zipPath, []string{"a.txt", "b.txt", "c.txt"}, 1)

	target := filepath.Join(dir, "target")
	if _, err := unzip(zipPath, target, 1<<30, 2); err == nil {
		t.Fatal("条目数超过上限应失败")
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(target, name)); !os.IsNotExist(err) {
			t.Fatalf("失败后不应残留 %s，stat err = %v", name, err)
		}
	}
}

func TestUnzipRejectsSymlinkParentEscape(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(target, "link")); err != nil {
		t.Skipf("当前文件系统不支持符号链接: %v", err)
	}

	zipPath := filepath.Join(root, "evil.zip")
	newZipWithFiles(t, zipPath, []string{"link/evil.txt"}, 4)

	if _, err := unzip(zipPath, target, 1<<30, 20000); err == nil {
		t.Fatal("父目录符号链接指向目标外时应拒绝解压")
	}
	if _, err := os.Stat(filepath.Join(outside, "evil.txt")); !os.IsNotExist(err) {
		t.Fatalf("不应在符号链接指向的目录写入文件，stat err = %v", err)
	}
}

func TestCheckUnpackedSizeEntryLimit(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "flood.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for i := 0; i < maxUnzipEntries+1; i++ {
		if _, err := zw.Create(fmt.Sprintf("f%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	req := Request{
		Package:    model.Package{Kind: "zip"},
		Directory:  model.Directory{MaxSizeMB: 1024},
		SourcePath: zipPath,
	}
	if err := checkUnpackedSize(req); err == nil {
		t.Fatalf("条目数 %d 超过上限 %d，应拒绝", maxUnzipEntries+1, maxUnzipEntries)
	}
}
