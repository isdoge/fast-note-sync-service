package service

// 本文件集中存放“把 Vault 子集导出到静态博客仓库子目录”的通用能力：
// 路径重映射、Frontmatter 过滤与 Astro 预检、引用附件汇聚，以及受保护路径黑名单。
// 设计文档：.agents/tasks/evidence/2026-11-20-design-firefly-blog.md 第 3.1 节与第 4 节。
//
// 全部能力默认关闭：SourceDir/TargetSubdir 为空、两个开关为 false 时，
// 行为与 3.8.5 之前的整库镜像完全一致。

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// fireflyProtectedExact 是仓库根下绝不写入/删除的固定文件（NAS 设置中心与依赖锁文件）。
// 命中即跳过：这些文件一旦被 Vault 内容覆盖，NAS 的 settings-sync 会直接拒绝构建。
var fireflyProtectedExact = []string{
	"src/config/settings-overrides.json",
	"package.json",
	"pnpm-lock.yaml",
	"pnpm-workspace.yaml",
	".npmrc",
	"astro.config.mjs",
	"tsconfig.json",
}

// fireflyProtectedPrefixes 是同样受保护的目录前缀（含自身）。
var fireflyProtectedPrefixes = []string{
	"public/uploads/settings",
	".settings-backups",
	".settings-site",
}

// isProtectedFireflyPath 判断仓库相对路径是否命中硬编码保护黑名单。
// 任何导出写入或删除命中都必须直接跳过（不变量 2 的第一层）。
// isProtectedFireflyPath reports whether a repo-relative path hits the hard-coded
// protection list that must never be written or deleted by the export layer.
func isProtectedFireflyPath(relToRepoRoot string) bool {
	p := normalizeSlashPath(relToRepoRoot)
	if p == "" {
		return false
	}
	for _, exact := range fireflyProtectedExact {
		if strings.EqualFold(p, exact) {
			return true
		}
	}
	for _, prefix := range fireflyProtectedPrefixes {
		if strings.EqualFold(p, prefix) || strings.HasPrefix(strings.ToLower(p), strings.ToLower(prefix)+"/") {
			return true
		}
	}
	return false
}

// normalizeSlashPath 统一为“/”分隔、去掉首尾“/”和“./”前缀的仓库相对路径。
func normalizeSlashPath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	for strings.HasPrefix(p, "./") {
		p = strings.TrimPrefix(p, "./")
	}
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	cleaned := path.Clean(p)
	if cleaned == "." {
		return ""
	}
	return cleaned
}

// normalizeRelativeSubdir 清洗并校验 SourceDir / TargetSubdir：
// 统一为“/”分隔、去首尾“/”；拒绝绝对路径、盘符路径、“..”段与“.git”段。
// 空输入返回空串（表示未配置）。
// normalizeRelativeSubdir cleans and validates a configured subdirectory: slashes are
// normalized, leading/trailing slashes removed, and absolute paths, drive letters, ".."
// and ".git" segments are rejected. Empty input stays empty (feature disabled).
func normalizeRelativeSubdir(sub string) (string, error) {
	s := strings.TrimSpace(sub)
	if s == "" {
		return "", nil
	}
	s = strings.ReplaceAll(s, "\\", "/")
	// 去首尾 “/”（设计稿要求“转 /、去首尾 /”）；清洗后仍含盘符的按非法处理。
	// Strip leading/trailing slashes; a drive letter after cleanup is still invalid.
	if len(s) >= 2 && s[1] == ':' {
		return "", fmt.Errorf("subdirectory must not be a drive path: %q", sub)
	}
	segments := strings.Split(s, "/")
	out := make([]string, 0, len(segments))
	for _, seg := range segments {
		if seg == "" || seg == "." {
			continue
		}
		if seg == ".." {
			return "", fmt.Errorf("subdirectory must not contain '..': %q", sub)
		}
		if strings.EqualFold(strings.TrimRight(seg, ". "), ".git") {
			return "", fmt.Errorf("subdirectory must not contain '.git': %q", sub)
		}
		out = append(out, seg)
	}
	return strings.Join(out, "/"), nil
}

