package middleware

// #342 S-18：访问日志必须脱敏查询串中的 token/password 等敏感参数。
// 鉴权支持 ?token=（user_auth_token.go 的 ExtractUserAuthToken 读 c.Query("token")），
// sanitizeQuery 是最后一道防线。

import (
	"strings"
	"testing"
)

func TestSanitizeQuery_MasksSensitiveParams(t *testing.T) {
	// 修复前（无 sanitizeQuery）：查询串原样入日志。现状：值被替换为 ******（URL 编码后为 %2A...）。
	// 断言只要求「敏感值不再出现 + 含掩码」，编码形态不影响脱敏效果。
	cases := []struct {
		in          string
		sensitiveKV string // 原始敏感键值对，脱敏后不得出现
	}{
		{"token=abc", "token=abc"},
		{"x=1&token=abc&y=2", "token=abc"},
		{"password=p", "password=p"},
		{"access_token=t", "access_token=t"},
		{"share-token=s", "share-token=s"},
		{"TOKEN=abc", "TOKEN=abc"},
		{"", ""},
	}
	for _, tc := range cases {
		got := sanitizeQuery(tc.in)
		if got == "" && tc.in != "" {
			t.Errorf("sanitizeQuery(%q) 不应清空非空查询串：%q", tc.in, got)
		}
		if tc.in != "" {
			if strings.Contains(got, tc.sensitiveKV) {
				t.Errorf("sanitizeQuery(%q) 泄露敏感键值对：%q", tc.in, got)
			}
			if !strings.Contains(strings.ToLower(got), "token") && strings.Contains(strings.ToLower(tc.in), "token") {
				t.Errorf("sanitizeQuery(%q) 应保留参数名（已打码）：%q", tc.in, got)
			}
			if !strings.Contains(got, "*") && !strings.Contains(got, "%2A") {
				t.Errorf("sanitizeQuery(%q) 应包含掩码：%q", tc.in, got)
			}
		}
		// 非敏感参数保留
		if strings.Contains(tc.in, "x=1") && !strings.Contains(got, "x=1") {
			t.Errorf("sanitizeQuery(%q) 应保留非敏感参数 x=1：%q", tc.in, got)
		}
	}
}
