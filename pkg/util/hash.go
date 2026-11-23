package util

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
	"unicode/utf16"
)

// EncodeMD5 performs MD5 encoding on a string
// EncodeMD5 对字符串进行MD5编码
// str: string to be encoded
// str: 待编码的字符串
// return: MD5 encoded 32-bit hexadecimal string
// 返回值: MD5编码后的32位十六进制字符串
func EncodeMD5(str string) string {
	h := md5.New()
	h.Write([]byte(str))
	return hex.EncodeToString(h.Sum(nil))
}

// EncodeHash32 performs 32-bit hash encoding on a string
// EncodeHash32 对字符串进行 32 位哈希编码
func EncodeHash32(content string) string {
	// Convert string to rune slice, then to UTF-16 code units (consistent with JS internal representation)
	// 首先将字符串转为 rune 切片，再转为 UTF-16 code units（与 JS 的内部表示一致）
	runes := []rune(content)
	utf16Units := utf16.Encode(runes) // []uint16
	var hash int32 = 0
	for _, u := range utf16Units {
		char := int32(u) // Consistent with 16-bit value returned by JS charCodeAt // 与 JS charCodeAt 返回的 16-bit 值一致
		hash = (hash << 5) - hash + char
		// int32 will automatically overflow, equivalent to JS 32-bit bitwise operation result
		// int32 会自动溢出，等价于 JS 的 32-bit 位运算结果
	}
	return strconv.Itoa(int(hash))
}

const (
	// FileHashThreshold defines the size threshold (10MB) above which partial hashing is used
	// FileHashThreshold 定义触发分段哈希的阈值 (10MB)
	FileHashThreshold = 10 * 1024 * 1024
	// FileHashSliceSize defines the size of each sampled slice of large files (5MB)
	// FileHashSliceSize 定义大文件分段哈希时每段采样的大小 (5MB)
	FileHashSliceSize = 5 * 1024 * 1024
)

// computeMidSliceStart returns the start offset of the middle sample slice for large
// files: centered on the file's midpoint and clamped to a valid range. It must stay
// byte-for-byte identical to computeMidSliceStart in the Obsidian plugin
// (src/lib/utils/helpers.ts).
// computeMidSliceStart 返回大文件中段采样切片的起始偏移：以文件中点为中心并夹紧到
// 有效范围。必须与插件 src/lib/utils/helpers.ts 中的同名函数逐字节一致。
func computeMidSliceStart(size int) int {
	idealStart := size/2 - FileHashSliceSize/2
	maxStart := size - FileHashSliceSize
	if maxStart < 0 {
		maxStart = 0
	}
	if idealStart < 0 {
		idealStart = 0
	}
	if idealStart > maxStart {
		return maxStart
	}
	return idealStart
}

// hashSegment folds a byte slice into the JS-compatible rolling hash.
// hashSegment 把一段字节折叠进与 JS 兼容的滚动哈希。
func hashSegment(hash int32, data []byte) int32 {
	for _, b := range data {
		hash = (hash << 5) - hash + int32(b)
		// int32 overflow is intentional and matches JS ToInt32 after `hash &= hash`.
		// int32 溢出是刻意为之，等价于 JS 执行 `hash &= hash` 后的 ToInt32 结果。
	}
	return hash
}

// EncodeHash32Bytes performs 32-bit hash encoding on raw bytes, matching the Obsidian
// plugin's hashArrayBuffer exactly:
//   - at most 10MB: hash every byte;
//   - above 10MB: hash the first 5MB, the middle 5MB (centered on the file midpoint)
//     and the last 5MB, concatenated in that order.
//
// EncodeHash32Bytes 对原始字节进行 32 位哈希编码，与插件的 hashArrayBuffer 完全一致：
//   - 不超过 10MB：全量哈希；
//   - 超过 10MB：依次哈希前 5MB、中段 5MB（以文件中点为中心）、后 5MB。
func EncodeHash32Bytes(data []byte) string {
	size := len(data)
	var hash int32

	if size <= FileHashThreshold {
		hash = hashSegment(hash, data)
	} else {
		hash = hashSegment(hash, data[:FileHashSliceSize])

		midStart := computeMidSliceStart(size)
		midLen := size - midStart
		if midLen > FileHashSliceSize {
			midLen = FileHashSliceSize
		}
		hash = hashSegment(hash, data[midStart:midStart+midLen])

		hash = hashSegment(hash, data[size-FileHashSliceSize:])
	}
	return strconv.Itoa(int(hash))
}

