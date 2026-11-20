package util

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

var pngHeader = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00}

// TestEncodeDecodeSettingContentRoundTrip 覆盖二进制/文本载荷的编码-解码往返：
// 含 NUL 的 PNG 头、全零字节、空文件、普通 JSON、以二进制前缀开头的普通文本。
func TestEncodeDecodeSettingContentRoundTrip(t *testing.T) {
	cases := []struct {
		name         string
		raw          []byte
		wantEncoded  string // "" means "just check round trip", otherwise exact wire form
		wantIsBinary bool
	}{
		{name: "png header", raw: append(append([]byte{}, pngHeader...), []byte("body")...), wantIsBinary: true},
		{name: "all zero bytes", raw: make([]byte, 16), wantIsBinary: true},
		{name: "empty file", raw: []byte{}, wantEncoded: "", wantIsBinary: false},
		{name: "plain json", raw: []byte(`{"theme":"dark"}`), wantEncoded: `{"theme":"dark"}`, wantIsBinary: false},
		{name: "plain text with prefix", raw: []byte("fns-bin-v1:not-really"), wantEncoded: "fns-bin-v1:esc:fns-bin-v1:not-really", wantIsBinary: false},
		{name: "utf8 chinese", raw: []byte("字体配置"), wantEncoded: "字体配置", wantIsBinary: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded := EncodeSettingContentBytes(tc.raw)
			if tc.wantEncoded != "" && encoded != tc.wantEncoded {
				t.Fatalf("EncodeSettingContentBytes = %q, want %q", encoded, tc.wantEncoded)
			}
			if tc.wantIsBinary && !strings.HasPrefix(encoded, SettingContentB64Prefix) {
				t.Fatalf("binary payload must use %q prefix, got %q", SettingContentB64Prefix, encoded)
			}
			if !tc.wantIsBinary && strings.HasPrefix(encoded, SettingContentB64Prefix) {
				t.Fatalf("text payload must not use %q prefix, got %q", SettingContentB64Prefix, encoded)
			}

			decoded, isBinary, err := DecodeSettingContentBytes(encoded)
			if err != nil {
				t.Fatalf("DecodeSettingContentBytes(%q) error: %v", encoded, err)
			}
			if isBinary != tc.wantIsBinary {
				t.Fatalf("isBinary = %v, want %v", isBinary, tc.wantIsBinary)
			}
			if !bytes.Equal(decoded, tc.raw) {
				t.Fatalf("round trip mismatch: decoded %v, want %v", decoded, tc.raw)
			}
		})
	}
}

// TestDecodeSettingContentLegacyPassthrough 验证没有前缀的旧配置完全原样返回。
func TestDecodeSettingContentLegacyPassthrough(t *testing.T) {
	for _, content := range []string{"dark", "", `{"a":1}`, "多行\n文本", "fns-bin-v1"} {
		raw, isBinary, err := DecodeSettingContentBytes(content)
		if err != nil {
			t.Fatalf("DecodeSettingContentBytes(%q) error: %v", content, err)
		}
		if isBinary {
			t.Fatalf("DecodeSettingContentBytes(%q) reported binary for plain text", content)
		}
		if string(raw) != content {
			t.Fatalf("DecodeSettingContentBytes(%q) = %q, want unchanged", content, raw)
		}
	}
}

// TestDecodeSettingContentCorruptBase64 验证损坏的 base64 载荷返回错误而不是脏数据。
func TestDecodeSettingContentCorruptBase64(t *testing.T) {
	for _, content := range []string{
		SettingContentB64Prefix + "!!!not-base64!!!",
		SettingContentB64Prefix + "AAAA=",
		SettingContentB64Prefix + "a",
	} {
		if _, _, err := DecodeSettingContentBytes(content); err == nil {
			t.Fatalf("DecodeSettingContentBytes(%q) must fail on corrupt base64", content)
		}
	}
}

// TestDecodeSettingContentEscUnescapes 验证转义前缀还原成原始文本。
func TestDecodeSettingContentEscUnescapes(t *testing.T) {
	raw, isBinary, err := DecodeSettingContentBytes(SettingContentEscPrefix + "fns-bin-v1:hello")
	if err != nil {
		t.Fatal(err)
	}
	if isBinary {
		t.Fatal("escaped text must not be reported as binary")
	}
	if string(raw) != "fns-bin-v1:hello" {
		t.Fatalf("decoded %q", raw)
	}
}

