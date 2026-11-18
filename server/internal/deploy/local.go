package deploy

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/packrun/packrun/internal/model"
)

// LocalDeployer 在本机沙箱中模拟业务服务器：真实落盘、真实解包，
// 但不执行任意脚本（只渲染并回显），避免开发机被注入命令。
type LocalDeployer struct {
	SandboxRoot string
	StepDelay   time.Duration
}

func (d *LocalDeployer) Deploy(ctx context.Context, req Request) (retErr error) {
	target, err := d.resolveTarget(req)
	if err != nil {
		return err
	}
	if err := checkUnpackedSize(req); err != nil {
		return err
	}
	var backupPath string
	defer func() {
		if retErr == nil || backupPath == "" {
			return
		}
		req.Hooks.logf("部署失败，正在恢复部署前版本…")
		if restoreErr := restoreLocalBackup(target, backupPath); restoreErr != nil {
			req.Hooks.logf("自动恢复失败：" + restoreErr.Error())
			retErr = fmt.Errorf("%w；自动恢复失败：%v（备份保留在 %s）", retErr, restoreErr, backupPath)
			return
		}
		req.Hooks.logf("已自动恢复部署前版本")
		retErr = fmt.Errorf("%w（已自动恢复部署前版本）", retErr)
	}()
	maxBytes, maxEntries := unpackBudget(req)

	// 1. 准备目录
	req.Hooks.step(StepPrepare)
	req.Hooks.stage("prepare", 10)
	req.Hooks.logf(fmt.Sprintf("目标目录：%s", target))
	if err := d.sleep(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	// 空间阈值：目标目录已占用超过策略上限时，拒绝本次部署
	if mb, err := dirSizeMB(target); err != nil {
		return fmt.Errorf("统计目录占用失败: %w", err)
	} else if req.Directory.MaxSizeMB > 0 && mb > int64(req.Directory.MaxSizeMB) {
		return fmt.Errorf("目标目录已占用 %d MB，超过阈值 %d MB", mb, req.Directory.MaxSizeMB)
	} else if req.Directory.MaxSizeMB > 0 {
		req.Hooks.logf(fmt.Sprintf("目录占用 %d MB / 阈值 %d MB", mb, req.Directory.MaxSizeMB))
	}

	// 2. 前置校验
	if req.PreScript != nil {
		req.Hooks.step(StepPreCheck)
		req.Hooks.stage("precheck", 15)
		previewScript(req.Hooks, req.PreScript)
		if err := d.sleep(ctx); err != nil {
			return err
		}
	}

	// 3. 备份
	req.Hooks.step(StepBackup)
	req.Hooks.stage("backup", 25)
	if req.Directory.BackupBeforeDeploy {
		if entries, _ := os.ReadDir(target); len(entries) > 0 {
			backup := target + ".bak-" + time.Now().Format("20060102150405")
			if err := os.Rename(target, backup); err != nil {
				return fmt.Errorf("备份失败: %w", err)
			}
			backupPath = backup
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			req.Hooks.logf("已备份至 " + filepath.Base(backup))
		} else {
			req.Hooks.logf("目标目录为空，跳过备份")
		}
	} else {
		req.Hooks.logf("目录策略未开启备份，跳过")
	}
	if err := d.sleep(ctx); err != nil {
		return err
	}

	// 4. 上传
	req.Hooks.step(StepUpload)
	req.Hooks.stage("upload", 45)
	tmp := filepath.Join(target, ".incoming-"+req.Package.Name)
	if err := copyFile(req.SourcePath, tmp); err != nil {
		return fmt.Errorf("写入安装包失败: %w", err)
	}
	req.Hooks.logf(fmt.Sprintf("已写入 %s（%d 字节）", req.Package.Name, req.Package.Size))
	if err := d.sleep(ctx); err != nil {
		return err
	}

	// 5. 校验
	req.Hooks.step(StepVerify)
	req.Hooks.stage("verify", 60)
	sum, err := fileSHA256(tmp)
	if err != nil {
		return err
	}
	if sum != req.Package.SHA256 {
		return fmt.Errorf("包校验失败：期望 %s，实际 %s", req.Package.SHA256, sum)
	}
	req.Hooks.logf("SHA256 校验通过")
	if err := d.sleep(ctx); err != nil {
		return err
	}

	// 6. 解包/替换
	req.Hooks.step(StepUnpack)
	req.Hooks.stage("unpack", 75)
	if req.Package.Kind == "zip" && req.Directory.PackageMode != model.PackageModeUpload {
		n, err := unzip(tmp, target, maxBytes, maxEntries)
		if err != nil {
			return err
		}
		req.Hooks.logf(fmt.Sprintf("已解压 %d 个文件", n))
	} else { // jar 等单文件制品，或 zip 选择「仅上传」由脚本处理
		final := filepath.Join(target, req.Package.Name)
		if !req.Directory.AllowOverwrite {
			if _, err := os.Stat(final); err == nil {
				return fmt.Errorf("目标已存在 %s，且目录策略禁止覆盖", req.Package.Name)
			}
		}
		if err := os.Rename(tmp, final); err != nil {
			return err
		}
		if req.Package.Kind == "zip" {
			req.Hooks.logf("已上传压缩包 " + req.Package.Name)
		} else {
			req.Hooks.logf("已替换制品 " + req.Package.Name)
		}
	}
	_ = os.Remove(tmp)
	if err := d.sleep(ctx); err != nil {
		return err
	}

	// 7. 后置脚本
	req.Hooks.step(StepScript)
	req.Hooks.stage("script", 90)
	if req.Script == nil {
		req.Hooks.logf("未绑定部署后脚本，跳过")
	} else {
		previewScript(req.Hooks, req.Script)
	}
	if err := d.sleep(ctx); err != nil {
		return err
	}

	// 8. 清理超出保留版本的备份
	if req.Directory.BackupBeforeDeploy {
		keep := req.Directory.KeepReleases
		pruned, err := pruneBackups(target, keep)
		switch {
		case err != nil:
			req.Hooks.logf("清理过期备份失败：" + err.Error())
		case pruned > 0:
			req.Hooks.logf(fmt.Sprintf("已清理 %d 个过期备份（保留 %d 版）", pruned, keep))
		}
	}

	req.Hooks.stage("done", 100)
	return nil
}

// previewScript 在本地沙箱中只渲染脚本内容，不做实际执行。
func previewScript(h Hooks, script *model.Script) {
	h.logf(fmt.Sprintf("脚本：%s（%s v%d，超时 %ds，执行身份 %s）",
		script.Name, script.Phase, script.Version, script.TimeoutSec, orDefault(script.RunAs, "默认")))
	for _, line := range strings.Split(strings.TrimSpace(script.Content), "\n") {
		h.logf("  | " + line)
	}
	h.logf("本地沙箱模式：脚本仅回显，未实际执行（切换 deploy.mode=ssh 后由目标机执行）")
}

func (d *LocalDeployer) resolveTarget(req Request) (string, error) {
	name := strings.TrimSpace(req.Server.Name)
	if err := ValidateServerName(name); err != nil {
		return "", err
	}
	clean, err := NormalizePath(req.Directory.Path)
	if err != nil {
		return "", err
	}
	base := filepath.Join(d.SandboxRoot, name)
	target := filepath.Join(base, strings.TrimPrefix(clean, "/"))
	if target != base && !strings.HasPrefix(target, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("部署目录越界: %s", target)
	}
	return target, nil
}

func (d *LocalDeployer) sleep(ctx context.Context) error {
	if d.StepDelay <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d.StepDelay):
		return nil
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

const (
	maxUnzipEntries = 20000
	maxUnzipBytes   = int64(1 << 30)
)

// unpackBudget 从目录策略推导本次解包预算；未配置上限时按 1GB 处理。
func unpackBudget(req Request) (int64, int) {
	maxBytes := int64(req.Directory.MaxSizeMB) << 20
	if maxBytes <= 0 {
		maxBytes = maxUnzipBytes
	}
	return maxBytes, maxUnzipEntries
}

func withinRoot(root, p string) bool {
	root = filepath.Clean(root)
	p = filepath.Clean(p)
	return p == root || strings.HasPrefix(p, root+string(os.PathSeparator))
}

// unzip 解压并阻止路径穿越、符号链接逃逸与解压炸弹。
func unzip(src, target string, maxBytes int64, maxEntries int) (int, error) {
	if maxBytes <= 0 {
		maxBytes = maxUnzipBytes
	}
	if maxEntries <= 0 {
		maxEntries = maxUnzipEntries
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return 0, err
	}
	realRoot, err := filepath.EvalSymlinks(target)
	if err != nil {
		return 0, fmt.Errorf("解析目标目录失败: %w", err)
	}
	realRoot = filepath.Clean(realRoot)

	r, err := zip.OpenReader(src)
	if err != nil {
		return 0, fmt.Errorf("读取压缩包失败: %w", err)
	}
	defer r.Close()
	if len(r.File) > maxEntries {
		return 0, fmt.Errorf("压缩包条目数 %d 超过上限 %d", len(r.File), maxEntries)
	}

	perFile := maxBytes
	if perFile > maxUnzipBytes {
		perFile = maxUnzipBytes
	}

	count := 0
	var totalWritten int64
	writtenPaths := make([]string, 0, len(r.File))
	cleanup := func() {
		for i := len(writtenPaths) - 1; i >= 0; i-- {
			_ = os.Remove(writtenPaths[i])
		}
	}

	cleanTarget := filepath.Clean(target)
	for _, f := range r.File {
		name := filepath.Clean(f.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			cleanup()
			return count, fmt.Errorf("压缩包内存在非法路径 %q", f.Name)
		}
		if f.Mode()&os.ModeSymlink != 0 {
			cleanup()
			return count, fmt.Errorf("压缩包内不允许符号链接 %q", f.Name)
		}
		dst := filepath.Join(target, name)
		if !strings.HasPrefix(dst, cleanTarget+string(os.PathSeparator)) && dst != cleanTarget {
			cleanup()
			return count, fmt.Errorf("压缩包内存在越界路径 %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				cleanup()
				return count, err
			}
			continue
		}

		// 父目录必须真实落在目标根内，防止通过预先存在的符号链接逃逸。
		parent := filepath.Dir(dst)
		if err := os.MkdirAll(parent, 0o755); err != nil {
			cleanup()
			return count, err
		}
		realParent, err := filepath.EvalSymlinks(parent)
		if err != nil {
			cleanup()
			return count, fmt.Errorf("解析条目父目录失败 %q: %w", f.Name, err)
		}
		if !withinRoot(realRoot, realParent) {
			cleanup()
			return count, fmt.Errorf("压缩包条目 %q 的父目录超出目标范围", f.Name)
		}
		if info, err := os.Lstat(dst); err == nil && info.Mode()&os.ModeSymlink != 0 {
			cleanup()
			return count, fmt.Errorf("目标路径 %q 已存在符号链接", f.Name)
		}

		rc, err := f.Open()
		if err != nil {
			cleanup()
			return count, err
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode().Perm()&0o777)
		if err != nil {
			rc.Close()
			cleanup()
			return count, err
		}
		writtenPaths = append(writtenPaths, dst)

		limit := perFile
		if remaining := maxBytes - totalWritten; remaining < limit {
			limit = remaining
		}
		if limit <= 0 {
			out.Close()
			rc.Close()
			cleanup()
			return count, fmt.Errorf("解压后大小超过上限 %d MB", maxBytes>>20)
		}
		n, copyErr := io.Copy(out, io.LimitReader(rc, limit+1))
		out.Close()
		rc.Close()
		if copyErr != nil {
			cleanup()
			return count, copyErr
		}
		if n > limit {
			cleanup()
			return count, fmt.Errorf("解压后大小超过上限 %d MB", maxBytes>>20)
		}
		totalWritten += n
		count++
	}
	return count, nil
}

// zipUncompressedSize 统计压缩包解压后的总大小（字节），用于阈值与 zip bomb 防护。
func zipUncompressedSize(path string) (uint64, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return 0, fmt.Errorf("读取压缩包失败: %w", err)
	}
	defer r.Close()
	var total uint64
	for _, f := range r.File {
		total += f.UncompressedSize64
	}
	return total, nil
}