// mapSourceToTargetPath 把 Vault 内的条目路径映射到 Git 仓库路径：
//   - 配置了 sourceDir 时，条目必须位于该目录下，并剥离前缀得到 relInTarget；
//   - 再拼到 targetSubdir 之下得到 relToRepo（仓库根相对路径）。
//
// 返回 matched=false 表示该条目不在本次导出范围内。
// mapSourceToTargetPath maps a vault item path to the repo path inside targetSubdir,
// stripping the optional sourceDir prefix. matched=false means "not exported".
func mapSourceToTargetPath(itemPath, sourceDir, targetSubdir string) (relToRepo string, relInTarget string, matched bool) {
	item := normalizeSlashPath(itemPath)
	if item == "" {
		return "", "", false
	}
	src := normalizeSlashPath(sourceDir)
	if src != "" {
		switch {
		case item == src:
			relInTarget = ""
		case strings.HasPrefix(item, src+"/"):
			relInTarget = item[len(src)+1:]
		default:
			return "", "", false
		}
	} else {
		relInTarget = item
	}
	if relInTarget == "" {
		return "", "", false
	}
	tgt := normalizeSlashPath(targetSubdir)
	if tgt == "" {
		relToRepo = relInTarget
	} else {
		relToRepo = tgt + "/" + relInTarget
	}
	return relToRepo, relInTarget, true
}

// splitFrontmatter 把 Markdown 拆成 frontmatter 原文块（含两侧分隔行）、正文与解析结果。
// 同时兼容 \r\n 与 \n，并允许文件以 BOM 开头。ok=false 表示没有合法的 YAML 头。
func splitFrontmatter(content string) (fmText string, body string, fm map[string]any, ok bool) {
	s := strings.TrimPrefix(content, "\ufeff")
	i := strings.IndexByte(s, '\n')
	if i < 0 {
		return "", content, nil, false
	}
	first := strings.TrimRight(s[:i], "\r")
	if strings.TrimRight(first, " \t") != "---" {
		return "", content, nil, false
	}

	pos := i + 1
	yamlStart := pos
	for pos <= len(s) {
		j := strings.IndexByte(s[pos:], '\n')
		var line string
		next := len(s)
		if j >= 0 {
			line = s[pos : pos+j]
			next = pos + j + 1
		} else {
			line = s[pos:]
		}
		trimmed := strings.TrimRight(strings.TrimRight(line, "\r"), " \t")
		if trimmed == "---" || trimmed == "..." {
			raw := s[yamlStart:pos]
			parsed := map[string]any{}
			if err := yaml.Unmarshal([]byte(raw), &parsed); err != nil {
				return "", content, nil, false
			}
			if parsed == nil {
				parsed = map[string]any{}
			}
			return s[:next], s[next:], parsed, true
		}
		if j < 0 {
			break
		}
		pos = next
	}
	return "", content, nil, false
}

// parseFrontmatter 是 splitFrontmatter 的简化封装。
func parseFrontmatter(content string) (map[string]any, string, bool) {
	_, body, fm, ok := splitFrontmatter(content)
	return fm, body, ok
}

// matchFrontmatterFilter 判断笔记是否满足 Frontmatter 过滤表达式。
// 表达式以逗号分隔多个 AND 条件；条件为 key=value（布尔/字符串/数字均可）或单键存在（key）。
// 空表达式视为全部匹配。
// matchFrontmatterFilter evaluates a comma-separated AND filter over the YAML frontmatter,
// e.g. "type=share,blog=true" or a bare key "blog". An empty expression matches everything.
func matchFrontmatterFilter(content, expr string) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true
	}
	fm, _, ok := parseFrontmatter(content)
	if !ok {
		return false
	}
	for _, raw := range strings.Split(expr, ",") {
		term := strings.TrimSpace(raw)
		if term == "" {
			continue
		}
		key, want, hasValue := strings.Cut(term, "=")
		key = strings.TrimSpace(key)
		if key == "" {
			return false
		}
		value, present := fm[key]
		if !present || value == nil {
			return false
		}
		if !hasValue {
			// 单键存在性检查：显式的 false / 空串视为“没有标记”
			switch v := value.(type) {
			case bool:
				if !v {
					return false
				}
			case string:
				if strings.TrimSpace(v) == "" {
					return false
				}
			}
			continue
		}
		if !frontmatterValueEquals(value, strings.TrimSpace(want)) {
			return false
		}
	}
	return true
}

// frontmatterValueEquals 比较 YAML 值与过滤条件里的字符串形式（不区分大小写，布尔接受 true/1）。
func frontmatterValueEquals(value any, want string) bool {
	switch v := value.(type) {
	case bool:
		w := strings.ToLower(want)
		return (v && (w == "true" || w == "1")) || (!v && (w == "false" || w == "0"))
	case string:
		return strings.EqualFold(strings.TrimSpace(v), want)
	case time.Time:
		return strings.EqualFold(v.Format("2006-01-02"), want)
	default:
		return strings.EqualFold(strings.TrimSpace(fmt.Sprint(v)), want)
	}
}

