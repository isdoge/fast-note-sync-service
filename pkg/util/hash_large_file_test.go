package util

import (
	"os"
	"path/filepath"
	"testing"
)

// Expected values below were produced by a byte-for-byte Node.js reference of the
// Obsidian plugin's hashArrayBuffer / computeRollingHash / computeMidSliceStart
// (src/lib/utils/helpers.ts). The generated payload is deterministic:
//
//	a[i] = (i*31 + 17) & 0xff
//	a[0] = 0xff, a[1] = 0x80, a[size-1] = 0xff, a[size-2] = 0x80
//	a[midStart+1] = 0xff, a[midStart+2] = 0x80
//
// so it exercises 0x80..0xff boundary bytes in the head, middle and tail samples.
//
// 下面的期望值由逐字节复刻插件 hashArrayBuffer / computeRollingHash /
// computeMidSliceStart 的 Node.js 参考实现算出，载荷为上方确定性生成的数据，
// 覆盖首/中/尾采样中的 0x80..0xff 边界字节。
type pluginHashCase struct {
	name       string
	size       int
	midStart   int
	pluginHash string // three-segment (head + middle + tail)
	legacyHash string // old server (head + tail)
}

var pluginHashCases = []pluginHashCase{
	{name: "1MB", size: 1 * 1024 * 1024, midStart: 0, pluginHash: "-519348912", legacyHash: "-519348912"},
	{name: "10MB_threshold", size: 10 * 1024 * 1024, midStart: 2621440, pluginHash: "-1754844768", legacyHash: "-1754844768"},
	{name: "10MB_plus_1", size: 10*1024*1024 + 1, midStart: 2621440, pluginHash: "-151163714", legacyHash: "-424202560"},
	{name: "12MB", size: 12 * 1024 * 1024, midStart: 3670016, pluginHash: "1672310686", legacyHash: "1399271840"},
	{name: "16MB", size: 16 * 1024 * 1024, midStart: 5767168, pluginHash: "1480536480", legacyHash: "-1678183518"},
}

// pluginTestData reproduces the deterministic Node.js payload byte for byte.
// pluginTestData 逐字节复现 Node.js 参考实现使用的确定性载荷。
func pluginTestData(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte((i*31 + 17) & 0xff)
	}
	data[0] = 0xff
	data[1] = 0x80
	data[size-1] = 0xff
	data[size-2] = 0x80
	midStart := computeMidSliceStart(size)
	if midStart+3 < size {
		data[midStart+1] = 0xff
		data[midStart+2] = 0x80
	}
	return data
}

// TestEncodeHash32BytesMatchesPlugin locks the Go byte hash to the plugin's
// hashArrayBuffer output for sizes around the 10MB sampling threshold.
// TestEncodeHash32BytesMatchesPlugin 用插件 hashArrayBuffer 的输出锁定 Go 字节哈希，
// 覆盖 10MB 采样阈值上下的各个尺寸。
func TestEncodeHash32BytesMatchesPlugin(t *testing.T) {
	for _, tc := range pluginHashCases {
		t.Run(tc.name, func(t *testing.T) {
			data := pluginTestData(tc.size)

			if got := EncodeHash32Bytes(data); got != tc.pluginHash {
				t.Errorf("EncodeHash32Bytes(%d bytes) = %s, want plugin value %s", tc.size, got, tc.pluginHash)
			}
			if got := EncodeHash32BytesLegacy(data); got != tc.legacyHash {
				t.Errorf("EncodeHash32BytesLegacy(%d bytes) = %s, want legacy value %s", tc.size, got, tc.legacyHash)
			}
		})
	}
}

// TestComputeMidSliceStartMatchesPlugin locks the middle-slice offset to the plugin.
// TestComputeMidSliceStartMatchesPlugin 用插件值锁定中段切片起始偏移。
func TestComputeMidSliceStartMatchesPlugin(t *testing.T) {
	for _, tc := range pluginHashCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := computeMidSliceStart(tc.size); got != tc.midStart {
				t.Errorf("computeMidSliceStart(%d) = %d, want plugin value %d", tc.size, got, tc.midStart)
			}
		})
	}
}

// TestEncodeHash32FileMatchesPlugin verifies that the streaming file hash reads only
// the sampled slices yet produces exactly the same value as the byte hash, so REST
// uploads, MCP writes and plugin-side file hashes agree for >10MB attachments.
// TestEncodeHash32FileMatchesPlugin 验证流式文件哈希只读取采样分段，却与字节哈希结果
// 完全一致，从而让 REST 上传、MCP 写入与插件端文件哈希在 >10MB 附件上口径一致。
func TestEncodeHash32FileMatchesPlugin(t *testing.T) {
	for _, tc := range pluginHashCases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file.dat")
			if err := os.WriteFile(path, pluginTestData(tc.size), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}

			got, err := EncodeHash32File(path)
			if err != nil {
				t.Fatalf("EncodeHash32File: %v", err)
			}
			if got != tc.pluginHash {
				t.Errorf("EncodeHash32File(%s) = %s, want plugin value %s", tc.name, got, tc.pluginHash)
			}

			legacy, err := EncodeHash32FileLegacy(path)
			if err != nil {
				t.Fatalf("EncodeHash32FileLegacy: %v", err)
			}
			if legacy != tc.legacyHash {
				t.Errorf("EncodeHash32FileLegacy(%s) = %s, want legacy value %s", tc.name, legacy, tc.legacyHash)
			}

			current, legacy, err := EncodeHash32FileWithLegacy(path)
			if err != nil {
				t.Fatalf("EncodeHash32FileWithLegacy: %v", err)
			}
			if current != tc.pluginHash || legacy != tc.legacyHash {
				t.Errorf("EncodeHash32FileWithLegacy(%s) = (%s, %s), want (%s, %s)",
					tc.name, current, legacy, tc.pluginHash, tc.legacyHash)
			}
		})
	}
}

// TestLegacyAndPluginHashDivergeAboveThreshold documents why the bridge exists: above
// 10MB the two sampling schemes genuinely disagree, while at/below the threshold they
// are the same full-content hash.
// TestLegacyAndPluginHashDivergeAboveThreshold 说明兼容桥接的必要性：>10MB 时两种采样
// 口径确实不同，阈值及以下则是同一个全量哈希。
func TestLegacyAndPluginHashDivergeAboveThreshold(t *testing.T) {
	for _, tc := range pluginHashCases {
		t.Run(tc.name, func(t *testing.T) {
			data := pluginTestData(tc.size)
			newHash := EncodeHash32Bytes(data)
			legacyHash := EncodeHash32BytesLegacy(data)
			if tc.size <= FileHashThreshold && newHash != legacyHash {
				t.Fatalf("at/below threshold both schemes must hash all bytes: new=%s legacy=%s", newHash, legacyHash)
			}
			if tc.size > FileHashThreshold && newHash == legacyHash {
				t.Fatalf("above threshold the schemes must diverge, both returned %s", newHash)
			}
		})
	}
}
