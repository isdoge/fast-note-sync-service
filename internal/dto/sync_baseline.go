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
