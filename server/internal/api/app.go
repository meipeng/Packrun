package api

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/packrun/packrun/internal/auth"
	"github.com/packrun/packrun/internal/config"
	"github.com/packrun/packrun/internal/deploy"
	"github.com/packrun/packrun/internal/model"
)

// App 聚合服务依赖。
type App struct {
	Cfg      *config.Config
	DB       *gorm.DB
	Deployer deploy.Deployer
	Sessions *sessionRegistry // 用于任务取消
	Sem      chan struct{}    // 全局并发闸门

	trusted []*net.IPNet // 可信代理网段，仅这些对端下才采信转发头

	gatesMu sync.Mutex
	gates   map[uint]chan struct{} // 每台服务器的并发闸门

	dirLocksMu sync.Mutex
	dirLocks   map[uint]*sync.Mutex // 同一部署目录串行执行

	// 健康检查结果缓存：探针与概览页轮询时避免反复做完整性检查。
	healthMu       sync.Mutex
	healthCache    healthReport
	healthAt       time.Time
	healthCacheFor time.Duration
}

func NewApp(cfg *config.Config, db *gorm.DB, d deploy.Deployer) *App {
	if cfg.Deploy.MaxConcurrent <= 0 {
		cfg.Deploy.MaxConcurrent = 3
	}
	return &App{
		Cfg:      cfg,
		DB:       db,
		Deployer: d,
		Sessions: newSessionRegistry(),
		Sem:      make(chan struct{}, cfg.Deploy.MaxConcurrent),
		trusted:  parseTrustedProxies(cfg.Server.TrustedProxies),
		gates:    map[uint]chan struct{}{},
		dirLocks: map[uint]*sync.Mutex{},

		healthCacheFor: 5 * time.Second,
	}
}

// parseTrustedProxies 将配置中的 CIDR 或单个 IP 转为网段，非法项忽略。
func parseTrustedProxies(entries []string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(entries))
	for _, raw := range entries {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(raw); err == nil {
			nets = append(nets, n)
			continue
		}
		ip := net.ParseIP(raw)
		if ip == nil {
			continue
		}
		if v4 := ip.To4(); v4 != nil {
			nets = append(nets, &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)})
			continue
		}
		nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)})
	}
	return nets
}

// lockDirectory 串行化同一部署目录上的任务，避免并发备份/解包互相覆盖。
func (a *App) lockDirectory(id uint) func() {
	a.dirLocksMu.Lock()
	mu, exists := a.dirLocks[id]
	if !exists {
		mu = &sync.Mutex{}
		a.dirLocks[id] = mu
	}
	a.dirLocksMu.Unlock()

	mu.Lock()
	return mu.Unlock
}

// acquireServerGate 按服务器并发上限获取一个槽位，返回释放函数。
// 服务器配置变更后，在无任务占用时按新上限重建闸门。
func (a *App) acquireServerGate(serverID uint, limit int) func() {
	if limit <= 0 {
		limit = 1
	}
	a.gatesMu.Lock()
	gate, exists := a.gates[serverID]
	if !exists || (cap(gate) != limit && len(gate) == 0) {
		gate = make(chan struct{}, limit)
		a.gates[serverID] = gate
	}
	a.gatesMu.Unlock()

	gate <- struct{}{}
	return func() { <-gate }
}

const ctxUserKey = "packrun.user"

// effectiveTokenVersion 历史数据缺少该列时按 1 处理，避免所有会话永久失效。
func effectiveTokenVersion(v int) int {
	if v <= 0 {
		return 1
	}
	return v
}

// authRequired 校验 Bearer 令牌并加载当前用户。
func (a *App) authRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			fail(c, 401, CodeAuthFailed, "未登录")
			return
		}
		claims, err := auth.ParseToken(a.Cfg.Auth.JWTSecret, strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			fail(c, 401, CodeTokenInvalid, "登录已失效，请重新登录")
			return
		}

		var user model.User
		if err := a.DB.First(&user, claims.UserID).Error; err != nil {
			fail(c, 401, CodeTokenInvalid, "用户不存在")
			return
		}
		expectedVersion := effectiveTokenVersion(user.TokenVersion)
		if claims.TokenVersion != expectedVersion {
			fail(c, 401, CodeTokenInvalid, "登录已失效，请重新登录")
			return
		}
		if user.Status != "active" {
			fail(c, 403, CodeForbidden, "账号已被禁用")
			return
		}
		c.Set(ctxUserKey, &user)
		c.Next()
	}
}

func currentUser(c *gin.Context) *model.User {
	v, exists := c.Get(ctxUserKey)
	if !exists {
		return nil
	}
	return v.(*model.User)
}

// requireRoles 限制可访问的角色集合。
func requireRoles(roles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := currentUser(c)
		if u == nil {
			fail(c, 401, CodeAuthFailed, "未登录")
			return
		}
		for _, r := range roles {
			if u.Role == r {
				c.Next()
				return
			}
		}
		forbidden(c, "当前角色无权执行该操作")
	}
}

