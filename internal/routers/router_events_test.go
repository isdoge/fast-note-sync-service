package routers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/locales/en"
	"github.com/go-playground/locales/zh"
	ut "github.com/go-playground/universal-translator"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/require"
)

type routeEventRecorder struct {
	*httptest.ResponseRecorder
	flush func(string)
}

func (w *routeEventRecorder) Flush() { w.ResponseRecorder.Flush(); w.flush(w.Body.String()) }

func TestWebGUIEventRouteAuthentication(t *testing.T) {
	for _, test := range []struct {
		name, client, bound, vaults, query string
		issue                              int
		missing                            bool
		status                             int
	}{
		{name: "no token", client: "webgui", missing: true, status: 401},
		{name: "manual token", client: "webgui", issue: 2, bound: "*", status: 403},
		{name: "non WebGUI login", client: "obsidian", issue: 1, bound: "obsidian", status: 403},
		{name: "spoofed WebGUI header", client: "webgui", issue: 1, bound: "obsidian", status: 403},
		{name: "missing client", issue: 1, bound: "webgui", status: 403},
		{name: "restricted denied", client: "webgui", issue: 1, bound: "webgui", vaults: "A", query: "?vault=B", status: 403},
		{name: "multi vault denied", client: "webgui", issue: 1, bound: "webgui", vaults: "A", query: "?vault=A,B", status: 403},
		{name: "literal star", client: "webgui", issue: 1, bound: "webgui", vaults: "A", query: "?vault=*", status: 403},
		{name: "login", client: "webgui", issue: 1, bound: "webgui", status: 200},
		{name: "query client", issue: 1, bound: "webgui", query: "?client=webgui", status: 200},
		{name: "restricted all allowed", client: "webgui", issue: 1, bound: "webgui", vaults: "A,B", query: "?vault=A,B", status: 200},
		{name: "restricted implicit", client: "webgui", issue: 1, bound: "webgui", vaults: "A", status: 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			tokenSvc := &adminGroupTokenService{activeToken: &domain.AuthToken{ID: 5, UID: 1, TokenString: "event-test-nonce", Scope: "p:rest c:* f:*", IssueType: test.issue, ClientType: test.bound, Vaults: test.vaults}}
			a := newAdminGroupTestApp(t, tokenSvc)
			a.Config().Security.AuthTokenKey = "event-test-signing-key"
			a.Config().App.DefaultContextTimeout = 0
			wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, a)
			router := gin.New()
			registerAPIRoutes(router, a, wss, ut.New(en.New(), en.New(), zh.New()))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			req := httptest.NewRequest(http.MethodGet, "/api/webgui/events"+test.query, nil).WithContext(ctx)
			if !test.missing {
				req.Header.Set("Authorization", "Bearer "+adminGroupJWT(t, a.Config().Security.AuthTokenKey, "event-test-nonce", 1, 5))
			}
			req.Header.Set("X-Client", test.client)
			rec := &routeEventRecorder{ResponseRecorder: httptest.NewRecorder()}
			published := false
			rec.flush = func(body string) {
				if !published {
					published = true
					wss.BroadcastToUser(1, code.Success.WithVault("forbidden-vault").WithData(dto.NoteDTO{Path: "forbidden.md"}), "NoteSyncModify")
					wss.BroadcastToUser(1, code.Success.WithVault("A").WithData(dto.NoteDTO{Path: "allowed.md", Content: "private body"}), "NoteSyncModify")
				}
				if strings.Contains(body, `"path":"allowed.md"`) {
					cancel()
				}
			}
			router.ServeHTTP(rec, req)
			require.Equal(t, test.status, rec.Code, rec.Body.String())
			if test.status == 200 {
				require.True(t, strings.HasPrefix(rec.Body.String(), "event: hello\n"))
				require.Contains(t, rec.Body.String(), `"path":"allowed.md"`)
				require.NotContains(t, rec.Body.String(), "private body")
				if test.vaults != "" {
					require.NotContains(t, rec.Body.String(), "forbidden.md")
				}
			} else {
				require.NotContains(t, rec.Body.String(), "event: hello")
			}
		})
	}
}
