package websocket_router

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/lxzan/gws"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type syncDeleteEvents struct {
	gws.BuiltinEventHandler
	messages chan string
}

func (e *syncDeleteEvents) OnMessage(_ *gws.Conn, msg *gws.Message) {
	defer msg.Close()
	e.messages <- msg.Data.String()
}

type syncDeleteResponseWriter struct {
	*httptest.ResponseRecorder
	conn net.Conn
}

func (w *syncDeleteResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

// syncWriteHookConn 在服务端每次写出前回调，用来检查"某条消息写出时"的服务端状态。
type syncWriteHookConn struct {
	net.Conn
	onWrite func([]byte)
}

func (c *syncWriteHookConn) Write(b []byte) (int, error) {
	if c.onWrite != nil {
		c.onWrite(b)
	}
	return c.Conn.Write(b)
}

// newSyncDeletePeer 用内存管道驱动真实 WebSocket 收发，不监听端口。
func newSyncDeletePeer(t *testing.T, testApp *app.App) (*pkgapp.WebsocketClient, <-chan string) {
	return newSyncDeletePeerWithHook(t, testApp, nil)
}

func newSyncDeletePeerWithHook(t *testing.T, testApp *app.App, onServerWrite func([]byte)) (*pkgapp.WebsocketClient, <-chan string) {
	t.Helper()
	rawServerConn, peerConn := net.Pipe()
	var serverConn net.Conn = &syncWriteHookConn{Conn: rawServerConn, onWrite: onServerWrite}
	t.Cleanup(func() { _ = serverConn.Close(); _ = peerConn.Close() })
	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{PingWait: 60}, testApp)
	clients := make(chan *pkgapp.WebsocketClient, 1)
	wss.UseWithoutAuth("SyncDeleteFixture", func(c *pkgapp.WebsocketClient, _ *pkgapp.WebSocketMessage) {
		c.User = &pkgapp.UserEntity{UID: 1}
		clients <- c
	})
	upgradeDone := make(chan error, 1)
	go func() {
		req, err := http.ReadRequest(bufio.NewReader(serverConn))
		if err == nil {
			ctx, _ := gin.CreateTestContext(&syncDeleteResponseWriter{httptest.NewRecorder(), serverConn})
			ctx.Request = req
			wss.Run()(ctx)
		}
		upgradeDone <- err
	}()
	events := &syncDeleteEvents{messages: make(chan string, 16)}
	peer, _, err := gws.NewClientFromConn(events, &gws.ClientOption{Addr: "ws://sync.test/sync"}, peerConn)
	require.NoError(t, err)
	require.NoError(t, <-upgradeDone)
	go peer.ReadLoop()
	require.NoError(t, peer.WriteMessage(gws.OpcodeText, []byte("SyncDeleteFixture|{}")))
	select {
	case c := <-clients:
		t.Cleanup(func() {
			c.WsCancel()
			_ = peerConn.Close()
			_ = serverConn.Close()
			wss.WaitAllClosed(time.Second)
		})
		return c, events.messages
	case <-time.After(time.Second):
		t.Fatal("未取得测试客户端")
		return nil, nil
	}
}

func syncDeleteMessage(t *testing.T, messages <-chan string, action string) map[string]any {
	t.Helper()
	select {
	case msg := <-messages:
		prefix, payload, ok := strings.Cut(msg, "|")
		require.True(t, ok)
		require.Equal(t, action, prefix)
		var res map[string]any
		require.NoError(t, json.Unmarshal([]byte(payload), &res))
		return res
	case <-time.After(time.Second):
		t.Fatalf("未收到 %s", action)
		return nil
	}
}

