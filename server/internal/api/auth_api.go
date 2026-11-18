package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/packrun/packrun/internal/auth"
	"github.com/packrun/packrun/internal/model"
)

type loginReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	OTP      string `json:"otp"`
}

const (
	loginAcctFailLimit = 5  // 单账号阈值
	loginIPFailLimit   = 30 // 单 IP 阈值
	stepUpFailLimit    = 5  // 高危操作动态口令阈值

	failLockWindow = 5 * time.Minute
	failItemTTL    = 10 * time.Minute
	failMapLimit   = 10000
)

// dummyPasswordHash 用于账号不存在时也执行一次 bcrypt，避免通过响应时间枚举账号。
var dummyPasswordHash = func() string {
	h, _ := auth.HashPassword("packrun-dummy-password")
	return h
}()

// failureCounter 记录连续失败次数并在达到阈值后临时锁定。
type failureCounter struct {
	mu    sync.Mutex
	items map[string]*failureItem
}

type failureItem struct {
	count    int
	until    time.Time
	lastSeen time.Time
}

func newFailureCounter() *failureCounter {
	return &failureCounter{items: map[string]*failureItem{}}
}

var (
	loginFailures  = newFailureCounter()
	stepUpFailures = newFailureCounter()
)

func stepUpKey(uid uint) string {
	return fmt.Sprintf("stepup:%d", uid)
}

func (f *failureCounter) locked(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[key]
	if !ok {
		return false
	}
	return time.Now().Before(it.until)
}

func (f *failureCounter) fail(key string, limit int) {
	if limit <= 0 {
		limit = 5
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	f.pruneLocked(now)
	it, ok := f.items[key]
	if !ok {
		it = &failureItem{}
		f.items[key] = it
	}
	it.count++
	it.lastSeen = now
	if it.count >= limit {
		it.until = now.Add(failLockWindow)
		it.count = 0
	}
}

func (f *failureCounter) reset(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items, key)
}

// pruneLocked 惰性清理长期未更新的条目；map 过大时再淘汰最旧约 10%。
func (f *failureCounter) pruneLocked(now time.Time) {
	for k, it := range f.items {
		if now.Sub(it.lastSeen) > failItemTTL {
			delete(f.items, k)
		}
	}
	overflow := len(f.items) - failMapLimit
	if overflow <= 0 {
		return
	}
	type entry struct {
		key  string
		seen time.Time
	}
	entries := make([]entry, 0, len(f.items))
	for k, it := range f.items {
		entries = append(entries, entry{key: k, seen: it.lastSeen})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].seen.Before(entries[j].seen) })
	drop := len(f.items) / 10
	if drop < overflow {
		drop = overflow
	}
	for i := 0; i < drop && i < len(entries); i++ {
		delete(f.items, entries[i].key)
	}
}

// loginFailed 同时累加账号与 IP 两个维度的失败计数并写审计。
func (a *App) loginFailed(c *gin.Context, actorID uint, username, acctKey, ipKey, detail string) {
	loginFailures.fail(acctKey, loginAcctFailLimit)
	loginFailures.fail(ipKey, loginIPFailLimit)
	a.auditAs(c, actorID, username, "auth.login", username, "fail", "", detail, false)
}

