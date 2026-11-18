package config

// UserConfig user configuration
// UserConfig 用户配置
type UserConfig struct {
	// RegisterIsEnable whether registration is enabled
	// RegisterIsEnable 注册是否启用
	RegisterIsEnable bool `yaml:"register-is-enable"`
	// AdminUID admin UID. 0 means no explicit admin is configured; the server
	// then treats the user with the smallest existing UID as admin (nobody if
	// there are no users). It no longer means "every user is admin".
	// AdminUID 管理员 UID。0 表示未显式配置管理员；此时服务端按“现存用户中
	// UID 最小者”认定管理员（无用户则无人是管理员），不再表示“人人都是管理员”。
	AdminUID int `yaml:"admin-uid" default:"0"`
}
