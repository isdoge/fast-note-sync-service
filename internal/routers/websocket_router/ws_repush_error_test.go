package websocket_router

// RePush 只有在记录确实不存在（或本身是删除态）时才下发 *SyncDelete；
// 哈希歧义、数据库错误等其它查询失败必须回错误响应并带 data.path，不能让客户端
// 把服务端存活的笔记 / 附件 / 配置在本地删除。

import (
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRePush_OnlyNotFoundSendsDelete(t *testing.T) {
	t.Run("note database error returns error", func(t *testing.T) {
		m := newErrPathMocks(t)
		m.noteSvc.On("Get", mock.Anything, int64(1), mock.Anything).
			Return(nil, code.ErrorDBQuery.WithDetails("db down")).Once()
		c, messages := newSyncDeletePeer(t, m.testApp)
		NewNoteWSHandler(m.testApp).NoteRePush(c, wsMsg(NoteReceiveRePush, map[string]any{
			"vault": "v", "path": "/notes/a.md", "pathHash": "h1",
		}))

		res := syncDeleteMessage(t, messages, NoteReceiveRePush)
		require.Greater(t, res["code"], float64(299))
		data, ok := res["data"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "/notes/a.md", data["path"])
		m.noteSvc.AssertNotCalled(t, "Delete", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("note not found still sends delete", func(t *testing.T) {
		m := newErrPathMocks(t)
		m.noteSvc.On("Get", mock.Anything, int64(1), mock.Anything).
			Return(nil, code.ErrorNoteNotFound).Once()
		c, messages := newSyncDeletePeer(t, m.testApp)
		NewNoteWSHandler(m.testApp).NoteRePush(c, wsMsg(NoteReceiveRePush, map[string]any{
			"vault": "v", "path": "/notes/a.md", "pathHash": "h1",
		}))

		res := syncDeleteMessage(t, messages, string(NoteSyncDelete))
		require.Equal(t, float64(1), res["code"])
		require.Equal(t, "/notes/a.md", res["data"].(map[string]any)["path"])
	})

	t.Run("file database error returns error", func(t *testing.T) {
		m := newErrPathMocks(t)
		m.fileSvc.On("Get", mock.Anything, int64(1), mock.Anything).
			Return(nil, code.ErrorDBQuery.WithDetails("db down")).Once()
		c, messages := newSyncDeletePeer(t, m.testApp)
		NewFileWSHandler(m.testApp).FileRePush(c, wsMsg(FileReceiveRePush, map[string]any{
			"vault": "v", "path": "/img/a.png", "pathHash": "h2",
		}))

		res := syncDeleteMessage(t, messages, FileReceiveRePush)
		require.Greater(t, res["code"], float64(299))
		data, ok := res["data"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "/img/a.png", data["path"])
	})

	t.Run("file not found still sends delete", func(t *testing.T) {
		m := newErrPathMocks(t)
		m.fileSvc.On("Get", mock.Anything, int64(1), mock.Anything).
			Return((*dto.FileDTO)(nil), gorm.ErrRecordNotFound).Once()
		c, messages := newSyncDeletePeer(t, m.testApp)
		NewFileWSHandler(m.testApp).FileRePush(c, wsMsg(FileReceiveRePush, map[string]any{
			"vault": "v", "path": "/img/a.png", "pathHash": "h2",
		}))

		res := syncDeleteMessage(t, messages, string(FileSyncDelete))
		require.Equal(t, float64(1), res["code"])
	})

	t.Run("setting database error returns error", func(t *testing.T) {
		m := newErrPathMocks(t)
		m.settingSvc.On("Get", mock.Anything, int64(1), mock.Anything).
			Return(nil, code.ErrorDBQuery.WithDetails("db down")).Once()
		c, messages := newSyncDeletePeer(t, m.testApp)
		NewSettingWSHandler(m.testApp).SettingRePush(c, wsMsg(SettingReceiveRePush, map[string]any{
			"vault": "v", "path": ".obsidian/app.json", "pathHash": "h3",
		}))

		res := syncDeleteMessage(t, messages, SettingReceiveRePush)
		require.Greater(t, res["code"], float64(299))
		data, ok := res["data"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, ".obsidian/app.json", data["path"])
	})

	t.Run("setting not found still sends delete", func(t *testing.T) {
		m := newErrPathMocks(t)
		m.settingSvc.On("Get", mock.Anything, int64(1), mock.Anything).
			Return(nil, code.ErrorSettingNotFound).Once()
		c, messages := newSyncDeletePeer(t, m.testApp)
		NewSettingWSHandler(m.testApp).SettingRePush(c, wsMsg(SettingReceiveRePush, map[string]any{
			"vault": "v", "path": ".obsidian/app.json", "pathHash": "h3",
		}))

		res := syncDeleteMessage(t, messages, string(SettingSyncDelete))
		require.Equal(t, float64(1), res["code"])
	})
}
