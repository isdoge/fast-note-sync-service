package util

import "testing"

// #342 S-8 / haierkeys/obsidian-fast-note-sync#244：MCP 写入工具的路径清洗辅助。
// CleanVaultPath 必须去掉前导 "/"，并拒绝空路径、绝对路径、".." 与 NUL；
// 反斜杠不转换（是否当分隔符是下一版要定的事）。
func TestCleanVaultPath(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		wantOK bool
	}{
		{"leading slash", "/notes/todo.md", "notes/todo.md", true},
		{"multiple leading slashes", "//notes/todo.md", "notes/todo.md", true},
		{"plain relative", "notes/todo.md", "notes/todo.md", true},
		{"empty", "", "", false},
		{"only slashes", "/", "", false},
		{"traversal", "../etc/passwd", "", false},
		{"inner traversal", "notes/../../etc", "", false},
		{"nul byte", "notes\x00.md", "", false},
		{"backslash kept", `notes\sub\todo.md`, `notes\sub\todo.md`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := CleanVaultPath(tt.in)
			if ok != tt.wantOK {
				t.Errorf("CleanVaultPath(%q) ok = %v, want %v", tt.in, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("CleanVaultPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// EnsureMDExt 大小写不敏感补 .md 后缀
func TestEnsureMDExt(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"notes/todo", "notes/todo.md"},
		{"notes/todo.md", "notes/todo.md"},
		{"notes/TODO.MD", "notes/TODO.MD"},
		{"notes/todo.Md", "notes/todo.Md"},
		{"notes/README.markdown", "notes/README.markdown.md"},
	}
	for _, tt := range tests {
		if got := EnsureMDExt(tt.in); got != tt.want {
			t.Errorf("EnsureMDExt(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
