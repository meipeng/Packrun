package deploy

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/packrun/packrun/internal/model"
)

// HostKeyTrust TOFU 首次信任钩子：由装配层注入，负责把首次握手看到的主机公钥落库并写审计。
// deploy 包不直接访问数据库。
type HostKeyTrust func(srv model.Server, algo, fingerprint, keyBase64 string)

// SecretResolver 可选能力：解密「直接录入」凭据（secret: 前缀的加密存储）。
type SecretResolver interface {
	ResolveSecret(cipher string) (string, error)
}

// SSHDeployer 通过 SSH 把包推送到业务服务器并远程执行脚本。
// 业务服务器无需对公网开放，只要求中控机可达。
type SSHDeployer struct {
	StepDelay  time.Duration
	KnownHosts string // 默认 ~/.ssh/known_hosts
	Insecure   bool   // 仅开发环境：跳过主机指纹校验
	AutoTrust  bool   // TOFU：服务器未登记指纹时，首次连接自动登记
	OnTrust    HostKeyTrust

	// SecretOpener 内置凭据解密函数：装配层注入（secret.Open + 主密钥），
	// 为 nil 时 secret: 凭据不可用。
	SecretOpener func(cipher string) (string, error)
}

// ResolveSecret 实现 SecretResolver 能力（nil 安全）。
func (d *SSHDeployer) ResolveSecret(cipher string) (string, error) {
	if d.SecretOpener == nil {
		return "", errors.New("执行器未配置解密密钥，内置（直接录入）凭据不可用")
	}
	return d.SecretOpener(cipher)
}

// CredentialRefBuiltIn 内置凭据的引用标记：只表示「走加密存储」，不含任何凭据材料。
const CredentialRefBuiltIn = "secret:内置凭据"

// IsBuiltInCredential 判断凭据引用是否指向内置加密存储。
func IsBuiltInCredential(ref string) bool {
	return strings.HasPrefix(strings.TrimSpace(ref), "secret:")
}

// ResolveServerCredential 凭据解析统一入口：secret: 前缀走内置解密，
// 其余走 env:/file: 引用解析。
func ResolveServerCredential(srv model.Server, resolver SecretResolver) (string, error) {
	if IsBuiltInCredential(srv.CredentialRef) {
		if resolver == nil {
			return "", errors.New("当前执行器不支持内置凭据（本地沙箱模式无解密能力）")
		}
		return resolver.ResolveSecret(srv.SecretCipher)
	}
	return ResolveCredential(srv.CredentialRef)
}