func (a *App) login(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请输入账号与密码")
		return
	}

	acctKey := "acct:" + req.Username
	ipKey := "ip:" + a.clientIP(c)
	if loginFailures.locked(acctKey) || loginFailures.locked(ipKey) {
		a.auditAs(c, 0, req.Username, "auth.login", req.Username, "fail", "", "失败次数过多，登录已临时锁定", false)
		fail(c, http.StatusTooManyRequests, CodeAuthFailed, "失败次数过多，请 5 分钟后再试")
		return
	}

	var user model.User
	if err := a.DB.Where("username = ?", req.Username).First(&user).Error; err != nil {
		// 账号不存在时也执行一次 bcrypt，保持与口令错误的响应时间一致。
		auth.VerifyPassword(dummyPasswordHash, req.Password)
		a.loginFailed(c, 0, req.Username, acctKey, ipKey, "账号不存在")
		fail(c, http.StatusUnauthorized, CodeAuthFailed, "账号或密码错误")
		return
	}

	if user.Status != "active" {
		a.loginFailed(c, user.ID, user.Username, acctKey, ipKey, "账号已被禁用")
		fail(c, http.StatusUnauthorized, CodeAuthFailed, "账号或密码错误")
		return
	}

	if !auth.VerifyPassword(user.PasswordHash, req.Password) {
		a.loginFailed(c, user.ID, user.Username, acctKey, ipKey, "口令错误")
		fail(c, http.StatusUnauthorized, CodeAuthFailed, "账号或密码错误")
		return
	}

	// 第二因子：已绑定动态口令的账号必须校验。
	// 动态口令一次性消费；失效时允许使用一次性恢复码，恢复码使用后立即作废。
	usedRecovery := false
	if user.OTPEnabled {
		if strings.TrimSpace(req.OTP) == "" {
			a.loginFailed(c, user.ID, user.Username, acctKey, ipKey, "未输入动态口令")
			fail(c, http.StatusUnauthorized, CodeOTPRequired, "请输入动态口令")
			return
		}
		verified := false
		if step, ok := auth.ValidateOTPCode(user.OTPSecret, req.OTP, time.Now(), a.Cfg.Auth.OTPPeriod, a.Cfg.Auth.OTPSkew); ok {
			verified = a.consumeOTPStep(user.ID, step)
		}
		if !verified {
			if !a.consumeRecoveryCode(user.ID, req.OTP) {
				a.loginFailed(c, user.ID, user.Username, acctKey, ipKey, "动态口令或恢复码错误")
				fail(c, http.StatusUnauthorized, CodeOTPInvalid, "动态口令无效，恢复码也可能已使用")
				return
			}
			usedRecovery = true
		}
	}

	// 历史数据可能为 0；先落为 1 再签发，确保后续改密递增后旧令牌立即失效。
	if user.TokenVersion <= 0 {
		if err := a.DB.Model(&model.User{}).Where("id = ?", user.ID).
			Update("token_version", 1).Error; err != nil {
			internal(c, "初始化令牌版本失败")
			return
		}
		user.TokenVersion = 1
	}

	token, err := auth.IssueToken(a.Cfg.Auth.JWTSecret, user.ID, user.Username, user.Role, user.TokenVersion,
		time.Duration(a.Cfg.Auth.TokenTTLMinute)*time.Minute)
	if err != nil {
		internal(c, "签发令牌失败")
		return
	}

	now := time.Now()
	a.DB.Model(&user).Update("last_login_at", now)
	loginFailures.reset(acctKey)
	loginFailures.reset(ipKey)

	detail := fmt.Sprintf("角色=%s 动态口令=%v", user.Role, user.OTPEnabled)
	otpVerified := user.OTPEnabled
	if usedRecovery {
		detail = fmt.Sprintf("角色=%s 使用一次性恢复码登录", user.Role)
		otpVerified = false
		a.auditAs(c, user.ID, user.Username, "auth.recovery", user.Username, "ok", "", "消耗一枚恢复码完成登录", false)
	}
	a.auditAs(c, user.ID, user.Username, "auth.login", user.Username, "ok", "", detail, otpVerified)
	ok(c, gin.H{"token": token, "user": user})
}

func (a *App) me(c *gin.Context) {
	ok(c, currentUser(c))
}

type passwordReq struct {
	OldPassword string `json:"oldPassword" binding:"required"`
	NewPassword string `json:"newPassword" binding:"required"`
	OTP         string `json:"otp"`
}

func (a *App) changePassword(c *gin.Context) {
	var req passwordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "参数不完整")
		return
	}
	if len(req.NewPassword) < 8 {
		badRequest(c, "新口令至少 8 位")
		return
	}
	u := currentUser(c)
	if !auth.VerifyPassword(u.PasswordHash, req.OldPassword) {
		a.audit(c, "auth.password", u.Username, "fail", "", "原口令不正确", false)
		badRequest(c, "原口令不正确")
		return
	}
	// 已绑定动态口令的账号改密属于高危操作，需再次验证第二因子。
	if u.OTPEnabled {
		if strings.TrimSpace(req.OTP) == "" {
			badRequest(c, "请输入动态口令")
			return
		}
		if passed, msg := a.verifyOTPWithLimit(u, u.OTPSecret, req.OTP); !passed {
			a.audit(c, "auth.password", u.Username, "fail", "", "动态口令校验失败："+msg, false)
			fail(c, http.StatusUnauthorized, CodeOTPInvalid, msg)
			return
		}
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		internal(c, "口令加密失败")
		return
	}
	// 递增 token_version 使包括当前会话在内的旧令牌全部失效。
	if err := a.DB.Model(&model.User{}).Where("id = ?", u.ID).Updates(map[string]any{
		"password_hash": hash,
		"token_version": gorm.Expr("token_version + 1"),
	}).Error; err != nil {
		internal(c, "更新口令失败")
		return
	}
	a.audit(c, "auth.password", u.Username, "ok", "", "修改登录口令并注销其他会话", u.OTPEnabled)
	ok(c, gin.H{"updated": true})
}

