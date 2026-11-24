package util

import "testing"

// IsSystemJunkPath 与 Obsidian 插件 isSystemJunkPath 的规则保持一致：
// 按 "/" 与 "\" 切段，整段命中 .DS_Store / Thumbs.db / desktop.ini（大小写不敏感），
// 或任一段以 "._" 开头（macOS AppleDouble）。只含这些字样的正常文件名不命中。
func TestIsSystemJunkPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		// 三类整段文件名
		{"ds_store", ".DS_Store", true},
		{"ds_store lowercase", ".ds_store", true},
		{"ds_store upper", ".DS_STORE", true},
		{"thumbs db", "Thumbs.db", true},
		{"thumbs db lowercase", "thumbs.db", true},
		{"desktop ini", "desktop.ini", true},
		{"desktop ini upper", "Desktop.INI", true},

		// AppleDouble 前缀
		{"appledouble file", "._note.md", true},
		{"appledouble bare", "._", true},
		{"appledouble nested", "notes/._hidden.md", true},

		// 嵌套目录与混合分隔符
		{"nested ds_store", "notes/sub/.DS_Store", true},
		{"nested thumbs", "a/b/c/Thumbs.db", true},
		{"backslash nested", `notes\sub\.DS_Store`, true},
		{"backslash appledouble", `notes\._note.md`, true},
		{"mixed separators", `notes/sub\.DS_Store`, true},
		{"leading slash", "/notes/.DS_Store", true},
		{"trailing slash dir", "notes/Thumbs.db/", true},

		// 只在文件名里包含关键字，不是整段
		{"contains appledouble", "my._notes.md", false},
		{"contains appledouble middle", "foo._.bar.md", false},
		{"contains ds_store", "my.DS_Store.md", false},
		{"contains thumbs", "Thumbs.db.backup", false},
		{"contains desktop ini", "desktop.ini.txt", false},

		// 普通路径
		{"plain note", "notes/todo.md", false},
		{"plain attachment", "img/photo.png", false},
		{"hidden obsidian dir", ".obsidian/app.json", false},
		{"dot file", ".gitignore", false},
		{"backslash normal", `notes\sub\todo.md`, false},
		{"empty", "", false},
		{"only separators", "///", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSystemJunkPath(tt.in); got != tt.want {
				t.Errorf("IsSystemJunkPath(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
