package mcp_router

import (
	"encoding/json"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// decodeConflictText parses the conflict body from the result's text content.
// decodeConflictText 从结果文本内容中解析冲突体。
func decodeConflictText(t *testing.T, result *mcp.CallToolResult) mcpConditionalConflictOutput {
	t.Helper()
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok, "首段内容应为文本")
	var out mcpConditionalConflictOutput
	require.NoError(t, json.Unmarshal([]byte(text.Text), &out), "text: %s", text.Text)
	return out
}

// 条件写冲突结果：IsError=true、不带结构化内容，冲突信息在文本 JSON 里。
// Conflict result is an error with no structured content; the body lives in text.
func TestMCPConflictResult_TextOnlyWithVault(t *testing.T) {
	result := mcpConflictResult("vault-a", &dto.BaselineConflictError{
		Reason:         dto.SyncConflictReasonBothModified,
		ServerRevision: "rev-server",
		ServerHash:     "h-server",
	}, dto.SyncConflictKindNote, "a.md", "path-hash", "base-hash")

	require.NotNil(t, result)
	assert.True(t, result.IsError)
	assert.Nil(t, result.StructuredContent, "冲突结果不应填结构化内容")

	out := decodeConflictText(t, result)
	assert.Equal(t, "vault-a", out.Vault)
	require.NotNil(t, out.Conflict)
	assert.True(t, out.Conflict.Conflict)
	assert.Equal(t, dto.SyncConflictReasonBothModified, out.Conflict.Reason)
	assert.Equal(t, "rev-server", out.Conflict.ServerRevision)
	assert.Equal(t, "h-server", out.Conflict.ServerHash)
	assert.Equal(t, dto.SyncConflictKindNote, out.Conflict.Kind)
	assert.Equal(t, "a.md", out.Conflict.Path)
	assert.Equal(t, "path-hash", out.Conflict.PathHash)
	assert.Equal(t, "base-hash", out.Conflict.BaseHash)
}

// conflict 传 nil 时按 revision_changed 处理。
// A nil conflict defaults to revision_changed.
func TestMCPConflictResult_NilConflictDefaultsRevisionChanged(t *testing.T) {
	result := mcpConflictResult("vault-b", nil, dto.SyncConflictKindFile, "a.png", "", "")

	require.NotNil(t, result)
	assert.True(t, result.IsError)
	assert.Nil(t, result.StructuredContent)

	out := decodeConflictText(t, result)
	assert.Equal(t, "vault-b", out.Vault)
	require.NotNil(t, out.Conflict)
	assert.Equal(t, dto.SyncConflictReasonRevisionChanged, out.Conflict.Reason)
}