// EncodeHash32BytesLegacy keeps the old server algorithm (first 5MB + last 5MB) so
// stored legacy hashes can still be recomputed and validated.
// EncodeHash32BytesLegacy 保留旧版服务端算法（前 5MB + 后 5MB），用于校验存量哈希。
func EncodeHash32BytesLegacy(data []byte) string {
	size := len(data)
	var hash int32

	if size <= FileHashThreshold {
		hash = hashSegment(hash, data)
	} else {
		hash = hashSegment(hash, data[:FileHashSliceSize])
		hash = hashSegment(hash, data[size-FileHashSliceSize:])
	}
	return strconv.Itoa(int(hash))
}

// EncodeHash32File computes the same hash as EncodeHash32Bytes for the file at path,
// but reads only the sampled slices of large files instead of loading them whole.
// EncodeHash32File 计算与 EncodeHash32Bytes 相同的哈希，但大文件只读取采样分段。
func EncodeHash32File(path string) (string, error) {
	current, _, err := encodeHash32File(path)
	return current, err
}

// EncodeHash32FileLegacy computes the legacy (first 5MB + last 5MB) hash of the file
// at path, reading only the sampled slices of large files.
// EncodeHash32FileLegacy 计算文件的旧版（前 5MB + 后 5MB）哈希，大文件只读采样分段。
func EncodeHash32FileLegacy(path string) (string, error) {
	_, legacy, err := encodeHash32File(path)
	return legacy, err
}

// EncodeHash32FileWithLegacy returns both the current three-segment hash and the
// legacy two-segment hash in a single sampling pass. The upload-check bridge uses it
// to confirm that a stored legacy hash describes the on-disk payload before realigning.
// EncodeHash32FileWithLegacy 在一次采样中同时返回三分段新哈希与旧版两段哈希，供
// 上传检查的存量兼容分支确认库内旧哈希确实描述磁盘上的实体文件。
func EncodeHash32FileWithLegacy(path string) (current, legacy string, err error) {
	current, legacy, err = encodeHash32File(path)
	return current, legacy, err
}

// encodeHash32File computes current and legacy hashes with one pass over the sampled
// slices. Small files are read in full.
// encodeHash32File 在一次采样中同时计算新/旧哈希；小文件全量读取。
func encodeHash32File(path string) (current string, legacy string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("hash: %s is not a regular file", path)
	}
	size := info.Size()

	var newHash, legacyHash int32

	if size <= FileHashThreshold {
		data := make([]byte, size)
		if _, err := io.ReadFull(f, data); err != nil {
			return "", "", err
		}
		newHash = hashSegment(newHash, data)
		legacyHash = newHash
		return strconv.Itoa(int(newHash)), strconv.Itoa(int(legacyHash)), nil
	}

	// Reuse a single slice buffer for the three sampled segments.
	// 三段采样复用同一个缓冲区。
	buf := make([]byte, FileHashSliceSize)
	readSegment := func(offset int64, length int, foldLegacy bool) error {
		if length == 0 {
			return nil
		}
		if _, err := io.ReadFull(io.NewSectionReader(f, offset, int64(length)), buf[:length]); err != nil {
			return err
		}
		newHash = hashSegment(newHash, buf[:length])
		if foldLegacy {
			legacyHash = hashSegment(legacyHash, buf[:length])
		}
		return nil
	}

	// First 5MB feeds both algorithms.
	// 前 5MB 同时计入新旧两种算法。
	if err := readSegment(0, FileHashSliceSize, true); err != nil {
		return "", "", err
	}

	// Middle 5MB only exists for the three-segment algorithm.
	// 中段 5MB 只计入三分段新算法。
	midStart := int64(computeMidSliceStart(int(size)))
	midLen := int64(FileHashSliceSize)
	if size-midStart < midLen {
		midLen = size - midStart
	}
	if err := readSegment(midStart, int(midLen), false); err != nil {
		return "", "", err
	}

	// Last 5MB feeds both algorithms.
	// 后 5MB 同时计入新旧两种算法。
	if err := readSegment(size-FileHashSliceSize, FileHashSliceSize, true); err != nil {
		return "", "", err
	}

	return strconv.Itoa(int(newHash)), strconv.Itoa(int(legacyHash)), nil
}
