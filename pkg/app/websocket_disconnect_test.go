package app

import (
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/lxzan/gws"
)

// 验证正常关闭码及包装错误，并保持原有断开错误的判定
// Verify normal close codes and wrapped errors while preserving existing disconnect classification
func TestIsNormalDisconnectError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "normal closure", err: &gws.CloseError{Code: 1000}, want: true},
		{name: "going away", err: &gws.CloseError{Code: 1001}, want: true},
		{name: "wrapped going away", err: fmt.Errorf("read: %w", &gws.CloseError{Code: 1001}), want: true},
		{name: "protocol error", err: &gws.CloseError{Code: 1002}, want: false},
		{name: "no status code", err: &gws.CloseError{Code: 1005}, want: false},
		{name: "abnormal closure", err: &gws.CloseError{Code: 1006}, want: false},
		{name: "policy violation", err: &gws.CloseError{Code: 1008}, want: false},
		{name: "message too big", err: &gws.CloseError{Code: 1009}, want: false},
		{name: "internal error", err: &gws.CloseError{Code: 1011}, want: false},
		{name: "wrapped internal error", err: fmt.Errorf("read: %w", &gws.CloseError{Code: 1011}), want: false},
		// 识别出关闭码后以关闭码为准：reason 里的网络错误文本不能把异常码降级为正常。
		{name: "abnormal code with network-like reason", err: &gws.CloseError{Code: 1011, Reason: []byte("i/o timeout")}, want: false},
		// 连续广播失败被服务端主动断开（1000 + broadcast failed）属于异常。
		{name: "broadcast failure close", err: &gws.CloseError{Code: 1000, Reason: []byte("broadcast failed")}, want: false},
		{name: "wrapped broadcast failure close", err: fmt.Errorf("read: %w", &gws.CloseError{Code: 1000, Reason: []byte("broadcast failed")}), want: false},
		{name: "nil", err: nil, want: true},
		{name: "EOF", err: io.EOF, want: true},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF, want: true},
		{name: "connection reset", err: errors.New("read: connection reset by peer"), want: true},
		{name: "broken pipe", err: errors.New("write: broken pipe"), want: true},
		{name: "timeout", err: errors.New("read: i/o timeout"), want: true},
		{name: "closed network connection", err: errors.New("use of closed network connection"), want: true},
		{name: "unexpected EOF string", err: errors.New("read: unexpected EOF"), want: true},
		{name: "other error", err: errors.New("other error"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNormalDisconnectError(tt.err); got != tt.want {
				t.Fatalf("isNormalDisconnectError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// 连续广播失败被主动断开的连接单独识别，供 OnClose 用 Warn 记录。
func TestIsBroadcastFailureDisconnect(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"broadcast failure", &gws.CloseError{Code: 1000, Reason: []byte("broadcast failed")}, true},
		{"wrapped broadcast failure", fmt.Errorf("read: %w", &gws.CloseError{Code: 1000, Reason: []byte("broadcast failed")}), true},
		{"normal closure", &gws.CloseError{Code: 1000}, false},
		{"other reason", &gws.CloseError{Code: 1000, Reason: []byte("ClientClose")}, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBroadcastFailureDisconnect(tc.err); got != tc.want {
				t.Fatalf("isBroadcastFailureDisconnect(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
