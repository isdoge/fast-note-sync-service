# 环境变量（可选）
# include .env
# export $(shell sed 's/=.*//' .env)

# -------------------------
# 项目配置
# -------------------------
REPO = $(eval REPO := $$(shell go list -f '{{.ImportPath}}' .))$(value REPO)

P_NAME          = fast-note-sync
P_BIN           = fast-note-sync-service

# -------------------------
# Git / 构建信息
# -------------------------
Version         ?= $(shell sed -nE 's/^[[:space:]]*Version[[:space:]]+string[[:space:]]*=[[:space:]]*"([^"]+)".*/\1/p' internal/app/version.go)
GitVersion      = $(shell git log -1 --format=%h)
GitVersionDesc  = $(shell git log -1 --format=%s)
BuildTime       = $(shell date +%FT%T%z)

# LDFLAGS: 注入版本信息到二进制
# COMMIT_MSG 清洗：正文只通过环境变量（$$COMMIT_MSG）交给 shell，绝不把内容展开进命令字面量，
# 否则正文里的反引号 / $(...) 会被 /bin/sh 当作命令执行。
#   - 去掉 \r，换行统一替换为 @@@（不用 tr '\n' '^'，避免污染正文中已有的 ^）；
#   - ASCII 双引号 → “，ASCII 单引号 → ’：既不闭合外层单引号，也不闭合 Go -ldflags 的双引号
#     （Go 的 quoted.Split 在引号内不做转义，任何 ASCII " 都会提前闭合字段）；
#   - 正文中的 $ 先换成占位符，再由 $(subst) 还原成字面 $，避免 $GITHUB_OUTPUT 之类的文本被吃掉。
# 注意：$(shell) 读到的是环境变量（CI 以此方式传入）；GNU make 3.81 的 $(shell) 看不到命令行变量，
# 因此请用 `COMMIT_MSG=... make ...` 而非 `make COMMIT_MSG=...`。
export COMMIT_MSG
COMMIT_MSG_CLEAN = $(subst @@FN_DOLLAR@@,$$,$(shell printf '%s' "$$COMMIT_MSG" | python3 -c 'import sys;d=sys.stdin.read().rstrip(chr(13)+chr(10));d=d.replace(chr(13),"").replace(chr(10),"@@@").replace(chr(34),"“").replace(chr(39),"’").replace(chr(36),"@@FN_DOLLAR@@");sys.stdout.write(d)'))
Changelog       ?= $(COMMIT_MSG_CLEAN)
LDFLAGS = -ldflags '-s -w -X ${REPO}/internal/app.Version=$(Version) -X "${REPO}/internal/app.GitTag=$(GitVersion)" -X ${REPO}/internal/app.BuildTime=$(BuildTime) -X "${REPO}/internal/app.Changelog=$(Changelog)"'

# go 命令封装
gob = go build ${LDFLAGS}
gor = go run ${LDFLAGS}

# 编译相关
CGO = CGO_ENABLED=0

rootDir = $(shell pwd)
buildDir = $(rootDir)/build

# -------------------------
# PHONY 目标
# -------------------------
.PHONY: all  build-all run test clean \
        build-macos-amd64 build-macos-arm64 build-linux-amd64 \
        build-linux-arm64 build-linux-arm build-windows-amd64 gox-linux gox-all \
		docs fmt update air dev ver gen sup proto

# 默认目标
all: test build-all

# -------------------------
# 简单目标
# -------------------------
sup:
	node scripts/process_support_csv.js
	node scripts/process_support.mjs --model Qwen/Qwen3.6-35B-A3B

test:
	go test $$(go list ./... | grep -v -E 'internal/service/mocks|internal/domain/mocks|internal/dto|internal/model|internal/query|internal/config|internal/app|/docs|internal/middleware|cmd')

dev:
	air -c ./scripts/.air.toml

air:
	air -c ./scripts/.air.toml

