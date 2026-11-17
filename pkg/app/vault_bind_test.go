package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type vaultBindReq struct {
	Vault string `json:"vault" form:"vault"`
	Path  string `json:"path" form:"path"`
}

// #458：受限令牌只允许 B 库。中间件按 query→form→JSON 精确键取 vault，而处理器绑定的是请求体；
// 查询串写 B、请求体写 A（或用大小写不同的键 "Vault"），绑定结果是 A，必须在绑定处拒绝。
func TestBindAndValid_RejectsBoundVaultOutsideTokenScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name, query, body, allowed string
		ok                         bool
	}{
		{"query B body A", "?vault=B", `{"vault":"A","path":"x.md"}`, "B", false},
		{"case-variant key", "", `{"Vault":"A","path":"x.md"}`, "B", false},
		{"empty vault with restricted token", "", `{"path":"x.md"}`, "B", false},
		{"allowed vault", "", `{"vault":"B","path":"x.md"}`, "B", true},
		{"unrestricted token", "", `{"vault":"A","path":"x.md"}`, "", true},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/note"+test.query, strings.NewReader(test.body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("vaults", test.allowed)
		var req vaultBindReq
		if ok, errs := BindAndValid(c, &req); ok != test.ok {
			t.Fatalf("%s: BindAndValid ok=%v (errs %v, bound vault %q), want %v", test.name, ok, errs, req.Vault, test.ok)
		}
	}
}

type vaultBindApp struct{ AppContainer }

func (vaultBindApp) Validator() ValidatorInterface { return nil }

// #458：protobuf 连接上前置拦截器按 JSON 解析 vault 失败即放行；绑定 protobuf 解码出的 vault 时必须校验令牌范围。
func TestWebsocketBindAndValid_RejectsProtobufVaultOutsideTokenScope(t *testing.T) {
	server := &WebsocketServer{ProtobufDecoder: func(action string, data []byte, obj any) (bool, error) {
		obj.(*vaultBindReq).Vault = string(data)
		return true, nil
	}}
	for _, test := range []struct {
		vault, allowed string
		ok             bool
	}{
		{"A", "B", false},
		{"", "B", false},
		{"B", "B", true},
		{"A", "", true},
	} {
		c := &WebsocketClient{Server: server, Vaults: test.allowed, app: vaultBindApp{}}
		c.useProtobuf = true
		var req vaultBindReq
		if ok, errs := c.BindAndValidWithAction("NoteSync", []byte(test.vault), &req); ok != test.ok {
			t.Fatalf("vault %q allowed %q: ok=%v (errs %v), want %v", test.vault, test.allowed, ok, errs, test.ok)
		}
	}
}
