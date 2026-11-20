package util

import (
	"sync"
)

var (
	// tokenSigningSeed overrides the machine-ID-derived token signing seed.
	// tokenSigningSeed 覆盖基于机器标识派生的令牌签名种子。
	tokenSigningSeed string
	seedMutex        sync.RWMutex
)

// SetTokenSigningSeed sets a stable signing seed for all tokens.
// SetTokenSigningSeed 为所有令牌设置稳定的签名种子。
// When set to a non-empty value, token signing keys are derived from it instead
// of the machine ID, so tokens survive machine migration, OS reinstallation or
// container rebuilds. Set to an empty string to fall back to the machine ID.
// 设为非空值时，令牌签名密钥改由该值派生，不再依赖机器标识，令牌在迁移机器、
// 重装系统或重建容器后仍然有效。设为空字符串则回退到机器标识。
func SetTokenSigningSeed(seed string) {
	seedMutex.Lock()
	tokenSigningSeed = seed
	seedMutex.Unlock()
}

// GetTokenSigningSeed returns the effective token signing seed.
// GetTokenSigningSeed 返回当前生效的令牌签名种子。
// Returns the configured stable seed if set, otherwise the machine ID.
// 已配置稳定种子则返回之，否则返回机器标识。
func GetTokenSigningSeed() string {
	seedMutex.RLock()
	if tokenSigningSeed != "" {
		seedMutex.RUnlock()
		return tokenSigningSeed
	}
	seedMutex.RUnlock()

	// Fall back to machine ID (legacy behavior)
	// 回退到机器标识（旧版行为）
	return GetMachineID()
}
