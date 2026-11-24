package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 存量大文件哈希口径桥接测试
// Legacy large-file hash bridge tests
//
// 背景：旧版服务端对 >10MB 附件存「前 5MB + 后 5MB」采样哈希，新版插件上报
// 「前 5MB + 中 5MB + 后 5MB」。若不桥接，升级后客户端会把所有大附件重传一遍。
// 修复：UploadCheck/UpdateCheck 在库内哈希与客户端哈希不一致时重读磁盘实体文件，
// 同时算出新旧两种哈希；只有「库内旧哈希确实描述该文件」且「文件新哈希等于客户端
// 哈希」时才把库内 content_hash 就地升级，否则仍要求 UpdateContent。
// ---------------------------------------------------------------------------

const (
	bridgeFileID = int64(9)
	bridgeUID    = int64(1)
	bridgeSize   = 12 * 1024 * 1024
)

// bridgeTestData builds a deterministic >10MB payload including 0x80/0xff bytes.
func bridgeTestData(size int, seed byte) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte((i*29 + 7 + int(seed)) & 0xff)
	}
	data[0] = 0xff
	data[1] = 0x80
	data[size-1] = 0xff
	data[size-2] = 0x80
	return data
}

func writeBridgePayload(t *testing.T, size int, seed byte) (path, current, legacy string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "file.dat")
	require.NoError(t, os.WriteFile(path, bridgeTestData(size, seed), 0o644))
	current, legacy, err := util.EncodeHash32FileWithLegacy(path)
	require.NoError(t, err)
	return path, current, legacy
}

func newBridgeFixture(t *testing.T, size int) (*fileService, *domainmocks.MockFileRepository, *domain.File, string, string) {
	t.Helper()

	path, current, legacy := writeBridgePayload(t, size, 0)
	file := &domain.File{
		ID:          bridgeFileID,
		VaultID:     1,
		Path:        "big.bin",
		PathHash:    "ph",
		Action:      domain.FileActionCreate,
		ContentHash: legacy,
		SavePath:    path,
		Size:        int64(size),
		Ctime:       900,
		Mtime:       1000,
	}

	repo := new(domainmocks.MockFileRepository)
	repo.On("GetByPathHashAndPath", mock.Anything, "ph", "big.bin", int64(1), bridgeUID).
		Return(file, nil)

	svc := NewFileService(nil, repo, nil, stubVaultService{}, nil, nil, nil, nil, nil).(*fileService)
	return svc, repo, file, current, legacy
}

func bridgeCheckRequest(contentHash string, mtime int64) *dto.FileUpdateCheckRequest {
	return &dto.FileUpdateCheckRequest{
		Vault:       "vault",
		Path:        "big.bin",
		PathHash:    "ph",
		ContentHash: contentHash,
		Size:        bridgeSize,
		Ctime:       900,
		Mtime:       mtime,
	}
}

