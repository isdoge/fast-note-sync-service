package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/require"
)

// TestGitSyncService_ProcessSettingsBatch_DecodesBinaryContent 验证开启 IncludeConfig
// 后，fns-bin-v1: 编码的二进制配置导出到 Git 工作区时写的是解码后的原始字节
// （真实可用的图片/字体文件），普通文本配置保持原样。
// TestGitSyncService_ProcessSettingsBatch_DecodesBinaryContent verifies that with
// IncludeConfig enabled, fns-bin-v1: encoded binary settings are exported to the
// Git workspace as their decoded raw bytes (real image/font files), while plain
// text settings are written unchanged.
func TestGitSyncService_ProcessSettingsBatch_DecodesBinaryContent(t *testing.T) {
	svc := newTestGitSyncService(new(domainmocks.MockGitSyncRepository))
	wsPath := t.TempDir()
	conf := &domain.GitSyncConfig{ConfigSyncRules: []string{".obsidian", ".fonts"}}

	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0xff, 0x01, 0x02}
	font := []byte{0x00, 0x01, 0xff, 0xfe, 'w', 'O', 'F', '2'}
	escapedText := []byte("fns-bin-v1:this is plain text")
	settings := []*domain.Setting{
		{Path: ".obsidian/themes/lake/bg.png", Content: util.EncodeSettingContentBytes(png), Mtime: 1700000000000},
		{Path: ".fonts/sample.woff2", Content: util.EncodeSettingContentBytes(font), Mtime: 1700000000000},
		{Path: ".obsidian/escaped.txt", Content: util.EncodeSettingContentBytes(escapedText), Mtime: 1700000000000},
		{Path: ".obsidian/app.json", Content: `{"theme":"dark"}`, Mtime: 1700000000000},
	}

	changed, err := svc.processSettingsBatch(settings, conf, wsPath)
	require.NoError(t, err)
	require.True(t, changed)

	read := func(rel string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(wsPath, filepath.FromSlash(rel)))
		require.NoError(t, err)
		return data
	}
	require.Equal(t, png, read(".obsidian/themes/lake/bg.png"), "binary setting must be exported as raw bytes")
	require.Equal(t, font, read(".fonts/sample.woff2"), "binary font must be exported as raw bytes")
	require.Equal(t, escapedText, read(".obsidian/escaped.txt"), "escaped text must be unescaped on export")
	require.Equal(t, []byte(`{"theme":"dark"}`), read(".obsidian/app.json"), "plain text must stay unchanged")

	// 第二轮内容未变，导出不应再被判定为变更（比较的也是解码后的字节）。
	changed, err = svc.processSettingsBatch(settings, conf, wsPath)
	require.NoError(t, err)
	require.False(t, changed, "unchanged binary settings must not be rewritten")
}

// TestGitSyncService_ProcessSettingsBatch_SkipsCorruptBinary 验证损坏的
// fns-bin-v1:b64: 载荷既不会写出 base64 文本，也不会让整批导出失败。
// TestGitSyncService_ProcessSettingsBatch_SkipsCorruptBinary verifies a corrupt
// fns-bin-v1:b64: payload neither writes base64 text nor fails the whole batch.
func TestGitSyncService_ProcessSettingsBatch_SkipsCorruptBinary(t *testing.T) {
	svc := newTestGitSyncService(new(domainmocks.MockGitSyncRepository))
	wsPath := t.TempDir()
	conf := &domain.GitSyncConfig{ConfigSyncRules: []string{".obsidian"}}

	settings := []*domain.Setting{
		{Path: ".obsidian/bad.bin", Content: util.SettingContentB64Prefix + "!!!not-base64!!!"},
		{Path: ".obsidian/good.json", Content: `{"ok":true}`},
	}

	changed, err := svc.processSettingsBatch(settings, conf, wsPath)
	require.NoError(t, err)
	require.True(t, changed)

	_, statErr := os.Stat(filepath.Join(wsPath, ".obsidian", "bad.bin"))
	require.True(t, os.IsNotExist(statErr), "corrupt binary setting must be skipped")
	data, err := os.ReadFile(filepath.Join(wsPath, ".obsidian", "good.json"))
	require.NoError(t, err)
	require.Equal(t, `{"ok":true}`, string(data))
}
