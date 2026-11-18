package config

import (
	"fmt"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
)

// SecurityConfig security configuration
// SecurityConfig 安全配置
type SecurityConfig struct {
	AuthTokenKey string `yaml:"auth-token-key" default:"fast-note-sync-Auth-Token"`
	TokenExpiry  string `yaml:"token-expiry" default:"365d"` // Token expiry, supports format: 7d (days), 24h (hours), 30m (minutes)
	// Token 过期时间，支持格式：7d（天）、24h（小时）、30m（分钟）
	ShareTokenKey string `yaml:"share-token-key" default:"fns"`
	// ShareTokenExpiry share Token expiry
	// ShareTokenExpiry 分享 Token 过期时间
	ShareTokenExpiry string `yaml:"share-token-expiry" default:"30d"`
	// WebGUILoginTokenExpiry expiry duration for WebGUI auto-issued login tokens (e.g. 7d, 24h)
	// WebGUILoginTokenExpiry WebGUI 自动签发登录 Token 的有效期（如 7d、24h）
	WebGUILoginTokenExpiry string `yaml:"webgui-login-token-expiry" default:"7d"`
	// WebGUILoginTokenBindIP whether to bind the client IP when issuing WebGUI login tokens
	// WebGUILoginTokenBindIP 签发 WebGUI 登录 Token 时是否绑定客户端 IP
	WebGUILoginTokenBindIP *bool `yaml:"webgui-login-token-bind-ip" default:"true"`
	// WebGUILoginTokenBindUA whether to bind the User-Agent when issuing WebGUI login tokens
	// WebGUILoginTokenBindUA 签发 WebGUI 登录 Token 时是否绑定 User-Agent
	WebGUILoginTokenBindUA *bool `yaml:"webgui-login-token-bind-ua" default:"true"`
	// TokenSigningKey stable signing key for all tokens. When set, token signing
	// keys are derived from this value instead of the machine ID.
	// TokenSigningKey 所有令牌的稳定签名密钥。设置后令牌签名密钥改由该值派生，
	// 不再依赖机器标识，迁移机器、重装系统或重建容器后令牌仍然有效。
	// 留空时在 storage 目录生成并持久化随机密钥文件（token-signing.key）。
	TokenSigningKey string `yaml:"token-signing-key"`
}

// Validate rejects token lifetimes that are nonpositive or overflow time.Duration.
func (c SecurityConfig) Validate() error {
	for _, field := range []struct{ name, value string }{
		{"token-expiry", c.TokenExpiry},
		{"share-token-expiry", c.ShareTokenExpiry},
		{"webgui-login-token-expiry", c.WebGUILoginTokenExpiry},
	} {
		d, err := util.ParseDuration(field.value)
		if err != nil {
			return fmt.Errorf("security.%s: %w", field.name, err)
		}
		if d <= 0 {
			return fmt.Errorf("security.%s must be positive", field.name)
		}
	}
	return nil
}
