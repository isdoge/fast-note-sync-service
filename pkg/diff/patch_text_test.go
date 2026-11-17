package diff

import (
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// PatchText 必须与 go-diff 原实现输出逐字节一致，且能被 PatchFromText / PatchApply 还原。
func TestPatchText_MatchesGoDiff(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	words := []string{"alpha", "beta", "中文", "段落", "🙂", "a+b", "%20", "x/y?z", "\n", " ", "#tag", "[[link]]"}
	gen := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(words[r.Intn(len(words))])
		}
		return b.String()
	}
	mutate := func(s string) string {
		parts := strings.Split(s, " ")
		for i := range parts {
			switch r.Intn(6) {
			case 0:
				parts[i] = ""
			case 1:
				parts[i] += gen(1 + r.Intn(3))
			}
		}
		return strings.Join(parts, " ")
	}

	dmp := diffmatchpatch.New()
	cases := [][2]string{{"", ""}, {"", "abc"}, {"abc", ""}, {"same", "same"}, {"aaaaaaaaaaaaaaaaaaaa", "aaaaaaaaaXaaaaaaaaaa"}}
	for i := 0; i < 300; i++ {
		a := gen(r.Intn(400))
		cases = append(cases, [2]string{a, mutate(a)})
	}
	for i, c := range cases {
		diffs := dmp.DiffMain(c[0], c[1], false)
		want := dmp.PatchToText(dmp.PatchMake(c[0], diffs))
		got := PatchText(dmp, c[0], diffs)
		if got != want {
			t.Fatalf("case %d: PatchText differs from go-diff\nwant %q\ngot  %q", i, want, got)
		}
		patches, err := dmp.PatchFromText(got)
		if err != nil {
			t.Fatalf("case %d: PatchFromText: %v", i, err)
		}
		if restored, _ := dmp.PatchApply(patches, c[0]); restored != c[1] {
			t.Fatalf("case %d: PatchApply did not restore text2", i)
		}
	}
}

// #499：大笔记被大量零散改动时，生成补丁的堆峰值不得随“补丁数 × 笔记大小”增长。
// 0.6MB、约 300 个补丁：go-diff 原实现峰值约 0.6MB × 300 ≈ 180MB，PatchText 应只有几 MB。
func TestPatchText_PeakHeapBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("memory test")
	}
	r := rand.New(rand.NewSource(1))
	lines := make([]string, 6000)
	for i := range lines {
		lines[i] = fmt.Sprintf("第 %d 行：这是一段用于复现的中文 Markdown 正文 %d", i, r.Int63())
	}
	oldText := strings.Join(lines, "\n")
	for i := range lines {
		if r.Float64() < 0.05 {
			lines[i] += " 已修改"
		}
	}
	newText := strings.Join(lines, "\n")

	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(oldText, newText, false)

	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)

	var peak uint64
	done := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			peak = max(peak, m.HeapAlloc)
			select {
			case <-done:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	patchText := PatchText(dmp, oldText, diffs)
	close(done)
	<-sampled

	growth := int64(peak) - int64(base.HeapAlloc)
	const limit = 64 << 20
	if growth > limit {
		t.Fatalf("peak heap grew by %dMB while generating %d bytes of patch text (limit %dMB)", growth>>20, len(patchText), limit>>20)
	}
}
