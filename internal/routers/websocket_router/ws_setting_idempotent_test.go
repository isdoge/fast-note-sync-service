package websocket_router

// SettingModify 缺省 Mtime 的幂等语义：内容与服务端一致时以 0 做 UpdateCheck，
// 走 UpdateMtime 分支回 Ack（不改库、不广播），不得提前用当前时间兜底把内容未变的
// 配置写成新时间戳；只有确需写入时才用服务端当前时间兜底。

import (
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestSettingModify_MissingMtimeIdempotent(t *testing.T) {
	t.Run("unchanged content neither writes nor broadcasts", func(t *testing.T) {
		m := newErrPathMocks(t)
		m.settingSvc.On("UpdateCheck", mock.Anything, int64(1), mock.MatchedBy(func(req *dto.SettingUpdateCheckRequest) bool {
			// 缺省 Mtime 必须以 0 进入检查，而不是当前时间。
			return req.Mtime == 0
		})).Return("UpdateMtime", &dto.SettingDTO{
			Path: ".workspace/app.json", PathHash: "h", UpdatedTimestamp: 77,
		}, nil).Once()

		c, messages := newSyncDeletePeer(t, m.testApp)
		NewSettingWSHandler(m.testApp).SettingModify(c, wsMsg(SettingReceiveModify, map[string]any{
			"vault": "v", "path": ".workspace/app.json", "pathHash": "h",
			"content": "{}", "contentHash": "ch",
		}))

		res := syncDeleteMessage(t, messages, string(SettingModifyAck))
		require.Equal(t, float64(77), res["data"].(map[string]any)["lastTime"])
		m.settingSvc.AssertNotCalled(t, "ModifyOrCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		select {
		case extra := <-messages:
			t.Fatalf("幂等返回不应再有广播或其它消息：%q", extra)
		case <-time.After(100 * time.Millisecond):
		}
	})

	t.Run("actual write still defaults mtime", func(t *testing.T) {
		m := newErrPathMocks(t)
		m.settingSvc.On("UpdateCheck", mock.Anything, int64(1), mock.MatchedBy(func(req *dto.SettingUpdateCheckRequest) bool {
			return req.Mtime == 0
		})).Return("UpdateContent", &dto.SettingDTO{
			Path: ".workspace/app.json", PathHash: "h", ContentHash: "old", UpdatedTimestamp: 10,
		}, nil).Once()
		m.settingSvc.On("ModifyOrCreate", mock.Anything, int64(1), mock.MatchedBy(func(req *dto.SettingModifyOrCreateRequest) bool {
			return req.Mtime > 0
		}), true).Return(false, &dto.SettingDTO{
			Path: ".workspace/app.json", PathHash: "h", ContentHash: "ch", UpdatedTimestamp: 100,
		}, nil).Once()

		c, messages := newSyncDeletePeer(t, m.testApp)
		NewSettingWSHandler(m.testApp).SettingModify(c, wsMsg(SettingReceiveModify, map[string]any{
			"vault": "v", "path": ".workspace/app.json", "pathHash": "h",
			"content": "{}", "contentHash": "ch",
		}))

		res := syncDeleteMessage(t, messages, string(SettingModifyAck))
		require.Equal(t, float64(100), res["data"].(map[string]any)["lastTime"])
	})
}