// validateAstroPostFrontmatter 预检一篇准备导出的文章，确保不会弄挂 Astro 构建：
// title 非空、published/updated 为合法日期、本地相对图片文件全部存在。
// validateAstroPostFrontmatter checks one post about to be exported: non-empty title,
// valid published/updated dates, and every local relative image existing in the target dir.
func validateAstroPostFrontmatter(notePath, content, targetNoteDir string, fileExists func(repoRelPath string) bool) error {
	fm, body, ok := parseFrontmatter(content)
	if !ok {
		return fmt.Errorf("missing YAML frontmatter")
	}
	if err := validateAstroPostMeta(fm); err != nil {
		return err
	}
	return validateAstroPostImages(fm, body, targetNoteDir, fileExists)
}

// validateAstroPostMeta 只校验 Frontmatter 必填项（title / published / updated）。
func validateAstroPostMeta(fm map[string]any) error {
	title, _ := fm["title"].(string)
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("missing or empty title")
	}

	published, present := fm["published"]
	if !present || published == nil {
		return fmt.Errorf("missing published date")
	}
	if _, ok := parseFrontmatterDate(published); !ok {
		return fmt.Errorf("invalid published date %q (expect YYYY-MM-DD or RFC3339)", fmt.Sprint(published))
	}

	if updated, present := fm["updated"]; present && updated != nil {
		if s, isString := updated.(string); isString && strings.TrimSpace(s) == "" {
			// 空串等同于未填写
			return nil
		}
		if _, ok := parseFrontmatterDate(updated); !ok {
			return fmt.Errorf("invalid updated date %q (expect YYYY-MM-DD or RFC3339)", fmt.Sprint(updated))
		}
	}
	return nil
}

// parseFrontmatterDate 接受 YAML 自动解析出的 time.Time、YYYY-MM-DD 或 RFC3339 字符串。
// 占位符（如 YYYY-MM-DD）与空串一律判为非法。
func parseFrontmatterDate(value any) (time.Time, bool) {
	switch v := value.(type) {
	case time.Time:
		return v, true
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return time.Time{}, false
		}
		if d, err := time.Parse("2006-01-02", s); err == nil {
			return d, true
		}
		if d, err := time.Parse(time.RFC3339, s); err == nil {
			return d, true
		}
		return time.Time{}, false
	default:
		return time.Time{}, false
	}
}

// validateAstroPostImages 校验 frontmatter.image 与正文（剥离代码块后）中的本地相对图片都存在。
func validateAstroPostImages(fm map[string]any, body, targetNoteDir string, fileExists func(repoRelPath string) bool) error {
	check := func(src, where string) error {
		src = strings.TrimSpace(src)
		if src == "" || isExternalImageRef(src) {
			return nil
		}
		rel := path.Clean(path.Join(targetNoteDir, normalizeSlashPath(src)))
		if !fileExists(rel) {
			return fmt.Errorf("missing image file: %s (%s)", src, where)
		}
		return nil
	}

	if image, ok := fm["image"].(string); ok {
		if err := check(image, "frontmatter.image"); err != nil {
			return err
		}
	}

	var firstErr error
	transformMarkdownOutsideCode(body, func(text string) string {
		if firstErr != nil {
			return text
		}
		for _, m := range markdownImageRe.FindAllStringSubmatch(text, -1) {
			if err := check(m[2], "body image"); err != nil {
				firstErr = err
				return text
			}
		}
		return text
	})
	return firstErr
}

// isExternalImageRef 判断图片引用是否无需本地文件校验（外链、站点根路径、data URI、API 占位等）。
func isExternalImageRef(src string) bool {
	s := strings.TrimSpace(src)
	s = strings.Trim(s, "<>\"'")
	if s == "" {
		return true
	}
	lower := strings.ToLower(s)
	if lower == "api" || strings.HasPrefix(lower, "#") {
		return true
	}
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "\\") {
		return true
	}
	for _, scheme := range []string{"http://", "https://", "data:", "mailto:", "obsidian://", "ftp://"} {
		if strings.HasPrefix(lower, scheme) {
			return true
		}
	}
	// 含协议样式的其它引用（如 //cdn.example.com/a.png）也按外链处理
	if strings.HasPrefix(s, "//") {
		return true
	}
	return false
}

