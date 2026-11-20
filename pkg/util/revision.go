package util

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"time"
)

// RevisionTokenBytes is the raw entropy of a revision token: 128 bits.
// RevisionTokenBytes 是版本令牌的原始熵：128 位。
const RevisionTokenBytes = 16

// NewRevisionToken returns a fresh 128-bit random revision token encoded as 32
// lowercase hex characters. The token identifies one concrete server-side state
// (create, content change, delete, restore, rebuild, rename); it is never derived
// from content, mtime or the path, and it must not be sorted or used as a cursor.
// NewRevisionToken 生成新的 128 位随机版本令牌（32 位小写十六进制）。
// 令牌标识服务端某一次具体状态（创建、正文修改、删除、恢复、重建、重命名），
// 不由内容、mtime 或路径推导，不能排序，也不作增量游标使用。
func NewRevisionToken() string {
	buf := make([]byte, RevisionTokenBytes)
	if _, err := cryptorand.Read(buf); err == nil {
		return hex.EncodeToString(buf)
	}
	// crypto/rand 不可用时退回时间+计数器混合熵：宁可降低不可预测性，
	// 也不能让持久化写路径失败或写出空令牌。
	// Fall back to a time/counter mix if crypto/rand is unavailable: a weaker
	// token is still better than failing the write path or persisting an empty one.
	return newFallbackRevisionToken()
}

var fallbackRevisionCounter uint64

// newFallbackRevisionToken builds a 32-hex token without crypto/rand.
// newFallbackRevisionToken 在 crypto/rand 不可用时构造 32 位十六进制令牌。
func newFallbackRevisionToken() string {
	fallbackRevisionCounter++
	now := uint64(time.Now().UnixNano())
	buf := make([]byte, RevisionTokenBytes)
	for i := 0; i < 8; i++ {
		buf[i] = byte(now >> (8 * i))
		buf[8+i] = byte(fallbackRevisionCounter >> (8 * i))
	}
	return hex.EncodeToString(buf)
}

// IsValidRevisionToken reports whether s looks like a 32-hex revision token.
// Empty means "no trusted revision" and is reported as false.
// IsValidRevisionToken 判断 s 是否形如 32 位十六进制版本令牌；空串表示没有可信令牌。
func IsValidRevisionToken(s string) bool {
	if len(s) != 2*RevisionTokenBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
