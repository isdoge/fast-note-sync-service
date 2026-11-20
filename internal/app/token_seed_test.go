package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"go.uber.org/zap"
)

func securityWithSigningKey(key string) config.SecurityConfig {
	return config.SecurityConfig{TokenSigningKey: key}
}

// newSeedTestInfra builds the minimal Infra needed by resolveTokenSigningSeed.
func newSeedTestInfra(t *testing.T, signingKey string) *Infra {
	t.Helper()
	return &Infra{
		config: &AppConfig{
			File:     filepath.Join(t.TempDir(), "config.yaml"),
			Security: securityWithSigningKey(signingKey),
		},
		logger: zap.NewNop(),
	}
}

func TestResolveTokenSigningSeed_ConfigValueTakesPriority(t *testing.T) {
	defer util.SetTokenSigningSeed("")

	infra := newSeedTestInfra(t, "my-configured-seed-0123456789abcdef")
	app := &App{Infra: infra}
	app.resolveTokenSigningSeed()

	if got := util.GetTokenSigningSeed(); got != "my-configured-seed-0123456789abcdef" {
		t.Fatalf("expected config value as seed, got %q", got)
	}
}

func TestLoadOrCreateTokenSigningKeyFile_Lifecycle(t *testing.T) {
	defer util.SetTokenSigningSeed("")
	defer util.SetUUIDPath(filepath.Join(t.TempDir(), ".server_uuid"))

	dir := t.TempDir()
	path := filepath.Join(dir, "storage", TokenSigningKeyFileName)

	// 1. First startup: generated, persisted, mode 600
	key1, err := loadOrCreateTokenSigningKeyFile(path)
	if err != nil {
		t.Fatalf("first create failed: %v", err)
	}
	if len(key1) < minTokenSigningKeyLen {
		t.Fatalf("generated key too short: %d", len(key1))
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("key file should exist: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("key file mode should be 600, got %v", fi.Mode().Perm())
	}

	// 2. Second startup: reads the same key
	key2, err := loadOrCreateTokenSigningKeyFile(path)
	if err != nil {
		t.Fatalf("second read failed: %v", err)
	}
	if key1 != key2 {
		t.Fatalf("key must be stable across restarts: %q vs %q", key1, key2)
	}

	// 3. Truncated / empty file: clear error, no silent regeneration
	if err := os.WriteFile(path, []byte("short"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateTokenSigningKeyFile(path); err == nil {
		t.Fatalf("truncated key file must fail with error")
	} else if !strings.Contains(err.Error(), TokenSigningKeyFileName) {
		t.Fatalf("error should mention the key file, got: %v", err)
	}

	// 4. Empty file: clear error
	if err := os.WriteFile(path, []byte("   \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateTokenSigningKeyFile(path); err == nil {
		t.Fatalf("empty key file must fail with error")
	}

	// 5. A directory in place of the key file: read error, not silent regeneration
	blocked := filepath.Join(dir, "blocked.key")
	if err := os.Mkdir(blocked, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateTokenSigningKeyFile(blocked); err == nil {
		t.Fatalf("unreadable key file must fail with error")
	}

	// 6. Two different dirs get different random keys (sanity of randomness)
	p1 := filepath.Join(t.TempDir(), "s1", TokenSigningKeyFileName)
	p2 := filepath.Join(t.TempDir(), "s2", TokenSigningKeyFileName)
	k1, _ := loadOrCreateTokenSigningKeyFile(p1)
	k2, _ := loadOrCreateTokenSigningKeyFile(p2)
	if k1 == k2 {
		t.Fatalf("independent key files should differ")
	}
}

func TestResolveTokenSigningSeed_EmptyConfigGeneratesKeyFile(t *testing.T) {
	defer util.SetTokenSigningSeed("")
	defer util.SetUUIDPath(filepath.Join(t.TempDir(), ".server_uuid"))

	// chdir into temp dir so the key file lands there
	dir := t.TempDir()
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	infra := newSeedTestInfra(t, "")
	app := &App{Infra: infra}
	app.resolveTokenSigningSeed()

	seed := util.GetTokenSigningSeed()
	if seed == "" {
		t.Fatalf("seed must not be empty")
	}
	// The persisted file must match the applied seed
	data, err := os.ReadFile(filepath.Join(dir, "storage", TokenSigningKeyFileName))
	if err != nil {
		t.Fatalf("key file must be persisted under storage/: %v", err)
	}
	if strings.TrimSpace(string(data)) != seed {
		t.Fatalf("persisted key must match applied seed")
	}
	if seed == util.GetMachineID() {
		t.Fatalf("seed must not be the machine ID when key file was created")
	}

	// Restart in the same dir: same seed again
	util.SetTokenSigningSeed("")
	app2 := &App{Infra: newSeedTestInfra(t, "")}
	app2.resolveTokenSigningSeed()
	if util.GetTokenSigningSeed() != seed {
		t.Fatalf("seed must be stable across restarts")
	}
}

func TestResolveTokenSigningSeed_CorruptKeyFileFallsBackWithLog(t *testing.T) {
	defer util.SetTokenSigningSeed("")
	defer util.SetUUIDPath(filepath.Join(t.TempDir(), ".server_uuid"))

	dir := t.TempDir()
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	// Pre-create a truncated key file
	keyPath := filepath.Join(dir, "storage", TokenSigningKeyFileName)
	if err := os.MkdirAll(filepath.Dir(keyPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	app := &App{Infra: newSeedTestInfra(t, "")}
	app.resolveTokenSigningSeed()

	if util.GetTokenSigningSeed() != util.GetMachineID() {
		t.Fatalf("corrupt key file must fall back to machine ID, not regenerate")
	}
	// And the corrupt file must NOT have been overwritten with a new key
	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "x" {
		t.Fatalf("corrupt key file must not be silently overwritten")
	}
}

// 多个进程在同一个全新 storage 目录上并发首次启动时，必须最终拿到同一个完整密钥。
func TestLoadOrCreateTokenSigningKeyFile_ConcurrentStartupAgree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage", TokenSigningKeyFileName)

	const workers = 32
	start := make(chan struct{})
	keys := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			key, err := loadOrCreateTokenSigningKeyFile(path)
			keys <- key
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(keys)
	close(errs)

	var first string
	for key := range keys {
		if key == "" {
			continue
		}
		if first == "" {
			first = key
			continue
		}
		if key != first {
			t.Fatalf("concurrent starters observed different keys: %q vs %q", first, key)
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent startup must not fail: %v", err)
		}
	}
	if first == "" {
		t.Fatal("no key returned")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != first {
		t.Fatalf("persisted key must match the returned key")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("key file mode should be 600, got %v", fi.Mode().Perm())
	}
}

// 竞争失败的一方读到刚创建但尚未写入的空文件时，应等待胜出者写完并返回其内容，
// 而不是报错或生成另一把密钥。
func TestLoadOrCreateTokenSigningKeyFile_WaitsForConcurrentWriter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "storage", TokenSigningKeyFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		key string
		err error
	}
	read := make(chan result, 1)
	go func() {
		key, err := loadOrCreateTokenSigningKeyFile(path)
		read <- result{key: key, err: err}
	}()

	// Give the reader time to observe the empty file and enter its retry loop;
	// the retry window is much larger, so the outcome does not depend on this delay.
	time.Sleep(100 * time.Millisecond)
	winner := "winner-key-0123456789abcdef-0123456789"
	if _, err := f.WriteString(winner + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got := <-read
	if got.err != nil {
		t.Fatalf("reader must wait for the winner, got error: %v", got.err)
	}
	if got.key != winner {
		t.Fatalf("reader must return the winner key %q, got %q", winner, got.key)
	}
}