var (
	// wikiEmbedRe 匹配 Obsidian 图片嵌入 ![[img.png]] / ![[img.png|说明]]
	wikiEmbedRe = regexp.MustCompile(`!\[\[([^\]\n]+)\]\]`)
	// markdownImageRe 匹配标准 Markdown 图片 ![alt](src) 与 ![alt](src "title")
	markdownImageRe = regexp.MustCompile(`!\[([^\]\n]*)\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)
)

// transformMarkdownOutsideCode 对 Markdown 中“代码块与行内代码之外”的文本应用 fn，
// 代码块（``` / ~~~）与行内代码原样保留。
// transformMarkdownOutsideCode applies fn to Markdown text outside fenced and inline code.
func transformMarkdownOutsideCode(content string, fn func(string) string) string {
	var b strings.Builder
	pos := 0
	inFence := false
	fenceMarker := ""
	for pos < len(content) {
		nl := strings.IndexByte(content[pos:], '\n')
		var line string
		next := len(content)
		if nl >= 0 {
			line = content[pos : pos+nl]
			next = pos + nl + 1
		} else {
			line = content[pos:]
		}

		trimmed := strings.TrimLeft(line, " \t")
		marker := ""
		switch {
		case strings.HasPrefix(trimmed, "```"):
			marker = "```"
		case strings.HasPrefix(trimmed, "~~~"):
			marker = "~~~"
		}

		if inFence {
			b.WriteString(line)
			if marker == fenceMarker {
				inFence = false
			}
		} else if marker != "" {
			inFence = true
			fenceMarker = marker
			b.WriteString(line)
		} else {
			b.WriteString(transformOutsideInlineCode(line, fn))
		}

		if nl >= 0 {
			b.WriteByte('\n')
		}
		pos = next
	}
	return b.String()
}

// transformOutsideInlineCode 对一行文本中行内代码（成对的等长反引号串）之外的部分应用 fn。
func transformOutsideInlineCode(line string, fn func(string) string) string {
	var b strings.Builder
	i := 0
	for i < len(line) {
		if line[i] != '`' {
			start := i
			for i < len(line) && line[i] != '`' {
				i++
			}
			b.WriteString(fn(line[start:i]))
			continue
		}
		// 统计反引号串长度，找等长的闭合串
		j := i
		for j < len(line) && line[j] == '`' {
			j++
		}
		width := j - i
		closeAt := -1
		for k := j; k < len(line); {
			if line[k] != '`' {
				k++
				continue
			}
			m := k
			for m < len(line) && line[m] == '`' {
				m++
			}
			if m-k == width {
				closeAt = m
				break
			}
			k = m
		}
		if closeAt < 0 {
			// 未闭合：其余内容原样保留
			b.WriteString(line[i:])
			return b.String()
		}
		b.WriteString(line[i:closeAt])
		i = closeAt
	}
	return b.String()
}

// fireflySandbox 返回本次导出允许写入的物理根目录，以及规范化后的 TargetSubdir。
// TargetSubdir 为空时根目录就是工作区根（保持旧行为）。
// fireflySandbox returns the physical root the export may write into, plus the normalized
// TargetSubdir. An empty TargetSubdir keeps the legacy workspace-root behavior.
func fireflySandbox(wsPath string, conf *domain.GitSyncConfig) (root string, subdir string, err error) {
	subdir, err = normalizeRelativeSubdir(conf.TargetSubdir)
	if err != nil {
		return "", "", err
	}
	if subdir == "" {
		return wsPath, "", nil
	}
	return filepath.Join(wsPath, filepath.FromSlash(subdir)), subdir, nil
}

// fireflyRepoFileExists 判断仓库相对路径对应的文件是否已存在于沙箱内（必须是 TargetSubdir 之内）。
// fireflyRepoFileExists reports whether a repo-relative file exists inside the sandbox.
func fireflyRepoFileExists(sandboxRoot, targetSubdir, repoRel string) bool {
	repoRel = normalizeSlashPath(repoRel)
	if repoRel == "" || repoRel == ".." || strings.HasPrefix(repoRel, "../") {
		return false
	}
	rel := repoRel
	if targetSubdir != "" {
		prefix := targetSubdir + "/"
		if !strings.HasPrefix(repoRel, prefix) {
			return false
		}
		rel = repoRel[len(prefix):]
	}
	full, err := safeWorkspacePath(sandboxRoot, filepath.FromSlash(rel))
	if err != nil {
		return false
	}
	info, err := os.Stat(full)
	return err == nil && !info.IsDir()
}

// resolveVaultAsset 在 Vault 中定位一个被引用的图片附件（存活记录，action != delete）。
// 查找顺序：笔记目录相对路径 → 库根相对路径 → 裸文件名同名 → assets/ 目录。
// resolveVaultAsset locates a referenced attachment in the vault (live rows only).
func (s *gitSyncService) resolveVaultAsset(ctx context.Context, conf *domain.GitSyncConfig, noteVaultDir, ref string) *domain.File {
	ref = strings.TrimSpace(ref)
	ref = strings.Trim(ref, "<>\"'")
	ref = normalizeSlashPath(ref)
	if ref == "" || isExternalImageRef(ref) {
		return nil
	}

	seen := map[string]bool{}
	try := func(candidate string) *domain.File {
		candidate = normalizeSlashPath(candidate)
		if candidate == "" || seen[candidate] {
			return nil
		}
		seen[candidate] = true
		f, err := s.fileRepo.GetByPath(ctx, candidate, conf.VaultID, conf.UID)
		if err != nil || f == nil {
			return nil
		}
		if f.Action == domain.FileActionDelete || f.SavePath == "" {
			return nil
		}
		return f
	}

	if f := try(path.Join(noteVaultDir, ref)); f != nil {
		return f
	}
	if f := try(ref); f != nil {
		return f
	}
	if !strings.Contains(ref, "/") {
		// Obsidian 裸文件名：按库内同名后缀匹配，再回退到 assets/ 目录
		if f, err := s.fileRepo.GetByPathLike(ctx, "/"+ref, conf.VaultID, conf.UID); err == nil && f != nil &&
			f.Action != domain.FileActionDelete && f.SavePath != "" {
			return f
		}
		if f := try("assets/" + ref); f != nil {
			return f
		}
	}
	return nil
}

// sanitizeAssetBaseName 只保留安全的基础文件名。
func sanitizeAssetBaseName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "\x00/") {
		return ""
	}
	return name
}

