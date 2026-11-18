package deploy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/packrun/packrun/internal/model"
	"github.com/packrun/packrun/internal/secret"
)

// fmtSscan 字符串转整数的小包装，便于在测试里解析端口。
func fmtSscan(s string, n *int) (int, error) { return fmt.Sscan(s, n) }

// waitTimeout TOFU 回调等待上限。
func waitTimeout() <-chan time.Time { return time.After(2 * time.Second) }

// fakeSSHServer 进程内最小 SSH 服务端：接受任意认证，exec 时回放固定输出。
// 用于端到端验证 TOFU 指纹登记、指纹校验与指标采集，无需真实机器。
type fakeSSHServer struct {
	t          *testing.T
	cfg        *ssh.ServerConfig
	listener   net.Listener
	execOutput string
	hostPub    ssh.PublicKey

	mu       sync.Mutex
	execCmds []string
}

func startFakeSSHServer(t *testing.T, execOutput string) *fakeSSHServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成主机密钥失败: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("签名器失败: %v", err)
	}
	s := &fakeSSHServer{t: t, execOutput: execOutput, hostPub: signer.PublicKey()}
	s.cfg = &ssh.ServerConfig{NoClientAuth: true}
	s.cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	s.listener = ln
	t.Cleanup(func() { ln.Close() })
	go s.serve()
	return s
}

func (s *fakeSSHServer) addr() string { return s.listener.Addr().String() }

func (s *fakeSSHServer) execCommands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.execCmds...)
}

func (s *fakeSSHServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeSSHServer) handle(conn net.Conn) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, s.cfg)
	if err != nil {
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)
	for newChan := range chans {
		go s.handleChannel(newChan)
	}
}

