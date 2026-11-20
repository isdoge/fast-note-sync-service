// Package app provides application container, encapsulates all dependencies and services
// Package app 提供应用容器，封装所有依赖和服务
package app

import (
	"os"
	"path/filepath"
	"time"

	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/haierkeys/fast-note-sync-service/pkg/workerpool"
	"github.com/haierkeys/fast-note-sync-service/pkg/writequeue"

	"github.com/creasty/defaults"
	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

// AppConfig application configuration
// AppConfig 应用配置
type AppConfig struct {
	File string `yaml:"-"` // config file path, not serialized
	// 配置文件路径, 不序列化

	Server           config.ServerConfig           `yaml:"server"`
	App              config.AppSettings            `yaml:"app"`
	Security         config.SecurityConfig         `yaml:"security"`
	Database         config.DatabaseConfig         `yaml:"database"`
	UserDatabase     config.DatabaseConfig         `yaml:"user-database"`
	Log              config.LogConfig              `yaml:"log"`
	User             config.UserConfig             `yaml:"user"`
	Tracer           config.TracerConfig           `yaml:"tracer"`
	ShortLink        config.ShortLinkConfig        `yaml:"short-link"`
	Storage          config.StorageConfig          `yaml:"storage"`
	Git              config.GitConfig              `yaml:"git"`
	WebGUI           config.WebGUIConfig           `yaml:"webgui"`
	Cloudflare       config.CloudflareConfig       `yaml:"cloudflare"`
	OAuth            config.OAuthConfig            `yaml:"oauth"`
	OIDC             config.OIDCConfig             `yaml:"oidc"`
	AttachmentStatic config.AttachmentStaticConfig `yaml:"attachment-static"` // Attachment static access configuration // 附件模拟静态访问配置
}

// LoadConfig loads configuration from file
// LoadConfig 从文件加载配置
// returns configuration instance and absolute path of configuration file
// 返回配置实例和配置文件的绝对路径
func LoadConfig(f string) (*AppConfig, string, error) {
	realpath, err := filepath.Abs(f)
	if err != nil {
		return nil, "", err
	}
	realpath = filepath.Clean(realpath)

	c := new(AppConfig)
	c.File = realpath

	// Set default values
	// 设置默认值
	if err := defaults.Set(c); err != nil {
		return nil, realpath, errors.Wrap(err, "set default config failed")
	}

	file, err := os.ReadFile(realpath)
	if err != nil {
		return nil, realpath, errors.Wrap(err, "read config file failed")
	}

	err = yaml.Unmarshal(file, c)
	if err != nil {
		return nil, realpath, errors.Wrap(err, "parse config file failed")
	}

	// Set default values again to fill fields that exist in YAML but have empty values
	// 再次设置默认值，以填充 YAML 中存在但值为空的字段
	// defaults.Set filled only when the field is the zero value of the type
	// defaults.Set 只有在字段为该类型的零值时才会填充
	if err := defaults.Set(c); err != nil {
		return nil, realpath, errors.Wrap(err, "re-set default config failed")
	}
	if err := c.App.ValidateWebSocketHeartbeat(); err != nil {
		return nil, realpath, errors.Wrap(err, "validate websocket heartbeat config failed")
	}
	if err := c.Security.Validate(); err != nil {
		return nil, realpath, errors.Wrap(err, "validate security config failed")
	}
	c.OAuth.Normalize()
	if err := c.OAuth.Validate(); err != nil {
		return nil, realpath, errors.Wrap(err, "validate oauth config failed")
	}
	c.OIDC.Normalize()
	if err := c.OIDC.Validate(); err != nil {
		return nil, realpath, errors.Wrap(err, "validate oidc config failed")
	}

	return c, realpath, nil
}

// Save saves configuration to file
// Save 保存配置到文件
func (c *AppConfig) Save() error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return errors.Wrap(err, "marshal config failed")
	}

	err = os.WriteFile(c.File, data, 0644)
	if err != nil {
		return errors.Wrap(err, "write config file failed")
	}

	return nil
}

