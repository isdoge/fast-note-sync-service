package app

import (
	"errors"
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

type rejectingValidator struct{}

func (rejectingValidator) ValidateStruct(any) error { return errors.New("invalid request") }

type validatorBindApp struct{ AppContainer }

func (validatorBindApp) Validator() ValidatorInterface { return rejectingValidator{} }

// 只有成功完成绑定、库权限检查及参数校验的请求才声明笔记库；JSON 和 Protobuf 共用此约定。
func TestWebsocketBindAndValid_RecordsDeclaredVaultAfterSuccess(t *testing.T) {
	server := &WebsocketServer{ProtobufDecoder: func(action string, data []byte, obj any) (bool, error) {
		if string(data) == "invalid" {
			return false, errors.New("invalid protobuf")
		}
		obj.(*vaultBindReq).Vault = string(data)
		return true, nil
	}}
	newClient := func(vaults string, protobuf bool) *WebsocketClient {
		return &WebsocketClient{Server: server, Vaults: vaults, app: vaultBindApp{}, useProtobuf: protobuf}
	}
	for _, test := range []struct {
		name, data, allowedVault, acceptedVault string
		protobuf, success                       bool
	}{
		{name: "JSON allowed", data: `{"vault":"vault-a","path":"a.md"}`, acceptedVault: "vault-a", success: true},
		{name: "JSON restricted", data: `{"vault":"vault-b","path":"a.md"}`, allowedVault: "vault-a"},
		{name: "JSON empty", data: `{"vault":"","path":"a.md"}`, acceptedVault: "other-vault", success: true},
		{name: "JSON invalid", data: `{"vault":`},
		{name: "Protobuf allowed", data: "vault-a", protobuf: true, acceptedVault: "vault-a", success: true},
		{name: "Protobuf restricted", data: "vault-b", allowedVault: "vault-a", protobuf: true},
		{name: "Protobuf invalid", data: "invalid", protobuf: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := newClient(test.allowedVault, test.protobuf)
			var req vaultBindReq
			ok, _ := c.BindAndValidWithAction("NoteModify", []byte(test.data), &req)
			if ok != test.success {
				t.Fatalf("BindAndValidWithAction ok=%v, want %v", ok, test.success)
			}
			c.broadcastVaultsMu.RLock()
			declaredCount := len(c.declaredVaults)
			_, vaultDeclared := c.declaredVaults[test.acceptedVault]
			c.broadcastVaultsMu.RUnlock()
			if test.success && req.Vault != "" {
				if declaredCount != 1 || !vaultDeclared {
					t.Fatalf("成功绑定的库未进入集合：count=%d, declared=%v", declaredCount, vaultDeclared)
				}
			} else if declaredCount != 0 {
				t.Fatalf("失败或空库名请求产生了 %d 条库声明", declaredCount)
			}
		})
	}

	t.Run("request without Vault field", func(t *testing.T) {
		c := newClient("", false)
		if ok, _ := c.BindAndValidWithAction("ClientInfo", []byte(`{"path":"a.md"}`), &struct {
			Path string `json:"path"`
		}{}); !ok {
			t.Fatal("无 Vault 字段的请求应绑定成功")
		}
		if len(c.declaredVaults) != 0 {
			t.Fatalf("无 Vault 字段的请求不得声明笔记库：%v", c.declaredVaults)
		}
		// 尚未声明任何库的连接不接收带库名的广播（随后的首次同步会把内容拉齐），
		// 但用户级（不带库名）广播照常接收。
		if c.acceptsBroadcast("vault-a") {
			t.Fatal("未声明库的连接不应接收带库名的广播")
		}
		if !c.acceptsBroadcast("") {
			t.Fatal("未声明库的连接仍应接收用户级广播")
		}
	})

	t.Run("parameter validation failure", func(t *testing.T) {
		c := &WebsocketClient{Server: server, app: validatorBindApp{}}
		var req vaultBindReq
		if ok, _ := c.BindAndValidWithAction("NoteModify", []byte(`{"vault":"vault-a"}`), &req); ok {
			t.Fatal("参数校验失败的请求不得绑定成功")
		}
		if len(c.declaredVaults) != 0 {
			t.Fatalf("参数校验失败不得声明笔记库：%v", c.declaredVaults)
		}
		if c.acceptsBroadcast("vault-b") {
			t.Fatal("未声明库的连接不应接收带库名的广播")
		}
	})
}
