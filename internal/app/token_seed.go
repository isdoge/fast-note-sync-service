package app

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"go.uber.org/zap"
)

// TokenSigningKeyFileName name of the persistent token signing key file under
// the storage directory.
// TokenSigningKeyFileName storage 目录下持久化令牌签名密钥的文件名。
const TokenSigningKeyFileName = "token-signing.key"

// minTokenSigningKeyLen minimum length (after trimming) for a key file or
// configured value to be considered usable.
// minTokenSigningKeyLen 密钥文件内容或配置值（去除首尾空白后）可用的最小长度。
const minTokenSigningKeyLen = 32

// resolveTokenSigningSeed resolves the stable token signing seed.
// resolveTokenSigningSeed 解析稳定的令牌签名种子。
//
// Priority: security.token-signing-key config value > key file under the storage
// directory (auto-generated on first startup, mode 600). The seed is applied via
// util.SetTokenSigningSeed; when it cannot be resolved, the machine ID is used
// (legacy behavior) and a clear error is logged. Only config and infra are
// needed, so it is safe to call on a partially built App.
// 优先级：security.token-signing-key 配置值 > storage 目录下的密钥文件（首次启动
// 自动生成，权限 600）。解析出的种子通过 util.SetTokenSigningSeed 生效；完全解析
// 不到时回退机器标识（旧版行为）并输出明确的错误日志。只用到配置与基础设施，
// 在部分构建的 App 上调用也是安全的。
func (a *App) resolveTokenSigningSeed() {
	if key := strings.TrimSpace(a.config.Security.TokenSigningKey); key != "" {
		util.SetTokenSigningSeed(key)
		return
	}

	seed, err := loadOrCreateTokenSigningKeyFile(a.tokenSigningKeyFilePath())
	if err != nil {
		// Log a clear error and fall back to the machine ID instead of silently
		// generating a new random key each startup (which would disconnect all
		// clients on every restart). Key content is never logged.
		// 输出明确错误并回退机器标识，而不是悄悄在每次启动生成新的随机密钥
		//（那样每次重启全部掉线）。密钥内容绝不写入日志。
		a.logger.Error("failed to load or create token signing key file; falling back to machine ID, "+
			"tokens will be invalidated on machine migration or reinstall",
			zap.String("file", a.tokenSigningKeyFilePath()), zap.Error(err))
		return
	}
	util.SetTokenSigningSeed(seed)
}

// tokenSigningKeyFilePath returns the key file path inside the storage directory.
// tokenSigningKeyFilePath 返回 storage 目录下的密钥文件路径。
func (a *App) tokenSigningKeyFilePath() string {
	return filepath.Join("storage", TokenSigningKeyFileName)
}

// loadOrCreateTokenSigningKeyFile reads the key file, or generates and persists
// a random key on first startup. Errors out on unreadable, empty or truncated
// content instead of regenerating silently.
// loadOrCreateTokenSigningKeyFile 读取密钥文件；首次启动时生成并持久化随机密钥。
// 内容为空或被截断时报错返回，而不是悄悄重新生成。
func loadOrCreateTokenSigningKeyFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		key := strings.TrimSpace(string(data))
		if len(key) < minTokenSigningKeyLen {
			return "", fmt.Errorf(
				"token signing key file %s is empty or truncated (len %d, want >= %d); "+
					"restore it from backup or configure security.token-signing-key",
				path, len(key), minTokenSigningKeyLen)
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("read token signing key file %s failed: %w", path, err)
	}

	// First startup: generate a random key and persist it with mode 600.
	// 首次启动：生成随机密钥并以 600 权限持久化。
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate random token signing key failed: %w", err)
	}
	key := base64.RawStdEncoding.EncodeToString(raw)

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("create directory for token signing key file %s failed: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(key+"\n"), 0600); err != nil {
		return "", fmt.Errorf("write token signing key file %s failed: %w", path, err)
	}
	return key, nil
}