func (s *fakeSSHServer) handleChannel(newChan ssh.NewChannel) {
	if newChan.ChannelType() != "session" {
		newChan.Reject(ssh.UnknownChannelType, "unsupported")
		return
	}
	ch, reqs, _ := newChan.Accept()
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "exec":
			s.mu.Lock()
			s.execCmds = append(s.execCmds, string(req.Payload))
			s.mu.Unlock()
			_, _ = ch.Write([]byte(s.execOutput))
			req.Reply(true, nil)
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
			ch.Close()
			return
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

func newFakeServerModel(port int) model.Server {
	return model.Server{
		Name: "fake-01", Env: model.EnvTest, Host: "127.0.0.1", Port: port,
		Username: "deploy", AuthType: "password", CredentialRef: "env:PACKRUN_FAKE_SSH_PW",
	}
}

func TestSSHTOFUAutoTrustAndPinVerify(t *testing.T) {
	srv := startFakeSSHServer(t, "")
	port := func() int {
		_, p, _ := net.SplitHostPort(srv.addr())
		var n int
		_, _ = fmtSscan(p, &n)
		return n
	}()
	t.Setenv("PACKRUN_FAKE_SSH_PW", "anything")

	// 1. 首次连接：TOFU 自动登记
	var gotAlgo, gotFP, gotB64 string
	trusted := make(chan struct{}, 1)
	d := &SSHDeployer{
		AutoTrust: true,
		OnTrust: func(_ model.Server, algo, fingerprint, keyBase64 string) {
			gotAlgo, gotFP, gotB64 = algo, fingerprint, keyBase64
			trusted <- struct{}{}
		},
	}
	mdl := newFakeServerModel(port)
	if err := d.Ping(context.Background(), mdl); err != nil {
		t.Fatalf("首次连接应成功: %v", err)
	}
	select {
	case <-trusted:
	case <-waitTimeout():
		t.Fatal("TOFU 登记未被触发")
	}
	if gotFP != ssh.FingerprintSHA256(srv.hostPub) {
		t.Errorf("登记指纹 = %s, 期望 %s", gotFP, ssh.FingerprintSHA256(srv.hostPub))
	}
	if gotAlgo != srv.hostPub.Type() || gotB64 == "" {
		t.Errorf("算法/公钥不符: %s / %q", gotAlgo, gotB64)
	}

	// 2. 已登记指纹：再次连接校验通过
	mdl.HostKeyAlgo = gotAlgo
	mdl.HostKeyFingerprint = gotFP
	d2 := &SSHDeployer{}
	if err := d2.Ping(context.Background(), mdl); err != nil {
		t.Fatalf("已登记指纹连接应成功: %v", err)
	}

	// 3. 指纹不匹配：拒绝连接并给出明确提示
	mdl.HostKeyFingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	err := d2.Ping(context.Background(), mdl)
	if err == nil || !strings.Contains(err.Error(), "主机指纹不匹配") {
		t.Fatalf("指纹不匹配应拒绝, 得到: %v", err)
	}
}

func TestSSHProbeHostKey(t *testing.T) {
	srv := startFakeSSHServer(t, "")
	_, port, _ := net.SplitHostPort(srv.addr())
	var p int
	_, _ = fmtSscan(port, &p)
	mdl := newFakeServerModel(p)
	// 无有效凭据也能拿到指纹：握手先于认证完成
	mdl.CredentialRef = "env:PACKRUN_FAKE_MISSING_PW"

	d := &SSHDeployer{}
	info, err := d.ProbeHostKey(context.Background(), mdl)
	if err != nil {
		t.Fatalf("指纹采集失败: %v", err)
	}
	if info.Fingerprint != ssh.FingerprintSHA256(srv.hostPub) || info.Pinned || !info.Matched {
		t.Errorf("采集结果不符: %+v", info)
	}
}

const fakeMetricsOutput = "PRHOST=fake-01\n" +
	"PROS=Linux 5.15.0 x86_64\n" +
	"PRUP=86400.5 100000.0\n" +
	"PRCORES=4\n" +
	"PRLOAD=1.25 0.80 0.50 1/100 200\n" +
	"PRMEM=4096000 2048000 \n" +
	"PRDF /|/dev/sda1 10240000 5120000 5120000 50% /\n" +
	"PRDF /opt/app|/dev/sdb1 2048000 204800 1843200 10% /opt/app\n"

func TestSSHMetricsProbe(t *testing.T) {
	srv := startFakeSSHServer(t, fakeMetricsOutput)
	_, port, _ := net.SplitHostPort(srv.addr())
	var p int
	_, _ = fmtSscan(port, &p)
	t.Setenv("PACKRUN_FAKE_SSH_PW", "anything")

	// 模拟真实流程：先采集指纹 → 登记后再采指标
	d := &SSHDeployer{}
	mdl := newFakeServerModel(p)
	mdl.ID = 42
	info, err := d.ProbeHostKey(context.Background(), mdl)
	if err != nil {
		t.Fatalf("指纹采集失败: %v", err)
	}
	mdl.HostKeyAlgo = info.Algo
	mdl.HostKeyFingerprint = info.Fingerprint

	m, err := d.Probe(context.Background(), mdl, []string{"/opt/app"})
	if err != nil {
		t.Fatalf("指标采集失败: %v", err)
	}
	if m.ServerID != mdl.ID {
		t.Errorf("ServerID = %d, 期望 %d", m.ServerID, mdl.ID)
	}
	if m.Hostname != "fake-01" || m.CPUCores != 4 || m.UptimeSec != 86400 {
		t.Errorf("基础指标不符: %+v", m)
	}
	if m.Load1 != 1.25 || m.MemTotalMB != 4000 || m.MemUsedMB != 2000 {
		t.Errorf("负载/内存不符: load1=%v mem=%d/%d", m.Load1, m.MemUsedMB, m.MemTotalMB)
	}
	if !strings.Contains(m.DirUsage, `"path":"/opt/app"`) || !strings.Contains(m.DirUsage, `"pct":10`) {
		t.Errorf("目录占用不符: %s", m.DirUsage)
	}
	cmds := srv.execCommands()
	if len(cmds) != 1 || !strings.Contains(cmds[0], "df -kP") || !strings.Contains(cmds[0], "'/opt/app'") {
		t.Errorf("采集命令不符: %v", cmds)
	}
}

func TestSSHBuiltInCredentialDial(t *testing.T) {
	srvFake := startFakeSSHServer(t, "")
	_, portStr, _ := net.SplitHostPort(srvFake.addr())
	var port int
	_, _ = fmtSscan(portStr, &port)

	const masterKey = "test-master-key-phrase-32-chars!"
	sealed, err := secret.Seal(masterKey, "any-accepted-password")
	if err != nil {
		t.Fatalf("加密失败: %v, 期望 %q", err, sealed)
	}

	mdl := newFakeServerModel(port)
	mdl.CredentialRef = CredentialRefBuiltIn
	mdl.SecretCipher = sealed
	mdl.AuthType = "password"

	// 前置：先采集并登记主机指纹（真实流程中由 TOFU 或「指纹」按钮完成）
	d0 := &SSHDeployer{}
	info, err := d0.ProbeHostKey(context.Background(), mdl)
	if err != nil {
		t.Fatalf("指纹采集失败: %v", err)
	}
	mdl.HostKeyAlgo = info.Algo
	mdl.HostKeyFingerprint = info.Fingerprint

	// 1. 正确主密钥：内置凭据解密后建连成功
	d := &SSHDeployer{
		SecretOpener: func(cipherText string) (string, error) { return secret.Open(masterKey, cipherText) },
	}
	if err := d.Ping(context.Background(), mdl); err != nil {
		t.Fatalf("内置凭据建连应成功: %v", err)
	}

	// 2. 错误主密钥：解密失败并提示重新录入
	dWrong := &SSHDeployer{
		SecretOpener: func(cipherText string) (string, error) {
			return secret.Open("wrong-key-phrase-32-characters!!", cipherText)
		},
	}
	err = dWrong.Ping(context.Background(), mdl)
	if err == nil || !strings.Contains(err.Error(), "重新录入") {
		t.Fatalf("错误主密钥应提示重新录入, 得到: %v", err)
	}

	// 3. 未注入解密能力：明确报错
	dNone := &SSHDeployer{}
	err = dNone.Ping(context.Background(), mdl)
	if err == nil || !strings.Contains(err.Error(), "内置") {
		t.Fatalf("未注入解密能力应明确报错, 得到: %v", err)
	}

	// 4. 自检：内置口令可解密
	msg, err := InspectCredential(mdl, d)
	if err != nil || !strings.Contains(msg, "内置口令可解密") {
		t.Fatalf("自检结果不符: %q, %v", msg, err)
	}
}
