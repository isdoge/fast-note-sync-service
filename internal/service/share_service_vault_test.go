package service

import (
	"context"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// newShareServiceForTest builds a shareService with mocked repositories.
// newShareServiceForTest 用 mock 仓储构造 shareService。
func newShareServiceForTest(repo *domainmocks.MockUserShareRepository, noteRepo *domainmocks.MockNoteRepository, fileRepo *domainmocks.MockFileRepository, vaultRepo *domainmocks.MockVaultRepository) *shareService {
	return &shareService{
		repo:         repo,
		tokenManager: &mockTokenManager{},
		noteRepo:     noteRepo,
		fileRepo:     fileRepo,
		vaultRepo:    vaultRepo,
		logger:       zap.NewNop(),
	}
}

// #461：受限令牌（只允许 B 库）列分享，A 库的分享必须被过滤，只返回 B 库的。
func TestShareService_ListShares_RestrictedVaultsFiltered(t *testing.T) {
	repo := new(domainmocks.MockUserShareRepository)
	noteRepo := new(domainmocks.MockNoteRepository)
	fileRepo := new(domainmocks.MockFileRepository)
	vaultRepo := new(domainmocks.MockVaultRepository)
	svc := newShareServiceForTest(repo, noteRepo, fileRepo, vaultRepo)

	// 两条分享：id=1 属于 A 库（note 11），id=2 属于 B 库（note 22）
	shares := []*domain.UserShare{
		{ID: 1, UID: 1, ResType: "note", ResID: 11, Status: 1},
		{ID: 2, UID: 1, ResType: "note", ResID: 22, Status: 1},
	}
	repo.On("CountByUID", mock.Anything, int64(1)).Return(int64(2), nil)
	repo.On("ListByUID", mock.Anything, int64(1), "created_at", "desc", 0, 10).Return(shares, nil)

	noteRepo.On("GetByID", mock.Anything, int64(11), int64(1)).Return(&domain.Note{ID: 11, VaultID: 101}, nil)
	noteRepo.On("GetByID", mock.Anything, int64(22), int64(1)).Return(&domain.Note{ID: 22, VaultID: 202}, nil)
	vaultRepo.On("GetByID", mock.Anything, int64(101), int64(1)).Return(&domain.Vault{ID: 101, Name: "A"}, nil)
	vaultRepo.On("GetByID", mock.Anything, int64(202), int64(1)).Return(&domain.Vault{ID: 202, Name: "B"}, nil)

	// 过滤后只剩 B 库分享，填充标题时批量查询
	noteRepo.On("ListByIDs", mock.Anything, []int64{22}, int64(1)).Return([]*domain.Note{{ID: 22, VaultID: 202, Path: "b.md"}}, nil)

	items, count, err := svc.ListShares(context.Background(), 1, "created_at", "desc", &pkgapp.Pager{Page: 1, PageSize: 10}, "B")

	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Len(t, items, 1)
	require.Equal(t, int64(2), items[0].ID)
	require.Equal(t, "B", items[0].VaultName)
	repo.AssertExpectations(t)
	noteRepo.AssertExpectations(t)
	vaultRepo.AssertExpectations(t)
}

// #461：非受限令牌（allowedVaults 为空）列分享，行为不变，全部返回。
func TestShareService_ListShares_UnrestrictedUnchanged(t *testing.T) {
	repo := new(domainmocks.MockUserShareRepository)
	noteRepo := new(domainmocks.MockNoteRepository)
	fileRepo := new(domainmocks.MockFileRepository)
	vaultRepo := new(domainmocks.MockVaultRepository)
	svc := newShareServiceForTest(repo, noteRepo, fileRepo, vaultRepo)

	shares := []*domain.UserShare{
		{ID: 1, UID: 1, ResType: "note", ResID: 11, Status: 1},
		{ID: 2, UID: 1, ResType: "note", ResID: 22, Status: 1},
	}
	repo.On("CountByUID", mock.Anything, int64(1)).Return(int64(2), nil)
	repo.On("ListByUID", mock.Anything, int64(1), "created_at", "desc", 0, 10).Return(shares, nil)

	// 填充标题时批量查询笔记与 vault 名称（原有逻辑）
	noteRepo.On("ListByIDs", mock.Anything, []int64{11, 22}, int64(1)).Return([]*domain.Note{
		{ID: 11, VaultID: 101, Path: "a.md"},
		{ID: 22, VaultID: 202, Path: "b.md"},
	}, nil)
	vaultRepo.On("GetByID", mock.Anything, int64(101), int64(1)).Return(&domain.Vault{ID: 101, Name: "A"}, nil)
	vaultRepo.On("GetByID", mock.Anything, int64(202), int64(1)).Return(&domain.Vault{ID: 202, Name: "B"}, nil)

	items, count, err := svc.ListShares(context.Background(), 1, "created_at", "desc", &pkgapp.Pager{Page: 1, PageSize: 10}, "")

	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.Len(t, items, 2)
	repo.AssertExpectations(t)
	noteRepo.AssertExpectations(t)
	vaultRepo.AssertExpectations(t)
}

// #461：GetShareVaultName 解析笔记分享所属库（note → vault）。
func TestShareService_GetShareVaultName_Note(t *testing.T) {
	repo := new(domainmocks.MockUserShareRepository)
	noteRepo := new(domainmocks.MockNoteRepository)
	fileRepo := new(domainmocks.MockFileRepository)
	vaultRepo := new(domainmocks.MockVaultRepository)
	svc := newShareServiceForTest(repo, noteRepo, fileRepo, vaultRepo)

	repo.On("GetByID", mock.Anything, int64(1), int64(10)).Return(&domain.UserShare{ID: 10, ResType: "note", ResID: 11}, nil)
	noteRepo.On("GetByID", mock.Anything, int64(11), int64(1)).Return(&domain.Note{ID: 11, VaultID: 101}, nil)
	vaultRepo.On("GetByID", mock.Anything, int64(101), int64(1)).Return(&domain.Vault{ID: 101, Name: "A"}, nil)

	name, err := svc.GetShareVaultName(context.Background(), 1, 10)
	require.NoError(t, err)
	require.Equal(t, "A", name)
	repo.AssertExpectations(t)
}

// #461：GetShareVaultName 解析文件分享所属库（file → vault）。
func TestShareService_GetShareVaultName_File(t *testing.T) {
	repo := new(domainmocks.MockUserShareRepository)
	noteRepo := new(domainmocks.MockNoteRepository)
	fileRepo := new(domainmocks.MockFileRepository)
	vaultRepo := new(domainmocks.MockVaultRepository)
	svc := newShareServiceForTest(repo, noteRepo, fileRepo, vaultRepo)

	repo.On("GetByID", mock.Anything, int64(1), int64(20)).Return(&domain.UserShare{ID: 20, ResType: "file", ResID: 21}, nil)
	fileRepo.On("GetByID", mock.Anything, int64(21), int64(1)).Return(&domain.File{ID: 21, VaultID: 202}, nil)
	vaultRepo.On("GetByID", mock.Anything, int64(202), int64(1)).Return(&domain.Vault{ID: 202, Name: "B"}, nil)

	name, err := svc.GetShareVaultName(context.Background(), 1, 20)
	require.NoError(t, err)
	require.Equal(t, "B", name)
	repo.AssertExpectations(t)
}
