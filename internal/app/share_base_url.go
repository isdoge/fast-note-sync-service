package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// ShareBaseURLFileName is the file name of the runtime share base URL
	// override, relative to the storage directory.
	// ShareBaseURLFileName 运行时分享基址覆盖值在 storage 目录下的文件名。
	ShareBaseURLFileName = "share_base_url"

	// ShareBaseURLSourceOverride marks a value supplied at runtime through the
	// admin API and persisted under the storage directory.
	// ShareBaseURLSourceOverride 表示该值来自管理接口的运行时覆盖（持久化在
	// storage 目录）。
	ShareBaseURLSourceOverride = "override"
	// ShareBaseURLSourceConfig marks the value read from server.share-base-url.
	// ShareBaseURLSourceConfig 表示该值来自配置项 server.share-base-url。
	ShareBaseURLSourceConfig = "config"

	// shareBaseURLFileMode keeps the override file private to the service user.
	// shareBaseURLFileMode 让覆盖文件仅服务用户可读写。
	shareBaseURLFileMode = 0600
)

// ShareBaseURLStore holds the runtime share base URL override.
// ShareBaseURLStore 保存运行时分享基址覆盖值。
//
// The override is optional: an empty override means "use the configured
// server.share-base-url". Persistence is atomic (temp file + rename) so a
// crash mid-write never leaves a truncated value behind. All reads and writes
// are guarded by an RWMutex and safe for concurrent handlers.
// 覆盖值是可选的：为空表示“沿用 server.share-base-url 配置”。写入采用临时文件
// 加 rename 的原子方式，进程中途退出不会留下半截值。读写由 RWMutex 保护，
// 可被多个 handler 并发使用。
type ShareBaseURLStore struct {
	mu       sync.RWMutex
	override string
	path     string
}

// NewShareBaseURLStore creates a store persisted at path. An empty path keeps
// the store memory-only, which is what tests use.
// NewShareBaseURLStore 创建持久化到 path 的存储；path 为空时只在内存中保存
// （供测试使用）。
func NewShareBaseURLStore(path string) *ShareBaseURLStore {
	return &ShareBaseURLStore{path: path}
}

// Load reads the persisted override, if any. A missing file is not an error.
// Load 读取已持久化的覆盖值；文件不存在不算错误。
func (s *ShareBaseURLStore) Load() error {
	if s == nil || s.path == "" {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read share base url file %s failed: %w", s.path, err)
	}
	value := strings.TrimSpace(string(data))
	s.mu.Lock()
	s.override = value
	s.mu.Unlock()
	return nil
}

// Override returns the current runtime override (empty when unset).
// Override 返回当前运行时覆盖值（未设置时为空字符串）。
func (s *ShareBaseURLStore) Override() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.override
}

// Set stores value atomically. An empty value clears the override and removes
// the persisted file so the service falls back to the configured base URL.
// Set 原子写入 value；空值表示清除覆盖并删除持久化文件，回落到配置的基址。
func (s *ShareBaseURLStore) Set(value string) error {
	if s == nil {
		return fmt.Errorf("share base url store is nil")
	}
	value = strings.TrimSpace(value)

	// Memory-only store (tests) or nothing to persist for the empty case.
	if s.path == "" {
		s.mu.Lock()
		s.override = value
		s.mu.Unlock()
		return nil
	}

	if value == "" {
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove share base url file %s failed: %w", s.path, err)
		}
		s.mu.Lock()
		s.override = ""
		s.mu.Unlock()
		return nil
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory for share base url file %s failed: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".share_base_url-*")
	if err != nil {
		return fmt.Errorf("create temp file for share base url failed: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(shareBaseURLFileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod share base url temp file failed: %w", err)
	}
	if _, err := tmp.WriteString(value + "\n"); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write share base url temp file failed: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync share base url temp file failed: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close share base url temp file failed: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("persist share base url file %s failed: %w", s.path, err)
	}

	s.mu.Lock()
	s.override = value
	s.mu.Unlock()
	return nil
}

// defaultShareBaseURLStorePath returns the default persistence path used by the
// running server (relative to the working directory, like other storage paths).
// defaultShareBaseURLStorePath 返回运行中服务使用的默认持久化路径（与其它
// storage 路径一样相对于工作目录）。
func defaultShareBaseURLStorePath() string {
	return filepath.Join("storage", ShareBaseURLFileName)
}

// ShareBaseURLStore returns the runtime share base URL store.
// ShareBaseURLStore 返回运行时分享基址存储。
func (a *App) ShareBaseURLStore() *ShareBaseURLStore {
	if a == nil {
		return nil
	}
	return a.shareBaseURL
}

// SetShareBaseURLStore replaces the runtime share base URL store (tests).
// SetShareBaseURLStore 替换运行时分享基址存储（供测试注入临时路径）。
func (a *App) SetShareBaseURLStore(store *ShareBaseURLStore) {
	if a == nil {
		return
	}
	a.shareBaseURL = store
}

// ConfiguredShareBaseURL returns the configured server.share-base-url only when
// share link generation would actually use it, i.e. when both webgui-port and
// share-port are configured. It returns an empty string otherwise so callers
// can tell "no fixed base URL" apart from "configured but ignored".
// ConfiguredShareBaseURL 返回配置项 server.share-base-url 中会被分享链接实际采用
// 的值：只有 webgui-port 与 share-port 都配置了才返回，否则返回空字符串，调用
// 方据此区分“没有固定基址”和“配置了但不会生效”。
func (a *App) ConfiguredShareBaseURL() string {
	if a == nil || a.config == nil {
		return ""
	}
	cfg := a.config.Server
	if cfg.WebGuiPort == "" || cfg.SharePort == "" {
		return ""
	}
	return strings.TrimSpace(cfg.ShareBaseUrl)
}

// ShareBaseURLValue returns the effective share base URL and where it came
// from: the runtime override when set, otherwise the configured
// server.share-base-url value that share links would use. An empty value means
// there is no fixed share base URL right now and links are built from
// ext-api-url or the request address.
// ShareBaseURLValue 返回当前生效的分享基址及来源：有运行时覆盖时返回覆盖值，
// 否则返回分享链接实际会采用的 server.share-base-url 配置值。值为空表示当前
// 没有固定的分享基址，链接按 ext-api-url 或请求地址生成。
func (a *App) ShareBaseURLValue() (value string, source string) {
	if a != nil && a.shareBaseURL != nil {
		if override := a.shareBaseURL.Override(); override != "" {
			return override, ShareBaseURLSourceOverride
		}
	}
	return a.ConfiguredShareBaseURL(), ShareBaseURLSourceConfig
}
