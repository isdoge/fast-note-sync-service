package util

import "strings"

// VerifyVaultAccess checks if a target vault is allowed under the restricted allowedVaults list.
// VerifyVaultAccess 检查目标笔记库是否在允许访问的限制笔记库列表中。
// allowedVaults: comma-separated vaults allowed (empty means no restriction) // 允许访问的笔记库（逗号分隔，为空表示不限制）
// targetVault: target vault to check // 待检查的目标笔记库
// return: true if access is allowed, false otherwise // 允许访问返回 true，否则返回 false
//
// A comma or backslash inside a vault name can be escaped with a backslash
// (see JoinVaultAllowlist); existing unescaped comma-separated values keep
// their original meaning, so the stored token format is unchanged.
// 库名内的逗号或反斜杠可用反斜杠转义（见 JoinVaultAllowlist）；既有的未转义
// 逗号分隔值语义不变，令牌存储格式不变。
func VerifyVaultAccess(allowedVaults string, targetVault string) bool {
	if allowedVaults == "" {
		return true
	}
	if targetVault == "" {
		return false
	}
	for _, v := range splitVaultAllowlist(allowedVaults) {
		if strings.TrimSpace(v) == targetVault {
			return true
		}
	}
	return false
}

// JoinVaultAllowlist serializes a structured vault allowlist into the
// comma-separated format while escaping commas and backslashes inside a vault
// name. It is used for OAuth configuration values, which are a real list of
// complete vault names rather than the legacy token string.
// JoinVaultAllowlist 把结构化的库白名单序列化为逗号分隔格式，并转义库名内的
// 逗号与反斜杠。用于 OAuth 配置值：它是一组完整的库名，而不是存量的令牌串。
func JoinVaultAllowlist(vaults []string) string {
	escaped := make([]string, 0, len(vaults))
	for _, vault := range vaults {
		vault = strings.ReplaceAll(vault, `\`, `\\`)
		vault = strings.ReplaceAll(vault, ",", `\,`)
		escaped = append(escaped, vault)
	}
	return strings.Join(escaped, ",")
}

// splitVaultAllowlist splits on unescaped commas and unescapes "\\," and "\\\\".
// An unknown escape sequence is kept verbatim so legacy values containing a
// literal backslash still match.
// splitVaultAllowlist 按未转义的逗号拆分，并还原 "\\," 与 "\\\\"；未知转义
// 序列原样保留，含字面反斜杠的存量值仍可匹配。
func splitVaultAllowlist(value string) []string {
	result := make([]string, 0, strings.Count(value, ",")+1)
	var current strings.Builder
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '\\':
			if i+1 < len(value) && (value[i+1] == ',' || value[i+1] == '\\') {
				current.WriteByte(value[i+1])
				i++
				continue
			}
			current.WriteByte('\\')
		case ',':
			result = append(result, current.String())
			current.Reset()
		default:
			current.WriteByte(value[i])
		}
	}
	return append(result, current.String())
}

// SplitVaultAllowlist exposes the parsed entries of a stored vault allowlist,
// honoring the escaped-comma format written by JoinVaultAllowlist. It always
// returns a non-nil slice and drops empty entries.
// SplitVaultAllowlist 解析存储格式的库白名单，支持 JoinVaultAllowlist 写入的
// 转义逗号格式；始终返回非 nil 切片并丢弃空项。
func SplitVaultAllowlist(value string) []string {
	out := make([]string, 0)
	if strings.TrimSpace(value) == "" {
		return out
	}
	for _, item := range splitVaultAllowlist(value) {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
