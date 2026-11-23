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
	// StrictEmptyProtocol rejects scopes whose protocol dimension is empty
	// (whole-string empty scope or "p:" with no value), which otherwise act as
	// wildcards. Defaults to false for backward compatibility with legacy
	// tokens; enable to make such stored scopes fail closed.
	// StrictEmptyProtocol 拒绝协议维度为空的 scope（整串空 scope 或值为空的
	// "p:"），这类 scope 在默认匹配中等同通配。默认 false 以兼容存量令牌；
	// 开启后这类已落库的 scope 将按拒绝处理。
	StrictEmptyProtocol bool `yaml:"strict-empty-protocol"`
	// ShareBaseURLToken is an optional static token for the runtime share base
	// URL admin endpoints (POST/GET /api/admin/share-base-url). When set, an
	// external notifier (e.g. a dynamic-DNS / tunnel script) can push a new
	// public base URL with this token instead of an admin login token. When
	// empty, only the effective administrator may call those endpoints.
	// ShareBaseURLToken 是运行时分享基址管理接口（POST/GET
	// /api/admin/share-base-url）的可选静态令牌。设置后，外部通知脚本
	//（如动态 DNS / 内网穿透脚本）可用它推送新的公开基址，而无需管理员登录
	// 令牌；留空时仅生效管理员可调用。
	ShareBaseURLToken string `yaml:"share-base-url-token"`
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
