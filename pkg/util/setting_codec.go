package util

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"
)

// SettingContentBinaryPrefix is the wire prefix for binary setting payloads
// carried inside the plain-text setting `content` field.
// SettingContentBinaryPrefix 是配置 `content` 纯文本字段里承载二进制载荷的线上前缀。
const SettingContentBinaryPrefix = "fns-bin-v1:"

// SettingContentB64Prefix marks a standard-base64 encoded binary payload.
// SettingContentB64Prefix 标记标准 base64 编码的二进制载荷。
const SettingContentB64Prefix = SettingContentBinaryPrefix + "b64:"

// SettingContentEscPrefix marks plain UTF-8 text that itself starts with the
// binary prefix and therefore has to be escaped.
// SettingContentEscPrefix 标记本身以二进制前缀开头的普通 UTF-8 文本（需要转义）。
const SettingContentEscPrefix = SettingContentBinaryPrefix + "esc:"

// IsEncodedSettingContent reports whether the content uses the fns-bin-v1 wire
// encoding (binary base64 payload or escaped text).
// IsEncodedSettingContent 判断配置内容是否使用了 fns-bin-v1 线上编码
// （二进制 base64 载荷或转义文本）。
func IsEncodedSettingContent(content string) bool {
	return strings.HasPrefix(content, SettingContentBinaryPrefix)
}

// DecodeSettingContentBytes turns a setting `content` string into the raw bytes
// it represents. Settings that were never encoded (no prefix) are returned
// verbatim, so old clients and plain-text configs keep working unchanged.
// DecodeSettingContentBytes 把配置 `content` 字符串还原成它代表的原始字节。
// 未编码（无前缀）的配置原样返回，旧客户端与普通文本配置的行为完全不变。
//   - "fns-bin-v1:b64:<base64>" -> (decoded bytes, true, nil)
//   - "fns-bin-v1:esc:<text>"   -> (text bytes, false, nil)
//   - anything else             -> (original bytes, false, nil)
func DecodeSettingContentBytes(encoded string) ([]byte, bool, error) {
	switch {
	case strings.HasPrefix(encoded, SettingContentB64Prefix):
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, SettingContentB64Prefix))
		if err != nil {
			return nil, true, fmt.Errorf("invalid %sb64 payload: %w", SettingContentBinaryPrefix, err)
		}
		return raw, true, nil
	case strings.HasPrefix(encoded, SettingContentEscPrefix):
		return []byte(strings.TrimPrefix(encoded, SettingContentEscPrefix)), false, nil
	default:
		return []byte(encoded), false, nil
	}
}

// EncodeSettingContentBytes is the symmetric encoder for DecodeSettingContentBytes.
// Raw bytes that contain a NUL byte or are not valid UTF-8 are stored as base64;
// plain text that happens to start with the binary prefix is escaped; everything
// else is returned unchanged so existing configs stay byte-identical.
// EncodeSettingContentBytes 是 DecodeSettingContentBytes 的对称编码函数。
// 含 NUL 字节或不是合法 UTF-8 的原始字节按 base64 存储；本身以二进制前缀开头
// 的普通文本做转义；其余原样返回，已有配置保持逐字节不变。
func EncodeSettingContentBytes(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return SettingContentB64Prefix + base64.StdEncoding.EncodeToString(raw)
	}
	text := string(raw)
	if strings.HasPrefix(text, SettingContentBinaryPrefix) {
		return SettingContentEscPrefix + text
	}
	return text
}

// SettingContentHashBytes computes the content hash the plugin reports for a
// setting payload.
//
// On-disk configuration files (including UTF-8 Chinese .json/.css and binary
// assets) are hashed by the plugin's hashFileAsync over their raw UTF-8 bytes,
// so the server must do the same: EncodeHash32Bytes(raw) for every disk-backed
// payload. Only the virtual _localStorage/ entries are hashed as UTF-16 strings
// (EncodeHash32); use SettingContentHashBytesForPath to select that branch.
//
// An invalid base64 payload returns an error so callers can reject it instead of
// persisting a value the client can never reproduce.
// SettingContentHashBytes 计算插件对配置载荷上报的内容哈希。
//
// 磁盘配置文件（含 UTF-8 中文 .json/.css 与二进制资源）由插件的 hashFileAsync 按原始
// UTF-8 字节哈希，服务端必须一致：所有落盘载荷一律 EncodeHash32Bytes(raw)。只有
// _localStorage/ 虚拟项按 UTF-16 字符串算 EncodeHash32；用
// SettingContentHashBytesForPath 选择该分支。
func SettingContentHashBytes(content string) (string, error) {
	raw, _, err := DecodeSettingContentBytes(content)
	if err != nil {
		return "", err
	}
	return EncodeHash32Bytes(raw), nil
}

// SettingLocalStoragePathPrefix is the plugin's virtual configuration path prefix.
// Those entries never touch disk and are hashed as UTF-16 strings by the plugin.
// SettingLocalStoragePathPrefix 是插件虚拟配置路径前缀；这类条目不落盘，插件按
// UTF-16 字符串哈希。
const SettingLocalStoragePathPrefix = "_localStorage/"

// SettingContentHashBytesForPath hashes a setting the way the plugin reports it for
// the given path: _localStorage/ virtual entries keep the UTF-16 string hash, every
// disk-backed configuration file hashes its raw UTF-8 bytes.
// SettingContentHashBytesForPath 按插件对该路径上报的方式计算哈希：_localStorage/
// 虚拟项用 UTF-16 字符串哈希，落盘配置文件按原始 UTF-8 字节哈希。
func SettingContentHashBytesForPath(path, content string) (string, error) {
	if strings.HasPrefix(path, SettingLocalStoragePathPrefix) {
		return EncodeHash32(content), nil
	}
	return SettingContentHashBytes(content)
}