// otpSetup 生成动态口令种子，需再调用 activate 完成绑定。
func (a *App) otpSetup(c *gin.Context) {
	u := currentUser(c)
	if u.OTPEnabled {
		badRequest(c, "已绑定动态口令，如需换绑请先解绑")
		return
	}
	secret, err := auth.GenerateSecret()
	if err != nil {
		internal(c, "生成种子失败")
		return
	}
	if err := a.DB.Model(&model.User{}).Where("id = ?", u.ID).Updates(map[string]any{
		"otp_secret": secret, "pending_otp_secret": "", "pending_otp_at": nil, "last_otp_step": 0,
	}).Error; err != nil {
		internal(c, "保存种子失败")
		return
	}
	otpauth := auth.OtpauthURL(a.Cfg.Auth.OTPIssuer, u.Username, secret, a.Cfg.Auth.OTPPeriod)
	ok(c, gin.H{"secret": secret, "otpauth": otpauth, "issuer": a.Cfg.Auth.OTPIssuer, "period": a.Cfg.Auth.OTPPeriod})
}

type otpCodeReq struct {
	Code string `json:"code" binding:"required"`
}

func (a *App) otpActivate(c *gin.Context) {
	var req otpCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请输入动态口令")
		return
	}
	u := currentUser(c)
	secret := u.OTPSecret
	rebind := false
	if u.OTPEnabled {
		rebind = true
		if u.PendingOTPSecret == "" {
			badRequest(c, "已绑定动态口令，如需换绑请先发起换绑")
			return
		}
		if u.PendingOTPAt == nil || time.Since(*u.PendingOTPAt) > 10*time.Minute {
			badRequest(c, "换绑种子已过期，请重新发起换绑")
			return
		}
		secret = u.PendingOTPSecret
	}
	if secret == "" {
		badRequest(c, "请先生成动态口令种子")
		return
	}
	action := "auth.otp.activate"
	if rebind {
		action = "auth.otp.rebind.confirm"
	}
	// 一次性消费待确认种子的时间步进，并计入 step-up 失败限流。
	if passed, msg := a.verifyOTPWithLimit(u, secret, req.Code); !passed {
		a.audit(c, action, u.Username, "fail", "", msg, false)
		badRequest(c, msg)
		return
	}

	var codes []string
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.User{}).Where("id = ?", u.ID).Updates(map[string]any{
			"otp_secret": secret, "otp_enabled": true,
			"pending_otp_secret": "", "pending_otp_at": nil,
		}).Error; err != nil {
			return err
		}
		var err error
		codes, err = issueRecoveryCodes(tx, u.ID)
		return err
	})
	if err != nil {
		internal(c, "启用动态口令失败")
		return
	}
	if rebind {
		a.audit(c, "auth.otp.rebind.confirm", u.Username, "ok", "", "换绑动态口令并重新生成恢复码", true)
	} else {
		a.audit(c, "auth.otp.activate", u.Username, "ok", "", "绑定动态口令并生成恢复码", true)
	}
	ok(c, gin.H{"enabled": true, "recoveryCodes": codes})
}

// otpRebind 生成新的动态口令种子，确认前原口令仍然有效。
func (a *App) otpRebind(c *gin.Context) {
	var req otpCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请输入当前动态口令")
		return
	}
	u := currentUser(c)
	if !u.OTPEnabled {
		badRequest(c, "尚未绑定动态口令")
		return
	}
	if passed, msg := a.verifyOTPWithLimit(u, u.OTPSecret, req.Code); !passed {
		a.audit(c, "auth.otp.rebind", u.Username, "fail", "", msg, false)
		badRequest(c, msg)
		return
	}
	secret, err := auth.GenerateSecret()
	if err != nil {
		internal(c, "生成种子失败")
		return
	}
	// 新种子对应独立的时间步进空间，重置已消费记录。
	if err := a.DB.Model(&model.User{}).Where("id = ?", u.ID).Updates(map[string]any{
		"pending_otp_secret": secret, "pending_otp_at": time.Now(), "last_otp_step": 0,
	}).Error; err != nil {
		internal(c, "保存换绑种子失败")
		return
	}
	a.audit(c, "auth.otp.rebind", u.Username, "ok", "", "生成换绑种子，确认前原动态口令仍然有效", true)
	ok(c, gin.H{
		"secret":  secret,
		"otpauth": auth.OtpauthURL(a.Cfg.Auth.OTPIssuer, u.Username, secret, a.Cfg.Auth.OTPPeriod),
		"issuer":  a.Cfg.Auth.OTPIssuer,
		"period":  a.Cfg.Auth.OTPPeriod,
	})
}

