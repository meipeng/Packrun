package config

import (
	"fmt"
	"net"
	"os"
	"path"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultJWTSecret       = "packrun-dev-secret-change-me"
	defaultAdminPassword   = "Admin@12345"
	minJWTSecretLength     = 32
	defaultCredentialKey   = "packrun-dev-credential-key-change-me"
	minCredentialKeyLength = 32
)

// Config 是中控服务的全部可调参数。
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Auth      AuthConfig      `yaml:"auth"`
	Security  SecurityConfig  `yaml:"security"`
	Storage   StorageConfig   `yaml:"storage"`
	Deploy    DeployConfig    `yaml:"deploy"`
	Alert     AlertConfig     `yaml:"alert"`
	Bootstrap BootstrapConfig `yaml:"bootstrap"`
}

// AlertConfig 外部告警推送。webhook_url 为空时只写服务日志，不推送。
type AlertConfig struct {
	WebhookURL string `yaml:"webhook_url"`
	// Format 决定推送体格式：wecom（企业微信群机器人）或 generic（{"text","level"} 通用 JSON）。
	Format string `yaml:"format"`
	// MinIntervalSec 同一类告警的去重窗口，避免故障期间刷屏。
	MinIntervalSec int `yaml:"min_interval_seconds"`
	// CheckIntervalSec 后台巡检间隔；0 表示关闭巡检。
	CheckIntervalSec int `yaml:"check_interval_seconds"`
	// DiskMinFreeMB 包目录所在磁盘剩余空间低于该值时告警。
	DiskMinFreeMB int `yaml:"disk_min_free_mb"`
	// CertFiles 需要监控到期的证书文件（PEM），例如 Nginx 的 fullchain。
	CertFiles []string `yaml:"cert_files"`
	// CertExpireDays 证书剩余天数低于该值时告警。
	CertExpireDays int `yaml:"cert_expire_days"`
}

// SecurityConfig 凭据加密相关：主密钥不落库，仅来自配置/环境变量。
type SecurityConfig struct {
	// CredentialKey 「直接录入」凭据的加密主密钥短语；未配置时直接录入能力不可用。
	CredentialKey string `yaml:"credential_key"`
}

type ServerConfig struct {
	Addr           string   `yaml:"addr"`
	Mode           string   `yaml:"mode"`
	BasePath       string   `yaml:"base_path"`
	AllowedOrigins []string `yaml:"allowed_origins"`
	TrustedProxies []string `yaml:"trusted_proxies"`
}

type AuthConfig struct {
	JWTSecret      string `yaml:"jwt_secret"`
	TokenTTLMinute int    `yaml:"token_ttl_minutes"`
	OTPIssuer      string `yaml:"otp_issuer"`
	OTPPeriod      int    `yaml:"otp_period"`
	OTPSkew        int    `yaml:"otp_skew"`
}

type StorageConfig struct {
	DBPath      string `yaml:"db_path"`
	PackageDir  string `yaml:"package_dir"`
	SandboxRoot string `yaml:"sandbox_root"`
	MaxTotalMB  int    `yaml:"max_total_mb"`
	// TaskLogRetentionDays 任务步骤日志正文保留天数，超期只清正文保留步骤记录；0 表示不清理。
	TaskLogRetentionDays int `yaml:"task_log_retention_days"`
	// TaskRetentionDays 终态任务（成功/失败/取消）保留天数，超期连同步骤一起删除；0 表示不清理。
	TaskRetentionDays int `yaml:"task_retention_days"`
	// AuditRetentionDays 审计日志保留天数；0 表示不清理。合规要求更久时请先导出再调大。
	AuditRetentionDays int `yaml:"audit_retention_days"`
	// PackageKeepReleases 每个部署目录保留最近多少个版本的安装包，保证最近 N 次回滚可用；0 表示不做版本回收。
	PackageKeepReleases int `yaml:"package_keep_releases"`
	// PackageKeepDays 最近多少天内上传的安装包一律保留（可能刚发还没验证完）；0 表示只按版本窗口判断。
	PackageKeepDays int `yaml:"package_keep_days"`
}

