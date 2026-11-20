package service

import (
	"errors"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/stretchr/testify/require"
)

// TestCheckBaselineDelete_UnifiedLocalDeletedReason 覆盖 E2E-4：实时删除的每一种拒绝
// （令牌过期、内容变化、同内容重建、缺基线）都必须回 local_deleted，与批量删除语义一致。
// Every realtime delete rejection must use local_deleted, matching the batch delete path.
func TestCheckBaselineDelete_UnifiedLocalDeletedReason(t *testing.T) {
	const currentRevision = "0123456789abcdef0123456789abcdef"
	conflictErr := func(reason string) error {
		return &dto.BaselineConflictError{Reason: reason}
	}
	cases := []struct {
		name     string
		commit   *dto.BaselineCommit
		revision string
		hash     string
	}{
		{"stale revision", &dto.BaselineCommit{HasBase: true, BaseHash: "H1", BaseRevision: "stale"}, currentRevision, "H1"},
		{"missing revision", &dto.BaselineCommit{HasBase: true, BaseHash: "H1"}, currentRevision, "H1"},
		{"same-content rebuild", &dto.BaselineCommit{HasBase: true, BaseHash: "H1", BaseRevision: currentRevision}, "fedcba9876543210fedcba9876543210", "H1"},
		{"content moved", &dto.BaselineCommit{HasBase: true, BaseHash: "H0", BaseRevision: currentRevision}, currentRevision, "H1"},
		{"no baseline but hash present", &dto.BaselineCommit{BaseRevision: currentRevision}, currentRevision, "H1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkBaselineDelete(tc.commit, tc.revision, tc.hash, conflictErr)
			require.Error(t, err)
			var conflict *dto.BaselineConflictError
			require.True(t, errors.As(err, &conflict))
			require.Equal(t, dto.SyncConflictReasonLocalDeleted, conflict.Reason)
		})
	}

	// 全部匹配时才放行，且旧客户端（无基线）保持无条件删除。
	require.NoError(t, checkBaselineDelete(&dto.BaselineCommit{HasBase: true, BaseHash: "H1", BaseRevision: currentRevision}, currentRevision, "H1", conflictErr))
	require.NoError(t, checkBaselineDelete(nil, currentRevision, "H1", conflictErr))
}

// TestCheckBaselineCreate_CarriedTokenReportsServerMissing 覆盖 P1-08：携带基线令牌
// 却落到"新建"分支，说明服务端行已被清理或重置，必须报 server_missing（保留本地），
// 而不是 remote_deleted（会让插件误以为远端做过删除）。
// A carried baseline token landing on the create branch must report server_missing.
func TestCheckBaselineCreate_CarriedTokenReportsServerMissing(t *testing.T) {
	err := checkBaselineCreate(&dto.BaselineCommit{HasBase: true, BaseHash: "H1", BaseRevision: "rev-1"})
	require.Error(t, err)
	var conflict *dto.BaselineConflictError
	require.True(t, errors.As(err, &conflict))
	require.Equal(t, dto.SyncConflictReasonServerMissing, conflict.Reason)

	// 用户已明确解决：允许创建。
	require.NoError(t, checkBaselineCreate(&dto.BaselineCommit{
		HasBase: true, BaseHash: "H1", BaseRevision: "rev-1", IsConflictResolved: true,
	}))
	// 无令牌：正常新建，不受影响。
	require.NoError(t, checkBaselineCreate(&dto.BaselineCommit{}))
	require.NoError(t, checkBaselineCreate(nil))
}