// TestSyncDelete_IdempotentDoesNotBroadcast 覆盖笔记/附件批量删除的不存在、墓碑、
// 预查后并发删除以及真实删除；检查其他端实际收包和发起端 End 计数。
func TestSyncDelete_IdempotentDoesNotBroadcast(t *testing.T) {
	for _, kind := range []string{"note", "file"} {
		for _, state := range []string{"missing", "deleted", "unchanged-after-check", "live"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				noteSvc := new(svcmocks.MockNoteService)
				fileSvc := new(svcmocks.MockFileService)
				vaultSvc := new(svcmocks.MockVaultService)
				vaultSvc.On("GetOrCreate", mock.Anything, int64(1), "vault").Return(nil, nil)
				testApp := app.NewTestApp(&app.Services{NoteService: noteSvc, FileService: fileSvc, VaultService: vaultSvc})
				testApp.Config().App.SyncDownChunkNum = 1
				c, originMessages := newSyncDeletePeer(t, testApp)
				other, otherMessages := newSyncDeletePeer(t, testApp)
				c.UserClients = pkgapp.ConnStorage{nil: other}
				exists := state == "live" || state == "unchanged-after-check"
				unchanged := state == "unchanged-after-check"

				// 客户端主动删除的路径应被排除，另两个墓碑保留为两页下发。
				if kind == "note" {
					list := []*dto.NoteDTO{
						{Path: "pending", PathHash: "pending-hash", Action: "delete"},
						{Path: "pending-2", PathHash: "pending-hash-2", Action: "delete"},
					}
					if state != "missing" {
						action := "delete"
						if exists {
							action = "create"
						}
						list = append(list, &dto.NoteDTO{Path: "gone", PathHash: "gone-hash", Action: action})
					}
					noteSvc.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).
						Return(list, nil)
					noteSvc.On("ExistsPathsBatch", mock.Anything, int64(1), "vault", []string{"gone"}).
						Return(map[string]bool{"gone": exists}, nil)
					if exists {
						noteSvc.On("Delete", mock.Anything, int64(1), mock.Anything).
							Return(&dto.NoteDTO{Path: "gone", PathHash: "gone-hash", Action: "delete", Unchanged: unchanged}, nil)
					}
					NewNoteWSHandler(testApp).doNoteSync(c, &dto.NoteSyncRequest{
						Context: "delete-note-" + state, Vault: "vault",
						DelNotes: []dto.NoteSyncDelNote{{Path: "gone", PathHash: "gone-hash"}},
					})
				} else {
					list := []*dto.FileDTO{
						{Path: "pending", PathHash: "pending-hash", Action: "delete"},
						{Path: "pending-2", PathHash: "pending-hash-2", Action: "delete"},
					}
					if state != "missing" {
						action := "delete"
						if exists {
							action = "create"
						}
						list = append(list, &dto.FileDTO{Path: "gone", PathHash: "gone-hash", Action: action})
					}
					fileSvc.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).
						Return(list, nil)
					var checked *dto.FileDTO
					var getErr error
					if state == "missing" {
						getErr = gorm.ErrRecordNotFound
					} else {
						checked = &dto.FileDTO{Action: "create"}
						if state == "deleted" {
							checked.Action = "delete"
						}
					}
					fileSvc.On("Get", mock.Anything, int64(1), mock.Anything).Return(checked, getErr)
					if exists {
						fileSvc.On("Delete", mock.Anything, int64(1), mock.Anything).
							Return(&dto.FileDTO{Path: "gone", PathHash: "gone-hash", Action: "delete", Unchanged: unchanged}, nil)
					}
					NewFileWSHandler(testApp).doFileSync(c, &dto.FileSyncRequest{
						Context: "delete-file-" + state, Vault: "vault",
						DelFiles: []dto.FileSyncDelFile{{Path: "gone", PathHash: "gone-hash"}},
					})
				}

				endAction, deleteAction := NoteSyncEnd, NoteSyncDelete
				if kind == "file" {
					endAction, deleteAction = FileSyncEnd, FileSyncDelete
				}
				end := syncDeleteMessage(t, originMessages, endAction)
				counts := end["data"].(map[string]any)
				require.Equal(t, float64(2), counts["needDeleteCount"])
				for _, name := range []string{"needUploadCount", "needModifyCount", "needSyncMtimeCount"} {
					require.Equal(t, float64(0), counts[name], name)
				}
				syncContext := "delete-" + kind + "-" + state
				entry, ok := syncDownloadGet(syncContext, kind)
				require.True(t, ok, "必须保留分页队列")
				t.Cleanup(func() { syncDownloadDelete(syncContext, kind) })
				require.Len(t, entry.MessageQueue, 2)
				require.Equal(t, deleteAction, entry.MessageQueue[0].Action)
				require.Zero(t, entry.SentPage, "等待 SyncPageAck 后才发送")
				require.Zero(t, entry.AckedPage)

				// 首拉 -1 下发第一页，Ack 0 后才下发第二页并清除缓存。
				pageAction := NoteSyncPage
				ack := NewNoteWSHandler(testApp).NoteSyncPageAck
				if kind == "file" {
					pageAction = FileSyncPage
					ack = NewFileWSHandler(testApp).FileSyncPageAck
				}
				for i, hash := range []string{"pending-hash", "pending-hash-2"} {
					payload, err := json.Marshal(dto.SyncPageAckRequest{
						Context: syncContext, Vault: "vault", PageIndex: i - 1,
					})
					require.NoError(t, err)
					ack(c, &pkgapp.WebSocketMessage{Data: payload})
					page := syncDeleteMessage(t, originMessages, pageAction)
					require.Equal(t, float64(i), page["data"].(map[string]any)["pageIndex"])
					require.Equal(t, i == 1, page["data"].(map[string]any)["isLast"])
					deletion := syncDeleteMessage(t, originMessages, deleteAction)
					require.Equal(t, hash, deletion["data"].(map[string]any)["pathHash"])
				}
				_, ok = syncDownloadGet(syncContext, kind)
				require.False(t, ok, "末页发送后应清除分页缓存")

				if state == "live" {
					deletion := syncDeleteMessage(t, otherMessages, deleteAction)
					require.Equal(t, "gone-hash", deletion["data"].(map[string]any)["pathHash"])
				} else {
					select {
					case msg := <-otherMessages:
						t.Errorf("幂等删除不应广播，实际收到 %s", msg)
					case <-time.After(100 * time.Millisecond):
					}
				}
				noteSvc.AssertExpectations(t)
				fileSvc.AssertExpectations(t)
				vaultSvc.AssertExpectations(t)
			})
		}
	}
}

