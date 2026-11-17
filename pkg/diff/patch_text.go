package diff

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// PatchText returns the same text as dmp.PatchToText(dmp.PatchMake(text1, diffs)), without
// its peak memory blow-up on large notes (#499).
//
// go-diff v1.4.0's patchMake2 rebuilds the whole intermediate text for every patch, and
// PatchAddContext slices each patch's prefix/suffix context out of that full copy; since Go
// substrings share their backing array, every retained patch pins one full copy of the note
// (peak ≈ patch count × note size). This port of patchMake2 / PatchAddContext / Patch.String
// writes each patch out as soon as it is complete and keeps no patch afterwards, so at most
// one intermediate copy is alive at a time.
//
// PatchText 输出与 dmp.PatchToText(dmp.PatchMake(text1, diffs)) 完全相同，但避免其在大笔记上的内存暴涨（#499）。
// go-diff v1.4.0 的 patchMake2 每生成一个补丁都会重建一份完整中间文本，PatchAddContext 从这份全文切片取前后文；
// Go 子串共享底层数组，于是每个保留下来的补丁都占住一份完整笔记（峰值 ≈ 补丁数 × 笔记大小）。
// 这里移植 patchMake2 / PatchAddContext / Patch.String，每个补丁完成即写出文本、不再保留，
// 同一时刻最多只有一份中间文本存活。
func PatchText(dmp *diffmatchpatch.DiffMatchPatch, text1 string, diffs []diffmatchpatch.Diff) string {
	var out strings.Builder
	if len(diffs) == 0 {
		return ""
	}

	p := textPatch{}
	charCount1 := 0 // Number of characters into the text1 string.
	charCount2 := 0 // Number of characters into the text2 string.
	prepatchText := text1
	postpatchText := text1

	for i, aDiff := range diffs {
		if len(p.diffs) == 0 && aDiff.Type != diffmatchpatch.DiffEqual {
			// A new patch starts here.
			p.start1 = charCount1
			p.start2 = charCount2
		}

		switch aDiff.Type {
		case diffmatchpatch.DiffInsert:
			p.diffs = append(p.diffs, aDiff)
			p.length2 += len(aDiff.Text)
			postpatchText = postpatchText[:charCount2] + aDiff.Text + postpatchText[charCount2:]
		case diffmatchpatch.DiffDelete:
			p.length1 += len(aDiff.Text)
			p.diffs = append(p.diffs, aDiff)
			postpatchText = postpatchText[:charCount2] + postpatchText[charCount2+len(aDiff.Text):]
		case diffmatchpatch.DiffEqual:
			if len(aDiff.Text) <= 2*dmp.PatchMargin && len(p.diffs) != 0 && i != len(diffs)-1 {
				// Small equality inside a patch.
				p.diffs = append(p.diffs, aDiff)
				p.length1 += len(aDiff.Text)
				p.length2 += len(aDiff.Text)
			}
			if len(aDiff.Text) >= 2*dmp.PatchMargin && len(p.diffs) != 0 {
				// Time for a new patch; patch lists have a rolling context.
				p.addContext(dmp, prepatchText)
				p.writeTo(&out)
				p = textPatch{}
				prepatchText = postpatchText
				charCount1 = charCount2
			}
		}

		if aDiff.Type != diffmatchpatch.DiffInsert {
			charCount1 += len(aDiff.Text)
		}
		if aDiff.Type != diffmatchpatch.DiffDelete {
			charCount2 += len(aDiff.Text)
		}
	}

	// Pick up the leftover patch if not empty.
	if len(p.diffs) != 0 {
		p.addContext(dmp, prepatchText)
		p.writeTo(&out)
	}
	return out.String()
}

// textPatch mirrors diffmatchpatch.Patch, whose diffs field is unexported.
// textPatch 对应 diffmatchpatch.Patch（其 diffs 字段未导出）。
type textPatch struct {
	diffs            []diffmatchpatch.Diff
	start1, start2   int
	length1, length2 int
}

// addContext is diffmatchpatch.PatchAddContext.
// addContext 即 diffmatchpatch.PatchAddContext。
func (p *textPatch) addContext(dmp *diffmatchpatch.DiffMatchPatch, text string) {
	if len(text) == 0 {
		return
	}

	pattern := text[p.start2 : p.start2+p.length1]
	padding := 0

	// Increase the context until the pattern is unique, bounded by MatchMaxBits.
	for strings.Index(text, pattern) != strings.LastIndex(text, pattern) &&
		len(pattern) < dmp.MatchMaxBits-2*dmp.PatchMargin {
		padding += dmp.PatchMargin
		pattern = text[max(0, p.start2-padding):min(len(text), p.start2+p.length1+padding)]
	}
	// Add one chunk for good luck.
	padding += dmp.PatchMargin

	prefix := text[max(0, p.start2-padding):p.start2]
	if len(prefix) != 0 {
		p.diffs = append([]diffmatchpatch.Diff{{Type: diffmatchpatch.DiffEqual, Text: prefix}}, p.diffs...)
	}
	suffix := text[p.start2+p.length1 : min(len(text), p.start2+p.length1+padding)]
	if len(suffix) != 0 {
		p.diffs = append(p.diffs, diffmatchpatch.Diff{Type: diffmatchpatch.DiffEqual, Text: suffix})
	}

	p.start1 -= len(prefix)
	p.start2 -= len(prefix)
	p.length1 += len(prefix) + len(suffix)
	p.length2 += len(prefix) + len(suffix)
}

// writeTo is diffmatchpatch.Patch.String (GNU diff style header, %xx-escaped body).
// writeTo 即 diffmatchpatch.Patch.String（GNU diff 风格头部，正文 %xx 转义）。
func (p *textPatch) writeTo(out *strings.Builder) {
	var text strings.Builder
	text.WriteString("@@ -" + patchCoords(p.start1, p.length1) + " +" + patchCoords(p.start2, p.length2) + " @@\n")
	for _, aDiff := range p.diffs {
		switch aDiff.Type {
		case diffmatchpatch.DiffInsert:
			text.WriteString("+")
		case diffmatchpatch.DiffDelete:
			text.WriteString("-")
		case diffmatchpatch.DiffEqual:
			text.WriteString(" ")
		}
		text.WriteString(strings.Replace(url.QueryEscape(aDiff.Text), "+", " ", -1))
		text.WriteString("\n")
	}
	out.WriteString(patchUnescaper.Replace(text.String()))
}

// patchCoords formats one side of the header; indices are printed 1-based.
// patchCoords 格式化头部的一侧坐标，下标按 1 起始输出。
func patchCoords(start, length int) string {
	switch length {
	case 0:
		return strconv.Itoa(start) + ",0"
	case 1:
		return strconv.Itoa(start + 1)
	}
	return strconv.Itoa(start+1) + "," + strconv.Itoa(length)
}

// patchUnescaper is go-diff's unescaper (encodeURI compatibility).
// patchUnescaper 即 go-diff 的 unescaper（与 encodeURI 兼容）。
var patchUnescaper = strings.NewReplacer(
	"%21", "!", "%7E", "~", "%27", "'",
	"%28", "(", "%29", ")", "%3B", ";",
	"%2F", "/", "%3F", "?", "%3A", ":",
	"%40", "@", "%26", "&", "%3D", "=",
	"%2B", "+", "%24", "$", "%2C", ",", "%23", "#", "%2A", "*")