type DeployConfig struct {
	Mode              string `yaml:"mode"`
	MaxConcurrent     int    `yaml:"max_concurrent"`
	StepDelayMS       int    `yaml:"step_delay_ms"`
	MaxPendingPerUser int    `yaml:"max_pending_per_user"`
	// MetricsIntervalSec 后台指标采集间隔（秒）；0 表示关闭后台采集，负数非法。
	MetricsIntervalSec int `yaml:"metrics_interval_seconds"`
	// AutoTrustHostKey TOFU：目标机未登记指纹时，首次连接自动登记并写审计日志。
	AutoTrustHostKey bool `yaml:"auto_trust_hostkey"`
}

type BootstrapConfig struct {
	AdminUsername string `yaml:"admin_username"`
	AdminPassword string `yaml:"admin_password"`
}

func defaults() *Config {
	return &Config{
		Server: ServerConfig{
			Addr:           "0.0.0.0:9000",
			Mode:           "debug",
			BasePath:       "/packrun",
			AllowedOrigins: []string{"http://localhost:5173"},
			TrustedProxies: []string{"127.0.0.1/32", "::1/128"},
		},
		Auth: AuthConfig{
			JWTSecret:      defaultJWTSecret,
			TokenTTLMinute: 480,
			OTPIssuer:      "Packrun",
			OTPPeriod:      30,
			OTPSkew:        1,
		},
		Security: SecurityConfig{
			// debug 占位默认值；release 未配置时直接录入能力不可用（使用时报错），启动不受影响。
			CredentialKey: defaultCredentialKey,
		},
		Storage: StorageConfig{
			DBPath:               "./data/packrun.db",
			PackageDir:           "./data/packages",
			SandboxRoot:          "./data/sandbox",
			MaxTotalMB:           20480,
			TaskLogRetentionDays: 90,
			TaskRetentionDays:    180,
			AuditRetentionDays:   180,
			PackageKeepReleases:  10,
			PackageKeepDays:      7,
		},
		Deploy: DeployConfig{
			// 默认 ssh：无配置文件时也按真实远程部署处理，避免误把生产任务落进本机沙箱。
			Mode:               "ssh",
			MaxConcurrent:      3,
			StepDelayMS:        350,
			MaxPendingPerUser:  10,
			MetricsIntervalSec: 300,
			AutoTrustHostKey:   true,
		},
		Alert: AlertConfig{
			Format:           "wecom",
			MinIntervalSec:   600,
			CheckIntervalSec: 60,
			DiskMinFreeMB:    1024,
			CertExpireDays:   14,
		},
		Bootstrap: BootstrapConfig{
			AdminUsername: "admin",
			AdminPassword: defaultAdminPassword,
		},
	}
}

