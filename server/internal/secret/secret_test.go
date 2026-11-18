package secret

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestSealOpenRoundtrip(t *testing.T) {
	plain := "server-password-!@# 中文\nmulti-line-private-key"
	sealed, err := Seal("a-very-long-master-key-phrase-32chars", plain)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if !strings.HasPrefix(sealed, "v1:") {
		t.Errorf("密文应带版本前缀: %s", sealed)
	}
	if strings.Contains(sealed, "server-password") {
		t.Error("密文不得包含明文片段")
	}
	got, err := Open("a-very-long-master-key-phrase-32chars", sealed)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if got != plain {
		t.Errorf("往返不一致: %q != %q", got, plain)
	}
}

func TestSealSamePlaintextDifferentCiphertext(t *testing.T) {
	// 随机 nonce：同一明文两次加密产出不同密文
	s1, _ := Seal("key-key-key-key-32-chars-long!!", "same")
	s2, _ := Seal("key-key-key-key-32-chars-long!!", "same")
	if s1 == s2 {
		t.Error("相同明文不应产生相同密文（nonce 必须随机）")
	}
}

func TestOpenWrongKey(t *testing.T) {
	sealed, _ := Seal("correct-key-phrase-32-characters!", "secret-data")
	if _, err := Open("wrong-key-phrase-32-characters!!", sealed); err == nil {
		t.Fatal("错误主密钥应解密失败")
	}
}

func TestOpenTampered(t *testing.T) {
	sealed, _ := Seal("master-key-phrase-32-characters!", "data")
	i := strings.LastIndex(sealed, ":")
	raw, err := base64.StdEncoding.DecodeString(sealed[i+1:])
	if err != nil {
		t.Fatalf("密文解码失败: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("密文为空")
	}
	raw[0] ^= 0xFF // 确定性翻转一个字节
	tampered := sealed[:i+1] + base64.StdEncoding.EncodeToString(raw)
	if _, err := Open("master-key-phrase-32-characters!", tampered); err == nil {
		t.Fatal("篡改密文应解密失败")
	}
}

func TestEmptyMasterKey(t *testing.T) {
	if _, err := Seal("  ", "data"); err != ErrNoMasterKey {
		t.Errorf("空主密钥应返回 ErrNoMasterKey, 得到: %v", err)
	}
	if _, err := Open("", "v1:AA:BB"); err != ErrNoMasterKey {
		t.Errorf("空主密钥应返回 ErrNoMasterKey, 得到: %v", err)
	}
}

func TestOpenBadFormat(t *testing.T) {
	key := "master-key-phrase-32-characters!"
	cases := []string{"", "not-sealed", "v2:AA:BB", "v1:only-two-parts", "v1:!!!:!!!"}
	for _, c := range cases {
		if _, err := Open(key, c); err == nil {
			t.Errorf("非法密文 %q 应返回错误", c)
		}
	}
}