// shortAssetHash 返回附件来源路径的短哈希，用于同名不同源时的去重命名。
func shortAssetHash(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%08x", h.Sum32())
}

// rewriteFrontmatterImage 只替换 frontmatter 块中顶层的 image: 行，保留其余排版。
func rewriteFrontmatterImage(fmText, newValue string) string {
	if fmText == "" {
		return fmText
	}
	lines := strings.SplitAfter(fmText, "\n")
	for i, line := range lines {
		body := strings.TrimSuffix(line, "\n")
		body = strings.TrimSuffix(body, "\r")
		if strings.HasPrefix(body, " ") || strings.HasPrefix(body, "\t") {
			continue
		}
		if !strings.HasPrefix(body, "image") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(body, "image"))
		if !strings.HasPrefix(rest, ":") {
			continue
		}
		suffix := "\n"
		if !strings.HasSuffix(line, "\n") {
			suffix = ""
		}
		lines[i] = `image: "` + strings.ReplaceAll(newValue, `"`, `\"`) + `"` + suffix
		break
	}
	return strings.Join(lines, "")
}

// bundleNoteAssets 把笔记引用的库内图片汇聚到工作区目标文章同目录的 ./assets/ 下，
// 并返回改写后的 Markdown 副本。它只改工作区副本，绝不触碰数据库中的原始内容（不变量 4）。
// bundleNoteAssets copies every vault image referenced by the note into ./assets/ next to the
// exported post and rewrites the exported copy. The stored note content is never modified.
func (s *gitSyncService) bundleNoteAssets(ctx context.Context, conf *domain.GitSyncConfig, notePath, content, targetNoteFullPath, sandboxRoot string) (string, bool, error) {
	if !conf.BundleAssets {
		return content, false, nil
	}

	fmText, body, fm, ok := splitFrontmatter(content)
	if !ok {
		fmText, body, fm = "", content, map[string]any{}
	}

	noteVaultDir := path.Dir(normalizeSlashPath(notePath))
	if noteVaultDir == "." {
		noteVaultDir = ""
	}

	relNoteDir, err := filepath.Rel(sandboxRoot, filepath.Dir(targetNoteFullPath))
	if err != nil {
		return content, false, err
	}
	assetsRel := filepath.Join(relNoteDir, "assets")

	wikiDest := map[string]string{}
	markdownDest := map[string]string{}
	usedNames := map[string]string{}
	changed := false

	// resolveAndCopy 定位附件并复制到目标文章的 ./assets/，返回安全文件名。
	resolveAndCopy := func(ref string) (string, bool) {
		f := s.resolveVaultAsset(ctx, conf, noteVaultDir, ref)
		if f == nil {
			return "", false
		}
		base := sanitizeAssetBaseName(f.Path)
		if base == "" {
			base = sanitizeAssetBaseName(ref)
		}
		if base == "" {
			return "", false
		}
		if prev, exists := usedNames[base]; exists && prev != f.Path {
			ext := path.Ext(base)
			base = strings.TrimSuffix(base, ext) + "-" + shortAssetHash(f.Path) + ext
		}
		usedNames[base] = f.Path

		dstRel := filepath.Join(assetsRel, base)
		dstFull, pathErr := safeWorkspacePath(sandboxRoot, dstRel)
		if pathErr != nil {
			s.logger.Warn("Skipping bundled asset whose path escapes the sandbox",
				zap.String("asset", f.Path), zap.Error(pathErr))
			return "", false
		}
		if err := os.MkdirAll(filepath.Dir(dstFull), 0755); err != nil {
			return "", false
		}
		copied, copyErr := s.copyFileIfDifferent(f.SavePath, dstFull)
		if copyErr != nil {
			return "", false
		}
		if copied {
			changed = true
		}
		return base, true
	}

	// 收集正文引用（代码块外）与 frontmatter.image，先完成复制再统一改写。
	transformMarkdownOutsideCode(body, func(text string) string {
		for _, m := range wikiEmbedRe.FindAllStringSubmatch(text, -1) {
			inner := m[1]
			ref := inner
			if idx := strings.Index(inner, "|"); idx >= 0 {
				ref = inner[:idx]
			}
			ref = strings.TrimSpace(ref)
			if _, done := wikiDest[ref]; done || ref == "" {
				continue
			}
			if dest, okCopy := resolveAndCopy(ref); okCopy {
				wikiDest[ref] = dest
			}
		}
		for _, m := range markdownImageRe.FindAllStringSubmatch(text, -1) {
			ref := strings.TrimSpace(m[2])
			if _, done := markdownDest[ref]; done || ref == "" || isExternalImageRef(ref) {
				continue
			}
			if dest, okCopy := resolveAndCopy(ref); okCopy {
				markdownDest[ref] = dest
			}
		}
		return text
	})

	if image, okImg := fm["image"].(string); okImg {
		ref := strings.TrimSpace(image)
		if ref != "" && !isExternalImageRef(ref) {
			if _, done := markdownDest[ref]; !done {
				if dest, okCopy := resolveAndCopy(ref); okCopy {
					markdownDest[ref] = dest
				}
			}
		}
	}

	if len(wikiDest) == 0 && len(markdownDest) == 0 {
		return content, changed, nil
	}

	newBody := transformMarkdownOutsideCode(body, func(text string) string {
		if len(wikiDest) > 0 {
			text = wikiEmbedRe.ReplaceAllStringFunc(text, func(m string) string {
				sub := wikiEmbedRe.FindStringSubmatch(m)
				if sub == nil {
					return m
				}
				inner := sub[1]
				ref, alias := inner, ""
				if idx := strings.Index(inner, "|"); idx >= 0 {
					ref, alias = inner[:idx], inner[idx+1:]
				}
				dest, okDest := wikiDest[strings.TrimSpace(ref)]
				if !okDest {
					return m
				}
				alt := strings.TrimSpace(alias)
				if alt == "" {
					alt = strings.TrimSuffix(path.Base(dest), path.Ext(dest))
				}
				return "![" + alt + "](./assets/" + dest + ")"
			})
		}
		if len(markdownDest) > 0 {
			text = markdownImageRe.ReplaceAllStringFunc(text, func(m string) string {
				sub := markdownImageRe.FindStringSubmatch(m)
				if sub == nil {
					return m
				}
				dest, okDest := markdownDest[strings.TrimSpace(sub[2])]
				if !okDest {
					return m
				}
				return "![" + sub[1] + "](./assets/" + dest + ")"
			})
		}
		return text
	})

	if image, okImg := fm["image"].(string); okImg {
		if dest, okDest := markdownDest[strings.TrimSpace(image)]; okDest {
			fmText = rewriteFrontmatterImage(fmText, "./assets/"+dest)
		}
	}

	rewritten := fmText + newBody
	return rewritten, changed || rewritten != content, nil
}

