// Package dto Defines data transfer objects (request parameters and response structs)
// Package dto 定义数据传输对象（请求参数和响应结构体）
package dto

// EmptyContentHash is the content hash of an empty text body.
// It is a real, usable hash and must never be treated as "no baseline".
// EmptyContentHash 是空文本内容的哈希；它是有效哈希，绝不能被当成"没有基线"。
const EmptyContentHash = "0"

// NormalizeBaseline collapses the two wire encodings (JSON object fields and proto3 scalars)
// onto one meaning:
//   - baseHashMissing=true or baseHash="" means "no trusted content baseline";
//   - any non-empty baseHash — including "0" for empty text — is a real baseline;
//   - when the two disagree, missing wins (the caller must not run destructive operations).
//
// NormalizeBaseline 把 JSON 与 proto3 两种编码归一为同一语义：
//   - baseHashMissing=true 或 baseHash="" 表示没有可信内容基线；
//   - 任何非空 baseHash（含空文本的 "0"）都是有效基线；
//   - 两者矛盾时以 missing 为准，调用方不得执行破坏性操作。
func NormalizeBaseline(baseHash string, baseHashMissing bool) (string, bool) {
	if baseHashMissing || baseHash == "" {
		return "", true
	}
	return baseHash, false
}

// HasTrustedBase reports whether the normalized pair describes a real baseline.
// HasTrustedBase 判断规范化后的二元组是否表示存在可信基线。
func HasTrustedBase(baseHash string, baseHashMissing bool) bool {
	_, missing := NormalizeBaseline(baseHash, baseHashMissing)
	return !missing
}

// SyncConflictMessage is the unified baseline conflict payload shared by all four resource kinds.
// Only sent to connections that negotiated syncBaselineVersion >= 1.
// SyncConflictMessage 是四类资源共用的统一基线冲突载荷，仅发送给已协商 syncBaselineVersion >= 1 的连接。
type SyncConflictMessage struct {
	Kind           string `json:"kind" example:"note"`                                       // note | file | setting | folder // 资源类型
	Path           string `json:"path" example:"ReadMe.md"`                                  // Full path // 完整路径
	PathHash       string `json:"pathHash" example:"hash123"`                                // Existing path hash // 现有路径哈希
	Reason         string `json:"reason" example:"both_modified"`                            // both_modified | remote_deleted | local_deleted | no_baseline | baseline_unverified | server_missing | revision_changed // 冲突原因
	ServerHash     string `json:"serverHash" example:"chash456"`                             // Server active content hash; empty when not applicable // 服务端活跃内容哈希；不适用时为空
	ServerRevision string `json:"serverRevision" example:"0123456789abcdef0123456789abcdef"` // Current server state revision token // 当前服务端状态令牌
	ServerDeleted  bool   `json:"serverDeleted" example:"false"`                             // Current state is a tombstone // 当前是墓碑
	BaseHash       string `json:"baseHash" example:"bhash789"`                               // Content baseline used for this decision // 本次判断使用的内容基线
	BaseContent    string `json:"baseContent,omitempty" example:"# old"`                     // Only when a real base is proven // 能证明是真实 base 时才提供
	BaseMissing    bool   `json:"baseMissing" example:"false"`                               // Real base content unavailable // 真实 base 内容不可用
	ServerContent  string `json:"serverContent,omitempty" example:"# new"`                   // Remote content for notes and text settings; never inlines attachment bytes // 笔记、文本配置的远端内容；附件不内联二进制
}

// SyncConflict reasons accepted by the unified conflict payload.
// SyncConflict 统一冲突载荷允许的原因取值。
const (
	SyncConflictReasonBothModified       = "both_modified"
	SyncConflictReasonRemoteDeleted      = "remote_deleted"
	SyncConflictReasonLocalDeleted       = "local_deleted"
	SyncConflictReasonNoBaseline         = "no_baseline"
	SyncConflictReasonBaselineUnverified = "baseline_unverified"
	SyncConflictReasonServerMissing      = "server_missing"
	SyncConflictReasonRevisionChanged    = "revision_changed"
)

// SyncConflict kinds.
// SyncConflict 资源类型取值。
const (
	SyncConflictKindNote    = "note"
	SyncConflictKindFile    = "file"
	SyncConflictKindSetting = "setting"
	SyncConflictKindFolder  = "folder"
)

// BaselineCommit carries the conditional-commit contract of a baseline-mode write.
// It is attached to realtime write/delete requests by the WebSocket handlers only;
// HTTP, MCP and legacy connections leave it nil and keep the unconditional behavior.
// BaselineCommit 是基线模式写入的条件提交契约。仅由 WebSocket 处理器附加到实时写/删请求；
// HTTP、MCP 与旧连接保持 nil，维持无条件写入行为。
type BaselineCommit struct {
	// HasBase reports a trusted content baseline (BaseHash may be "0" for empty text).
	// HasBase 表示存在可信内容基线（BaseHash 可为空文本的 "0"）。
	HasBase bool
	// BaseHash is the trusted last-synced content hash.
	// BaseHash 是可信的上次同步内容哈希。
	BaseHash string
	// BaseRevision is the server revision token the client confirmed.
	// BaseRevision 是客户端确认过的服务端版本令牌。
	BaseRevision string
	// IsConflictResolved marks an explicit user choice; it never skips the revision check.
	// IsConflictResolved 标记用户已作出选择；它绝不跳过版本校验。
	IsConflictResolved bool
	// ServerMissingResolved marks a "keep local" choice for a server_missing conflict.
	// When set with an empty BaseRevision, creation is allowed only while the server row
	// is still absent.
	// ServerMissingResolved 标记 server_missing 冲突下"保留本地"的选择。与空 BaseRevision 同时
	// 出现时，仅当服务端仍不存在该行才允许创建。
	ServerMissingResolved bool
}

// BaselineRejectReason lists the commit-time rejection reasons of the unified payload.
// BaselineRejectReason 列出统一载荷中提交阶段可用的拒绝原因。
const (
	BaselineRejectRemoteDeleted = SyncConflictReasonRemoteDeleted
	BaselineRejectLocalDeleted  = SyncConflictReasonLocalDeleted
	BaselineRejectNoBaseline    = SyncConflictReasonNoBaseline
	BaselineRejectBothModified  = SyncConflictReasonBothModified
	BaselineRejectRevisionMoved = SyncConflictReasonRevisionChanged
	BaselineRejectServerMissing = SyncConflictReasonServerMissing
)

// BaselineConflictError is returned by a rejected conditional commit. Callers map it
// onto the unified SyncConflict payload (530 for realtime writes).
// BaselineConflictError 是条件提交被拒绝时返回的错误；调用方把它映射为统一 SyncConflict 载荷
// （实时写入回 530）。
type BaselineConflictError struct {
	Reason         string // One of the BaselineReject* constants // BaselineReject* 常量之一
	ServerActive   bool   // Server row is live at rejection time // 拒绝时服务端行为活跃
	ServerDeleted  bool   // Server row is a tombstone // 服务端行是墓碑
	ServerRevision string // Current server revision // 当前服务端版本令牌
	ServerHash     string // Current server content hash // 当前服务端内容哈希
}

// Error implements error.
// Error 实现 error 接口。
func (e *BaselineConflictError) Error() string {
	if e == nil {
		return ""
	}
	return "baseline conflict: " + e.Reason
}
