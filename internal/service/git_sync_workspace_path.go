package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// errWorkspacePathEscapes 表示数据库里的路径越出工作区或落入 .git。
var errWorkspacePathEscapes = errors.New("workspace path escapes or targets .git")

// safeWorkspacePath 把 DB 里的相对路径解析为工作区内的绝对路径，并校验：
//  1. 不是绝对路径；
//  2. Clean 后不以 ".." 越出工作区；
//  3. 任何一级路径段都不是 ".git"（防止改写工作区 Git 配置，service#342 S-8）；
//  4. 工作区内已存在的父目录经符号链接解析后仍在工作区内（远端仓库可能带符号链接）。
//
// 失败时返回 errWorkspacePathEscapes 包装的具体原因；调用方应跳过该条目并记警告，
// 不要让整次同步失败。
// safeWorkspacePath resolves rel against wsPath, rejecting absolute paths, ".." escapes,
// any ".git" segment, and symlinked parents that resolve outside the workspace.
func safeWorkspacePath(wsPath, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("%w: empty path", errWorkspacePathEscapes)
	}
	// 同时拒绝 Unix/Windows 风格绝对路径；DB 里可能混入任意来源的字符串。
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "\\") {
		return "", fmt.Errorf("%w: absolute path %q", errWorkspacePathEscapes, rel)
	}
	// Windows 盘符路径（C:foo / C:\foo）即使非绝对也拒绝，避免跨平台歧义。
	if len(rel) >= 2 && rel[1] == ':' {
		return "", fmt.Errorf("%w: drive-letter path %q", errWorkspacePathEscapes, rel)
	}

	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path escapes workspace %q", errWorkspacePathEscapes, rel)
	}

	// 任何一级为 .git 都拒绝，包括 a/.git/b 与 .git 本身。
	for _, seg := range strings.Split(clean, string(filepath.Separator)) {
		if seg == ".git" {
			return "", fmt.Errorf("%w: .git segment in %q", errWorkspacePathEscapes, rel)
		}
	}

	absWs, err := filepath.Abs(wsPath)
	if err != nil {
		return "", err
	}
	full := filepath.Join(absWs, clean)

	// 用 Rel 做最终边界确认，防御 Clean/Separator 边角。
	relToWs, err := filepath.Rel(absWs, full)
	if err != nil || relToWs == ".." || strings.HasPrefix(relToWs, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: resolved path escapes workspace %q", errWorkspacePathEscapes, rel)
	}

	// 校验父目录的符号链接解析结果仍在工作区内。文件本身可以不存在；
	// 父目录可能部分不存在，向上找最近已存在的祖先做 EvalSymlinks。
	if err := ensureSymlinkedParentInside(absWs, filepath.Dir(full)); err != nil {
		return "", err
	}
	return full, nil
}

// ensureSymlinkedParentInside 校验 dir 经符号链接解析后的真实路径仍位于 wsAbs 内。
// dir 可能尚不存在，此时向上找到最近已存在的祖先做解析。
func ensureSymlinkedParentInside(wsAbs, dir string) error {
	// macOS 上 /tmp 等系统目录本身是符号链接，EvalSymlinks 之后的路径前缀与 filepath.Abs
	// 结果可能不同。先把 wsAbs 也解析到真实路径再比较。
	wsReal, err := filepath.EvalSymlinks(wsAbs)
	if err != nil {
		return err
	}

	probe := dir
	for {
		_, err := os.Lstat(probe)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}

	resolved, err := filepath.EvalSymlinks(probe)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	rel, err := filepath.Rel(wsReal, resolved)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: symlinked parent %q resolves outside workspace", errWorkspacePathEscapes, dir)
	}
	return nil
}