func (a *App) otpDisable(c *gin.Context) {
	var req otpCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请输入动态口令")
		return
	}
	u := currentUser(c)
	if !u.OTPEnabled {
		badRequest(c, "尚未绑定动态口令")
		return
	}
	if passed, msg := a.verifyOTPWithLimit(u, u.OTPSecret, req.Code); !passed {
		a.audit(c, "auth.otp.disable", u.Username, "fail", "", msg, false)
		badRequest(c, msg)
		return
	}
	a.DB.Model(u).Updates(map[string]any{
		"otp_enabled": false, "otp_secret": "",
		"pending_otp_secret": "", "pending_otp_at": nil,
	})
	a.DB.Where("user_id = ?", u.ID).Delete(&model.RecoveryCode{})
	a.audit(c, "auth.otp.disable", u.Username, "ok", "", "解绑动态口令并清除恢复码", true)
	ok(c, gin.H{"enabled": false})
}

// recoveryCodeStatus 返回当前账号恢复码的总量与剩余可用量。
func (a *App) recoveryCodeStatus(c *gin.Context) {
	u := currentUser(c)
	var total, remaining int64
	a.DB.Model(&model.RecoveryCode{}).Where("user_id = ?", u.ID).Count(&total)
	a.DB.Model(&model.RecoveryCode{}).Where("user_id = ? AND used_at IS NULL", u.ID).Count(&remaining)
	ok(c, gin.H{"total": total, "remaining": remaining})
}

// regenerateRecoveryCodes 校验当前动态口令后作废旧码并重新下发一组恢复码。
func (a *App) regenerateRecoveryCodes(c *gin.Context) {
	var req otpCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请输入当前动态口令")
		return
	}
	u := currentUser(c)
	if !u.OTPEnabled {
		badRequest(c, "尚未绑定动态口令")
		return
	}
	if passed, msg := a.verifyOTPWithLimit(u, u.OTPSecret, req.Code); !passed {
		a.audit(c, "auth.recovery.regenerate", u.Username, "fail", "", msg, false)
		badRequest(c, msg)
		return
	}

	var codes []string
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		codes, err = issueRecoveryCodes(tx, u.ID)
		return err
	})
	if err != nil {
		internal(c, "生成恢复码失败")
		return
	}
	a.audit(c, "auth.recovery.regenerate", u.Username, "ok", "", "重新生成恢复码，旧码全部失效", true)
	ok(c, gin.H{"recoveryCodes": codes})
}

// consumeRecoveryCode 原子消费一枚未使用的恢复码，成功返回 true。
func (a *App) consumeRecoveryCode(userID uint, code string) bool {
	if len(auth.NormalizeRecoveryCode(code)) != 16 {
		return false
	}
	res := a.DB.Model(&model.RecoveryCode{}).
		Where("user_id = ? AND code_hash = ? AND used_at IS NULL", userID, auth.HashRecoveryCode(code)).
		Update("used_at", time.Now())
	return res.RowsAffected == 1
}

// issueRecoveryCodes 在事务内作废旧码并写入一组新码，返回仅此一次可见的明文。
func issueRecoveryCodes(tx *gorm.DB, userID uint) ([]string, error) {
	codes, err := auth.GenerateRecoveryCodes(10)
	if err != nil {
		return nil, err
	}
	if err := tx.Where("user_id = ?", userID).Delete(&model.RecoveryCode{}).Error; err != nil {
		return nil, err
	}
	records := make([]model.RecoveryCode, 0, len(codes))
	for _, code := range codes {
		records = append(records, model.RecoveryCode{UserID: userID, CodeHash: auth.HashRecoveryCode(code)})
	}
	if err := tx.Create(&records).Error; err != nil {
		return nil, err
	}
	return codes, nil
}