// scopeServerIDs 解析用户的数据范围；unrestricted=true 表示管理员或全部服务器。
func scopeServerIDs(u *model.User) (ids []uint, unrestricted bool) {
	if u == nil {
		return nil, false
	}
	if u.Role == model.RoleAdmin || strings.TrimSpace(u.ServerScope) == "" || u.ServerScope == "*" {
		return nil, true
	}
	for _, part := range strings.Split(u.ServerScope, ",") {
		if id, err := strconv.ParseUint(strings.TrimSpace(part), 10, 64); err == nil && id > 0 {
			ids = append(ids, uint(id))
		}
	}
	return ids, false
}

// checkServerScope 校验用户是否有权操作指定服务器。
func checkServerScope(u *model.User, serverID uint) bool {
	ids, unrestricted := scopeServerIDs(u)
	if unrestricted {
		return true
	}
	for _, id := range ids {
		if id == serverID {
			return true
		}
	}
	return false
}

// applyServerScope 给带服务器归属列的查询追加数据范围限制。
func applyServerScope(q *gorm.DB, u *model.User, column string) *gorm.DB {
	ids, unrestricted := scopeServerIDs(u)
	if unrestricted {
		return q
	}
	if len(ids) == 0 {
		return q.Where("1 = 0")
	}
	return q.Where(column+" IN ?", ids)
}

// consumeOTPStep 原子消费一次性时间步进：仅当库中记录的步进更旧时才更新成功。
func (a *App) consumeOTPStep(userID uint, step int64) bool {
	res := a.DB.Model(&model.User{}).
		Where("id = ? AND last_otp_step < ?", userID, step).
		Update("last_otp_step", step)
	return res.Error == nil && res.RowsAffected == 1
}

// verifyOTPForUser 校验指定种子当前口令并原子消费对应时间步进。
func (a *App) verifyOTPForUser(u *model.User, secret, code string) (bool, string) {
	if strings.TrimSpace(code) == "" {
		return false, "请输入动态口令"
	}
	step, ok := auth.ValidateOTPCode(secret, code, time.Now(), a.Cfg.Auth.OTPPeriod, a.Cfg.Auth.OTPSkew)
	if !ok {
		return false, "动态口令无效或已过期"
	}
	if !a.consumeOTPStep(u.ID, step) {
		return false, "动态口令已使用"
	}
	return true, ""
}

// verifyOTPWithLimit 在 verifyOTPForUser 之上叠加 step-up 失败限流，用于高危操作。
func (a *App) verifyOTPWithLimit(u *model.User, secret, code string) (bool, string) {
	key := stepUpKey(u.ID)
	if stepUpFailures.locked(key) {
		return false, "动态口令失败次数过多，请 5 分钟后再试"
	}
	passed, msg := a.verifyOTPForUser(u, secret, code)
	if !passed {
		stepUpFailures.fail(key, stepUpFailLimit)
		return false, msg
	}
	stepUpFailures.reset(key)
	return true, ""
}

// verifyStepUpOTP 高危操作再认证：生产环境部署必须携带当次有效的动态口令。
func (a *App) verifyStepUpOTP(u *model.User, otp string) (bool, string) {
	if !u.OTPEnabled || u.OTPSecret == "" {
		return false, "当前账号尚未绑定动态口令，无法在生产环境执行部署"
	}
	if strings.TrimSpace(otp) == "" {
		return false, "生产环境操作需要输入动态口令"
	}
	return a.verifyOTPWithLimit(u, u.OTPSecret, otp)
}

// clientIP 还原真实客户端 IP：仅当 TCP 对端位于可信代理网段时才采信转发头。
func (a *App) clientIP(c *gin.Context) string {
	remote := c.Request.RemoteAddr
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = strings.Trim(remote, "[]")
	}
	peer := net.ParseIP(host)
	if peer == nil {
		return host
	}
	if !a.isTrustedProxy(peer) {
		return host
	}
	if v := c.GetHeader("X-Forwarded-For"); v != "" {
		first := strings.TrimSpace(strings.Split(v, ",")[0])
		if ip := net.ParseIP(first); ip != nil {
			return ip.String()
		}
	}
	if v := c.GetHeader("X-Real-IP"); v != "" {
		if ip := net.ParseIP(strings.TrimSpace(v)); ip != nil {
			return ip.String()
		}
	}
	return host
}

func (a *App) isTrustedProxy(ip net.IP) bool {
	for _, n := range a.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// audit 写入审计事件，失败不影响主流程。
func (a *App) audit(c *gin.Context, action, target, result, taskNo, detail string, otpVerified bool) {
	var actorID uint
	var actor string
	if u := currentUser(c); u != nil {
		actorID = u.ID
		actor = u.Username
	}
	a.auditAs(c, actorID, actor, action, target, result, taskNo, detail, otpVerified)
}

// auditAs 用于登录等尚未建立会话的场景，显式记录操作者身份。
func (a *App) auditAs(c *gin.Context, actorID uint, actor, action, target, result, taskNo, detail string, otpVerified bool) {
	a.DB.Create(&model.AuditLog{
		ActorID:     actorID,
		Actor:       actor,
		Action:      action,
		Target:      target,
		Result:      result,
		IP:          a.clientIP(c),
		TaskNo:      taskNo,
		OTPVerified: otpVerified,
		Detail:      detail,
	})
}
