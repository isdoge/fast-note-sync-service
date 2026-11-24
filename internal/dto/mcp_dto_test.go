package dto

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestToMcpFileDTOCarriesRevision guards the MCP conditional-write loop:
// file_list / file_get_info / file_write must hand back the server revision so
// a client can pass it as base_revision on the next write.
// TestToMcpFileDTOCarriesRevision 保证 MCP 条件写闭环：文件读/列表/写结果必须回传
// 服务端 revision，客户端才能把它作为下一次写入的 base_revision。
func TestToMcpFileDTOCarriesRevision(t *testing.T) {
	const rev = "0123456789abcdef0123456789abcdef"
	src := &FileDTO{Path: "assets/a.png", PathHash: "ph", ContentHash: "ch", Revision: rev}

	got := src.ToMcpFileDTO()
	if got == nil {
		t.Fatal("ToMcpFileDTO returned nil for non-nil input")
	}
	if got.Revision != rev {
		t.Fatalf("Revision = %q, want %q", got.Revision, rev)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"revision":"`+rev+`"`) {
		t.Fatalf("serialized MCP file DTO lacks revision: %s", raw)
	}
}

// TestToMcpFileDTOOmitsEmptyRevision keeps the output shape unchanged for rows
// that have no revision yet (omitempty).
// TestToMcpFileDTOOmitsEmptyRevision 确认尚无 revision 的行输出形状不变（omitempty）。
func TestToMcpFileDTOOmitsEmptyRevision(t *testing.T) {
	raw, err := json.Marshal((&FileDTO{Path: "x.pdf"}).ToMcpFileDTO())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"revision"`) {
		t.Fatalf("empty revision should be omitted: %s", raw)
	}
}

func TestToMcpFileDTONil(t *testing.T) {
	var f *FileDTO
	if f.ToMcpFileDTO() != nil {
		t.Fatal("nil FileDTO should convert to nil")
	}
}