// GetWorkerPoolConfig gets Worker Pool configuration
// GetWorkerPoolConfig 获取 Worker Pool 配置
func (c *AppConfig) GetWorkerPoolConfig() workerpool.Config {
	cfg := workerpool.DefaultConfig()

	if c.App.WorkerPoolMaxWorkers > 0 {
		cfg.MaxWorkers = c.App.WorkerPoolMaxWorkers
	}
	if c.App.WorkerPoolQueueSize > 0 {
		cfg.QueueSize = c.App.WorkerPoolQueueSize
	}

	return cfg
}

// GetWriteQueueConfig gets Write Queue configuration
// GetWriteQueueConfig 获取 Write Queue 配置
func (c *AppConfig) GetWriteQueueConfig() writequeue.Config {
	cfg := writequeue.DefaultConfig()

	if c.App.WriteQueueCapacity > 0 {
		cfg.QueueCapacity = c.App.WriteQueueCapacity
	}
	if c.App.WriteQueueTimeout != "" {
		if timeout, err := util.ParseDuration(c.App.WriteQueueTimeout); err == nil {
			cfg.WriteTimeout = timeout
		}
	}
	if c.App.WriteQueueIdleTime != "" {
		if idleTime, err := util.ParseDuration(c.App.WriteQueueIdleTime); err == nil {
			cfg.IdleTimeout = idleTime
		}
	}

	return cfg
}

// ConfigFingerprint returns a deterministic YAML representation of cfg, used to
// detect config-file changes. When ignoreAdminUID is true, user.admin-uid is
// normalized to 0 so callers can tell whether anything else changed too.
// ConfigFingerprint 返回配置的确定性 YAML 指纹，用于检测配置文件变更；
// ignoreAdminUID 为 true 时把 user.admin-uid 归一化为 0，便于判断是否只有该字段变化。
func ConfigFingerprint(cfg *AppConfig, ignoreAdminUID bool) (string, error) {
	if cfg == nil {
		return "", errors.New("config is nil")
	}
	// Shallow copy is enough: yaml.Marshal only reads the nested values.
	// 浅拷贝即可：yaml.Marshal 只读取嵌套值，不会写入。
	clone := *cfg
	clone.File = ""
	if ignoreAdminUID {
		clone.User.AdminUID = 0
	}
	data, err := yaml.Marshal(&clone)
	if err != nil {
		return "", errors.Wrap(err, "marshal config fingerprint failed")
	}
	return string(data), nil
}

// ConfigReloadRequiresRestart reports whether moving from current to next touches
// any setting that is bound at server initialization. A change confined to
// user.admin-uid is hot-reloadable: the admin resolver reads it live from the
// config on every request, so rebuilding the HTTP servers is unnecessary.
// ConfigReloadRequiresRestart 判断从 current 到 next 是否改动了服务初始化时绑定的设置。
// 仅 user.admin-uid 变化属于热更新字段：管理员判定在每次请求时实时读取配置，无需重建 HTTP 服务。
func ConfigReloadRequiresRestart(current, next *AppConfig) bool {
	cur, err := ConfigFingerprint(current, true)
	if err != nil {
		return true
	}
	nxt, err := ConfigFingerprint(next, true)
	if err != nil {
		return true
	}
	return cur != nxt
}

// GetTokenExpiry gets Token expiry duration
// GetTokenExpiry 获取 Token 过期时间
func (c *AppConfig) GetTokenExpiry() time.Duration {
	if expiry, err := util.ParseDuration(c.Security.TokenExpiry); err == nil {
		return expiry
	}
	return 365 * 24 * time.Hour // Theoretically will not reach here because of default values
	// 理论上不会走到这里，因为有默认值
}

// GetShareTokenExpiry gets share Token expiry duration
// GetShareTokenExpiry 获取分享 Token 过期时间
func (c *AppConfig) GetShareTokenExpiry() time.Duration {
	if expiry, err := util.ParseDuration(c.Security.ShareTokenExpiry); err == nil {
		return expiry
	}
	return 30 * 24 * time.Hour // Theoretically will not reach here because of default values
	// 理论上不会走到这里，因为有默认值
}