func (d *SSHDeployer) Deploy(ctx context.Context, req Request) (retErr error) {
	target, err := NormalizePath(req.Directory.Path)
	if err != nil {
		return err
	}
	if err := checkUnpackedSize(req); err != nil {
		return err
	}

	client, err := d.dial(req.Server)
	if err != nil {
		return err
	}
	defer client.Close()

	req.Hooks.logf(fmt.Sprintf("已连接 %s@%s:%d", req.Server.Username, req.Server.Host, req.Server.Port))
	remote := &remoteExec{client: client, timeout: 15 * time.Minute}

	var backupPath string
	defer func() {
		if retErr == nil || backupPath == "" {
			return
		}
		req.Hooks.logf("部署失败，正在恢复部署前版本…")
		// 任务取消时原 ctx 已失效，恢复动作使用独立超时，尽量把目标目录还原。
		restoreCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out, restoreErr := remote.run(restoreCtx, restoreRemoteBackupCommand(target, backupPath))
		if restoreErr != nil {
			msg := strings.TrimSpace(out)
			if msg == "" {
				msg = restoreErr.Error()
			}
			req.Hooks.logf("自动恢复失败：" + msg)
			retErr = fmt.Errorf("%w；自动恢复失败：%v（备份保留在 %s）", retErr, restoreErr, backupPath)
			return
		}
		req.Hooks.logf("已自动恢复部署前版本")
		retErr = fmt.Errorf("%w（已自动恢复部署前版本）", retErr)
	}()

	// 1. 准备目录
	req.Hooks.step(StepPrepare)
	req.Hooks.stage("prepare", 10)
	if out, err := remote.run(ctx, "mkdir -p "+shellQuote(target)); err != nil {
		return fmt.Errorf("创建目录失败: %v %s", err, out)
	}
	req.Hooks.logf("目标目录就绪：" + target)
	if err := pause(ctx, d.StepDelay); err != nil {
		return err
	}

	// 空间阈值：目标目录已占用超过策略上限时，拒绝本次部署
	if req.Directory.MaxSizeMB > 0 {
		out, err := remote.run(ctx, "du -sm "+shellQuote(target)+" 2>/dev/null | cut -f1")
		if err != nil {
			return fmt.Errorf("统计目录占用失败: %v %s", err, out)
		}
		if mb, convErr := strconv.Atoi(strings.TrimSpace(out)); convErr == nil {
			if mb > req.Directory.MaxSizeMB {
				return fmt.Errorf("目标目录已占用 %d MB，超过阈值 %d MB", mb, req.Directory.MaxSizeMB)
			}
			req.Hooks.logf(fmt.Sprintf("目录占用 %d MB / 阈值 %d MB", mb, req.Directory.MaxSizeMB))
		}
	}

	// 2. 前置校验
	if req.PreScript != nil {
		req.Hooks.step(StepPreCheck)
		req.Hooks.stage("precheck", 15)
		if err := runRemoteScript(ctx, remote, req.Server, target, req.Hooks, req.PreScript); err != nil {
			return err
		}
	}

	// 3. 备份
	req.Hooks.step(StepBackup)
	req.Hooks.stage("backup", 25)
	if req.Directory.BackupBeforeDeploy {
		stamp := time.Now().Format("20060102150405")
		backup := target + ".bak-" + stamp
		cmd := fmt.Sprintf("if [ -n \"$(ls -A %s 2>/dev/null)\" ]; then cp -a %s %s && echo BACKED_UP; else echo EMPTY; fi",
			shellQuote(target), shellQuote(target), shellQuote(backup))
		out, err := remote.run(ctx, cmd)
		if err != nil {
			return fmt.Errorf("备份失败: %v %s", err, out)
		}
		if strings.Contains(out, "BACKED_UP") {
			backupPath = backup
		}
		req.Hooks.logf(strings.TrimSpace(out))
	} else {
		req.Hooks.logf("目录策略未开启备份，跳过")
	}
	if err := pause(ctx, d.StepDelay); err != nil {
		return err
	}

	// 3. 上传
	req.Hooks.step(StepUpload)
	req.Hooks.stage("upload", 45)
	safeName := sanitizeName(req.Package.Name)
	if strings.TrimSpace(safeName) == "" || safeName == "." || safeName == ".." {
		return fmt.Errorf("安装包文件名不合法")
	}
	tmp := fmt.Sprintf("/tmp/packrun-%d-%s", time.Now().UnixNano(), safeName)
	if err := remote.upload(ctx, req.SourcePath, tmp); err != nil {
		return fmt.Errorf("上传失败: %w", err)
	}
	req.Hooks.logf(fmt.Sprintf("已上传至 %s（%d 字节）", tmp, req.Package.Size))

	// 4. 校验
	req.Hooks.step(StepVerify)
	req.Hooks.stage("verify", 60)
	out, err := remote.run(ctx, "sha256sum "+shellQuote(tmp))
	if err != nil {
		return fmt.Errorf("校验失败: %v %s", err, out)
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		return fmt.Errorf("校验失败：远程未返回摘要")
	}
	if fields[0] != req.Package.SHA256 {
		return fmt.Errorf("包校验不一致：期望 %s，实际 %s", req.Package.SHA256, fields[0])
	}
	req.Hooks.logf("SHA256 校验通过")
	if err := pause(ctx, d.StepDelay); err != nil {
		return err
	}

	// 5. 解包/替换
	req.Hooks.step(StepUnpack)
	req.Hooks.stage("unpack", 75)
	if req.Package.Kind == "zip" && req.Directory.PackageMode != model.PackageModeUpload {
		maxBytes, maxEntries := unpackBudget(req)
		// 解压前先统计条目数，避免海量小文件拖垮目标机。
		countCmd := fmt.Sprintf("unzip -Z1 %s | wc -l", shellQuote(tmp))
		countOut, err := remote.run(ctx, countCmd)
		if err != nil {
			return fmt.Errorf("统计压缩包条目数失败: %v %s", err, countOut)
		}
		entryCount, convErr := strconv.Atoi(strings.TrimSpace(countOut))
		if convErr != nil {
			return fmt.Errorf("统计压缩包条目数失败: %s", strings.TrimSpace(countOut))
		}
		if entryCount > maxEntries {
			return fmt.Errorf("压缩包条目数 %d 超过上限 %d", entryCount, maxEntries)
		}
		// 解压前先审查条目：拒绝绝对路径、.. 穿越、符号链接
		checkCmd := fmt.Sprintf("unzip -Z1 %s | grep -Eq '(^/|\\.\\.)' && echo UNSAFE || echo SAFE", shellQuote(tmp))
		checkOut, err := remote.run(ctx, checkCmd)
		if err != nil {
			return fmt.Errorf("压缩包条目审查失败: %v %s", err, checkOut)
		}
		if strings.Contains(checkOut, "UNSAFE") {
			return fmt.Errorf("压缩包内存在越界路径，已拒绝解压")
		}
		symlinkCmd := fmt.Sprintf(
			"command -v zipinfo >/dev/null 2>&1 || echo NOZIPINFO; zipinfo -l %s | grep -Eq '^l' && echo SYMLINK || echo SAFE",
			shellQuote(tmp))
		symlinkOut, err := remote.run(ctx, symlinkCmd)
		if err != nil {
			return fmt.Errorf("压缩包符号链接审查失败: %v %s", err, symlinkOut)
		}
		if strings.Contains(symlinkOut, "NOZIPINFO") {
			return fmt.Errorf("目标机缺少 zipinfo，无法完成符号链接安全审查")
		}
		if strings.Contains(symlinkOut, "SYMLINK") {
			return fmt.Errorf("压缩包内不允许符号链接，已拒绝解压")
		}
		blocks := maxBytes / 1024
		if blocks < 1 {
			blocks = 1
		}
		unzipCmd := fmt.Sprintf("ulimit -f %d; unzip -o -q %s -d %s", blocks, shellQuote(tmp), shellQuote(target))
		out, err := remote.run(ctx, "sh -c "+shellQuote(unzipCmd))
		if err != nil {
			return fmt.Errorf("解压失败: %v %s", err, out)
		}
		// 解压后复核目录占用，ulimit 之外再兜一层。
		if req.Directory.MaxSizeMB > 0 {
			duOut, duErr := remote.run(ctx, "du -sm "+shellQuote(target)+" 2>/dev/null | cut -f1")
			if duErr != nil {
				remote.run(ctx, "rm -f "+shellQuote(tmp))
				return fmt.Errorf("解压后统计目录占用失败: %v %s", duErr, duOut)
			}
			if mb, convErr := strconv.Atoi(strings.TrimSpace(duOut)); convErr == nil && mb > req.Directory.MaxSizeMB {
				remote.run(ctx, "rm -f "+shellQuote(tmp))
				return fmt.Errorf("解压后目录占用 %d MB，超过阈值 %d MB，请人工检查目标目录", mb, req.Directory.MaxSizeMB)
			}
		}
		req.Hooks.logf("已解压到目标目录")
	} else {
		dst := filepath.Join(target, safeName)
		if !req.Directory.AllowOverwrite {
			if out, err := remote.run(ctx, "test -e "+shellQuote(dst)+" && echo EXISTS || echo FREE"); err == nil && strings.Contains(out, "EXISTS") {
				return fmt.Errorf("目标已存在 %s，且目录策略禁止覆盖", dst)
			}
		}
		if out, err := remote.run(ctx, fmt.Sprintf("mv -f %s %s", shellQuote(tmp), shellQuote(dst))); err != nil {
			return fmt.Errorf("替换制品失败: %v %s", err, out)
		}
		if req.Package.Kind == "zip" {
			req.Hooks.logf("已上传压缩包 " + dst)
		} else {
			req.Hooks.logf("已替换制品 " + dst)
		}
	}
	remote.run(ctx, "rm -f "+shellQuote(tmp))

	// 7. 后置脚本
	req.Hooks.step(StepScript)
	req.Hooks.stage("script", 90)
	if req.Script == nil {
		req.Hooks.logf("未绑定部署后脚本，跳过")
	} else if err := runRemoteScript(ctx, remote, req.Server, target, req.Hooks, req.Script); err != nil {
		return err
	}

	// 8. 清理超出保留版本的备份
	if req.Directory.BackupBeforeDeploy {
		keep := req.Directory.KeepReleases
		pruned, err := pruneRemoteBackups(ctx, remote, target, keep)
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

// runRemoteScript 以 base64 传递脚本内容，在目标目录内按超时与执行身份运行。
func runRemoteScript(ctx context.Context, remote *remoteExec, srv model.Server, target string, hooks Hooks, script *model.Script) error {
	timeout := script.TimeoutSec
	if timeout <= 0 {
		timeout = 120
	}
	hooks.logf(fmt.Sprintf("执行脚本「%s」v%d，超时 %ds", script.Name, script.Version, timeout))
	for _, line := range strings.Split(strings.TrimSpace(script.Content), "\n") {
		hooks.logf("  | " + line)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(script.Content))
	inner := fmt.Sprintf("echo %s | base64 -d | sh", encoded)
	if script.RunAs != "" && script.RunAs != srv.Username {
		inner = fmt.Sprintf("sudo -n -u %s sh -c %s", shellQuote(script.RunAs), shellQuote(inner))
	}
	cmd := fmt.Sprintf("cd %s && timeout %d sh -c %s", shellQuote(target), timeout, shellQuote(inner))
	out, err := remote.run(ctx, cmd)
	if out != "" {
		for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			hooks.logf("  | " + line)
		}
	}
	if err != nil {
		return fmt.Errorf("脚本「%s」执行失败: %w", script.Name, err)
	}
	hooks.logf(fmt.Sprintf("脚本「%s」执行完成", script.Name))
	return nil
}

// restoreRemoteBackupCommand 用备份目录换回目标目录。
// 失败现场先改名保留，恢复成功后再删除；恢复失败时现场与备份都保留，便于人工处理。
func restoreRemoteBackupCommand(target, backup string) string {
	failed := target + ".failed-" + time.Now().Format("20060102150405.000000000")
	return fmt.Sprintf(
		"if [ -e %s ]; then mv %s %s; fi; mv %s %s && rm -rf %s",
		shellQuote(target), shellQuote(target), shellQuote(failed),
		shellQuote(backup), shellQuote(target), shellQuote(failed),
	)
}

// pruneRemoteBackups 只保留最近 keep 个 <target>.bak-* 备份，返回清理数量。
func pruneRemoteBackups(ctx context.Context, remote *remoteExec, target string, keep int) (int, error) {
	if keep < 0 {
		keep = 0
	}
	cmd := fmt.Sprintf("ls -1dt %s 2>/dev/null | tail -n +%d | while IFS= read -r p; do rm -rf \"$p\" && echo \"$p\"; done",
		shellQuote(target)+".bak-*", keep+1)
	out, err := remote.run(ctx, cmd)
	if err != nil {
		return 0, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return 0, nil
	}
	return len(strings.Split(out, "\n")), nil
}

func (d *SSHDeployer) dial(srv model.Server) (*ssh.Client, error) {
	credential, err := ResolveServerCredential(srv, d)
	if err != nil {
		return nil, err
	}

	var authMethod ssh.AuthMethod
	if srv.AuthType == "password" {
		authMethod = ssh.Password(credential)
	} else {
		signer, err := ssh.ParsePrivateKey([]byte(credential))
		if err != nil {
			return nil, fmt.Errorf("解析私钥失败: %w", err)
		}
		authMethod = ssh.PublicKeys(signer)
	}

	cfg := &ssh.ClientConfig{
		User:            srv.Username,
		Auth:            []ssh.AuthMethod{authMethod},
		HostKeyCallback: d.hostKeyCallbackFor(srv),
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port))
	return ssh.Dial("tcp", addr, cfg)
}

