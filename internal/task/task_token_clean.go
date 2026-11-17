package task

import (
	"context"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"go.uber.org/zap"
)

// TokenCleanTask 定期清理已过期的登录令牌（签发类型 1），范围与手动清理接口的默认行为一致；
// 手工创建的 API 令牌和未过期的令牌不受影响。
type TokenCleanTask struct {
	app    *app.App
	logger *zap.Logger
}

func (t *TokenCleanTask) Name() string { return "TokenClean" }

func (t *TokenCleanTask) LoopInterval() time.Duration { return 24 * time.Hour }

func (t *TokenCleanTask) IsStartupRun() bool { return true }

func (t *TokenCleanTask) Run(ctx context.Context) error {
	uids, err := t.app.UserRepo.GetAllUIDs(ctx)
	if err != nil {
		return err
	}
	for _, uid := range uids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := t.app.TokenService.CleanExpired(ctx, uid, 1); err != nil {
			t.logger.Warn("failed to clean expired login tokens",
				zap.Int64("uid", uid), zap.Error(err))
		}
	}
	return nil
}

func NewTokenCleanTask(appContainer *app.App) (Task, error) {
	return &TokenCleanTask{app: appContainer, logger: appContainer.Logger()}, nil
}

func init() {
	RegisterWithApp(NewTokenCleanTask)
}