// TestUpdateCheck_LegacyLargeFileRealignsWithoutUpload is the核心 regression: a legacy
// row whose on-disk payload matches the client's three-segment hash must be realigned
// in place and reported as "no update", not "UpdateContent".
func TestUpdateCheck_LegacyLargeFileRealignsWithoutUpload(t *testing.T) {
	svc, repo, _, current, legacy := newBridgeFixture(t, bridgeSize)
	repo.On("UpdateContentHashIfCurrent", mock.Anything, bridgeFileID, bridgeUID, legacy, current).
		Return(true, nil)

	mode, fileDTO, err := svc.UpdateCheck(context.Background(), bridgeUID, bridgeCheckRequest(current, 1000))
	require.NoError(t, err)
	require.Equal(t, "", mode, "内容未变时不得要求重新上传")
	require.NotNil(t, fileDTO)
	require.Equal(t, current, fileDTO.ContentHash, "DTO 必须返回已对齐的三分段哈希")
	repo.AssertExpectations(t)
	repo.AssertNotCalled(t, "UpdateMtime", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestUpdateCheck_LegacyLargeFile_ClientOlderReturnsUpdateMtime keeps the original
// "server is newer" notification while still aligning the hash.
func TestUpdateCheck_LegacyLargeFile_ClientOlderReturnsUpdateMtime(t *testing.T) {
	svc, repo, _, current, legacy := newBridgeFixture(t, bridgeSize)
	repo.On("UpdateContentHashIfCurrent", mock.Anything, bridgeFileID, bridgeUID, legacy, current).
		Return(true, nil)

	mode, fileDTO, err := svc.UpdateCheck(context.Background(), bridgeUID, bridgeCheckRequest(current, 900))
	require.NoError(t, err)
	require.Equal(t, "UpdateMtime", mode)
	require.Equal(t, current, fileDTO.ContentHash)
	repo.AssertExpectations(t)
}

// TestUpdateCheck_LegacyLargeFile_ClientNewerBumpsMtime aligns the hash and writes the
// newer client mtime, mirroring the normal equal-content branch.
func TestUpdateCheck_LegacyLargeFile_ClientNewerBumpsMtime(t *testing.T) {
	svc, repo, _, current, legacy := newBridgeFixture(t, bridgeSize)
	repo.On("UpdateContentHashIfCurrent", mock.Anything, bridgeFileID, bridgeUID, legacy, current).
		Return(true, nil)
	repo.On("UpdateMtime", mock.Anything, int64(2000), bridgeFileID, bridgeUID).Return(nil)

	mode, fileDTO, err := svc.UpdateCheck(context.Background(), bridgeUID, bridgeCheckRequest(current, 2000))
	require.NoError(t, err)
	require.Equal(t, "", mode)
	require.Equal(t, current, fileDTO.ContentHash)
	repo.AssertExpectations(t)
}

// TestUpdateCheck_ChangedContentStillRequiresUpload proves the bridge never hides a
// real content change: the client hash of a different payload cannot match the disk
// payload, so the check must return UpdateContent and leave the row untouched.
func TestUpdateCheck_ChangedContentStillRequiresUpload(t *testing.T) {
	svc, repo, _, _, _ := newBridgeFixture(t, bridgeSize)

	// Same size, different content -> different three-segment hash.
	_, foreignCurrent, _ := writeBridgePayload(t, bridgeSize, 0x5a)

	mode, fileDTO, err := svc.UpdateCheck(context.Background(), bridgeUID, bridgeCheckRequest(foreignCurrent, 1000))
	require.NoError(t, err)
	require.Equal(t, "UpdateContent", mode, "内容真的变化时必须要求上传")
	require.Equal(t, "ph", fileDTO.PathHash)
	repo.AssertNotCalled(t, "UpdateContentHashIfCurrent", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestUpdateCheck_StoredHashDoesNotDescribeDiskRejectsBridge refuses to rewrite a row
// whose stored hash is not the legacy hash of the on-disk payload (DB/disk divergence).
func TestUpdateCheck_StoredHashDoesNotDescribeDiskRejectsBridge(t *testing.T) {
	svc, repo, file, current, _ := newBridgeFixture(t, bridgeSize)
	file.ContentHash = "stale-unrelated-hash"

	mode, _, err := svc.UpdateCheck(context.Background(), bridgeUID, bridgeCheckRequest(current, 1000))
	require.NoError(t, err)
	require.Equal(t, "UpdateContent", mode)
	repo.AssertNotCalled(t, "UpdateContentHashIfCurrent", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestUpdateCheck_MissingDiskPayloadRequiresUpload skips the bridge when the payload is
// gone instead of pretending the content still matches.
func TestUpdateCheck_MissingDiskPayloadRequiresUpload(t *testing.T) {
	svc, repo, file, current, _ := newBridgeFixture(t, bridgeSize)
	file.SavePath = filepath.Join(t.TempDir(), "missing.dat")

	mode, _, err := svc.UpdateCheck(context.Background(), bridgeUID, bridgeCheckRequest(current, 1000))
	require.NoError(t, err)
	require.Equal(t, "UpdateContent", mode)
	repo.AssertNotCalled(t, "UpdateContentHashIfCurrent", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestUpdateCheck_SmallFileIsNotBridged avoids the extra disk read below the sampling
// threshold, where the two schemes are by definition identical.
func TestUpdateCheck_SmallFileIsNotBridged(t *testing.T) {
	const smallSize = 1 * 1024 * 1024
	svc, repo, _, current, _ := newBridgeFixture(t, smallSize)
	_, foreignCurrent, _ := writeBridgePayload(t, smallSize, 0x33)

	mode, _, err := svc.UpdateCheck(context.Background(), bridgeUID, bridgeCheckRequest(foreignCurrent, 1000))
	require.NoError(t, err)
	require.Equal(t, "UpdateContent", mode)
	require.NotEqual(t, current, foreignCurrent)
	repo.AssertNotCalled(t, "UpdateContentHashIfCurrent", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestUpdateCheck_ConcurrentRewriteFallsBack uses the CAS-false result to prove a
// concurrently replaced row is not reported as "no update" by the bridge.
func TestUpdateCheck_ConcurrentRewriteFallsBack(t *testing.T) {
	svc, repo, _, current, legacy := newBridgeFixture(t, bridgeSize)
	repo.On("UpdateContentHashIfCurrent", mock.Anything, bridgeFileID, bridgeUID, legacy, current).
		Return(false, nil)

	mode, _, err := svc.UpdateCheck(context.Background(), bridgeUID, bridgeCheckRequest(current, 1000))
	require.NoError(t, err)
	require.Equal(t, "UpdateContent", mode)
	repo.AssertExpectations(t)
}

// TestUpdateCheck_AlreadyAlignedHashSkipsBridge keeps the existing fast path intact.
func TestUpdateCheck_AlreadyAlignedHashSkipsBridge(t *testing.T) {
	svc, repo, file, current, _ := newBridgeFixture(t, bridgeSize)
	file.ContentHash = current

	mode, fileDTO, err := svc.UpdateCheck(context.Background(), bridgeUID, bridgeCheckRequest(current, 1000))
	require.NoError(t, err)
	require.Equal(t, "", mode)
	require.Equal(t, current, fileDTO.ContentHash)
	repo.AssertNotCalled(t, "UpdateContentHashIfCurrent", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestBaselineDelete_AfterLegacyLargeHashRealignRejectsOldHash pins the self-healing
// conflict a second device sees after the stored hash was upgraded in place: its old
// base_hash is rejected once as local_deleted, the conflict carries the new server hash,
// and retrying with that hash under the same revision token succeeds.
// This is a known, one-time conflict, not data loss — the revision token never moved.
//
// TestBaselineDelete_AfterLegacyLargeHashRealignRejectsOldHash 固定哈希就地升级后的一次性
// 冲突：另一台设备仍拿升级前的旧哈希做条件删除会被拒一次（local_deleted），冲突里带着
// 服务端的新哈希；换成新哈希、同一版本令牌重试即放行。这是已知且可自愈的一次性冲突，
// 不是数据丢失——版本令牌始终没有变化。
func TestBaselineDelete_AfterLegacyLargeHashRealignRejectsOldHash(t *testing.T) {
	svc, repo, file, current, legacy := newBridgeFixture(t, bridgeSize)
	const rev = "rev-1"
	file.Revision = rev
	repo.On("UpdateContentHashIfCurrent", mock.Anything, bridgeFileID, bridgeUID, legacy, current).
		Return(true, nil)

	_, _, err := svc.UpdateCheck(context.Background(), bridgeUID, bridgeCheckRequest(current, 1000))
	require.NoError(t, err)
	require.Equal(t, current, file.ContentHash, "哈希必须就地升级为三分段口径")
	require.Equal(t, rev, file.Revision, "哈希升级不得改动版本令牌")
	repo.AssertExpectations(t)

	conflictOf := func(reason string) error { return baselineConflictFromFile(reason, file) }

	// 旧哈希 + 当前令牌：内容校验失败，拒绝一次。
	// Old hash + current token: the content check rejects it once.
	staleCommit := dto.BuildBaselineCommit(legacy, false, rev, false)
	err = checkBaselineDelete(staleCommit, file.Revision, file.ContentHash, conflictOf)
	var conflict *dto.BaselineConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, dto.SyncConflictReasonLocalDeleted, conflict.Reason)
	require.Equal(t, current, conflict.ServerHash, "冲突里必须带服务端当前的新哈希")
	require.Equal(t, rev, conflict.ServerRevision)

	// 冲突里给的新哈希 + 同一令牌：放行。
	// New hash from the conflict + same token: accepted.
	freshCommit := dto.BuildBaselineCommit(current, false, rev, false)
	require.NoError(t, checkBaselineDelete(freshCommit, file.Revision, file.ContentHash, conflictOf))
}
