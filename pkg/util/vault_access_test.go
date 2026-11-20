package util

import "testing"

func TestVerifyVaultAccess_LegacyCommaSeparated(t *testing.T) {
	for _, tc := range []struct {
		name    string
		allowed string
		target  string
		want    bool
	}{
		{"empty allows all", "", "any", true},
		{"empty target denied when restricted", "a,b", "", false},
		{"first entry", "a,b", "a", true},
		{"second entry trimmed", "a, b", "b", true},
		{"missing", "a,b", "c", false},
		{"literal backslash preserved", `a\b,c`, `a\b`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := VerifyVaultAccess(tc.allowed, tc.target); got != tc.want {
				t.Fatalf("VerifyVaultAccess(%q, %q) = %v, want %v", tc.allowed, tc.target, got, tc.want)
			}
		})
	}
}

// OAuth 配置是一组完整库名：库名里的逗号不得被拆成两个库的授权。
func TestJoinVaultAllowlist_CommaInVaultNameStaysOneEntry(t *testing.T) {
	joined := JoinVaultAllowlist([]string{"项目,私密", "其他"})

	if !VerifyVaultAccess(joined, "项目,私密") {
		t.Fatalf("full vault name with comma must be allowed, joined=%q", joined)
	}
	if VerifyVaultAccess(joined, "项目") {
		t.Fatalf("prefix before comma must not be authorized, joined=%q", joined)
	}
	if VerifyVaultAccess(joined, "私密") {
		t.Fatalf("suffix after comma must not be authorized, joined=%q", joined)
	}
	if !VerifyVaultAccess(joined, "其他") {
		t.Fatalf("sibling entry must stay allowed, joined=%q", joined)
	}
}

func TestJoinVaultAllowlist_BackslashRoundTrip(t *testing.T) {
	joined := JoinVaultAllowlist([]string{`a\b`, `c,d`})
	if !VerifyVaultAccess(joined, `a\b`) {
		t.Fatalf("vault name with backslash must round-trip, joined=%q", joined)
	}
	if !VerifyVaultAccess(joined, `c,d`) {
		t.Fatalf("vault name with comma must round-trip, joined=%q", joined)
	}
	if VerifyVaultAccess(joined, "c") || VerifyVaultAccess(joined, "d") {
		t.Fatalf("escaped comma must not split the entry, joined=%q", joined)
	}
}

func TestJoinVaultAllowlist_Empty(t *testing.T) {
	if got := JoinVaultAllowlist(nil); got != "" {
		t.Fatalf("empty allowlist must stay empty, got %q", got)
	}
}