fmt:
	go fmt ./...

update:
	go get -u ./...

# 更新版本脚本调用
ver:
	@node ./scripts/update-version.js $(filter-out $@,$(MAKECMDGOALS))

# 捕获 ver 后面的参数，防止 make 将其视为目标
%:
	@:

# 生成 internal/proto/v1/sync.pb.go。
# 需要 protoc（macOS: brew install protobuf；Ubuntu: apt-get install protobuf-compiler）与
# protoc-gen-go（go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11，确保 $(go env GOPATH)/bin 在 PATH 中）。
# 插件侧的 src/pb/v1/sync.js / sync.d.ts 由插件仓库的 `pnpm run proto` 从同一份 sync.proto 生成，
# 两端必须用同一份文件生成，禁止手工编辑生成物。
proto:
	protoc --go_out=. --go_opt=paths=source_relative internal/proto/v1/sync.proto

gen: proto
	go run -v ./cmd/gorm_gen/gen.go -type sqlite -dsn storage/database/db_full.sqlite3
	go run -v ./cmd/model_gen/gen.go

docs:
	go run github.com/swaggo/swag/cmd/swag@latest init -g main.go -o ./docs --parseDependency --parseInternal

# 运行
run:
#	$(call checkStatic)
	$(call init)
	$(gor) -v $(rootDir)

clean:
	rm -rf $(buildDir)

# -------------------------
# 构建集合
# -------------------------
build-all:
#	$(call checkStatic)
	$(MAKE) build-macos-amd64
	$(MAKE) build-macos-arm64
	$(MAKE) build-linux-amd64
	$(MAKE) build-linux-arm64
	$(MAKE) build-linux-arm
	$(MAKE) build-windows-amd64

# macOS
build-macos-amd64:
	$(CGO) GOOS=darwin GOARCH=amd64 $(gob) -o $(buildDir)/darwin_amd64/${P_BIN} $(bin) -v $(rootDir)

build-macos-arm64:
	$(CGO) GOOS=darwin GOARCH=arm64 $(gob) -o $(buildDir)/darwin_arm64/${P_BIN} -v $(rootDir)

# Linux
build-linux-amd64:
# CGO_ENABLED=1 CC=musl-gcc  GOOS=linux GOARCH=amd64 $(gob) -o $(buildDir)/linux_amd64/${P_BIN} -v $(rootDir)
	$(CGO) GOOS=linux GOARCH=amd64 $(gob) -o $(buildDir)/linux_amd64/${P_BIN} -v $(rootDir)

build-linux-arm64:
	$(CGO) GOOS=linux GOARCH=arm64 $(gob) -o $(buildDir)/linux_arm64/${P_BIN} -v $(rootDir)

build-linux-arm:
	$(CGO) GOOS=linux GOARCH=arm GOARM=7 $(gob) -o $(buildDir)/linux_arm/${P_BIN} -v $(rootDir)

# Windows
build-windows-amd64:
# CGO_ENABLED=0 CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC="x86_64-w64-mingw32-gcc -fno-stack-protector -D_FORTIFY_SOURCE=0 -lssp" $(gob) -o $(bin).exe -v $(rootDir)
	$(CGO) GOOS=windows GOARCH=amd64 $(gob) -o $(buildDir)/windows_amd64/${P_BIN}.exe -v $(rootDir)

# gox 辅助
gox-linux:
	$(CGO) GOARM=7 gox ${LDFLAGS} -osarch="linux/amd64 linux/arm64 linux/arm" -output="$(buildDir)/{{.OS}}_{{.Arch}}/${P_BIN}"

gox-all:
	$(CGO) GOARM=7 gox ${LDFLAGS} -osarch="darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 linux/arm windows/amd64" -output="$(buildDir)/{{.OS}}_{{.Arch}}/${P_BIN}"


# -------------------------
# 代码片段（定义）
# -------------------------
define init
	@echo "Build Init"
endef
