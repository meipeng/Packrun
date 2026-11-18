package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidOTP = errors.New("动态口令无效")

// ---------- 第一因子：口令 ----------

func HashPassword(raw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.DefaultCost)
	return string(b), err
}

func VerifyPassword(hash, raw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(raw)) == nil
}

// ---------- 会话令牌 ----------

type Claims struct {
	UserID       uint   `json:"uid"`
	Username     string `json:"usr"`
	Role         string `json:"rol"`
	TokenVersion int    `json:"ver"`
	jwt.RegisteredClaims
}

func IssueToken(secret string, uid uint, username, role string, tokenVersion int, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:       uid,
		Username:     username,
		Role:         role,
		TokenVersion: tokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprintf("%d", uid),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			Issuer:    "packrun",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

func ParseToken(secret, token string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("签名算法不匹配")
		}
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("packrun"))
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// ---------- 第二因子：TOTP 动态口令 ----------

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret 生成 20 字节随机种子（base32 无填充）。
func GenerateSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return b32.EncodeToString(buf), nil
}

// OtpauthURL 生成标准 otpauth 链接，供认证器扫码或手动录入。
func OtpauthURL(issuer, account, secret string, period int) string {
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", fmt.Sprintf("%d", period))
	label := url.PathEscape(issuer + ":" + account)
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// ValidateOTPCode 校验动态口令，允许 ±skew 个时间窗（应对时钟漂移）。
// 命中时返回该口令对应的时间步进（counter），供调用方做一次性消费。
func ValidateOTPCode(secret, code string, at time.Time, period, skew int) (int64, bool) {
	code = strings.TrimSpace(strings.ReplaceAll(code, " ", ""))
	if len(code) != 6 {
		return 0, false
	}
	if period <= 0 {
		period = 30
	}
	counter := at.Unix() / int64(period)
	for i := -skew; i <= skew; i++ {
		step := counter + int64(i)
		expect, err := hotp(secret, step)
		if err != nil {
			return 0, false
		}
		if hmac.Equal([]byte(expect), []byte(code)) {
			return step, true
		}
	}
	return 0, false
}

// ValidateOTP 只关心是否通过，语义同 ValidateOTPCode。
func ValidateOTP(secret, code string, at time.Time, period, skew int) bool {
	_, ok := ValidateOTPCode(secret, code, at, period, skew)
	return ok
}

// GenerateOTP 生成指定时刻的 6 位动态口令，供测试或服务端展示。
func GenerateOTP(secret string, at time.Time, period int) (string, error) {
	if period <= 0 {
		period = 30
	}
	return hotp(secret, at.Unix()/int64(period))
}

// ---------- 登录恢复码 ----------

// GenerateRecoveryCodes 生成 n 个高熵一次性恢复码，格式 XXXX-XXXX-XXXX-XXXX。
func GenerateRecoveryCodes(n int) ([]string, error) {
	if n <= 0 {
		n = 10
	}
	codes := make([]string, 0, n)
	for i := 0; i < n; i++ {
		buf := make([]byte, 10) // 80 bit，base32 后 16 字符
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		raw := b32.EncodeToString(buf)
		codes = append(codes, raw[0:4]+"-"+raw[4:8]+"-"+raw[8:12]+"-"+raw[12:16])
	}
	return codes, nil
}

// NormalizeRecoveryCode 去掉分隔符并统一大写，便于用户输入时不区分大小写。
func NormalizeRecoveryCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, "-", "")
	code = strings.ReplaceAll(code, " ", "")
	return code
}

// HashRecoveryCode 使用 SHA256 保存恢复码摘要；恢复码本身熵足够，无需慢哈希。
func HashRecoveryCode(code string) string {
	sum := sha256.Sum256([]byte(NormalizeRecoveryCode(code)))
	return hex.EncodeToString(sum[:])
}

func hotp(secret string, counter int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(counter))
	mac := hmac.New(sha1.New, key)
	mac.Write(buf)
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])
	return fmt.Sprintf("%06d", value%1000000), nil
}
