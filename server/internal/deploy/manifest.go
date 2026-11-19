package deploy

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Manifest 描述一次 zip 解包发布相对目标目录的文件级差异，随任务记录落库并在任务详情展示。
// Removed 只保留前 manifestRemovedLimit 条，超出以 Truncated 标注，避免超大目录撑爆任务记录。
type Manifest struct {
	TotalEntries int      `json:"totalEntries"`      // 包内文件条目数（不含目录）
	Added        int      `json:"added"`             // 新增文件数
	Overwritten  int      `json:"overwritten"`       // 覆盖现有文件数
	Removed      []string `json:"removed,omitempty"` // 干净替换清理的旧文件清单
	Truncated    bool     `json:"truncated,omitempty"`
	CleanReplace bool     `json:"cleanReplace"`
}

// manifestRemovedLimit 差异清单中保留的「已删除文件」条数上限。
const manifestRemovedLimit = 500

// maxTrackedFiles 参与文件级差异计算的目标目录文件数上限。
// 超过即视为「无法安全计算差异」：干净替换/仅新增约束宁可拒绝发布也不误删。
const maxTrackedFiles = 50000

// applyManifestLimits 按 manifestRemovedLimit 截断 Removed 清单并标注 Truncated。
func (m *Manifest) applyManifestLimits(removed []string) {
	if len(removed) > manifestRemovedLimit {
		m.Removed = append([]string{}, removed[:manifestRemovedLimit]...)
		m.Truncated = true
		return
	}
	if len(removed) > 0 {
		m.Removed = append([]string{}, removed...)
	}
}

// sanitizeRef 只保留文件名安全字符；空/全非法时回退 fallback。
// 用于目标机临时文件命名（任务号、包名等）。
func sanitizeRef(ref, fallback string) string {
	ref = strings.TrimSpace(ref)
	var b strings.Builder
	for _, r := range ref {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	if out := b.String(); out != "" {
		return out
	}
	return fallback
}

// splitRemoteEntries 按行拆分远程清单输出：去空行与 \r，剔除目录条目（以 / 结尾）。
func splitRemoteEntries(out string) []string {
	lines := strings.Split(strings.ReplaceAll(out, "\r", ""), "\n")
	entries := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasSuffix(l, "/") {
			continue
		}
		entries = append(entries, l)
	}
	return entries
}

// containsPathTraversal 判断压缩包条目是否包含 .. 穿越（按路径段判断）。
func containsPathTraversal(entry string) bool {
	for _, part := range strings.Split(entry, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// normalizeRelPaths 归一化相对路径清单：去掉 ./ 前缀，排序去重。
func normalizeRelPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		p = strings.TrimPrefix(strings.TrimPrefix(p, "./"), "/")
		p = strings.TrimSuffix(p, "/")
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// diffManifest 计算 zip 包条目与目标目录现有文件的文件级差异：
// added = 包内新增，overwritten = 覆盖现有，removed = 包内不存在的旧文件。
func diffManifest(existing, entries []string) (added, overwritten int, removed []string) {
	existingSet := make(map[string]bool, len(existing))
	for _, f := range normalizeRelPaths(existing) {
		existingSet[f] = true
	}
	removedSet := make(map[string]bool, len(existingSet))
	for k := range existingSet {
		removedSet[k] = true
	}
	for _, e := range normalizeRelPaths(entries) {
		if existingSet[e] {
			overwritten++
			delete(removedSet, e)
		} else {
			added++
		}
	}
	for k := range removedSet {
		removed = append(removed, k)
	}
	sort.Strings(removed)
	return added, overwritten, removed
}

// intersectEntries 返回 entries 中存在于 existing 的条目（用于「仅新增」冲突示例）。
func intersectEntries(existing, entries []string) []string {
	existingSet := make(map[string]bool, len(existing))
	for _, f := range existing {
		existingSet[strings.TrimPrefix(f, "./")] = true
	}
	var out []string
	for _, e := range entries {
		if existingSet[strings.TrimPrefix(e, "./")] {
			out = append(out, e)
		}
	}
	return out
}

// listRemoteExistingFiles 列出目标目录内的现有相对文件路径（find -type f）。
// 文件数超过上限或命令失败时返回 ok=false，调用方按「无法安全计算差异」拒绝相关操作。
func listRemoteExistingFiles(ctx context.Context, remote *remoteExec, target string, hooks Hooks) ([]string, bool) {
	out, err := remote.run(ctx,
		fmt.Sprintf("cd %s && find . -type f | head -n %d", shellQuote(target), maxTrackedFiles+1))
	if err != nil {
		hooks.logf("列出目标目录现有文件失败：" + err.Error())
		return nil, false
	}
	lines := splitRemoteEntries(out)
	if len(lines) > maxTrackedFiles {
		hooks.logf(fmt.Sprintf("目标目录文件数超过 %d，无法安全计算文件级差异", maxTrackedFiles))
		return nil, false
	}
	files := make([]string, 0, len(lines))
	for _, l := range lines {
		files = append(files, strings.TrimPrefix(l, "./"))
	}
	return files, true
}

// removeRemoteFiles 批量删除目标目录内的旧文件（干净替换），返回实际发起删除的文件数。
// 路径均为相对路径且来自 find 输出；仍逐条校验非空、非绝对路径、无 .. 穿越，防御性兜底。
func removeRemoteFiles(ctx context.Context, remote *remoteExec, target string, files []string, hooks Hooks) int {
	safe := make([]string, 0, len(files))
	for _, f := range files {
		if f == "" || strings.HasPrefix(f, "/") || containsPathTraversal(f) {
			continue
		}
		safe = append(safe, f)
	}
	removed := 0
	const batch = 50
	for start := 0; start < len(safe); start += batch {
		end := start + batch
		if end > len(safe) {
			end = len(safe)
		}
		var b strings.Builder
		b.WriteString("cd ")
		b.WriteString(shellQuote(target))
		b.WriteString(" && rm -f --")
		for _, f := range safe[start:end] {
			b.WriteString(" ")
			b.WriteString(shellQuote(f))
		}
		if _, err := remote.run(ctx, b.String()); err != nil {
			hooks.logf(fmt.Sprintf("清理旧文件失败（已清理 %d 个，剩余保留）：%v", removed, err))
			return removed
		}
		removed += end - start
	}
	return removed
}
