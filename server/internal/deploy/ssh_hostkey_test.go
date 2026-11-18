package deploy

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/packrun/packrun/internal/model"
)

// probeAddr knownhosts 回调要求非 nil 远端地址，与 ssh.Dial 实际行为一致。
func probeAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(10, 0, 0, 9), Port: 22}
}

// testPublicKey 生成一把临时 ed25519 主机公钥。
func testPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatalf("导出公钥失败: %v", err)
	}
	return pub
}

func TestHostKeyCallbackPinnedMatch(t *testing.T) {
	pub := testPublicKey(t)
	srv := model.Server{
		Name:               "app-01",
		HostKeyAlgo:        pub.Type(),
		HostKeyFingerprint: ssh.FingerprintSHA256(pub),
	}
	d := &SSHDeployer{}
	err := d.hostKeyCallbackFor(srv)("web-01", probeAddr(), pub)
	if err != nil {
		t.Fatalf("已登记指纹匹配时应放行, 得到: %v", err)
	}
}

func TestHostKeyCallbackPinnedMismatch(t *testing.T) {
	pub := testPublicKey(t)
	other := testPublicKey(t)
	srv := model.Server{
		Name:               "app-01",
		HostKeyAlgo:        pub.Type(),
		HostKeyFingerprint: ssh.FingerprintSHA256(pub),
	}
	d := &SSHDeployer{}
	err := d.hostKeyCallbackFor(srv)("web-01", probeAddr(), other)
	if err == nil {
		t.Fatal("指纹不匹配时应拒绝连接")
	}
	if !strings.Contains(err.Error(), "主机指纹不匹配") {
		t.Errorf("错误信息应提示指纹不匹配, 得到: %v", err)
	}
}

func TestHostKeyCallbackPinnedAlgoMismatch(t *testing.T) {
	pub := testPublicKey(t)
	srv := model.Server{
		Name:               "app-01",
		HostKeyAlgo:        "ssh-rsa", // 算法不一致即视为不匹配
		HostKeyFingerprint: ssh.FingerprintSHA256(pub),
	}
	d := &SSHDeployer{}
	if err := d.hostKeyCallbackFor(srv)("web-01", probeAddr(), pub); err == nil {
		t.Fatal("算法不一致时应拒绝连接")
	}
}

func TestHostKeyCallbackTOFU(t *testing.T) {
	pub := testPublicKey(t)
	trusted := make(chan string, 1)
	srv := model.Server{Name: "app-01", Host: "10.0.0.9", Port: 22}
	d := &SSHDeployer{
		AutoTrust: true,
		OnTrust: func(_ model.Server, _, fingerprint, _ string) {
			trusted <- fingerprint
		},
	}
	cb := d.hostKeyCallbackFor(srv)
	if err := cb("web-01", probeAddr(), pub); err != nil {
		t.Fatalf("TOFU 未登记时应放行, 得到: %v", err)
	}
	select {
	case fp := <-trusted:
		if fp != ssh.FingerprintSHA256(pub) {
			t.Errorf("TOFU 回调指纹 = %s, 期望 %s", fp, ssh.FingerprintSHA256(pub))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TOFU 回调未被触发")
	}
}

func TestHostKeyCallbackNoTrustNoKnownHosts(t *testing.T) {
	pub := testPublicKey(t)
	// 关闭 TOFU：回退中控机 known_hosts 文件校验。
	// 指向一个几乎不可能登记的主机名，应拒绝且不触发登记。
	called := false
	srv := model.Server{Name: "app-01"}
	d := &SSHDeployer{
		AutoTrust: false,
		OnTrust: func(model.Server, string, string, string) {
			called = true
		},
	}
	err := d.hostKeyCallbackFor(srv)("packrun-callback-test-host.invalid", probeAddr(), pub)
	if err == nil {
		t.Fatal("未登记且未开启 TOFU 时应拒绝未知主机")
	}
	if called {
		t.Error("拒绝路径不应触发指纹登记")
	}
}
