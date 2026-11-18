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
	defaultJWTSecret     = "packrun-dev-secret-change-me"
	defaultAdminPassword = "Admin@12345"
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
	Bootstrap BootstrapConfig `yaml:"bootstrap"`
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
			DBPath:      "./data/packrun.db",
			PackageDir:  "./data/packages",
			SandboxRoot: "./data/sandbox",
			MaxTotalMB:  20480,
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
}