// ---------------------------------------------------------------------------
// 运行报告：跳过告警与远端 hook 回执
// ---------------------------------------------------------------------------

// gitSyncRunReport 汇总单轮同步中需要在状态/历史里透出的信息。
type gitSyncRunReport struct {
	skipped     []gitSyncSkippedPost
	hookReceipt string
}

// gitSyncSkippedPost 是一篇被预检拦下的文章及原因。
type gitSyncSkippedPost struct {
	path   string
	reason string
}

// skipSummary 生成 "skipped N invalid post(s): foo.md: reason" 形式的摘要。
func (r *gitSyncRunReport) skipSummary() string {
	if r == nil || len(r.skipped) == 0 {
		return ""
	}
	first := r.skipped[0]
	msg := fmt.Sprintf("skipped %d invalid post", len(r.skipped))
	if len(r.skipped) > 1 {
		msg += "s"
	}
	msg += ": " + first.path + ": " + first.reason
	return msg
}

func (s *gitSyncService) setRunReport(configID int64, report *gitSyncRunReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runReports == nil {
		s.runReports = make(map[int64]*gitSyncRunReport)
	}
	s.runReports[configID] = report
}

func (s *gitSyncService) takeRunReport(configID int64) *gitSyncRunReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	report := s.runReports[configID]
	delete(s.runReports, configID)
	return report
}

