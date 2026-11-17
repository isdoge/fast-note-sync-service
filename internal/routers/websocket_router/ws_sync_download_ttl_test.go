package websocket_router

import (
	"testing"
	"time"
)

func TestSyncDownloadExpiredKeepsLiveConnection(t *testing.T) {
	now := time.Now()
	open := make(chan struct{})
	e := &syncDownloadEntry{UpdatedAt: now.Add(-30 * time.Minute), ConnDone: open}
	if syncDownloadExpired(e, now) {
		t.Fatal("entry of a live connection must survive past the idle TTL while the client processes a page")
	}
	close(open)
	if !syncDownloadExpired(e, now) {
		t.Fatal("entry must be evicted once its connection is closed")
	}
}

func TestSyncDownloadExpiredFallbacks(t *testing.T) {
	now := time.Now()
	if syncDownloadExpired(&syncDownloadEntry{UpdatedAt: now.Add(-5 * time.Minute)}, now) {
		t.Fatal("recently updated entry must not be evicted")
	}
	if !syncDownloadExpired(&syncDownloadEntry{UpdatedAt: now.Add(-11 * time.Minute)}, now) {
		t.Fatal("entry without a connection signal keeps the old idle TTL")
	}
	live := make(chan struct{})
	if !syncDownloadExpired(&syncDownloadEntry{UpdatedAt: now.Add(-25 * time.Hour), ConnDone: live}, now) {
		t.Fatal("max idle cap applies even to live connections")
	}
}

func TestEvictExpiredSyncDownloads(t *testing.T) {
	now := time.Now()
	live := make(chan struct{})
	closed := make(chan struct{})
	close(closed)
	syncDownloadCacheMap.Store("ttl-live_file", &syncDownloadEntry{UpdatedAt: now.Add(-20 * time.Minute), ConnDone: live})
	syncDownloadCacheMap.Store("ttl-closed_file", &syncDownloadEntry{UpdatedAt: now.Add(-20 * time.Minute), ConnDone: closed})
	defer syncDownloadCacheMap.Delete("ttl-live_file")
	defer syncDownloadCacheMap.Delete("ttl-closed_file")
	evictExpiredSyncDownloads(now)
	if _, ok := syncDownloadGet("ttl-live", "file"); !ok {
		t.Fatal("live entry was evicted")
	}
	if _, ok := syncDownloadGet("ttl-closed", "file"); ok {
		t.Fatal("closed entry was kept")
	}
}

// 同一连接同一类型开始新一轮同步时，旧轮的下行条目必须被删除；其他连接、其他类型、当前轮不受影响。
func TestSyncDownloadDropStale(t *testing.T) {
	connA := make(chan struct{})
	connB := make(chan struct{})
	entries := map[string]*syncDownloadEntry{
		"drop-old_note":   {Context: "drop-old", TypeName: "note", ConnDone: connA},
		"drop-new_note":   {Context: "drop-new", TypeName: "note", ConnDone: connA},
		"drop-old_file":   {Context: "drop-old", TypeName: "file", ConnDone: connA},
		"drop-other_note": {Context: "drop-other", TypeName: "note", ConnDone: connB},
		"drop-nil_note":   {Context: "drop-nil", TypeName: "note"},
	}
	for k, e := range entries {
		syncDownloadCacheMap.Store(k, e)
	}
	t.Cleanup(func() {
		for k := range entries {
			syncDownloadCacheMap.Delete(k)
		}
	})

	syncDownloadDropStale(connA, "note", "drop-new")

	if _, ok := syncDownloadCacheMap.Load("drop-old_note"); ok {
		t.Fatal("old round entry of the same connection and type must be dropped")
	}
	for _, k := range []string{"drop-new_note", "drop-old_file", "drop-other_note", "drop-nil_note"} {
		if _, ok := syncDownloadCacheMap.Load(k); !ok {
			t.Fatalf("entry %s must be kept", k)
		}
	}

	// 无连接信号时不做任何清理
	syncDownloadDropStale(nil, "note", "drop-new")
	if _, ok := syncDownloadCacheMap.Load("drop-nil_note"); !ok {
		t.Fatal("nil connection signal must not drop anything")
	}
}