// checkUnpackedSize 在部署前检查 zip 解压体积与条目数，超过预算时直接拒绝。
func checkUnpackedSize(req Request) error {
	if req.Package.Kind != "zip" || req.Directory.PackageMode == model.PackageModeUpload {
		return nil
	}
	maxBytes, maxEntries := unpackBudget(req)
	r, err := zip.OpenReader(req.SourcePath)
	if err != nil {
		return fmt.Errorf("读取压缩包失败: %w", err)
	}
	defer r.Close()

	if len(r.File) > maxEntries {
		return fmt.Errorf("压缩包条目数 %d 超过上限 %d", len(r.File), maxEntries)
	}
	limit := uint64(maxBytes)
	var total uint64
	for _, f := range r.File {
		if f.UncompressedSize64 > limit {
			return fmt.Errorf("压缩包内单个文件解压后约 %d MB，超过上限 %d MB",
				(f.UncompressedSize64+(1<<20)-1)>>20, maxBytes>>20)
		}
		total += f.UncompressedSize64
	}
	if total > limit {
		return fmt.Errorf("解压后大小约 %d MB，超过目录阈值 %d MB",
			(total+(1<<20)-1)>>20, maxBytes>>20)
	}
	req.Hooks.logf(fmt.Sprintf("解压后大小约 %d MB / 上限 %d MB", (total+(1<<20)-1)>>20, maxBytes>>20))
	return nil
}

