package websocket_router

import (
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"google.golang.org/protobuf/encoding/protowire"
)

// protobufRequestMeta is the subset of an action-level Protobuf request the
// pre-handler interceptor needs: the vault for the scope check, the path for
// concurrency-slot release on errors, and the old path for rename rollback.
// protobufRequestMeta 是动作级 Protobuf 请求中前置拦截器需要的字段子集：
// 用于库权限校验的 vault、出错时释放并发名额的 path，以及改名回滚用的 oldPath。
type protobufRequestMeta struct {
	Vault       string
	Path        string
	PathHash    string
	OldPath     string
	OldPathHash string
}

// protobufRequestMetaFields maps a receive action to the wire field numbers of
// the fields above. 0 means the action has no such field. Field numbers come
// from internal/proto/v1/sync.proto; sync requests and page acks put vault on
// field 2 (context occupies field 1), everything else on field 1.
// protobufRequestMetaFields 把接收动作映射到上述字段在 wire 上的编号，0 表示该动作没有这个字段。
// 编号取自 internal/proto/v1/sync.proto；同步请求与分页确认的 vault 在第 2 字段
// （第 1 字段是 context），其余动作在第 1 字段。
var protobufRequestMetaFields = map[string]struct {
	vault, path, pathHash, oldPath, oldPathHash int
}{
	NoteReceiveSync:          {vault: 2},
	NoteReceiveModify:        {vault: 1, path: 2, pathHash: 3},
	NoteReceiveCheck:         {vault: 1, path: 2, pathHash: 3},
	NoteReceiveDelete:        {vault: 1, path: 2, pathHash: 3},
	NoteReceiveRename:        {vault: 1, path: 2, pathHash: 3, oldPath: 4, oldPathHash: 5},
	NoteReceiveRePush:        {vault: 1, path: 2, pathHash: 3},
	FileReceiveSync:          {vault: 2},
	FileReceiveUploadCheck:   {vault: 1, path: 2, pathHash: 3},
	FileReceiveDelete:        {vault: 1, path: 2, pathHash: 3},
	FileReceiveRename:        {vault: 1, path: 2, pathHash: 3, oldPath: 4, oldPathHash: 5},
	FileReceiveChunkDownload: {vault: 1, path: 2, pathHash: 3},
	FileReceiveRePush:        {vault: 1, path: 2, pathHash: 3},
	SettingReceiveSync:       {vault: 2},
	SettingReceiveModify:     {vault: 1, path: 2, pathHash: 3},
	SettingReceiveCheck:      {vault: 1, path: 2, pathHash: 3},
	SettingReceiveDelete:     {vault: 1, path: 2, pathHash: 3},
	SettingReceiveClear:      {vault: 1},
	SettingReceiveRePush:     {vault: 1, path: 2, pathHash: 3},
	FolderReceiveSync:        {vault: 2},
	FolderReceiveModify:      {vault: 1, path: 2, pathHash: 3},
	FolderReceiveDelete:      {vault: 1, path: 2, pathHash: 3},
	FolderReceiveRename:      {vault: 1, path: 2, pathHash: 3, oldPath: 4, oldPathHash: 5},
	NoteSyncPageAck:          {vault: 2},
	FileSyncPageAck:          {vault: 2},
	SettingSyncPageAck:       {vault: 2},
	FolderSyncPageAck:        {vault: 2},
}

// isJSONMessagePayload reports whether the action-level payload is a JSON object
// rather than Protobuf. proto3 never emits 0x7B as a field tag (it is a
// start-group tag, which proto3 does not produce), so a leading '{' identifies
// the JSON text frame a protobuf-negotiated client may still send as a fallback.
// isJSONMessagePayload 判断动作级载荷是 JSON 对象而不是 Protobuf。proto3 不会用 0x7B
// 作字段标签（那是 start-group 标签，proto3 不产生），首字节为 '{' 即可认定为
// 已协商 Protobuf 的连接仍可能发来的 JSON 文本帧。
func isJSONMessagePayload(data []byte) bool {
	return len(data) > 0 && data[0] == '{'
}

// decodeProtobufRequestMeta extracts the interceptor-relevant fields from an
// action-level Protobuf payload without needing the action's generated type.
// Unknown fields are skipped; a malformed payload returns ok=false.
// decodeProtobufRequestMeta 在不需要动作生成类型的前提下，从动作级 Protobuf 载荷中
// 提取拦截器需要的字段；未知字段跳过，载荷格式非法时返回 ok=false。
func decodeProtobufRequestMeta(action string, data []byte) (protobufRequestMeta, bool) {
	spec, ok := protobufRequestMetaFields[action]
	if !ok {
		return protobufRequestMeta{}, false
	}

	var meta protobufRequestMeta
	fields := make(map[protowire.Number]*string, 5)
	if spec.vault > 0 {
		fields[protowire.Number(spec.vault)] = &meta.Vault
	}
	if spec.path > 0 {
		fields[protowire.Number(spec.path)] = &meta.Path
	}
	if spec.pathHash > 0 {
		fields[protowire.Number(spec.pathHash)] = &meta.PathHash
	}
	if spec.oldPath > 0 {
		fields[protowire.Number(spec.oldPath)] = &meta.OldPath
	}
	if spec.oldPathHash > 0 {
		fields[protowire.Number(spec.oldPathHash)] = &meta.OldPathHash
	}

	rest := data
	for len(rest) > 0 {
		num, typ, n := protowire.ConsumeTag(rest)
		if n < 0 {
			return protobufRequestMeta{}, false
		}
		rest = rest[n:]
		if typ == protowire.BytesType {
			value, n := protowire.ConsumeBytes(rest)
			if n < 0 {
				return protobufRequestMeta{}, false
			}
			if dst, wanted := fields[num]; wanted {
				*dst = string(value)
			}
			rest = rest[n:]
			continue
		}
		n = protowire.ConsumeFieldValue(num, typ, rest)
		if n < 0 {
			return protobufRequestMeta{}, false
		}
		rest = rest[n:]
	}
	return meta, true
}

// requestMeta decodes the action-level payload meta from either wire format.
// requestMeta 从两种上行编码中提取动作级载荷元数据。
func requestMeta(msg *pkgapp.WebSocketMessage) (protobufRequestMeta, bool) {
	if msg == nil || isJSONMessagePayload(msg.Data) {
		return protobufRequestMeta{}, false
	}
	return decodeProtobufRequestMeta(msg.Type, msg.Data)
}