// hostKeyCallbackFor 按服务器选择主机指纹校验策略：
// 已登记指纹 → 严格比对；未登记且 TOFU 开启 → 首次信任并异步落库；
// 否则回退中控机 known_hosts 文件（存量兼容）。
func (d *SSHDeployer) hostKeyCallbackFor(srv model.Server) ssh.HostKeyCallback {
	if d.Insecure {
		return ssh.InsecureIgnoreHostKey()
	}
	if srv.HostKeyFingerprint != "" {
		expectAlgo, expectFP := srv.HostKeyAlgo, srv.HostKeyFingerprint
		return func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fp := ssh.FingerprintSHA256(key)
			if fp == expectFP && (expectAlgo == "" || key.Type() == expectAlgo) {
				return nil
			}
			return fmt.Errorf("主机指纹不匹配：登记 %s（%s），实际 %s（%s）。服务器可能已重建或存在中间人风险；若确认服务器已重建，请在「服务器管理 → 指纹」中清除后重新检测",
				expectFP, orDefault(expectAlgo, "未知"), fp, key.Type())
		}
	}
	if d.AutoTrust && d.OnTrust != nil {
		return func(_ string, _ net.Addr, key ssh.PublicKey) error {
			algo := key.Type()
			fp := ssh.FingerprintSHA256(key)
			keyB64 := base64.StdEncoding.EncodeToString(key.Marshal())
			go d.OnTrust(srv, algo, fp, keyB64)
			return nil
		}
	}
	return d.knownHostsCallback()
}

