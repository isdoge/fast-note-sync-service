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
