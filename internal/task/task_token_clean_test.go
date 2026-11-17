package task

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func tokenCleanTestApp(t *testing.T) *app.App {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "tokens.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.AuthToken{}))
	d := dao.New(db, context.Background(), dao.WithConfig(&config.DatabaseConfig{}))
	a := app.NewTestApp(&app.Services{
		TokenService: service.NewTokenService(dao.NewAuthTokenRepository(d), nil, nil, zap.NewNop(), service.TokenServiceConfig{}),
	}, db)
	a.Repositories = &app.Repositories{UserRepo: dao.NewUserRepository(d)}
	return a
}

func TestTokenCleanRegisteredAndScope(t *testing.T) {
	a := tokenCleanTestApp(t)
	require.NoError(t, a.DB.Create(&[]model.User{{UID: 1}, {UID: 2}}).Error)
	past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	tokens := []model.AuthToken{
		{ID: 1, UID: 1, IssueType: 1, ClientType: "WebGUI", ExpiredAt: past, Status: 1},
		{ID: 2, UID: 2, IssueType: 1, ClientType: "webgui", ExpiredAt: past, Status: 1},
		{ID: 3, UID: 1, IssueType: 1, ClientType: "webgui", ExpiredAt: future, Status: 1},
		{ID: 4, UID: 1, IssueType: 2, ClientType: "webgui", ExpiredAt: past, Status: 1},
		{ID: 5, UID: 1, IssueType: 3, ClientType: "mcp", ExpiredAt: past, Status: 1},
		{ID: 6, UID: 2, IssueType: 4, ClientType: "webgui", ExpiredAt: past, Status: 1},
		{ID: 7, UID: 2, IssueType: 1, ClientType: "mcp", ExpiredAt: past, Status: 1},
	}
	require.NoError(t, a.DB.Create(&tokens).Error)
	// 读取数据库保存后的值，避免 SQLite 时间往返的时区/单调时钟表示差异。
	require.NoError(t, a.DB.Order("id").Find(&tokens).Error)

	// 通过注册表查找，缺少自动任务时此测试必须失败。
	var cleanTask Task
	for _, factory := range GetFactoriesWithApp() {
		candidate, err := factory(a)
		require.NoError(t, err)
		if candidate != nil && candidate.Name() == "TokenClean" {
			cleanTask = candidate
			break
		}
	}
	require.NotNil(t, cleanTask, "expired login token cleanup must be registered")
	require.Equal(t, 24*time.Hour, cleanTask.LoopInterval())
	require.True(t, cleanTask.IsStartupRun())
	require.NoError(t, cleanTask.Run(context.Background()))
	var actual []model.AuthToken
	require.NoError(t, a.DB.Order("id").Find(&actual).Error)
	require.Len(t, actual, len(tokens))
	for i, token := range actual {
		want := tokens[i]
		// 与手动清理接口的默认范围一致：已过期的登录令牌（签发类型 1）全部注销，不区分客户端。
		if want.ID <= 2 || want.ID == 7 {
			require.EqualValues(t, 0, token.Status)
			want.Status = token.Status
			want.UpdatedAt = token.UpdatedAt
		}
		require.Equal(t, want, token, "token %d", token.ID)
	}
	// 再运行一次应保持幂等。
	require.NoError(t, cleanTask.Run(context.Background()))
}

type failingTokenCleaner struct {
	service.TokenService
	called []int64
}

func (s *failingTokenCleaner) CleanExpired(_ context.Context, uid int64, issueType int) error {
	s.called = append(s.called, uid)
	if uid == 1 {
		return errors.New("user cleanup failed")
	}
	return s.TokenService.CleanExpired(context.Background(), uid, issueType)
}

func TestTokenCleanContinuesAfterUserFailure(t *testing.T) {
	a := tokenCleanTestApp(t)
	require.NoError(t, a.DB.Create(&[]model.User{{UID: 1}, {UID: 2}}).Error)
	repo := dao.NewAuthTokenRepository(dao.New(a.DB, context.Background(), dao.WithConfig(&config.DatabaseConfig{})))
	token, err := repo.Create(context.Background(), &domain.AuthToken{
		UID: 2, IssueType: 1, ClientType: "webgui", Status: 1, ExpiredAt: time.Now().Add(-time.Hour),
	})
	require.NoError(t, err)
	svc := &failingTokenCleaner{TokenService: a.TokenService}
	a.TokenService = svc
	core, logs := observer.New(zap.WarnLevel)
	task := &TokenCleanTask{app: a, logger: zap.New(core)}
	require.NoError(t, task.Run(context.Background()))
	require.ElementsMatch(t, []int64{1, 2}, svc.called)
	require.Equal(t, 1, logs.Len())
	require.EqualValues(t, 1, logs.All()[0].ContextMap()["uid"])
	got, err := repo.GetByID(context.Background(), token.ID)
	require.NoError(t, err)
	require.EqualValues(t, 0, got.Status)
}