// knownHostsCallback 回退到中控机 known_hosts 文件的存量校验方式。
func (d *SSHDeployer) knownHostsCallback() ssh.HostKeyCallback {
	path := d.KnownHosts
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".ssh", "known_hosts")
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		// 无 known_hosts 时拒绝连接，避免静默信任未知主机
		return func(_ string, _ net.Addr, _ ssh.PublicKey) error {
			return fmt.Errorf("无法加载 known_hosts（%s）：%v；请在「服务器管理」中对目标服务器执行检测以自动登记主机指纹，或仅在开发环境设置 PACKRUN_SSH_INSECURE=1", path, err)
		}
	}
	return cb
}

// HostKeyInfo 是一次主机指纹采集的结果。
type HostKeyInfo struct {
	Algo        string `json:"algo"`        // 公钥算法，如 ssh-ed25519
	Fingerprint string `json:"fingerprint"` // SHA256:...
	Pinned      bool   `json:"pinned"`      // 库中是否已登记指纹
	Matched     bool   `json:"matched"`     // 本次采集与登记指纹是否一致（未登记时恒为 true）
}

// ProbeHostKey 与目标机完成一次 SSH 握手并返回其主机公钥指纹。
// 指纹在握手阶段交换、先于认证完成，因此凭据无效时仍能拿到指纹。
// 已登记指纹不匹配时返回 Matched=false 而不是直接报错，便于管理员判断后处置。
func (d *SSHDeployer) ProbeHostKey(_ context.Context, srv model.Server) (*HostKeyInfo, error) {
	var captured *HostKeyInfo
	callback := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)
		matched := true
		if srv.HostKeyFingerprint != "" {
			matched = fp == srv.HostKeyFingerprint &&
				(srv.HostKeyAlgo == "" || key.Type() == srv.HostKeyAlgo)
		}
		captured = &HostKeyInfo{
			Algo:        key.Type(),
			Fingerprint: fp,
			Pinned:      srv.HostKeyFingerprint != "",
			Matched:     matched,
		}
		return nil
	}

	cfg := &ssh.ClientConfig{
		User:            srv.Username,
		Auth:            []ssh.AuthMethod{}, // 只握手不认证；认证失败不影响指纹采集
		HostKeyCallback: callback,
		Timeout:         10 * time.Second,
	}
	addr := net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port))
	client, dialErr := ssh.Dial("tcp", addr, cfg)
	if client != nil {
		client.Close()
	}
	if captured == nil {
		if dialErr == nil {
			dialErr = errors.New("握手未返回主机公钥")
		}
		return nil, fmt.Errorf("采集主机指纹失败: %w", dialErr)
	}
	return captured, nil
}

