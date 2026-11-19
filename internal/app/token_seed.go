package app

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// errTokenSigningKeyIncomplete marks an empty or truncated key file. A file in
// this state may be a concurrent creator that has not finished writing yet, so
// readers retry briefly instead of failing immediately.
// errTokenSigningKeyIncomplete 标记空或被截断的密钥文件。该状态可能是并发创建者
// 尚未写完，因此读取方短暂重试而不是立刻失败。
var errTokenSigningKeyIncomplete = errors.New("token signing key file is empty or truncated")

const (
	tokenSigningKeyRetryAttempts = 40
	tokenSigningKeyRetryDelay    = 25 * time.Millisecond
)

// loadOrCreateTokenSigningKeyFile reads the key file, or atomically creates it on
// first startup. Creation uses an exclusive create so concurrent processes that
// share a fresh storage directory converge on one complete key: the winner
// writes it, losers read the winner's content. Readers retry briefly while the
// file is still empty so a half-published key is never used. Errors out on
// permanently unreadable, empty or truncated content instead of regenerating
// silently.
// loadOrCreateTokenSigningKeyFile 读取密钥文件，首次启动时原子创建。创建使用独占
// 打开，让共用全新 storage 目录的多个进程最终得到同一份完整密钥：胜出者写入，
// 竞争失败者读取胜出者的内容；读到空文件时短暂重试，绝不使用写到一半的密钥。
// 永久不可读、为空或被截断时报错返回，而不是悄悄重新生成。
func loadOrCreateTokenSigningKeyFile(path string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("create directory for token signing key file %s failed: %w", path, err)
	}

	// Fast path: a complete key file already exists.
	if key, err, exists := readTokenSigningKeyFile(path); exists && err == nil {
		return key, nil
	} else if exists && !errors.Is(err, errTokenSigningKeyIncomplete) {
		return "", fmt.Errorf("read token signing key file %s failed: %w", path, err)
	}

	// Generate the candidate once; it is only used if this process wins the
	// exclusive create. Losers discard it and read the winner's key.
	key, err := generateTokenSigningKey()
	if err != nil {
		return "", err
	}

	for attempt := 0; attempt < tokenSigningKeyRetryAttempts; attempt++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			return persistTokenSigningKeyFile(f, key)
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("create token signing key file %s failed: %w", path, err)
		}

		// Another process created the file first. Read its key; if the file is
		// still empty the winner has not finished writing, so wait and retry.
		existing, readErr, exists := readTokenSigningKeyFile(path)
		if exists && readErr == nil {
			return existing, nil
		}
		if readErr != nil && !errors.Is(readErr, errTokenSigningKeyIncomplete) {
			return "", fmt.Errorf("read token signing key file %s failed: %w", path, readErr)
		}
		if !exists {
			// The winner abandoned and removed its failed file; try to create again.
			continue
		}
		time.Sleep(tokenSigningKeyRetryDelay)
	}

	if key, err, exists := readTokenSigningKeyFile(path); exists && err == nil {
		return key, nil
	} else if err != nil {
		return "", fmt.Errorf(
			"token signing key file %s is empty or truncated; "+
				"restore it from backup or configure security.token-signing-key: %w",
			path, err)
	}
	return "", fmt.Errorf("token signing key file %s disappeared while waiting for a concurrent writer", path)
}

// readTokenSigningKeyFile returns (key, nil, true) for a complete file,
// ("", errTokenSigningKeyIncomplete, true) for an empty/truncated one,
// ("", nil, false) when the file does not exist, and ("", err, true) for other
// read failures.
// readTokenSigningKeyFile 的返回：完整文件为 (key, nil, true)；空/截断为
// ("", errTokenSigningKeyIncomplete, true)；不存在为 ("", nil, false)；
// 其它读取失败为 ("", err, true)。
func readTokenSigningKeyFile(path string) (string, error, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, false
		}
		return "", err, true
	}
	key := strings.TrimSpace(string(data))
	if len(key) < minTokenSigningKeyLen {
		return "", fmt.Errorf("%w (len %d, want >= %d)", errTokenSigningKeyIncomplete, len(key), minTokenSigningKeyLen), true
	}
	return key, nil, true
}

func generateTokenSigningKey() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate random token signing key failed: %w", err)
	}
	return base64.RawStdEncoding.EncodeToString(raw), nil
}

// persistTokenSigningKeyFile writes and closes the exclusively created file.
// On failure the just-created file is removed so another process can retry.
// persistTokenSigningKeyFile 写入并关闭独占创建的文件；失败时删除刚创建的文件，
// 让其它进程可以重试。
func persistTokenSigningKeyFile(f *os.File, key string) (string, error) {
	name := f.Name()
	if _, err := f.WriteString(key + "\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf("write token signing key file %s failed: %w", name, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("close token signing key file %s failed: %w", name, err)
	}
	return key, nil
}
