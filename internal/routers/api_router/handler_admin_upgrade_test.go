package api_router

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/assert"
)

// 容器内运行时拒绝原地升级，提示更新镜像。
func TestAdminControlHandler_Upgrade_RefusedInContainer(t *testing.T) {
	orig := runningInContainer
	runningInContainer = func() bool { return true }
	defer func() { runningInContainer = orig }()

	handler, _, _ := newTestAdminHandler()
	c, w := newAdminTestContext("GET", "/api/admin/upgrade?version=latest", "", 1)
	handler.Upgrade(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.ErrorUpgradeInContainer.Code())
}

// 已有升级在下载时，再次触发直接拒绝。
func TestAdminControlHandler_Upgrade_RejectsConcurrent(t *testing.T) {
	orig := runningInContainer
	runningInContainer = func() bool { return false }
	defer func() { runningInContainer = orig }()

	upgradeMu.Lock()
	defer upgradeMu.Unlock()

	handler, _, _ := newTestAdminHandler()
	c, w := newAdminTestContext("GET", "/api/admin/upgrade?version=latest", "", 1)
	handler.Upgrade(c)

	assertResponseCode(t, w, code.ErrorUpgradeInProgress.Code())
}

// 下载上下文不随请求上下文取消，且有自己的时限。
func TestUpgradeDownloadContext_DetachedFromRequest(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancelParent()

	ctx, cancel := upgradeDownloadContext(parent)
	defer cancel()

	<-parent.Done()
	assert.NoError(t, ctx.Err(), "请求上下文超时后下载不应被取消")
	deadline, ok := ctx.Deadline()
	assert.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(upgradeDownloadTimeout), deadline, 5*time.Second)
}