func (s *gitSyncService) recordSkippedPost(configID int64, notePath, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	report, ok := s.runReports[configID]
	if !ok || report == nil {
		return
	}
	report.skipped = append(report.skipped, gitSyncSkippedPost{path: notePath, reason: reason})
}

// isObsidianConfigPath 判断条目是否为 .obsidian 配置目录下的文件。
func isObsidianConfigPath(itemPath string) bool {
	p := normalizeSlashPath(itemPath)
	if p == "" {
		return false
	}
	first := strings.SplitN(p, "/", 2)[0]
	return strings.EqualFold(strings.TrimRight(first, ". "), ".obsidian")
}

// normalizeTargetSubdirForConfig 返回规范化后的 TargetSubdir（非法值视为空，仅用于判断）。
func normalizeTargetSubdirForConfig(conf *domain.GitSyncConfig) string {
	sub, err := normalizeRelativeSubdir(conf.TargetSubdir)
	if err != nil {
		return conf.TargetSubdir
	}
	return sub
}

// ---------------------------------------------------------------------------
// 远端 post-receive sideband 回执
// ---------------------------------------------------------------------------

// gitSyncHookOutputLimit 限制单次 push 捕获的 sideband 输出大小，防止恶意/异常远端撑爆内存。
const gitSyncHookOutputLimit = 16 * 1024

// boundedProgressBuffer 实现 sideband.Progress，只保留前 limit 字节。
// boundedProgressBuffer is a sideband.Progress that keeps at most limit bytes.
type boundedProgressBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (b *boundedProgressBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - len(b.buf)
	if remaining > 0 {
		if len(p) > remaining {
			b.buf = append(b.buf, p[:remaining]...)
		} else {
			b.buf = append(b.buf, p...)
		}
	}
	return len(p), nil
}

func (b *boundedProgressBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// 远端 hook 的失败/成功标记（NAS settings-publish-nas.sh 与 post-receive 的实际输出）。
var (
	fireflyHookFailureMarkers = []string{
		"Preview deployment failed",
		"Saved settings await confirmation",
		"deploy failed",
	}
	fireflyHookSuccessMarkers = []string{
		"Preview deployed:",
		"Unified preview deployed:",
	}
)

// applyRemoteHookOutput 解析 sideband 输出：成功回执写入运行报告，失败标记转成错误，
// 让状态如实反映远端构建结果而不是静默报成功（不变量 6）。
// applyRemoteHookOutput turns the sideband output into a receipt or a hard error.
func (s *gitSyncService) applyRemoteHookOutput(configID int64, output string) error {
	if strings.TrimSpace(output) == "" {
		return nil
	}

	for _, marker := range fireflyHookFailureMarkers {
		if idx := strings.Index(output, marker); idx >= 0 {
			summary := hookLineFrom(output, idx)
			s.mu.Lock()
			if report, ok := s.runReports[configID]; ok && report != nil {
				report.hookReceipt = ""
			}
			s.mu.Unlock()
			return fmt.Errorf("git pushed, but remote hook deploy failed: %s", summary)
		}
	}

	for _, marker := range fireflyHookSuccessMarkers {
		if idx := strings.Index(output, marker); idx >= 0 {
			receipt := hookLineFrom(output, idx)
			s.mu.Lock()
			if report, ok := s.runReports[configID]; ok && report != nil {
				report.hookReceipt = receipt
			}
			s.mu.Unlock()
			return nil
		}
	}
	return nil
}

// hookLineFrom 截取从 idx 开始的一行，最长 200 字符。
func hookLineFrom(output string, idx int) string {
	rest := output[idx:]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	rest = strings.TrimSpace(strings.TrimRight(rest, "\r"))
	if len(rest) > 200 {
		rest = rest[:200]
	}
	return rest
}

// summarizeGitError 压缩多行错误为单行摘要（最多 300 字符）。
func summarizeGitError(err error) string {
	if err == nil {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(err.Error(), "\r\n", "\n"), "\n")
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			parts = append(parts, line)
		}
	}
	summary := strings.Join(parts, " | ")
	if len(summary) > 300 {
		summary = summary[:300]
	}
	return summary
}

// ---------------------------------------------------------------------------
// Webhook 触发（受控 POST）
// ---------------------------------------------------------------------------

// gitSyncWebhookTimeout 限制 Webhook 调用总耗时，避免拖住整轮同步。
const gitSyncWebhookTimeout = 15 * time.Second