// TestSyncEnd_PagedQueueRegisteredBeforeEnd 结束消息写出的那一刻，分页队列必须已经登记：
// 客户端收到结束消息会立刻回首个分页确认，并行处理下它可能先于登记被处理而被丢弃。
func TestSyncEnd_PagedQueueRegisteredBeforeEnd(t *testing.T) {
	for _, kind := range []string{"note", "file"} {
		t.Run(kind, func(t *testing.T) {
			noteSvc := new(svcmocks.MockNoteService)
			fileSvc := new(svcmocks.MockFileService)
			vaultSvc := new(svcmocks.MockVaultService)
			vaultSvc.On("GetOrCreate", mock.Anything, int64(1), "vault").Return(nil, nil)
			testApp := app.NewTestApp(&app.Services{NoteService: noteSvc, FileService: fileSvc, VaultService: vaultSvc})
			syncContext := "end-order-" + kind
			endAction := NoteSyncEnd
			if kind == "file" {
				endAction = FileSyncEnd
			}
			var sawEnd, registeredAtEnd atomic.Bool
			c, messages := newSyncDeletePeerWithHook(t, testApp, func(b []byte) {
				if bytes.Contains(b, []byte(endAction+"|")) {
					sawEnd.Store(true)
					_, ok := syncDownloadGet(syncContext, kind)
					registeredAtEnd.Store(ok)
				}
			})
			t.Cleanup(func() { syncDownloadDelete(syncContext, kind) })
			if kind == "note" {
				noteSvc.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).
					Return([]*dto.NoteDTO{{Path: "pending", PathHash: "pending-hash", Action: "delete"}}, nil)
				NewNoteWSHandler(testApp).doNoteSync(c, &dto.NoteSyncRequest{Context: syncContext, Vault: "vault"})
			} else {
				fileSvc.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).
					Return([]*dto.FileDTO{{Path: "pending", PathHash: "pending-hash", Action: "delete"}}, nil)
				NewFileWSHandler(testApp).doFileSync(c, &dto.FileSyncRequest{Context: syncContext, Vault: "vault"})
			}
			syncDeleteMessage(t, messages, endAction)
			require.True(t, sawEnd.Load(), "未观察到结束消息写出")
			require.True(t, registeredAtEnd.Load(), "结束消息写出时分页队列尚未登记")
		})
	}
}
