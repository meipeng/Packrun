package config

import "testing"

func TestValidate(t *testing.T) {
	const (
		validJWTSecret  = "0123456789abcdef0123456789abcdef" // 32 位
		validAdminPass  = "S3cure-Admin-Pass"
		shortJWTSecret  = "0123456789abcdef0123456789abcde" // 31 位
		defaultPassword = defaultAdminPassword // 与 config 包常量同源，避免字面量
	)

	tests := []struct {
		name            string
		mutate          func(*Config)
		sshInsecure     string
		wantErr         bool
		wantPendingPerU int
		wantBasePath    string
	}{
		{
			name:   "debug 模式允许默认 JWT 密钥与默认 admin 口令",
			mutate: func(c *Config) {},
		},
		{
			name: "release 模式拒绝默认 JWT 密钥",
			mutate: func(c *Config) {
				c.Server.Mode = "release"
				c.Bootstrap.AdminPassword = validAdminPass
			},
			wantErr: true,
		},
		{
			name: "release 模式拒绝 31 字符密钥",
			mutate: func(c *Config) {
				c.Server.Mode = "release"
				c.Auth.JWTSecret = shortJWTSecret
				c.Bootstrap.AdminPassword = validAdminPass
			},
			wantErr: true,
		},
		{
			name: "release 模式拒绝默认 admin 口令",
			mutate: func(c *Config) {
				c.Server.Mode = "release"
				c.Auth.JWTSecret = validJWTSecret
				c.Bootstrap.AdminPassword = defaultPassword
			},
			wantErr: true,
		},
		{
			name: "release 模式拒绝通配 allowed_origins",
			mutate: func(c *Config) {
				c.Server.Mode = "release"
				c.Auth.JWTSecret = validJWTSecret
				c.Bootstrap.AdminPassword = validAdminPass
				c.Server.AllowedOrigins = []string{"http://localhost:5173", "*"}
			},
			wantErr: true,
		},
		{
			name: "非法 trusted_proxies 被拒绝",
			mutate: func(c *Config) {
				c.Server.TrustedProxies = []string{"not-a-cidr"}
			},
			wantErr: true,
		},
		{
			name: "合法 CIDR 与纯 IP 混合通过",
			mutate: func(c *Config) {
				c.Server.TrustedProxies = []string{"10.0.0.0/8", "192.168.1.10", "::1", "2001:db8::/32"}
			},
		},
		{
			name: "非法 deploy.mode 被拒绝",
			mutate: func(c *Config) {
				c.Deploy.Mode = "remote"
			},
			wantErr: true,
		},
		{
			name: "非 debug + ssh + PACKRUN_SSH_INSECURE=1 被拒绝",
			mutate: func(c *Config) {
				c.Server.Mode = "release"
				c.Auth.JWTSecret = validJWTSecret
				c.Bootstrap.AdminPassword = validAdminPass
				c.Deploy.Mode = "ssh"
			},
			sshInsecure: "1",
			wantErr:     true,
		},
		{
			name: "max_pending_per_user 为 0 时回落为默认值 10",
			mutate: func(c *Config) {
				c.Deploy.MaxPendingPerUser = 0
			},
			wantPendingPerU: 10,
		},
		{
			name: "base_path 规范化为无尾斜杠",
			mutate: func(c *Config) {
				c.Server.BasePath = "/packrun/"
			},
			wantBasePath: "/packrun",
		},
		{
			name: "base_path 嵌套路径通过",
			mutate: func(c *Config) {
				c.Server.BasePath = "/tools/packrun"
			},
			wantBasePath: "/tools/packrun",
		},
		{
			name: "base_path 必须是绝对路径",
			mutate: func(c *Config) {
				c.Server.BasePath = "packrun"
			},
			wantErr: true,
		},
		{
			name: "base_path 拒绝路径穿越",
			mutate: func(c *Config) {
				c.Server.BasePath = "/a/../b"
			},
			wantErr: true,
		},
		{
			name: "base_path 拒绝特殊字符",
			mutate: func(c *Config) {
				c.Server.BasePath = `/packrun"<x>`
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PACKRUN_SSH_INSECURE", tt.sshInsecure)

			cfg := defaults()
			tt.mutate(cfg)

			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("期望 Validate 返回错误，实际通过")
				}
				return
			}
			if err != nil {
				t.Fatalf("期望 Validate 通过，实际返回错误: %v", err)
			}
			if tt.wantPendingPerU != 0 && cfg.Deploy.MaxPendingPerUser != tt.wantPendingPerU {
				t.Fatalf("deploy.max_pending_per_user = %d，期望 %d", cfg.Deploy.MaxPendingPerUser, tt.wantPendingPerU)
			}
			if tt.wantBasePath != "" && cfg.Server.BasePath != tt.wantBasePath {
				t.Fatalf("server.base_path = %q，期望 %q", cfg.Server.BasePath, tt.wantBasePath)
			}
		})
	}
}

func TestLoadBasePathFromEnv(t *testing.T) {
	t.Setenv("PACKRUN_BASE_PATH", "/packrun/")

	cfg, err := Load("test-no-such-config.yaml")
	if err != nil {
		t.Fatalf("加载默认配置失败: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("配置校验失败: %v", err)
	}
	if cfg.Server.BasePath != "/packrun" {
		t.Fatalf("server.base_path = %q，期望 /packrun", cfg.Server.BasePath)
	}
}
