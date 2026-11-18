package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	internalApp "github.com/haierkeys/fast-note-sync-service/internal/app"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// 启动时初始化的项目 logger 必须同时成为全局 logger，否则各处 zap.L() 的日志会被丢弃。
func TestInitLoggerWithConfigReplacesGlobalLogger(t *testing.T) {
	restore := zap.ReplaceGlobals(zap.NewNop())
	defer restore()

	cfg := &internalApp.AppConfig{}
	cfg.Log.Level = "info"
	cfg.Log.File = filepath.Join(t.TempDir(), "log.log")
	s := &Server{}
	if err := initLoggerWithConfig(s, cfg); err != nil {
		t.Fatalf("initLoggerWithConfig: %v", err)
	}
	if zap.L() != s.logger {
		t.Fatal("zap.L() is not the server logger; zap.L() logs would be dropped")
	}
}

// #4/B6：OAuth 启用且 allow-static-fns-token 为 true、default-fns-scope 含通配时，
// 启动日志必须输出对应提醒；未启用 OAuth 时不提醒。
func TestCheckOAuthSecurityConfigNotices(t *testing.T) {
	type entry struct {
		key, msg string
	}
	tests := []struct {
		name  string
		cfg   func(*internalApp.AppConfig)
		wants []string
	}{
		{
			name:  "oauth disabled stays silent",
			cfg:   func(c *internalApp.AppConfig) {},
			wants: nil,
		},
		{
			name: "static token allowed with oauth warns",
			cfg: func(c *internalApp.AppConfig) {
				c.OAuth.Enabled = true
				c.OAuth.AllowStaticFNSToken = true
			},
			wants: []string{"allow-static-fns-token"},
		},
		{
			name: "wildcard default scope warns",
			cfg: func(c *internalApp.AppConfig) {
				c.OAuth.Enabled = true
				c.OAuth.AllowStaticFNSToken = false
				c.OAuth.DefaultFNSScope = "p:mcp c:* f:*"
			},
			wants: []string{"default-fns-scope contains a wildcard"},
		},
		{
			name: "narrow scope does not warn",
			cfg: func(c *internalApp.AppConfig) {
				c.OAuth.Enabled = true
				c.OAuth.AllowStaticFNSToken = false
				c.OAuth.DefaultFNSScope = "p:mcp c:ChatGPT f:note_r"
			},
			wants: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := zap.ReplaceGlobals(zap.NewNop())
			defer restore()

			core, recorded := observer.New(zapcore.WarnLevel)
			lg := zap.New(core)

			cfg := &internalApp.AppConfig{}
			tt.cfg(cfg)
			checkOAuthSecurityConfig(cfg, lg)

			entries := make([]entry, 0, recorded.Len())
			for _, e := range recorded.All() {
				entries = append(entries, entry{key: "", msg: e.Message})
			}
			if len(tt.wants) == 0 {
				if recorded.Len() != 0 {
					t.Fatalf("不应有提醒，实际输出: %v", entries)
				}
				return
			}
			for _, want := range tt.wants {
				found := false
				for _, e := range entries {
					if strings.Contains(e.msg, want) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("缺少提醒 %q，实际输出: %v", want, entries)
				}
			}
		})
	}
}
