package app

import (
	"testing"
)

func TestVerifyPermissions(t *testing.T) {
	tests := []struct {
		name     string
		scope    string
		protocol string
		client   string
		function string
		want     bool
	}{
		{"Empty scope", "", "rest", "web", "note_r", true},
		{"Exact match P", "p:rest", "rest", "web", "note_r", true},
		{"Mismatch P", "p:ws", "rest", "web", "note_r", false},
		{"Multiple P match", "p:rest,ws", "rest", "web", "note_r", true},
		{"Multiple P match second", "p:rest,ws", "ws", "web", "note_r", true},
		{"Wildcard P", "p:*", "rest", "web", "note_r", true},

		{"Exact match C", "c:webgui", "rest", "webgui", "note_r", true},
		{"Mismatch C", "c:webgui", "rest", "obsidian", "note_r", false},
		{"Wildcard C", "c:*", "rest", "obsidian", "note_r", true},
		{"C with wildcard suffix", "c:obsidian*", "rest", "obsidian-mobile", "note_r", true},

		{"Exact match F", "f:note_r", "rest", "web", "note_r", true},
		{"Mismatch F", "f:note_r", "rest", "web", "note_w", false},
		{"Multiple F match first", "f:note_r,note_w", "rest", "web", "note_r", true},
		{"Multiple F match second", "f:note_r,note_w", "rest", "web", "note_w", true},
		{"Wildcard F", "f:*", "rest", "web", "note_r", true},

		{"Complex match", "p:rest,ws c:obsidian* f:note_r,note_w", "ws", "obsidian-desktop", "note_w", true},
		{"Complex mismatch P", "p:rest c:obsidian* f:note_r,note_w", "ws", "obsidian-desktop", "note_w", false},
		{"Complex mismatch C", "p:rest,ws c:webgui f:note_r,note_w", "ws", "obsidian-desktop", "note_w", false},
		{"Complex mismatch F", "p:rest,ws c:obsidian* f:note_r", "ws", "obsidian-desktop", "note_w", false},
		
		{"MCP Protocol Match", "p:mcp", "mcp", "cursor", "", true},
		{"MCP Protocol Mismatch", "p:rest", "mcp", "cursor", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VerifyPermissions(tt.scope, tt.protocol, tt.client, tt.function); got != tt.want {
				t.Errorf("VerifyPermissions() = %v, want %v for scope %s, p %s, c %s, f %s", got, tt.want, tt.scope, tt.protocol, tt.client, tt.function)
			}
		})
	}
}


// strict-empty-protocol：整串空 scope 与空 "p:" 在严格模式下拒绝，默认模式保持旧行为。
func TestVerifyPermissionsWithStrictEmptyProtocol(t *testing.T) {
	tests := []struct {
		name   string
		scope  string
		strict bool
		want   bool
	}{
		{"empty scope legacy allows", "", false, true},
		{"empty scope strict denies", "", true, false},
		{"empty p legacy allows", "c:web f:note_r", false, true},
		{"empty p strict denies", "c:web f:note_r", true, false},
		{"empty p value legacy allows", "p: c:* f:*", false, true},
		{"empty p value strict denies", "p: c:* f:*", true, false},
		{"normal scope unaffected strict", "p:rest c:* f:*", true, true},
		{"normal scope mismatch strict", "p:ws c:* f:*", true, false},
		{"wildcard unaffected strict", "p:* c:* f:*", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VerifyPermissionsWithStrictEmptyProtocol(tt.scope, "rest", "web", "note_r", tt.strict); got != tt.want {
				t.Errorf("VerifyPermissionsWithStrictEmptyProtocol() = %v, want %v for scope %q strict=%v", got, tt.want, tt.scope, tt.strict)
			}
		})
	}
}
