package mcp_router

import (
	"encoding/json"

	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/mark3labs/mcp-go/mcp"
)

type mcpNoteListOutput struct {
	Vault      string                     `json:"vault"`
	Count      int                        `json:"count"`
	Page       int                        `json:"page"`
	PageSize   int                        `json:"pageSize"`
	TotalCount int                        `json:"totalCount"`
	HasMore    bool                       `json:"hasMore"`
	Notes      []*dto.McpNoteNoContentDTO `json:"notes"`
}

type mcpNoteOutput struct {
	Vault string          `json:"vault"`
	Note  *dto.McpNoteDTO `json:"note"`
}

type mcpNoteMutationOutput struct {
	Vault     string          `json:"vault"`
	Operation string          `json:"operation"`
	Note      *dto.McpNoteDTO `json:"note,omitempty"`
	OldNote   *dto.McpNoteDTO `json:"oldNote,omitempty"`
	NewNote   *dto.McpNoteDTO `json:"newNote,omitempty"`
}

type mcpNoteRecycleClearOutput struct {
	Vault string `json:"vault"`
	Path  string `json:"path,omitempty"`
}

type mcpNoteReplaceOutput struct {
	Vault      string          `json:"vault"`
	MatchCount int             `json:"matchCount"`
	Note       *dto.McpNoteDTO `json:"note"`
}

type mcpNoteLinksOutput struct {
	Vault string              `json:"vault"`
	Path  string              `json:"path"`
	Count int                 `json:"count"`
	Links []*dto.NoteLinkItem `json:"links"`
}

type mcpFileListOutput struct {
	Vault      string            `json:"vault"`
	Count      int               `json:"count"`
	Page       int               `json:"page"`
	PageSize   int               `json:"pageSize"`
	TotalCount int               `json:"totalCount"`
	HasMore    bool              `json:"hasMore"`
	Files      []*dto.McpFileDTO `json:"files"`
}

type mcpFileOutput struct {
	Vault string          `json:"vault"`
	File  *dto.McpFileDTO `json:"file"`
}

type mcpFileReadOutput struct {
	Vault         string `json:"vault"`
	Path          string `json:"path"`
	ContentBase64 string `json:"contentBase64"`
	Size          int    `json:"size"`
}

type mcpFileMutationOutput struct {
	Vault     string          `json:"vault"`
	Operation string          `json:"operation"`
	File      *dto.McpFileDTO `json:"file,omitempty"`
	OldFile   *dto.McpFileDTO `json:"oldFile,omitempty"`
	NewFile   *dto.McpFileDTO `json:"newFile,omitempty"`
}

type mcpFileRecycleClearOutput struct {
	Vault string `json:"vault"`
	Path  string `json:"path,omitempty"`
}

type mcpFileWriteOutput struct {
	Vault string          `json:"vault"`
	File  *dto.McpFileDTO `json:"file"`
}

// mcpConditionalConflictOutput carries the structured conflict body of a rejected
// conditional write. It is returned as an error result (IsError=true) so clients see
// a failure, while the structured content still names the reason and the server state.
// mcpConditionalConflictOutput 承载条件写被拒时的结构化冲突体。它作为错误结果
// （IsError=true）返回，客户端因此看到失败，但结构化内容仍给出原因与服务端状态。
type mcpConditionalConflictOutput struct {
	Vault    string                      `json:"vault"`
	Conflict *dto.McpBaselineConflictDTO `json:"conflict"`
}

// mcpConflictResult builds the error tool result of a rejected conditional write:
// IsError=true with the structured conflict body, so a client sees a failure while
// an agent can still read conflict/reason/serverRevision and retry deliberately.
// mcpConflictResult 构造条件写被拒的错误结果：IsError=true 且带结构化冲突体，
// 客户端看到失败，Agent 仍能读到 conflict/reason/serverRevision 并有意识地重试。
func mcpConflictResult(vault string, conflict *dto.BaselineConflictError, kind, path, pathHash, baseHash string) *mcp.CallToolResult {
	if conflict == nil {
		conflict = &dto.BaselineConflictError{Reason: dto.SyncConflictReasonRevisionChanged}
	}
	body := &dto.McpBaselineConflictDTO{
		Conflict:       true,
		Kind:           kind,
		Path:           path,
		PathHash:       pathHash,
		Reason:         conflict.Reason,
		ServerRevision: conflict.ServerRevision,
		ServerHash:     conflict.ServerHash,
		ServerDeleted:  conflict.ServerDeleted,
		BaseHash:       baseHash,
	}
	text, err := json.Marshal(body)
	if err != nil {
		text = []byte(`{"conflict":true}`)
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: string(text)}},
		StructuredContent: mcpConditionalConflictOutput{
			Vault:    vault,
			Conflict: body,
		},
	}
}

type mcpFolderMutationOutput struct {
	Vault     string         `json:"vault"`
	Operation string         `json:"operation"`
	Folder    *dto.FolderDTO `json:"folder,omitempty"`
}

type mcpVaultListOutput struct {
	Count  int             `json:"count"`
	Vaults []*dto.VaultDTO `json:"vaults"`
}

type mcpVaultOutput struct {
	Vault *dto.VaultDTO `json:"vault"`
}

type mcpVaultMutationOutput struct {
	Operation string        `json:"operation"`
	Vault     *dto.VaultDTO `json:"vault,omitempty"`
	ID        int64         `json:"id,omitempty"`
}
