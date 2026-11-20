package app

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
)

type pathBindItem struct {
	Path        string         `json:"path" form:"path"`
	PathHash    string         `json:"pathHash" form:"pathHash"`
	OldPath     string         `json:"oldPath" form:"oldPath"`
	OldPathHash string         `json:"oldPathHash" form:"oldPathHash"`
	List        []pathBindItem `json:"list"`
}

func TestPathBindingNormalizesRESTAndWebsocket(t *testing.T) {
	body := `{"path":"testdir\\note.md","pathHash":"old","oldPath":"old\\note.md","oldPathHash":"old2","list":[{"path":"a\\\\b.md","pathHash":"stale"},{"path":"plain.md","pathHash":"client-hash"}]}`
	check := func(t *testing.T, req pathBindItem) {
		t.Helper()
		if req.Path != "testdir/note.md" || req.PathHash != util.EncodeHash32(req.Path) || req.OldPath != "old/note.md" || req.OldPathHash != util.EncodeHash32(req.OldPath) {
			t.Fatalf("paths/hash not normalized: %+v", req)
		}
		if req.List[0].Path != "a/b.md" || req.List[0].PathHash != util.EncodeHash32("a/b.md") || req.List[1].PathHash != "client-hash" {
			t.Fatalf("inventory not normalized: %+v", req.List)
		}
	}
	t.Run("REST JSON", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		var req pathBindItem
		if ok, errs := BindAndValid(c, &req); !ok {
			t.Fatal(errs)
		}
		check(t, req)
	})
	for _, binary := range []bool{false, true} {
		name := "WS JSON"
		if binary {
			name = "WS protobuf decoded DTO"
		}
		t.Run(name, func(t *testing.T) {
			c := &WebsocketClient{Server: &WebsocketServer{}, app: vaultBindApp{}}
			data := []byte(body)
			if binary {
				c.useProtobuf = true
				data = []byte{1}
				c.Server.ProtobufDecoder = func(_ string, _ []byte, obj any) (bool, error) {
					*(obj.(*pathBindItem)) = pathBindItem{Path: `testdir\note.md`, PathHash: "old", OldPath: `old\note.md`, OldPathHash: "old2", List: []pathBindItem{{Path: `a\\b.md`, PathHash: "stale"}, {Path: "plain.md", PathHash: "client-hash"}}}
					return true, nil
				}
			}
			var req pathBindItem
			if ok, errs := c.BindAndValidWithAction("NoteSync", data, &req); !ok {
				t.Fatal(errs)
			}
			check(t, req)
		})
	}
}

func TestPathBindingQueryAndTraversal(t *testing.T) {
	for _, query := range []string{"?path=a%5Cb.md&pathHash=stale", "?path=a%5C%5Cb.md&pathHash=stale"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/"+query, nil)
		var req pathBindItem
		if ok, errs := BindAndValid(c, &req); !ok {
			t.Fatal(errs)
		}
		if req.Path != "a/b.md" || req.PathHash != util.EncodeHash32(req.Path) {
			t.Fatalf("query: %+v", req)
		}
	}
	for _, path := range []string{`\`, `\\`, `a\..\b`, `a\/..\b`, `a\` + "\x00" + `b`} {
		data, _ := json.Marshal(pathBindItem{Path: path, PathHash: "stale"})
		c := &WebsocketClient{Server: &WebsocketServer{}, app: vaultBindApp{}}
		var req pathBindItem
		if ok, _ := c.BindAndValidWithAction("NoteModify", data, &req); ok {
			t.Errorf("accepted %q", path)
		}
	}
	// A database filesystem Path is outside the vault-path contract.
	admin := struct {
		Path string `json:"path"`
	}{Path: `C:\database\main.db`}
	if err := normalizeBoundPaths(&admin); err != nil {
		t.Fatal(err)
	}
	if admin.Path != `C:\database\main.db` {
		t.Fatal("admin database path changed")
	}
}