// TestSettingContentHashBytes 验证回退哈希：二进制按原始字节、文本按原字符串，
// 且非法 base64 返回错误。
func TestSettingContentHashBytes(t *testing.T) {
	png := append(append([]byte{}, pngHeader...), []byte("pixels")...)
	b64 := SettingContentB64Prefix + base64.StdEncoding.EncodeToString(png)

	got, err := SettingContentHashBytes(b64)
	if err != nil {
		t.Fatalf("hash binary: %v", err)
	}
	if want := EncodeHash32Bytes(png); got != want {
		t.Fatalf("binary hash = %q, want raw-bytes hash %q", got, want)
	}

	got, err = SettingContentHashBytes("dark")
	if err != nil {
		t.Fatalf("hash text: %v", err)
	}
	if want := EncodeHash32("dark"); got != want {
		t.Fatalf("text hash = %q, want %q", got, want)
	}

	// 转义文本按未转义的原始文本哈希，与插件读取到的磁盘内容一致。
	got, err = SettingContentHashBytes(SettingContentEscPrefix + "fns-bin-v1:x")
	if err != nil {
		t.Fatalf("hash escaped text: %v", err)
	}
	if want := EncodeHash32("fns-bin-v1:x"); got != want {
		t.Fatalf("escaped text hash = %q, want %q", got, want)
	}

	if _, err := SettingContentHashBytes(SettingContentB64Prefix + "!!!"); err == nil {
		t.Fatal("corrupt base64 must return an error")
	}
}

// TestSettingContentHashBytesForPath_MatchesPlugin 覆盖 P1-07：磁盘配置文件（含 UTF-8 中文
// JSON/CSS）必须按原始 UTF-8 字节哈希，与插件 hashFileAsync 一致；只有 _localStorage/
// 虚拟项按 UTF-16 字符串哈希。期望值由插件侧 helpers.ts 的同款算法算出。
// Disk-backed configs hash raw UTF-8 bytes exactly like the plugin's hashFileAsync;
// only _localStorage/ virtual entries keep the UTF-16 string hash.
func TestSettingContentHashBytesForPath_MatchesPlugin(t *testing.T) {
	cases := []struct {
		path    string
		content string
		want    string
	}{
		// 插件 hashArrayBuffer(new TextEncoder().encode(content)) 的结果
		{"User/Theme.json", `{"主题":"深色","字体":"思源黑体"}`, "-1336091758"},
		{"snippets/中文.css", "/* 注释：中文样式 */\nbody{color:red}", "1595614919"},
		{"app.json", "dark", "3075958"},
		{"fonts/note.txt", "字体配置", "1264281938"},
		{"empty.json", "", "0"},
		// 纯 ASCII 时字节哈希与字符串哈希相同
		{"a.json", "dark", "3075958"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got, err := SettingContentHashBytesForPath(tc.path, tc.content)
			if err != nil {
				t.Fatalf("SettingContentHashBytesForPath(%q) error: %v", tc.path, err)
			}
			if got != tc.want {
				t.Fatalf("hash(%q) = %q, want %q (plugin hashFileAsync parity)", tc.content, got, tc.want)
			}
		})
	}

	// _localStorage/ 虚拟项：插件用 hashContent（UTF-16 字符串）。
	got, err := SettingContentHashBytesForPath("_localStorage/some-key", "字体配置")
	if err != nil {
		t.Fatalf("virtual path hash error: %v", err)
	}
	if got != "717303709" {
		t.Fatalf("virtual _localStorage hash = %q, want 717303709", got)
	}
	// 空虚拟项：插件 hashContent("") = "0"。
	got, err = SettingContentHashBytesForPath("_localStorage/empty", "")
	if err != nil || got != "0" {
		t.Fatalf("empty virtual hash = %q (err=%v), want 0", got, err)
	}
}

// TestSettingContentHashBytes_EmptyMatchesPlugin 空文本在插件里是 "0"，服务端必须一致。
func TestSettingContentHashBytes_EmptyMatchesPlugin(t *testing.T) {
	got, err := SettingContentHashBytes("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "0" {
		t.Fatalf("EncodeHash32Bytes([]) = %q, want 0", got)
	}
}
