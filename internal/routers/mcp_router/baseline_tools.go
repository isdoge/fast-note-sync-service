package mcp_router

import (
	"errors"
	"strings"

	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/mark3labs/mcp-go/mcp"
)

// mcpBaseRevisionOption declares the optional base_revision argument. Write tools
// list it alongside base_hash and base_hash_missing; delete tools pair it with
// base_hash only. Omitting every baseline argument keeps the unconditional behavior.
// mcpBaseRevisionOption 声明可选的 base_revision 参数。写工具与 base_hash、base_hash_missing
// 一起列出，删除工具只与 base_hash 配对；全部省略时保持无条件行为。
func mcpBaseRevisionOption() mcp.ToolOption {
	return mcp.WithString("base_revision", mcp.Description("Optional. Server revision token you last saw for this item (returned as revision by reads/lists). Supplying it turns the operation into a guarded compare-and-set: if the server moved, the tool returns a structured conflict instead of applying the change."))
}

// mcpBaseHashOption declares the optional base_hash argument for write tools.
// mcpBaseHashOption 声明写工具可选的 base_hash 参数。
func mcpBaseHashOption() mcp.ToolOption {
	return mcp.WithString("base_hash", mcp.Description("Optional. Content hash baseline you last saw for this item (contentHash for files, content hash for notes). Only meaningful together with base_revision."))
}

// mcpBaseHashDeleteOption declares base_hash for delete tools, where a guarded
// delete requires both the revision token and the content hash.
// mcpBaseHashDeleteOption 声明删除工具的 base_hash 参数：条件删除必须同时提供版本令牌与内容哈希。
func mcpBaseHashDeleteOption() mcp.ToolOption {
	return mcp.WithString("base_hash", mcp.Description("Content hash you last saw for this item. A guarded delete requires both base_revision and base_hash; supplying only one, or a moved item, returns a structured conflict and deletes nothing."))
}

// mcpBaseHashMissingOption declares the optional base_hash_missing argument on
// write tools. It only relaxes the content check, never the revision check.
// mcpBaseHashMissingOption 声明写工具可选的 base_hash_missing 参数；它只放宽内容校验，
// 版本校验依然生效。
func mcpBaseHashMissingOption() mcp.ToolOption {
	return mcp.WithBoolean("base_hash_missing", mcp.Description("Optional, write tools only. Set true when you have no trusted content baseline but still hold base_revision; the write is then guarded against a moved revision only."))
}

// mcpBaselineFromArgs builds the conditional-commit contract from tool arguments.
// It returns nil when the caller supplied no baseline argument at all.
// mcpBaselineFromArgs 从工具参数构造条件提交契约；调用方未提供任何基线参数时返回 nil。
func mcpBaselineFromArgs(args map[string]interface{}) *dto.BaselineCommit {
	baseRevision := stringArg(args, "base_revision")
	baseHash := stringArg(args, "base_hash")
	baseMissing, _ := args["base_hash_missing"].(bool)
	return dto.BuildBaselineCommit(baseHash, baseMissing, baseRevision, false)
}

// stringArg reads a string tool argument, tolerating absent or non-string values.
// stringArg 读取字符串工具参数，缺失或类型不符时返回空串。
func stringArg(args map[string]interface{}, key string) string {
	v, _ := args[key].(string)
	return v
}

// mcpBaselineConflict reports whether err is a rejected conditional commit.
// mcpBaselineConflict 判断 err 是否为被拒绝的条件提交。
func mcpBaselineConflict(err error) (*dto.BaselineConflictError, bool) {
	var conflict *dto.BaselineConflictError
	if errors.As(err, &conflict) {
		return conflict, true
	}
	return nil, false
}

// mcpBaselineHint appends the conditional-write contract to a tool description so an
// agent knows the guarded mode exists without reading the source.
// mcpBaselineHint 把条件写契约追加到工具描述，使 Agent 无需读源码就知道存在受保护模式。
func mcpBaselineHint(desc string) string {
	return strings.TrimSpace(desc) + " Optional base_revision/base_hash/base_hash_missing enable a guarded compare-and-set write that returns a structured conflict instead of overwriting a moved item."
}

// mcpBaselineDeleteHint appends the conditional-delete contract to a delete tool
// description. Unlike writes, a guarded delete needs both the revision token and the
// content hash.
// mcpBaselineDeleteHint 把条件删除契约追加到删除工具描述：与写入不同，条件删除必须同时
// 提供版本令牌与内容哈希。
func mcpBaselineDeleteHint(desc string) string {
	return strings.TrimSpace(desc) + " Pass both base_revision and base_hash to turn the delete into a guarded compare-and-delete: supplying only one, or deleting a moved item, returns a structured conflict and deletes nothing."
}
