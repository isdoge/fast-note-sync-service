package api_router

import (
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAdminControlHandler_RejectsOverflowingTokenExpiry(t *testing.T) {
	for _, field := range []string{"tokenExpiry", "shareTokenExpiry", "webguiLoginTokenExpiry"} {
		t.Run(field, func(t *testing.T) {
			handler, app, _ := newTestAdminHandler()
			before := app.Config().Security
			c, w := newAdminTestContext("POST", "/api/admin/config", `{"`+field+`":"3650000000000000d"}`, 1)
			handler.UpdateConfig(c)
			assertResponseCode(t, w, code.ErrorInvalidParams.Code())
			require.Contains(t, w.Body.String(), field)
			require.Equal(t, before, app.Config().Security)
		})
	}
}