// Validate 校验启动配置的安全性，错误信息会明确指出出错的配置项。
func (c *Config) Validate() error {
	switch c.Server.Mode {
	case "debug", "release", "test":
	default:
		return fmt.Errorf("server.mode 非法：%q（仅支持 debug、release、test）", c.Server.Mode)
	}

	switch c.Deploy.Mode {
	case "local", "ssh":
	default:
		return fmt.Errorf("deploy.mode 非法：%q（仅支持 local、ssh）", c.Deploy.Mode)
	}

	if c.Storage.MaxTotalMB < 0 {
		return fmt.Errorf("storage.max_total_mb 不能为负数：%d", c.Storage.MaxTotalMB)
	}

	basePath, err := normalizeBasePath(c.Server.BasePath)
	if err != nil {
		return err
	}
	c.Server.BasePath = basePath

	// deploy.max_pending_per_user 必须大于 0；为 0 时按默认值 10 处理，负值视为非法。
	if c.Deploy.MaxPendingPerUser < 0 {
		return fmt.Errorf("deploy.max_pending_per_user 不能为负数：%d", c.Deploy.MaxPendingPerUser)
	}
	if c.Deploy.MaxPendingPerUser == 0 {
		c.Deploy.MaxPendingPerUser = 10
	}

	// deploy.metrics_interval_seconds 为 0 表示关闭后台采集，负数视为非法。
	if c.Deploy.MetricsIntervalSec < 0 {
		return fmt.Errorf("deploy.metrics_interval_seconds 不能为负数：%d（0 表示关闭后台采集）", c.Deploy.MetricsIntervalSec)
	}

	switch c.Alert.Format {
	case "", "wecom", "generic":
		if c.Alert.Format == "" {
			c.Alert.Format = "wecom"
		}
	default:
		return fmt.Errorf("alert.format 非法：%q（仅支持 wecom、generic）", c.Alert.Format)
	}
	if c.Alert.CheckIntervalSec < 0 {
		return fmt.Errorf("alert.check_interval_seconds 不能为负数：%d（0 表示关闭巡检）", c.Alert.CheckIntervalSec)
	}
	if c.Alert.MinIntervalSec < 0 {
		return fmt.Errorf("alert.min_interval_seconds 不能为负数：%d", c.Alert.MinIntervalSec)
	}
	if c.Alert.DiskMinFreeMB < 0 {
		return fmt.Errorf("alert.disk_min_free_mb 不能为负数：%d", c.Alert.DiskMinFreeMB)
	}
	if c.Alert.CertExpireDays < 0 {
		return fmt.Errorf("alert.cert_expire_days 不能为负数：%d", c.Alert.CertExpireDays)
	}
	if strings.TrimSpace(c.Alert.WebhookURL) != "" && !isHTTPURL(c.Alert.WebhookURL) {
		return fmt.Errorf("alert.webhook_url 必须是 http(s) 地址")
	}
	for _, item := range []struct {
		name  string
		value int
	}{
		{"storage.task_log_retention_days", c.Storage.TaskLogRetentionDays},
		{"storage.task_retention_days", c.Storage.TaskRetentionDays},
		{"storage.audit_retention_days", c.Storage.AuditRetentionDays},
		{"storage.package_keep_releases", c.Storage.PackageKeepReleases},
		{"storage.package_keep_days", c.Storage.PackageKeepDays},
	} {
		if item.value < 0 {
			return fmt.Errorf("%s 不能为负数：%d（0 表示不启用）", item.name, item.value)
		}
	}

	// credential_key 显式配置时必须足够强；空值或默认占位值视为「未配置」，
	// 允许启动（保护存量部署升级），非 debug 下使用「直接录入」时报错。
	if key := c.Security.CredentialKey; key != "" && key != defaultCredentialKey && len(key) < minCredentialKeyLength {
		return fmt.Errorf("security.credential_key 长度不足 %d 个字符（当前 %d）", minCredentialKeyLength, len(key))
	}

	for _, proxy := range c.Server.TrustedProxies {
		if _, _, err := net.ParseCIDR(proxy); err == nil {
			continue
		}
		// 纯 IP 视为 /32 或 /128 的单主机网段。
		if net.ParseIP(proxy) != nil {
			continue
		}
		return fmt.Errorf("server.trusted_proxies 含非法的 CIDR 或 IP：%q", proxy)
	}

	if c.Server.Mode == "debug" {
		return nil
	}

	if c.Auth.JWTSecret == "" {
		return fmt.Errorf("非 debug 模式下 auth.jwt_secret 不能为空")
	}
	if c.Auth.JWTSecret == defaultJWTSecret {
		return fmt.Errorf("非 debug 模式下 auth.jwt_secret 不能使用默认密钥，请通过 PACKRUN_JWT_SECRET 或配置文件设置")
	}
	if len(c.Auth.JWTSecret) < minJWTSecretLength {
		return fmt.Errorf("非 debug 模式下 auth.jwt_secret 长度不足 %d 个字符（当前 %d）", minJWTSecretLength, len(c.Auth.JWTSecret))
	}

	if c.Bootstrap.AdminPassword == "" {
		return fmt.Errorf("非 debug 模式下 bootstrap.admin_password 不能为空")
	}
	if c.Bootstrap.AdminPassword == defaultAdminPassword {
		return fmt.Errorf("非 debug 模式下 bootstrap.admin_password 不能使用默认口令，请通过 PACKRUN_ADMIN_PASSWORD 或配置文件设置")
	}

	for _, origin := range c.Server.AllowedOrigins {
		if origin == "*" {
			return fmt.Errorf("非 debug 模式下 server.allowed_origins 不能包含通配符 \"*\"")
		}
	}

	if c.Deploy.Mode == "ssh" && os.Getenv("PACKRUN_SSH_INSECURE") == "1" {
		return fmt.Errorf("非 debug 模式下禁止设置 PACKRUN_SSH_INSECURE=1（该选项仅允许开发环境使用）")
	}

	return nil
}

