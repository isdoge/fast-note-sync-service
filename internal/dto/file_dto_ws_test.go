package dto

import "testing"

// TestFileDTO_ToSyncModifyMessage guards that REST upload and MCP file_write
// broadcasts carry the revision token together with the other file fields, so
// receiving devices can record the update as a confirmed baseline.
// TestFileDTO_ToSyncModifyMessage 保证 REST 上传与 MCP file_write 的广播带上版本
// 令牌和其余文件字段，使接收设备能把这次更新记为已确认基线。
func TestFileDTO_ToSyncModifyMessage(t *testing.T) {
	const rev = "0123456789abcdef0123456789abcdef"
	file := &FileDTO{
		Path:             "notes/a.md",
		PathHash:         "fhash123",
		ContentHash:      "chash456",
		Size:             1024,
		Ctime:            1700000000,
		Mtime:            1700000001,
		UpdatedTimestamp: 1700000002,
		Revision:         rev,
	}

	msg := file.ToSyncModifyMessage()
	if msg.Path != file.Path ||
		msg.PathHash != file.PathHash ||
		msg.ContentHash != file.ContentHash ||
		msg.Size != file.Size ||
		msg.Ctime != file.Ctime ||
		msg.Mtime != file.Mtime ||
		msg.UpdatedTimestamp != file.UpdatedTimestamp {
		t.Fatalf("ToSyncModifyMessage lost file fields: %+v", msg)
	}
	if msg.Revision != rev {
		t.Fatalf("ToSyncModifyMessage revision = %q, want %q", msg.Revision, rev)
	}

	var nilFile *FileDTO
	if zero := nilFile.ToSyncModifyMessage(); zero != (FileSyncModifyMessage{}) {
		t.Fatalf("nil receiver should yield zero value, got %+v", zero)
	}
}
