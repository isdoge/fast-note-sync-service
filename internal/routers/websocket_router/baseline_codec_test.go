package websocket_router

// Stage C protocol-preparation tests: every field added for the sync baseline protocol must
// survive both wire encodings with identical meaning. The two things that must never diverge:
//   - the empty-text content hash "0" is a REAL baseline (helpers.ts hashContent("") === "0"),
//     so it must never be collapsed into "no baseline";
//   - baseHashMissing=true and baseHash="" must normalize to the same state in JSON and PB,
//     and missing must win when the two contradict each other.
//
// 阶段 C 协议准备测试：新增的基线字段在两种编码下必须具有同一含义。绝不能分歧的两点：
//   - 空文本内容哈希 "0" 是有效基线（helpers.ts hashContent("") === "0"），不得折叠为"无基线"；
//   - baseHashMissing=true 与 baseHash="" 在 JSON 与 PB 下必须归一为同一状态，矛盾时以 missing 为准。

import (
	"encoding/json"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	v1 "github.com/haierkeys/fast-note-sync-service/internal/proto/v1"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// encodeRequest wraps a payload in the WSMessage envelope the server receives.
func encodeRequest(t *testing.T, action string, msg proto.Message) []byte {
	t.Helper()
	inner, err := proto.Marshal(msg)
	require.NoError(t, err)
	envelope, err := proto.Marshal(&v1.WSMessage{Type: action, Data: inner})
	require.NoError(t, err)
	_, payload, err := DeReceivePacket(envelope)
	require.NoError(t, err)
	return payload
}

func decodePBRequest[T any](t *testing.T, action WebSocketReceiveAction, msg proto.Message, dest *T) {
	t.Helper()
	payload := encodeRequest(t, string(action), msg)
	ok, err := DeReceiveProtobufToDTO(action, payload, dest)
	require.NoError(t, err)
	require.True(t, ok, "action %s must be handled by the Protobuf decoder", action)
}

// decodeJSONRequest mirrors the JSON path (json.Unmarshal + the same normalization hook the
// server applies in validateBoundObject).
func decodeJSONRequest[T any](t *testing.T, raw string, dest *T) {
	t.Helper()
	require.NoError(t, json.Unmarshal([]byte(raw), dest))
	normalizeSyncBaseline(dest)
}

// normalizeSyncBaseline is the same hook the server applies in validateBoundObject; it lives in
// package app, so the test mirrors it through the DTO's exported method.
// normalizeSyncBaseline 与服务端 validateBoundObject 中的钩子一致；实现位于 app 包，
// 测试通过 DTO 导出的方法复现同一行为。
func normalizeSyncBaseline(obj any) {
	if n, ok := obj.(interface{ NormalizeSyncBaseline() }); ok {
		n.NormalizeSyncBaseline()
	}
}

func TestBaselineNormalization_EmptyTextHashIsReal(t *testing.T) {
	// "0" is the hash of an empty body. It must stay a baseline.
	hash, missing := dto.NormalizeBaseline("0", false)
	require.Equal(t, "0", hash)
	require.False(t, missing)
	require.True(t, dto.HasTrustedBase("0", false))

	// Empty string and the explicit flag both mean "no trusted baseline".
	for _, tc := range []struct {
		hash    string
		missing bool
	}{
		{"", false},
		{"", true},
		{"deadbeef", true},
	} {
		gotHash, gotMissing := dto.NormalizeBaseline(tc.hash, tc.missing)
		require.Equal(t, "", gotHash, "input %q/%v", tc.hash, tc.missing)
		require.True(t, gotMissing, "input %q/%v: missing must win", tc.hash, tc.missing)
	}
}

func TestBaselineCodec_NoteRequests(t *testing.T) {
	t.Run("NoteSync", func(t *testing.T) {
		pbMsg := &v1.NoteSyncRequest{
			Context: "ctx", Vault: "V", LastTime: 10, BatchIndex: 0, TotalBatches: 1,
			Notes: []*v1.NoteSyncCheckRequest{{
				Path: "a.md", PathHash: "ph", ContentHash: "ch", Mtime: 1, Ctime: 2,
				BaseHash: "0", BaseHashMissing: false, BaseRevision: "rev-a",
			}},
			DelNotes: []*v1.NoteSyncDelNote{{
				Path: "b.md", PathHash: "ph2", BaseHash: "bh", BaseRevision: "rev-b",
			}},
			MissingNotes: []*v1.NoteSyncDelNote{{
				Path: "c.md", PathHash: "ph3", BaseHashMissing: true, BaseRevision: "rev-c",
			}},
		}
		var pbDest dto.NoteSyncRequest
		decodePBRequest(t, NoteReceiveSync, pbMsg, &pbDest)
		normalizeSyncBaseline(&pbDest)

		var jsonDest dto.NoteSyncRequest
		decodeJSONRequest(t, `{"context":"ctx","vault":"V","lastTime":10,"batchIndex":0,"totalBatches":1,
			"notes":[{"path":"a.md","pathHash":"ph","contentHash":"ch","mtime":1,"ctime":2,"baseHash":"0","baseHashMissing":false,"baseRevision":"rev-a"}],
			"delNotes":[{"path":"b.md","pathHash":"ph2","baseHash":"bh","baseRevision":"rev-b"}],
			"missingNotes":[{"path":"c.md","pathHash":"ph3","baseHashMissing":true,"baseRevision":"rev-c"}]}`, &jsonDest)

		require.Equal(t, jsonDest, pbDest, "JSON and Protobuf note sync requests must decode identically")
		require.Equal(t, "0", pbDest.Notes[0].BaseHash, "empty-text hash must survive")
		require.False(t, pbDest.Notes[0].BaseHashMissing)
		require.True(t, pbDest.MissingNotes[0].BaseHashMissing)
		require.Empty(t, pbDest.MissingNotes[0].BaseHash)
		require.Equal(t, "rev-c", pbDest.MissingNotes[0].BaseRevision)
	})

	t.Run("NoteModify", func(t *testing.T) {
		pbMsg := &v1.NoteModifyOrCreateRequest{
			Vault: "V", Path: "a.md", PathHash: "ph", BaseHash: "0", BaseHashMissing: false,
			Content: "", ContentHash: "0", Ctime: 1, Mtime: 2, CreateOnly: true,
			Context: "ctx", IsConflictResolved: true, BaseRevision: "rev-1",
		}
		var pbDest dto.NoteModifyOrCreateRequest
		decodePBRequest(t, NoteReceiveModify, pbMsg, &pbDest)
		pbDest.NormalizeSyncBaseline()

		var jsonDest dto.NoteModifyOrCreateRequest
		decodeJSONRequest(t, `{"vault":"V","path":"a.md","pathHash":"ph","baseHash":"0","baseHashMissing":false,
			"content":"","contentHash":"0","ctime":1,"mtime":2,"createOnly":true,"context":"ctx",
			"isConflictResolved":true,"baseRevision":"rev-1"}`, &jsonDest)

		require.Equal(t, jsonDest, pbDest)
		require.Equal(t, "0", pbDest.BaseHash)
		require.Equal(t, "0", pbDest.ContentHash, "an empty note's content hash is \"0\", not \"\"")
		require.Equal(t, "rev-1", pbDest.BaseRevision)
	})

	t.Run("NoteCheck/NoteDelete", func(t *testing.T) {
		checkPB := &v1.NoteUpdateCheckRequest{
			Vault: "V", Path: "a.md", PathHash: "ph", ContentHash: "ch", Ctime: 1, Mtime: 2,
			BaseHash: "bh", BaseHashMissing: true, BaseRevision: "rev",
		}
		var checkPBVal dto.NoteUpdateCheckRequest
		decodePBRequest(t, NoteReceiveCheck, checkPB, &checkPBVal)
		checkPBVal.NormalizeSyncBaseline()
		var checkJSON dto.NoteUpdateCheckRequest
		decodeJSONRequest(t, `{"vault":"V","path":"a.md","pathHash":"ph","contentHash":"ch","ctime":1,"mtime":2,
			"baseHash":"bh","baseHashMissing":true,"baseRevision":"rev"}`, &checkJSON)
		require.Equal(t, checkJSON, checkPBVal)
		require.True(t, checkPBVal.BaseHashMissing)
		require.Empty(t, checkPBVal.BaseHash, "missing wins over a contradictory hash")

		delPB := &v1.NoteDeleteRequest{Vault: "V", Path: "a.md", PathHash: "ph", Context: "ctx",
			BaseHash: "0", BaseRevision: "rev-d"}
		var delPBVal dto.NoteDeleteRequest
		decodePBRequest(t, NoteReceiveDelete, delPB, &delPBVal)
		delPBVal.NormalizeSyncBaseline()
		var delJSON dto.NoteDeleteRequest
		decodeJSONRequest(t, `{"vault":"V","path":"a.md","pathHash":"ph","context":"ctx","baseHash":"0","baseRevision":"rev-d"}`, &delJSON)
		require.Equal(t, delJSON, delPBVal)
		require.Equal(t, "0", delPBVal.BaseHash)
	})
}

func TestBaselineCodec_FileRequests(t *testing.T) {
	syncPB := &v1.FileSyncRequest{
		Context: "ctx", Vault: "V", LastTime: 1, TotalBatches: 1,
		Files: []*v1.FileSyncCheckRequest{{
			Path: "a.png", PathHash: "ph", ContentHash: "ch", Size: 9, Mtime: 1, Ctime: 2,
			BaseHash: "0", BaseRevision: "rev-f",
		}},
		DelFiles: []*v1.FileSyncDelFile{{Path: "b.png", PathHash: "ph2", BaseHashMissing: true, BaseRevision: "rev-g"}},
	}
	var syncPBVal dto.FileSyncRequest
	decodePBRequest(t, FileReceiveSync, syncPB, &syncPBVal)
	normalizeSyncBaseline(&syncPBVal)
	var syncJSON dto.FileSyncRequest
	decodeJSONRequest(t, `{"context":"ctx","vault":"V","lastTime":1,"batchIndex":0,"totalBatches":1,
		"files":[{"path":"a.png","pathHash":"ph","contentHash":"ch","size":9,"mtime":1,"ctime":2,"baseHash":"0","baseRevision":"rev-f"}],
		"delFiles":[{"path":"b.png","pathHash":"ph2","baseHashMissing":true,"baseRevision":"rev-g"}],
		"missingFiles":[]}`, &syncJSON)
	require.Equal(t, syncJSON, syncPBVal)

	upPB := &v1.FileUploadCheckRequest{
		Vault: "V", Path: "a.png", PathHash: "ph", ContentHash: "ch", Size: 9, Ctime: 1, Mtime: 2,
		Context: "ctx", BaseHash: "0", BaseRevision: "rev-u", IsConflictResolved: true,
	}
	var upPBVal dto.FileUpdateCheckRequest
	decodePBRequest(t, FileReceiveUploadCheck, upPB, &upPBVal)
	upPBVal.NormalizeSyncBaseline()
	var upJSON dto.FileUpdateCheckRequest
	decodeJSONRequest(t, `{"vault":"V","path":"a.png","pathHash":"ph","contentHash":"ch","size":9,"ctime":1,"mtime":2,
		"context":"ctx","baseHash":"0","baseRevision":"rev-u","isConflictResolved":true}`, &upJSON)
	require.Equal(t, upJSON, upPBVal)

	delPB := &v1.FileDeleteRequest{Vault: "V", Path: "a.png", PathHash: "ph", Context: "ctx",
		BaseHash: "bh", BaseRevision: "rev-d"}
	var delPBVal dto.FileDeleteRequest
	decodePBRequest(t, FileReceiveDelete, delPB, &delPBVal)
	delPBVal.NormalizeSyncBaseline()
	var delJSON dto.FileDeleteRequest
	decodeJSONRequest(t, `{"vault":"V","path":"a.png","pathHash":"ph","context":"ctx","baseHash":"bh","baseRevision":"rev-d"}`, &delJSON)
	require.Equal(t, delJSON, delPBVal)
}

func TestBaselineCodec_SettingRequests(t *testing.T) {
	syncPB := &v1.SettingSyncRequest{
		Context: "ctx", Vault: "V", LastTime: 1, Cover: true, TotalBatches: 1,
		Settings: []*v1.SettingSyncCheckRequest{{
			Path: "cfg", PathHash: "ph", ContentHash: "ch", Mtime: 1, Ctime: 2,
			BaseHash: "0", BaseRevision: "rev-s",
		}},
		DelSettings: []*v1.SettingSyncDelSetting{{Path: "d", PathHash: "ph2", BaseHashMissing: true, BaseRevision: "rev-t"}},
	}
	var syncPBVal dto.SettingSyncRequest
	decodePBRequest(t, SettingReceiveSync, syncPB, &syncPBVal)
	normalizeSyncBaseline(&syncPBVal)
	var syncJSON dto.SettingSyncRequest
	decodeJSONRequest(t, `{"context":"ctx","vault":"V","lastTime":1,"cover":true,"batchIndex":0,"totalBatches":1,
		"settings":[{"path":"cfg","pathHash":"ph","contentHash":"ch","mtime":1,"ctime":2,"baseHash":"0","baseRevision":"rev-s"}],
		"delSettings":[{"path":"d","pathHash":"ph2","baseHashMissing":true,"baseRevision":"rev-t"}],
		"missingSettings":[]}`, &syncJSON)
	require.Equal(t, syncJSON, syncPBVal, "cover and ctime now agree across encodings")

	modPB := &v1.SettingModifyOrCreateRequest{
		Vault: "V", Path: "cfg", PathHash: "ph", Content: "{}", ContentHash: "ch", Ctime: 1, Mtime: 2,
		Context: "ctx", BaseHash: "0", BaseRevision: "rev-m", IsConflictResolved: true,
	}
	var modPBVal dto.SettingModifyOrCreateRequest
	decodePBRequest(t, SettingReceiveModify, modPB, &modPBVal)
	modPBVal.NormalizeSyncBaseline()
	var modJSON dto.SettingModifyOrCreateRequest
	decodeJSONRequest(t, `{"vault":"V","path":"cfg","pathHash":"ph","content":"{}","contentHash":"ch","ctime":1,"mtime":2,
		"context":"ctx","baseHash":"0","baseRevision":"rev-m","isConflictResolved":true}`, &modJSON)
	require.Equal(t, modJSON, modPBVal)

	checkPB := &v1.SettingUpdateCheckRequest{Vault: "V", Path: "cfg", PathHash: "ph", ContentHash: "ch",
		Ctime: 1, Mtime: 2, BaseHash: "bh", BaseRevision: "rev-k"}
	var checkPBVal dto.SettingUpdateCheckRequest
	decodePBRequest(t, SettingReceiveCheck, checkPB, &checkPBVal)
	checkPBVal.NormalizeSyncBaseline()
	var checkJSON dto.SettingUpdateCheckRequest
	decodeJSONRequest(t, `{"vault":"V","path":"cfg","pathHash":"ph","contentHash":"ch","ctime":1,"mtime":2,
		"baseHash":"bh","baseRevision":"rev-k"}`, &checkJSON)
	require.Equal(t, checkJSON, checkPBVal)

	delPB := &v1.SettingDeleteRequest{Vault: "V", Path: "cfg", PathHash: "ph", Context: "ctx",
		BaseHash: "0", BaseRevision: "rev-x"}
	var delPBVal dto.SettingDeleteRequest
	decodePBRequest(t, SettingReceiveDelete, delPB, &delPBVal)
	delPBVal.NormalizeSyncBaseline()
	var delJSON dto.SettingDeleteRequest
	decodeJSONRequest(t, `{"vault":"V","path":"cfg","pathHash":"ph","context":"ctx","baseHash":"0","baseRevision":"rev-x"}`, &delJSON)
	require.Equal(t, delJSON, delPBVal)
}

func TestBaselineCodec_FolderRequests(t *testing.T) {
	syncPB := &v1.FolderSyncRequest{
		Context: "ctx", Vault: "V", LastTime: 1, TotalBatches: 1,
		Folders:    []*v1.FolderSyncCheckRequest{{Path: "F", PathHash: "ph", Mtime: 1, BaseRevision: "rev-a"}},
		DelFolders: []*v1.FolderSyncDelFolder{{Path: "G", PathHash: "ph2", BaseRevision: "rev-b"}},
	}
	var syncPBVal dto.FolderSyncRequest
	decodePBRequest(t, FolderReceiveSync, syncPB, &syncPBVal)
	var syncJSON dto.FolderSyncRequest
	decodeJSONRequest(t, `{"context":"ctx","vault":"V","lastTime":1,"batchIndex":0,"totalBatches":1,
		"folders":[{"path":"F","pathHash":"ph","mtime":1,"baseRevision":"rev-a"}],
		"delFolders":[{"path":"G","pathHash":"ph2","baseRevision":"rev-b"}],
		"missingFolders":[]}`, &syncJSON)
	require.Equal(t, syncJSON, syncPBVal)

	createPB := &v1.FolderCreateRequest{Vault: "V", Path: "F", PathHash: "ph", Context: "ctx",
		BaseRevision: "rev-c", IsConflictResolved: true}
	var createPBVal dto.FolderCreateRequest
	decodePBRequest(t, FolderReceiveModify, createPB, &createPBVal)
	var createJSON dto.FolderCreateRequest
	decodeJSONRequest(t, `{"vault":"V","path":"F","pathHash":"ph","context":"ctx","baseRevision":"rev-c","isConflictResolved":true}`, &createJSON)
	require.Equal(t, createJSON, createPBVal)

	delPB := &v1.FolderDeleteRequest{Vault: "V", Path: "F", PathHash: "ph", Context: "ctx", BaseRevision: "rev-d"}
	var delPBVal dto.FolderDeleteRequest
	decodePBRequest(t, FolderReceiveDelete, delPB, &delPBVal)
	var delJSON dto.FolderDeleteRequest
	decodeJSONRequest(t, `{"vault":"V","path":"F","pathHash":"ph","context":"ctx","baseRevision":"rev-d"}`, &delJSON)
	require.Equal(t, delJSON, delPBVal)
}

// decodePBResponse unwraps an encoded response (WSMessage -> WSResponse) and returns the
// action payload bytes, i.e. exactly what the client feeds to its per-action decoder.
// decodePBResponse 解开响应报文（WSMessage -> WSResponse），返回动作载荷字节，
// 即客户端交给各动作解码器的内容。
func decodePBResponse(t *testing.T, action WebSocketSendAction, res *pkgapp.Res) []byte {
	t.Helper()
	encoded, err := EnSendDTOToProtobuf(action, res)
	require.NoError(t, err)
	require.True(t, len(encoded) > 2 && encoded[0] == 'p' && encoded[1] == 'b')
	env, respBytes, err := DeReceivePacket(encoded[2:])
	require.NoError(t, err)
	require.Equal(t, string(action), env)
	var wsResp v1.WSResponse
	require.NoError(t, proto.Unmarshal(respBytes, &wsResp))
	return wsResp.Data
}

func TestBaselineCodec_ResponseMessages(t *testing.T) {
	t.Run("Note", func(t *testing.T) {
		modify := decodePBResponse(t, NoteSyncModify, &pkgapp.Res{Data: dto.NoteSyncModifyMessage{
			Path: "a.md", PathHash: "ph", Content: "", ContentHash: "0", Ctime: 1, Mtime: 2,
			UpdatedTimestamp: 3, Revision: "rev-1",
		}})
		var modifyPB v1.NoteSyncModifyMessage
		require.NoError(t, proto.Unmarshal(modify, &modifyPB))
		require.Equal(t, "rev-1", modifyPB.Revision)
		require.Equal(t, "0", modifyPB.ContentHash)

		del := decodePBResponse(t, NoteSyncDelete, &pkgapp.Res{Data: dto.NoteSyncDeleteMessage{
			Path: "a.md", PathHash: "ph", Ctime: 1, Mtime: 2, Size: 3, UpdatedTimestamp: 4,
			Revision: "rev-2", ContentHash: "ch",
		}})
		var delPB v1.NoteSyncDeleteMessage
		require.NoError(t, proto.Unmarshal(del, &delPB))
		require.Equal(t, "rev-2", delPB.Revision)
		require.Equal(t, "ch", delPB.ContentHash)

		mtime := decodePBResponse(t, NoteSyncMtime, &pkgapp.Res{Data: dto.NoteSyncMtimeMessage{
			Path: "a.md", Ctime: 1, Mtime: 2, UpdatedTimestamp: 3, ContentHash: "0", Revision: "rev-3",
		}})
		var mtimePB v1.NoteSyncMtimeMessage
		require.NoError(t, proto.Unmarshal(mtime, &mtimePB))
		require.Equal(t, "0", mtimePB.ContentHash)
		require.Equal(t, "rev-3", mtimePB.Revision)

		end := decodePBResponse(t, NoteSyncEnd, &pkgapp.Res{Data: dto.NoteSyncEndMessage{
			LastTime: 1, NeedUploadCount: 2, NeedModifyCount: 3, NeedSyncMtimeCount: 4, NeedDeleteCount: 5,
			NeedConflictCount: 6,
		}})
		var endPB v1.NoteSyncEndMessage
		require.NoError(t, proto.Unmarshal(end, &endPB))
		require.EqualValues(t, 6, endPB.NeedConflictCount)

		ack := decodePBResponse(t, NoteModifyAck, &pkgapp.Res{Data: dto.NoteModifyAckMessage{
			LastTime: 1, Path: "a.md", PathHash: "ph", ContentHash: "0", Revision: "rev-4",
		}})
		var ackPB v1.NoteModifyAckMessage
		require.NoError(t, proto.Unmarshal(ack, &ackPB))
		require.Equal(t, "0", ackPB.ContentHash)
		require.Equal(t, "rev-4", ackPB.Revision)

		deleteAck := decodePBResponse(t, NoteDeleteAck, &pkgapp.Res{Data: dto.NoteDeleteAckMessage{
			LastTime: 1, Path: "a.md", PathHash: "ph", Revision: "rev-5",
		}})
		var deleteAckPB v1.NoteDeleteAckMessage
		require.NoError(t, proto.Unmarshal(deleteAck, &deleteAckPB))
		require.Equal(t, "rev-5", deleteAckPB.Revision)
	})

	t.Run("NeedPush baseMissing", func(t *testing.T) {
		for _, payload := range []any{
			dto.NoteSyncNeedPushMessage{Path: "a.md", PathHash: "ph", BaseMissing: true},
			&dto.NoteSyncNeedPushMessage{Path: "a.md", PathHash: "ph", BaseMissing: true},
			map[string]interface{}{"path": "a.md", "pathHash": "ph", "baseMissing": true},
		} {
			inner := decodePBResponse(t, NoteSyncNeedPush, &pkgapp.Res{Data: payload})
			var pb v1.NoteSyncNeedPushMessage
			require.NoError(t, proto.Unmarshal(inner, &pb))
			require.True(t, pb.BaseMissing, "payload %T", payload)
		}
	})

	t.Run("File", func(t *testing.T) {
		modify := decodePBResponse(t, FileSyncUpdate, &pkgapp.Res{Data: dto.FileSyncModifyMessage{
			Path: "a.png", PathHash: "ph", ContentHash: "ch", Size: 1, Ctime: 2, Mtime: 3,
			UpdatedTimestamp: 4, Revision: "rev-f1",
		}})
		var modifyPB v1.FileSyncModifyMessage
		require.NoError(t, proto.Unmarshal(modify, &modifyPB))
		require.Equal(t, "rev-f1", modifyPB.Revision)

		del := decodePBResponse(t, FileSyncDelete, &pkgapp.Res{Data: dto.FileSyncDeleteMessage{
			Path: "a.png", PathHash: "ph", Ctime: 1, Mtime: 2, Size: 3, UpdatedTimestamp: 4,
			Revision: "rev-f2", ContentHash: "ch",
		}})
		var delPB v1.FileSyncDeleteMessage
		require.NoError(t, proto.Unmarshal(del, &delPB))
		require.Equal(t, "rev-f2", delPB.Revision)
		require.Equal(t, "ch", delPB.ContentHash)

		mtime := decodePBResponse(t, FileSyncMtime, &pkgapp.Res{Data: dto.FileSyncMtimeMessage{
			Path: "a.png", Ctime: 1, Mtime: 2, UpdatedTimestamp: 3, ContentHash: "0", Revision: "rev-f3",
		}})
		var mtimePB v1.FileSyncMtimeMessage
		require.NoError(t, proto.Unmarshal(mtime, &mtimePB))
		require.Equal(t, "0", mtimePB.ContentHash)
		require.Equal(t, "rev-f3", mtimePB.Revision)

		end := decodePBResponse(t, FileSyncEnd, &pkgapp.Res{Data: dto.FileSyncEndMessage{
			LastTime: 1, NeedUploadCount: 2, NeedModifyCount: 3, NeedSyncMtimeCount: 4, NeedDeleteCount: 5,
			NeedConflictCount: 7,
		}})
		var endPB v1.FileSyncEndMessage
		require.NoError(t, proto.Unmarshal(end, &endPB))
		require.EqualValues(t, 7, endPB.NeedConflictCount)

		ack := decodePBResponse(t, FileUploadAck, &pkgapp.Res{Data: dto.FileUploadAckMessage{
			LastTime: 1, Path: "a.png", PathHash: "ph", ContentHash: "ch", Revision: "rev-f4",
		}})
		var ackPB v1.FileUploadAckMessage
		require.NoError(t, proto.Unmarshal(ack, &ackPB))
		require.Equal(t, "ch", ackPB.ContentHash)
		require.Equal(t, "rev-f4", ackPB.Revision)

		deleteAck := decodePBResponse(t, FileDeleteAck, &pkgapp.Res{Data: dto.FileDeleteAckMessage{
			LastTime: 1, Path: "a.png", PathHash: "ph", Revision: "rev-f5",
		}})
		var deleteAckPB v1.FileDeleteAckMessage
		require.NoError(t, proto.Unmarshal(deleteAck, &deleteAckPB))
		require.Equal(t, "rev-f5", deleteAckPB.Revision)
	})

	t.Run("Setting", func(t *testing.T) {
		modify := decodePBResponse(t, SettingSyncModify, &pkgapp.Res{Data: dto.SettingSyncModifyMessage{
			Vault: "V", Path: "cfg", PathHash: "ph", Content: "{}", ContentHash: "0", Ctime: 1, Mtime: 2,
			UpdatedTimestamp: 3, Revision: "rev-s1",
		}})
		var modifyPB v1.SettingSyncModifyMessage
		require.NoError(t, proto.Unmarshal(modify, &modifyPB))
		require.Equal(t, "rev-s1", modifyPB.Revision)
		require.Equal(t, "0", modifyPB.ContentHash)

		del := decodePBResponse(t, SettingSyncDelete, &pkgapp.Res{Data: dto.SettingSyncDeleteMessage{
			Path: "cfg", PathHash: "ph", Ctime: 1, Mtime: 2, UpdatedTimestamp: 3,
			Revision: "rev-s2", ContentHash: "ch",
		}})
		var delPB v1.SettingSyncDeleteMessage
		require.NoError(t, proto.Unmarshal(del, &delPB))
		require.Equal(t, "rev-s2", delPB.Revision)
		require.Equal(t, "ch", delPB.ContentHash)

		mtime := decodePBResponse(t, SettingSyncMtime, &pkgapp.Res{Data: dto.SettingSyncMtimeMessage{
			Path: "cfg", Ctime: 1, Mtime: 2, UpdatedTimestamp: 3, ContentHash: "0", Revision: "rev-s3",
		}})
		var mtimePB v1.SettingSyncMtimeMessage
		require.NoError(t, proto.Unmarshal(mtime, &mtimePB))
		require.Equal(t, "rev-s3", mtimePB.Revision)

		end := decodePBResponse(t, SettingSyncEnd, &pkgapp.Res{Data: dto.SettingSyncEndMessage{
			LastTime: 1, NeedUploadCount: 2, NeedModifyCount: 3, NeedSyncMtimeCount: 4, NeedDeleteCount: 5,
			NeedConflictCount: 8,
		}})
		var endPB v1.SettingSyncEndMessage
		require.NoError(t, proto.Unmarshal(end, &endPB))
		require.EqualValues(t, 8, endPB.NeedConflictCount)

		ack := decodePBResponse(t, SettingModifyAck, &pkgapp.Res{Data: dto.SettingModifyAckMessage{
			LastTime: 1, Path: "cfg", PathHash: "ph", ContentHash: "ch", Revision: "rev-s4",
		}})
		var ackPB v1.SettingModifyAckMessage
		require.NoError(t, proto.Unmarshal(ack, &ackPB))
		require.Equal(t, "rev-s4", ackPB.Revision)

		deleteAck := decodePBResponse(t, SettingDeleteAck, &pkgapp.Res{Data: dto.SettingDeleteAckMessage{
			LastTime: 1, Path: "cfg", PathHash: "ph", Revision: "rev-s5",
		}})
		var deleteAckPB v1.SettingDeleteAckMessage
		require.NoError(t, proto.Unmarshal(deleteAck, &deleteAckPB))
		require.Equal(t, "rev-s5", deleteAckPB.Revision)
	})

	t.Run("Folder", func(t *testing.T) {
		modify := decodePBResponse(t, FolderSyncModify, &pkgapp.Res{Data: dto.FolderSyncModifyMessage{
			Path: "F", PathHash: "ph", Ctime: 1, Mtime: 2, UpdatedTimestamp: 3, Revision: "rev-o1",
		}})
		var modifyPB v1.FolderSyncModifyMessage
		require.NoError(t, proto.Unmarshal(modify, &modifyPB))
		require.Equal(t, "rev-o1", modifyPB.Revision)

		del := decodePBResponse(t, FolderSyncDelete, &pkgapp.Res{Data: dto.FolderSyncDeleteMessage{
			Path: "F", PathHash: "ph", Ctime: 1, Mtime: 2, UpdatedTimestamp: 3, Revision: "rev-o2",
		}})
		var delPB v1.FolderSyncDeleteMessage
		require.NoError(t, proto.Unmarshal(del, &delPB))
		require.Equal(t, "rev-o2", delPB.Revision)

		end := decodePBResponse(t, FolderSyncEnd, &pkgapp.Res{Data: dto.FolderSyncEndMessage{
			LastTime: 1, NeedModifyCount: 2, NeedDeleteCount: 3, NeedConflictCount: 9,
		}})
		var endPB v1.FolderSyncEndMessage
		require.NoError(t, proto.Unmarshal(end, &endPB))
		require.EqualValues(t, 9, endPB.NeedConflictCount)

		modifyAck := decodePBResponse(t, FolderModifyAck, &pkgapp.Res{Data: dto.FolderModifyAckMessage{
			LastTime: 1, Path: "F", PathHash: "ph", Revision: "rev-o3",
		}})
		var modifyAckPB v1.FolderModifyAckMessage
		require.NoError(t, proto.Unmarshal(modifyAck, &modifyAckPB))
		require.Equal(t, "rev-o3", modifyAckPB.Revision)

		deleteAck := decodePBResponse(t, FolderDeleteAck, &pkgapp.Res{Data: dto.FolderDeleteAckMessage{
			LastTime: 1, Path: "F", PathHash: "ph", Revision: "rev-o4",
		}})
		var deleteAckPB v1.FolderDeleteAckMessage
		require.NoError(t, proto.Unmarshal(deleteAck, &deleteAckPB))
		require.Equal(t, "rev-o4", deleteAckPB.Revision)
	})
}

func TestBaselineCodec_CheckVersionInfo(t *testing.T) {
	inner := decodePBResponse(t, ClientInfo, &pkgapp.Res{Data: pkgapp.CheckVersionInfo{
		SyncUpChunkNum: 100, SyncDownChunkNum: 50, PipelineWindowUp: 8, PipelineWindowDown: 4,
		SyncBaselineVersion: 1,
	}})
	var pb v1.CheckVersionInfo
	require.NoError(t, proto.Unmarshal(inner, &pb))
	require.EqualValues(t, 1, pb.SyncBaselineVersion)
	require.EqualValues(t, 100, pb.SyncUpChunkNum)

	// Zero value must round-trip as 0, not disappear into an ambiguous "absent".
	innerZero := decodePBResponse(t, ClientInfo, &pkgapp.Res{Data: pkgapp.CheckVersionInfo{}})
	var pbZero v1.CheckVersionInfo
	require.NoError(t, proto.Unmarshal(innerZero, &pbZero))
	require.EqualValues(t, 0, pbZero.SyncBaselineVersion)
}

func TestBaselineCodec_SyncConflictMessage(t *testing.T) {
	full := dto.SyncConflictMessage{
		Kind: "note", Path: "a.md", PathHash: "ph", Reason: dto.SyncConflictReasonBothModified,
		ServerHash: "sh", ServerRevision: "srv-rev", ServerDeleted: true,
		BaseHash: "0", BaseContent: "", BaseMissing: false, ServerContent: "# new",
	}

	inner := decodePBResponse(t, SyncConflict, &pkgapp.Res{Data: full})
	var pb v1.SyncConflictMessage
	require.NoError(t, proto.Unmarshal(inner, &pb))
	require.Equal(t, "note", pb.Kind)
	require.Equal(t, "a.md", pb.Path)
	require.Equal(t, "ph", pb.PathHash)
	require.Equal(t, dto.SyncConflictReasonBothModified, pb.Reason)
	require.Equal(t, "sh", pb.ServerHash)
	require.Equal(t, "srv-rev", pb.ServerRevision)
	require.True(t, pb.ServerDeleted)
	require.Equal(t, "0", pb.BaseHash, "empty-text base hash survives the conflict payload")
	require.False(t, pb.BaseMissing)
	require.Equal(t, "# new", pb.ServerContent)

	// A proven-but-empty base is a legitimate case: baseMissing stays false, baseContent empty.
	// 已证实但为空的 base 是合法情形：baseMissing 保持 false，baseContent 为空。
	normalizedHash, normalizedMissing := dto.NormalizeBaseline(pb.BaseHash, pb.BaseMissing)
	require.Equal(t, "0", normalizedHash)
	require.False(t, normalizedMissing)

	// The map form (legacy call sites) maps the same fields.
	mapInner := decodePBResponse(t, SyncConflict, &pkgapp.Res{Data: map[string]interface{}{
		"kind": "file", "path": "a.png", "pathHash": "ph2", "reason": dto.SyncConflictReasonRemoteDeleted,
		"serverDeleted": true, "baseHash": "0", "baseMissing": false,
	}})
	var mapPB v1.SyncConflictMessage
	require.NoError(t, proto.Unmarshal(mapInner, &mapPB))
	require.Equal(t, "file", mapPB.Kind)
	require.True(t, mapPB.ServerDeleted)
	require.Equal(t, "0", mapPB.BaseHash)

	// JSON encoding carries the same names (omitempty only drops the two content fields).
	raw, err := json.Marshal(full)
	require.NoError(t, err)
	var jsonMap map[string]any
	require.NoError(t, json.Unmarshal(raw, &jsonMap))
	for _, key := range []string{"kind", "path", "pathHash", "reason", "serverHash", "serverRevision",
		"serverDeleted", "baseHash", "baseMissing"} {
		require.Contains(t, jsonMap, key, "JSON payload must expose %s", key)
	}
	require.Equal(t, "0", jsonMap["baseHash"])
}

func TestBaselineCodec_UnknownFieldsAndZeroValues(t *testing.T) {
	// An older/newer peer may add fields we do not know; decoding must stay lossless for ours.
	// 新旧对端可能新增未知字段；解码不得影响已知字段。
	inner, err := proto.Marshal(&v1.ClientInfoMessage{
		Name: "peer", SyncBaselineVersion: 1,
	})
	require.NoError(t, err)
	// Append an unknown field (field number 2000, varint) as a future client would.
	unknown := append(inner, 0xC0, 0x7D, 0x01)                 // tag (2000<<3|0), value 1
	payload := encodeRequest(t, "ClientInfo", &v1.WSMessage{}) // envelope helper for the type
	_ = payload
	envelope, err := proto.Marshal(&v1.WSMessage{Type: "ClientInfo", Data: unknown})
	require.NoError(t, err)
	_, decodedPayload, err := DeReceivePacket(envelope)
	require.NoError(t, err)

	var dest pkgapp.ClientInfoMessage
	ok, err := DeReceiveProtobufToDTO(ClientReceiveInfo, decodedPayload, &dest)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "peer", dest.Name)
	require.EqualValues(t, 1, dest.SyncBaselineVersion)

	// JSON with unknown keys decodes fine and keeps the known ones.
	var jsonDest pkgapp.ClientInfoMessage
	require.NoError(t, json.Unmarshal([]byte(`{"name":"peer","syncBaselineVersion":1,"futureFlag":true}`), &jsonDest))
	require.Equal(t, "peer", jsonDest.Name)
	require.EqualValues(t, 1, jsonDest.SyncBaselineVersion)

	// Zero values are meaningful: absent version means legacy, not "unknown".
	var zeroPB v1.ClientInfoMessage
	require.EqualValues(t, 0, zeroPB.SyncBaselineVersion)
	var zeroJSON pkgapp.ClientInfoMessage
	require.NoError(t, json.Unmarshal([]byte(`{}`), &zeroJSON))
	require.EqualValues(t, 0, zeroJSON.SyncBaselineVersion)
}
