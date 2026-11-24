package service_test

import (
	"os"
	"testing"
)

// TestMain runs the service test binary inside a temporary working directory. Production
// code under test writes relative to the process CWD (storage/vault/...), and a few
// fire-and-forget goroutines can outlive a test that switched directories with t.Chdir.
// Anchoring the binary in a temp directory keeps those late writes out of the repository.
// TestMain 让 service 测试二进制在临时工作目录里运行。被测代码按进程 CWD 拼接相对路径
// （storage/vault/...），而少量 fire-and-forget goroutine 可能活过用 t.Chdir 切换目录的
// 测试；把整个二进制锚在临时目录里，迟到写入就不会落进仓库。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fns-service-test-*")
	if err != nil {
		panic(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	if err := os.Chdir(dir); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.Chdir(wd)
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