// InspectCredential 自检凭据能否被运行时解析：内置凭据验证可解密，
// key 类型额外验证私钥可解析且不带 passphrase。返回可读的检查描述，失败时返回错误。
func InspectCredential(srv model.Server, resolver SecretResolver) (string, error) {
	credential, err := ResolveServerCredential(srv, resolver)
	if err != nil {
		return "", err
	}
	if srv.AuthType == "password" {
		if IsBuiltInCredential(srv.CredentialRef) {
			return "内置口令可解密", nil
		}
		return "口令引用可解析", nil
	}
	signer, err := ssh.ParsePrivateKey([]byte(credential))
	if err != nil {
		var pp *ssh.PassphraseMissingError
		if errors.As(err, &pp) {
			return "", errors.New("私钥已加密（需要 passphrase），中控无交互流程无法使用；请改用无 passphrase 的密钥")
		}
		return "", fmt.Errorf("私钥解析失败: %w", err)
	}
	return fmt.Sprintf("凭据可解析（%s 私钥）", signer.PublicKey().Type()), nil
}

// ResolveCredential 只接受引用，绝不把明文写进数据库。
// 支持 env:NAME 与 file:/abs/or/~/path 两种形式。
func ResolveCredential(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	switch {
	case strings.HasPrefix(ref, "env:"):
		name := strings.TrimPrefix(ref, "env:")
		v := os.Getenv(name)
		if v == "" {
			return "", fmt.Errorf("环境变量 %s 为空，无法解析凭据", name)
		}
		return v, nil
	case strings.HasPrefix(ref, "file:"):
		path := strings.TrimPrefix(ref, "file:")
		if strings.HasPrefix(path, "~/") {
			home, _ := os.UserHomeDir()
			path = filepath.Join(home, path[2:])
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("读取凭据文件失败: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	default:
		return "", fmt.Errorf("不支持的凭据引用 %q，请使用 env: 或 file: 前缀", ref)
	}
}

// ---------- 远程执行辅助 ----------

type remoteExec struct {
	client  *ssh.Client
	timeout time.Duration
}

func (r *remoteExec) run(ctx context.Context, cmd string) (string, error) {
	session, err := r.client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := session.CombinedOutput(cmd)
		done <- result{out: out, err: err}
	}()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return "", ctx.Err()
	case <-time.After(r.timeout):
		_ = session.Signal(ssh.SIGKILL)
		return "", fmt.Errorf("远程命令超时")
	case res := <-done:
		return string(res.out), res.err
	}
}

func (r *remoteExec) upload(ctx context.Context, local, remote string) error {
	session, err := r.client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f.Close()

	session.Stdin = f
	done := make(chan error, 1)
	go func() {
		done <- session.Run("cat > " + shellQuote(remote))
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func sanitizeName(name string) string {
	name = filepath.Base(name)
	return strings.ReplaceAll(name, " ", "_")
}

func pause(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

var _ Deployer = (*SSHDeployer)(nil)
