package routers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 广播归类守卫：业务广播必须显式携带 vault，供发送端按令牌范围和连接声明过滤。
// 目前不存在需要不带 vault 而送达全部连接的广播；确有这种需求时，应在下方按
// "relative/file.go:line" 加入放行清单，并在该位置说明用途，不能默认绕过受限连接。
var broadcastWithoutVaultAllowlist = map[string]string{}

func TestBroadcastCallsCarryVault(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	callCount := 0
	for _, dir := range []string{"internal/routers/websocket_router", "internal/routers/api_router", "internal/routers/mcp_router"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(root, dir, entry.Name())
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("解析 %s: %v", path, err)
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "BroadcastResponse" && selector.Sel.Name != "BroadcastToUser" {
					return true
				}
				callCount++
				hasVault := false
				if len(call.Args) > 0 {
					codeArg := call.Args[0]
					if selector.Sel.Name == "BroadcastToUser" && len(call.Args) > 1 {
						codeArg = call.Args[1]
					}
					ast.Inspect(codeArg, func(node ast.Node) bool {
						selector, ok := node.(*ast.SelectorExpr)
						if ok && selector.Sel.Name == "WithVault" {
							hasVault = true
						}
						return true
					})
				}
				if hasVault {
					return true
				}
				location := filepath.Join(dir, entry.Name()) + ":" + strconv.Itoa(fset.Position(call.Pos()).Line)
				if reason, allowed := broadcastWithoutVaultAllowlist[location]; allowed && reason != "" {
					t.Logf("显式放行无库名广播 %s：%s", location, reason)
					return true
				}
				t.Errorf("广播 %s (%s) 未携带 WithVault；请补库名，或在守卫测试中按位置显式放行并说明用途", location, selector.Sel.Name)
				return true
			})
		}
	}
	if callCount == 0 {
		t.Fatal("未发现广播调用，守卫未实际扫描到业务代码")
	}
}
