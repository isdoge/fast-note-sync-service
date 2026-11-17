package timex

import (
	"testing"
	"time"
)

func TestTime_UnixMethods(t *testing.T) {
	// Create a fixed time
	// 创建一个固定时间
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	tt := Time(now)

	// Test Unix()
	if tt.Unix() != now.Unix() {
		t.Errorf("Unix() = %v, want %v", tt.Unix(), now.Unix())
	}

	// Test UnixMilli()
	if tt.UnixMilli() != now.UnixMilli() {
		t.Errorf("UnixMilli() = %v, want %v", tt.UnixMilli(), now.UnixMilli())
	}

	// Test UnixMicro()
	if tt.UnixMicro() != now.UnixMicro() {
		t.Errorf("UnixMicro() = %v, want %v", tt.UnixMicro(), now.UnixMicro())
	}

	// Test UnixNano()
	if tt.UnixNano() != now.UnixNano() {
		t.Errorf("UnixNano() = %v, want %v", tt.UnixNano(), now.UnixNano())
	}

	// Verify it's not returning time.Now() by waiting a bit
	// 通过等待一会确认它不是返回 time.Now()
	time.Sleep(10 * time.Millisecond)
	if tt.Unix() != now.Unix() {
		t.Errorf("Unix() changed after sleep, it should be static. got %v, want %v", tt.Unix(), now.Unix())
	}
}

func TestNormalizeMilli(t *testing.T) {
	cases := []struct {
		name string
		in   int64
		want int64
	}{
		{"zero stays zero", 0, 0},
		{"negative unchanged", -1651719914, -1651719914},
		{"seconds converted", 1651719914, 1651719914000},
		{"small seconds converted", 1, 1000},
		{"largest seconds value converted", 99_999_999_999, 99_999_999_999_000},
		{"threshold treated as milliseconds", 100_000_000_000, 100_000_000_000},
		{"milliseconds unchanged", 1651719914000, 1651719914000},
		{"now milliseconds unchanged", 1785129518518, 1785129518518},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeMilli(tc.in); got != tc.want {
				t.Fatalf("NormalizeMilli(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