// dirSizeMB 统计目标目录当前占用（MB，向上取整），用于空间阈值校验。
func dirSizeMB(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return (total + (1 << 20) - 1) >> 20, nil
}

// restoreLocalBackup 用备份目录替换失败的目标目录。
// 先把失败现场改名保留，成功恢复后再清理；若恢复动作失败，失败现场和备份都还在。
func restoreLocalBackup(target, backup string) error {
	failed := target + ".failed-" + time.Now().Format("20060102150405.000000000")
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, failed); err != nil {
			return fmt.Errorf("保留失败目录: %w", err)
		}
	}
	if err := os.Rename(backup, target); err != nil {
		if _, statErr := os.Stat(failed); statErr == nil {
			_ = os.Rename(failed, target)
		}
		return fmt.Errorf("还原备份: %w", err)
	}
	_ = os.RemoveAll(failed)
	return nil
}

// pruneBackups 只保留最近 keep 个 <target>.bak-* 备份，返回清理数量。
func pruneBackups(target string, keep int) (int, error) {
	if keep < 0 {
		keep = 0
	}
	parent := filepath.Dir(target)
	prefix := filepath.Base(target) + ".bak-"
	entries, err := os.ReadDir(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var backups []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			backups = append(backups, entry.Name())
		}
	}
	sort.Strings(backups)
	if len(backups) <= keep {
		return 0, nil
	}
	pruned := 0
	for _, name := range backups[:len(backups)-keep] {
		if err := os.RemoveAll(filepath.Join(parent, name)); err != nil {
			return pruned, err
		}
		pruned++
	}
	return pruned, nil
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

var _ Deployer = (*LocalDeployer)(nil)
