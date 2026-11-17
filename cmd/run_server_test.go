package cmd

import (
	"path/filepath"
	"testing"

	internalApp "github.com/haierkeys/fast-note-sync-service/internal/app"
	"go.uber.org/zap"
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
