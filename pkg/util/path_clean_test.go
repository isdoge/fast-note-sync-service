package util

import "testing"

// #342 S-8 / haierkeys/obsidian-fast-note-sync#244：库相对路径清洗辅助。
// CleanVaultPath 去前导 "/"，按路径段（"/" 与 "\" 都算分隔符）拒绝整段 ".."；
// 不做 URL 解码、不做 ".." 子串匹配，因此文件名里带 ..、%、+、省略号的正常
// 笔记名都合法。
func TestCleanVaultPath(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		// 合法：正常笔记名
		{"leading slash", "/notes/todo.md", "notes/todo.md", true},
		{"multiple leading slashes", "//notes/todo.md", "notes/todo.md", true},
		{"plain relative", "notes/todo.md", "notes/todo.md", true},
		{"dot-dot inside filename", "a..b.md", "a..b.md", true},
		{"ellipsis in filename", "等等...md", "等等...md", true},
		{"to be continued", "To be continued...md", "To be continued...md", true},
		{"percent sign", "100%.md", "100%.md", true},
		{"percent in middle", "50% done.md", "50% done.md", true},
		{"plus sign", "a+b.md", "a+b.md", true},
		{"current-dir segment", "./a.md", "./a.md", true},
		{"single dot", ".", ".", true},
		{"trailing dots", "a...", "a...", true},
		{"backslash normalized", `notes\sub\todo.md`, "notes/sub/todo.md", true},

		{"repeated backslashes", `a\\b`, "a/b", true},
		{"mixed separators", `a\/b`, "a/b", true},
		{"edge separators", `\a\b\`, "a/b", true},
		{"only backslashes", `\\`, "", false},
		{"normalized traversal", `a\/..\b`, "", false},

		// Windows 习惯输入（#451）：冒号与问号在库相对路径里只是普通文件名字符
		// （非 Windows 服务端上不解析盘符），盘符前缀会保留为字面段；UNC 前导
		// 分隔符按现有规则折叠掉。整段 ".." 仍被拒，不因盘符前缀放宽。
		{"drive-letter kept literal", `C:\Users\note.md`, "C:/Users/note.md", true},
		{"bare drive-letter kept", `C:note.md`, "C:note.md", true},
		{"unc prefix collapsed", `\\server\share\note.md`, "server/share/note.md", true},
		{"unc dos-device kept literal", `\\?\C:\note.md`, "?/C:/note.md", true},
		{"drive-letter traversal rejected", `C:\..`, "", false},

		// 非法：空 / 整段 ".." 越界 / NUL
		{"empty", "", "", false},
		{"only slashes", "/", "", false},
		{"double dot segment", "..", "", false},
		{"traversal", "../etc/passwd", "", false},
		{"inner traversal", "notes/../../etc", "", false},
		{"backslash traversal", `a\..\x`, "", false},
		{"mixed separator traversal", `a/..\..\x`, "", false},
		{"nul byte", "notes\x00.md", "", false},
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