// UsableCredentialKey 返回「直接录入」凭据可用的主密钥短语。
// debug 模式可用占位默认值；非 debug 必须显式配置非默认值，否则返回错误（使用时报错，不阻止启动）。
func (c *Config) UsableCredentialKey() (string, error) {
	key := strings.TrimSpace(c.Security.CredentialKey)
	if key == "" {
		return "", fmt.Errorf("未配置 security.credential_key（或环境变量 PACKRUN_CREDENTIAL_KEY），无法使用「直接录入」凭据")
	}
	if c.Server.Mode != "debug" && key == defaultCredentialKey {
		return "", fmt.Errorf("非 debug 模式必须配置非默认的 credential_key（security.credential_key 或 PACKRUN_CREDENTIAL_KEY，≥%d 字符），才能使用「直接录入」凭据", minCredentialKeyLength)
	}
	return key, nil
}

// isHTTPURL 只接受 http/https 前缀，避免把 file:// 之类写进告警配置。
func isHTTPURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://")
}

// normalizeBasePath 将部署子路径规范化为 /foo 形式（根路径为 /）。
func normalizeBasePath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "/" {
		return "/", nil
	}
	if !strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("server.base_path 必须以 / 开头：%q", raw)
	}
	if strings.Contains(raw, "..") {
		return "", fmt.Errorf("server.base_path 不能包含 ..：%q", raw)
	}
	for _, r := range raw {
		switch {
		case r == '/' || r == '-' || r == '_' || r == '.' || r == '~':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		default:
			return "", fmt.Errorf("server.base_path 仅支持字母、数字和 -_.~/：%q", raw)
		}
	}
	return path.Clean(raw), nil
}

// Load 读取 YAML 配置，文件缺失时使用内置默认值；随后用环境变量覆盖敏感项。
func Load(path string) (*Config, error) {
	cfg := defaults()

	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(b, cfg); err != nil {
			return nil, err
		}
	case os.IsNotExist(err):
		// 允许无配置文件启动，全部走默认值
	default:
		return nil, err
	}

	applyEnv(cfg)
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("PACKRUN_ADDR"); v != "" {
		cfg.Server.Addr = v
	}
	if v := os.Getenv("PACKRUN_BASE_PATH"); v != "" {
		cfg.Server.BasePath = v
	}
	if v := os.Getenv("PACKRUN_JWT_SECRET"); v != "" {
		cfg.Auth.JWTSecret = v
	}
	if v := os.Getenv("PACKRUN_CREDENTIAL_KEY"); v != "" {
		cfg.Security.CredentialKey = v
	}
	if v := os.Getenv("PACKRUN_DEPLOY_MODE"); v != "" {
		cfg.Deploy.Mode = v
	}
	if v := os.Getenv("PACKRUN_ADMIN_PASSWORD"); v != "" {
		cfg.Bootstrap.AdminPassword = v
	}
	if v := os.Getenv("PACKRUN_MAX_CONCURRENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Deploy.MaxConcurrent = n
		}
	}
	if v := os.Getenv("PACKRUN_ALERT_WEBHOOK"); v != "" {
		cfg.Alert.WebhookURL = v
	}
}