// fireDeployWebhook 在配置了 WebhookURL 时发出一次受控 POST（带超时、不跟随跨主机跳转）。
// 失败只记日志：它只是“让远端重建”的辅助手段，不影响本轮同步结论。
// fireDeployWebhook sends one controlled POST when WebhookURL is configured. Failures are
// logged only: it is a best-effort rebuild trigger.
func (s *gitSyncService) fireDeployWebhook(ctx context.Context, conf *domain.GitSyncConfig) {
	webhookURL := strings.TrimSpace(conf.WebhookURL)
	if webhookURL == "" {
		return
	}
	// 每次调用前重新校验，防止库里遗留的旧值绕过白名单（SSRF）。
	if err := s.repoURLValidator().validateRepoURL(webhookURL); err != nil {
		s.logger.Warn("Skipping webhook call, URL rejected",
			zap.Int64("configId", conf.ID), zap.Error(err))
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, gitSyncWebhookTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, webhookURL, strings.NewReader(`{"source":"fast-note-sync"}`))
	if err != nil {
		s.logger.Warn("Failed to build webhook request", zap.Int64("configId", conf.ID), zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: gitSyncWebhookTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// 只允许同主机跳转，避免被重定向到内网其它地址。
			if len(via) > 0 && req.URL.Host != via[0].URL.Host {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		s.logger.Warn("Webhook call failed", zap.Int64("configId", conf.ID), zap.Error(err))
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 400 {
		s.logger.Warn("Webhook call returned an error status",
			zap.Int64("configId", conf.ID), zap.Int("status", resp.StatusCode))
	}
}

// ---------------------------------------------------------------------------
// receive-pack 响应捕获（post-receive sideband 输出）
// ---------------------------------------------------------------------------

// sidebandCapture 保存最近一次 receive-pack 响应体里解出的 sideband 进度/错误文本。
// sidebandCapture keeps the sideband progress/error text decoded from the latest
// receive-pack response body.
type sidebandCapture struct {
	mu   sync.Mutex
	text string
}

// set 记录本次 push 的 sideband 文本（空文本也会覆盖上一轮）。
func (c *sidebandCapture) set(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.text = text
}

// get 返回本轮捕获的 sideband 文本。
func (c *sidebandCapture) get() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text
}

// pushCaptureRoundTripper 在传输层截获 git-receive-pack 的响应体并解析 sideband 帧。
// pushCaptureRoundTripper captures receive-pack response bodies and decodes their
// sideband frames at the transport level.
type pushCaptureRoundTripper struct {
	base    http.RoundTripper
	capture *sidebandCapture
}

func (rt *pushCaptureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rt.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	if req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/git-receive-pack") {
		return resp, nil
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, gitSyncHookOutputLimit*4))
	_ = resp.Body.Close()
	if readErr != nil {
		// 读取失败时回退为空 body，让 go-git 报出它自己的错误。
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return resp, nil
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	rt.capture.set(decodeSidebandText(body))
	return resp, nil
}

// decodeSidebandText 从 receive-pack 响应中抽取 sideband 的进度（channel 2）与错误（channel 3）文本。
// 响应由 pkt-line 组成；通道数据本身也可能是嵌套的 pkt-line，因此两者都尝试解析。
// decodeSidebandText extracts sideband channel 2/3 text from a receive-pack response.
func decodeSidebandText(body []byte) string {
	var out strings.Builder
	for _, payload := range splitPktLines(body) {
		if len(payload) == 0 {
			continue
		}
		switch payload[0] {
		case 2, 3:
			chunk := payload[1:]
			if len(chunk) > 0 && looksLikePktLine(chunk) {
				for _, inner := range splitPktLines(chunk) {
					if len(inner) > 1 && (inner[0] == 2 || inner[0] == 3) {
						out.Write(inner[1:])
					} else if len(inner) > 0 && inner[0] != 1 && inner[0] != 2 && inner[0] != 3 {
						out.Write(inner)
					}
				}
				continue
			}
			out.Write(chunk)
		}
	}
	return out.String()
}

// looksLikePktLine 粗略判断字节串是否为 pkt-line 序列（4 位十六进制长度前缀）。
func looksLikePktLine(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	for i := 0; i < 4; i++ {
		c := data[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// splitPktLines 拆出所有 pkt-line 的 payload；遇到 flush/delim 或长度非法即停止。
func splitPktLines(data []byte) [][]byte {
	var out [][]byte
	pos := 0
	for pos+4 <= len(data) {
		n := hex4(data[pos : pos+4])
		if n < 0 {
			break
		}
		if n == 0 {
			pos += 4
			continue
		}
		if n < 4 || pos+n > len(data) {
			break
		}
		out = append(out, data[pos+4:pos+n])
		pos += n
	}
	return out
}

// hex4 解析 4 位十六进制长度；非法返回 -1。
func hex4(b []byte) int {
	v := 0
	for _, c := range b {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v += int(c - '0')
		case c >= 'a' && c <= 'f':
			v += int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v += int(c-'A') + 10
		default:
			return -1
		}
	}
	return v
}
