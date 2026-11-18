// Package secret 提供「直接录入凭据」的对称加密落库能力：
// 表单填入的密码/私钥经 AES-256-GCM 加密后存 SQLite，运行时解密使用。
// 主密钥不落库，来自配置 auth/security.credential_key 或环境变量 PACKRUN_CREDENTIAL_KEY。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// sealedFormat 密文格式：<版本>:<b64 nonce>:<b64 密文>，版本留主密钥轮换余地。
const sealedVersion = "v1"

// ErrNoMasterKey 主密钥未配置：直接录入能力不可用。
var ErrNoMasterKey = errors.New("未配置凭据主密钥（security.credential_key / PACKRUN_CREDENTIAL_KEY），无法使用「直接录入」凭据")

// Seal 用主密钥短语加密明文，返回带版本前缀的密文。
// 短语经 SHA-256 派生为 32 字节 AES-256 密钥，派生方式与 Open 严格对称。
func Seal(masterKey, plaintext string) (string, error) {
	key, err := deriveKey(masterKey)
	if err != nil {
		return "", err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("生成随机 nonce 失败: %w", err)
	}
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	return sealedVersion + ":" +
		base64.StdEncoding.EncodeToString(nonce) + ":" +
		base64.StdEncoding.EncodeToString(sealed), nil
}

// Open 解密 Seal 的产物。主密钥不符、密文损坏或被篡改时返回错误，
// 不会返回部分明文。
func Open(masterKey, sealedText string) (string, error) {
	key, err := deriveKey(masterKey)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.TrimSpace(sealedText), ":")
	if len(parts) != 3 || parts[0] != sealedVersion {
		return "", fmt.Errorf("密文格式不合法（缺少版本前缀或分段错误）")
	}
	nonce, err1 := base64.StdEncoding.DecodeString(parts[1])
	sealed, err2 := base64.StdEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil {
		return "", fmt.Errorf("密文 base64 解码失败")
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("解密失败：主密钥不匹配或密文已损坏（若曾更换主密钥，请重新录入该服务器的凭据）")
	}
	return string(plaintext), nil
}

// deriveKey 由主密钥短语派生 32 字节 AES-256 密钥。
func deriveKey(masterKey string) ([]byte, error) {
	if strings.TrimSpace(masterKey) == "" {
		return nil, ErrNoMasterKey
	}
	sum := sha256.Sum256([]byte(masterKey))
	return sum[:], nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES 失败: %w", err)
	}
	return cipher.NewGCM(block)
}
