package auth

import (
	"regexp"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestGenerateRecoveryCodes(t *testing.T) {
	codes, err := GenerateRecoveryCodes(10)
	if err != nil {
		t.Fatalf("生成恢复码失败: %v", err)
	}
	if len(codes) != 10 {
		t.Fatalf("期望 10 枚恢复码，实际 %d", len(codes))
	}

	pattern := regexp.MustCompile(`^[A-Z2-7]{4}-[A-Z2-7]{4}-[A-Z2-7]{4}-[A-Z2-7]{4}$`)
	seen := map[string]bool{}
	for _, code := range codes {
		if !pattern.MatchString(code) {
			t.Fatalf("恢复码格式不合法: %q", code)
		}
		if seen[code] {
			t.Fatalf("恢复码重复: %q", code)
		}
		seen[code] = true
	}
}

func TestRecoveryCodeNormalizeAndHash(t *testing.T) {
	code := "ABCD-EFGH-IJKL-MNOP"
	if got := NormalizeRecoveryCode(" abcd efgh ijkl mnop "); got != "ABCDEFGHIJKLMNOP" {
		t.Fatalf("归一化结果错误: %q", got)
	}
	if got := NormalizeRecoveryCode(code); got != "ABCDEFGHIJKLMNOP" {
		t.Fatalf("归一化结果错误: %q", got)
	}

	h1 := HashRecoveryCode(code)
	h2 := HashRecoveryCode("abcd efgh ijkl mnop")
	if h1 != h2 {
		t.Fatalf("同一恢复码不同书写方式应得到相同摘要")
	}
	if len(h1) != 64 {
		t.Fatalf("SHA256 摘要长度应为 64，实际 %d", len(h1))
	}
}

func TestValidateOTP(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("生成种子失败: %v", err)
	}

	const period = 30
	at := time.Unix(1700000000, 0).UTC()
	counter := at.Unix() / period

	current, err := hotp(secret, counter)
	if err != nil {
		t.Fatalf("计算动态口令失败: %v", err)
	}
	if !ValidateOTP(secret, current, at, period, 1) {
		t.Fatal("当前时间窗动态口令应校验通过")
	}

	previous, err := hotp(secret, counter-1)
	if err != nil {
		t.Fatalf("计算动态口令失败: %v", err)
	}
	if !ValidateOTP(secret, previous, at, period, 1) {
		t.Fatal("±1 时间窗内的动态口令应校验通过")
	}

	stale, err := hotp(secret, counter-3)
	if err != nil {
		t.Fatalf("计算动态口令失败: %v", err)
	}
	if ValidateOTP(secret, stale, at, period, 1) {
		t.Fatal("超出时间窗的动态口令不应通过")
	}

	if ValidateOTP(secret, "12345", at, period, 1) {
		t.Fatal("非 6 位口令不应通过")
	}
}

func TestValidateOTPCodeCounter(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("生成种子失败: %v", err)
	}

	const period = 30
	at := time.Unix(1700000000, 0).UTC()
	counter := at.Unix() / period

	code, err := GenerateOTP(secret, at, period)
	if err != nil {
		t.Fatalf("生成动态口令失败: %v", err)
	}
	step, ok := ValidateOTPCode(secret, code, at, period, 1)
	if !ok {
		t.Fatal("当前时间窗口令应校验通过")
	}
	if step != counter {
		t.Fatalf("命中步进 = %d，期望 %d", step, counter)
	}

	previous, err := GenerateOTP(secret, at.Add(-time.Duration(period)*time.Second), period)
	if err != nil {
		t.Fatalf("生成动态口令失败: %v", err)
	}
	step, ok = ValidateOTPCode(secret, previous, at, period, 1)
	if !ok {
		t.Fatal("±1 时间窗内的口令应校验通过")
	}
	if step != counter-1 {
		t.Fatalf("相邻时间窗步进 = %d，期望 %d", step, counter-1)
	}

	stale, err := GenerateOTP(secret, at.Add(-3*time.Duration(period)*time.Second), period)
	if err != nil {
		t.Fatalf("生成动态口令失败: %v", err)
	}
	if _, ok := ValidateOTPCode(secret, stale, at, period, 1); ok {
		t.Fatal("超出时间窗的口令不应通过")
	}
}

func TestParseTokenRejectsIssuerAndAlgorithm(t *testing.T) {
	const secret = "test-secret-for-token"
	build := func(issuer string, method jwt.SigningMethod) string {
		claims := Claims{
			UserID:       1,
			Username:     "alice",
			Role:         "admin",
			TokenVersion: 1,
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    issuer,
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
		}
		token, err := jwt.NewWithClaims(method, claims).SignedString([]byte(secret))
		if err != nil {
			t.Fatalf("签发测试令牌失败: %v", err)
		}
		return token
	}

	if _, err := ParseToken(secret, build("packrun", jwt.SigningMethodHS256)); err != nil {
		t.Fatalf("合法令牌应解析成功: %v", err)
	}
	if _, err := ParseToken(secret, build("other", jwt.SigningMethodHS256)); err == nil {
		t.Fatal("issuer 非 packrun 的令牌必须拒绝")
	}
	if _, err := ParseToken(secret, build("packrun", jwt.SigningMethodHS512)); err == nil {
		t.Fatal("非 HS256 算法的令牌必须拒绝")
	}
}

func TestIssueAndParseTokenVersion(t *testing.T) {
	const secret = "test-secret-for-token"
	token, err := IssueToken(secret, 42, "alice", "deployer", 7, time.Hour)
	if err != nil {
		t.Fatalf("签发令牌失败: %v", err)
	}
	claims, err := ParseToken(secret, token)
	if err != nil {
		t.Fatalf("解析令牌失败: %v", err)
	}
	if claims.UserID != 42 || claims.Username != "alice" || claims.Role != "deployer" {
		t.Fatalf("令牌主体信息错误: %+v", claims)
	}
	if claims.TokenVersion != 7 {
		t.Fatalf("令牌 ver = %d，期望 7", claims.TokenVersion)
	}
}

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword("Admin@12345")
	if err != nil {
		t.Fatalf("口令哈希失败: %v", err)
	}
	if !VerifyPassword(hash, "Admin@12345") {
		t.Fatal("正确口令应校验通过")
	}
	if VerifyPassword(hash, "wrong-password") {
		t.Fatal("错误口令不应通过")
	}
}
