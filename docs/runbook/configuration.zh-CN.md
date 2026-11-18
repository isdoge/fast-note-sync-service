# 配置项参考

本文以 3.7.0 的 `internal/config/*.go`、`internal/app/config.go` 和 `config/config.yaml` 为准。表中默认值是代码实际填充的值，不是注释推断值。路径相对于服务工作目录。时长支持 `s`、`m`、`h`，以及本项目扩展的 `d`。

## 部署时先检查安全项

- 用 `openssl rand -base64 32` 生成并设置 `security.auth-token-key`，不要保留默认值。修改它会使既有令牌失效；即使不修改，机器标识变化也会使既有用户令牌和分享令牌失效。
- 同时把 `security.share-token-key` 改成独立随机值。
- 无需开放注册时设置 `user.register-is-enable: false`。
- `user.admin-uid` 应设为指定管理员的 UID；`0` 表示未指定管理员，不是“任何人都是管理员”。
- `server.trusted-proxies` 默认只信任回环代理。跨主机代理或容器网关必须按应用实际看到的来源 IP/CIDR 配置，不要填 `0.0.0.0/0`。
- WebGUI 登录令牌默认绑定 IP 和 UA。链路改变客户端 IP 时可关闭 `webgui-login-token-bind-ip`；代理改写 UA 导致 313 时可关闭 `webgui-login-token-bind-ua`。关闭后对应维度的校验不再生效。

## server

| 键 | 类型 | 默认值 | 作用与修改场景 |
| --- | --- | --- | --- |
| `run-mode` | string | `release` | Gin 运行模式，可用 `release` / `debug`。调试时改。 |
| `http-port` | string | `:9000` | 主 HTTP/WebSocket 监听地址。端口冲突或只监听指定接口时改。 |
| `read-timeout` | int 秒 | `60` | HTTP 读取超时。慢请求需要更长读取时间时调整。 |
| `write-timeout` | int 秒 | `60` | HTTP 写入超时。 |
| `private-http-listen` | string | 空，关闭 | 私有监控/度量监听地址；需要独立私有端口时设如 `:9090`。 |
| `mcp-sse-ping-interval` | int 秒 | `30` | MCP SSE 保活间隔；SSE 链路需要不同心跳节奏时改。 |
| `mcp-disable-localhost-protection` | bool | `false` | 关闭 MCP localhost DNS 重绑定保护。仅可信本机代理/Tunnel 保留公网 Host 且 MCP 不对不可信本地客户端开放时开启。 |
| `webgui-port` | string | 空，关闭 | 独立 WebGUI 端口，如 `:9001`。 |
| `share-port` | string | 空，关闭 | 独立分享页面端口，如 `:9002`。 |
| `share-base-url` | string | 空 | 外部分享基础 URL；同时使用独立 WebGUI 和分享端口时设置。 |
| `ext-api-url` | string | 空 | WebGUI 使用的公开 API 地址；外部地址与内部监听地址不同时设置。 |
| `cors-allowed-origins` | string[] | 空，从 `ext-api-url` 推断 | 跨域 Origin 白名单；需要额外来源时显式列出。 |
| `trusted-proxies` | string[] | `[]`，仅回环 | 信任转发 Host、协议和客户端 IP 的代理 IP/CIDR。 |
| `custom-response-headers` | map | 空 | 给全部响应添加自定义 HTTP 头；确有部署要求时配置。 |

## app

| 键 | 类型 | 默认值 | 作用与修改场景 |
| --- | --- | --- | --- |
| `default-page-size` | int | `10` | 默认分页条数。 |
| `max-page-size` | int | `100` | 分页条数上限。 |
| `default-context-timeout` | int 秒 | `60` | 请求上下文默认超时。 |
| `temp-path` | path | `storage/temp` | 上传等临时文件目录；需要独立临时盘时改。 |
| `is-return-sussess` | bool | `false` | 是否返回成功详情消息；键名拼写必须照写。 |
| `soft-delete-retention-time` | duration | `90d` | 软删除笔记保留期；`0` 永久保留。 |
| `sync-log-retention-time` | duration | `30d` | 同步日志保留期。 |
| `history-keep-versions` | int | `100` | 每篇笔记保留的历史版本数；显式 `0` 不清理。 |
| `history-save-delay` | duration | `10s` | 历史保存延迟。 |
| `upload-session-timeout` | duration | `1d` | 上传会话超时；长中断上传需要更长恢复窗口时调整。 |
| `file-chunk-size` | size | `512KB` | 文件上下行分片大小。 |
| `download-session-timeout` | duration | `1h` | 下载会话超时。 |
| `sync-down-chunk-num` | int | `200` | 下行每页条目数。 |
| `sync-up-chunk-num` | int | `100` | 上行每批条目数。 |
| `pipeline-window-up` | int | `8` | pv2 上行流水线窗口，范围钳制为 0–32；`0` 退回逐批等待。 |
| `pipeline-window-down` | int | `4` | pv2 下行流水线窗口，范围钳制为 0–16；`0` 退回逐页等待。 |
| `worker-pool-max-workers` | int | `100` | Worker Pool 最大协程数。 |
| `worker-pool-queue-size` | int | `1000` | Worker Pool 队列容量；出现 302 且确为排队容量不足时评估。 |
| `write-queue-capacity` | int | `1000` | 每用户写队列容量。 |
| `write-queue-timeout` | duration | `30s` | 写队列操作超时。 |
| `write-queue-idle-time` | duration | `10m` | 写队列空闲清理时间。 |
| `ws-read-max-payload-size` | size | `128MB` | WebSocket 最大入站载荷。 |
| `ws-write-max-payload-size` | size | `128MB` | WebSocket 最大出站载荷。 |
| `ws-parallel-enabled` | bool | `true` | WebSocket 并行处理开关。 |
| `ws-parallel-golimit` | int | `3` | 每连接并行处理协程上限。示例 YAML 的 `5` 会覆盖代码默认。 |
| `ws-check-utf8-enabled` | bool | `true` | WebSocket UTF-8 校验。 |
| `ws-compression-enabled` | bool | `true` | WebSocket 压缩开关。 |
| `ws-compression-level` | int | `1` | 压缩级别 1–9。 |
| `ws-compression-threshold` | int 字节 | `512` | 达到该大小才压缩。 |
| `ws-write-timeout` | int 秒 | `10` | 应用层写超时；显式 `0` 禁用。 |
| `ws-ping-interval` | int 秒 | `25` | WebSocket ping 间隔；代码校验要求大于 0。Tunnel 空闲断线时可与下一项一起调整。 |
| `ws-ping-wait` | int 秒 | `60` | 上行存活超时，之后还有宽限；Tunnel 可先试 `180`。只延后重连，不能修复持续丢 pong 的链路。 |
| `pull-source` | string | `auto` | 更新数据拉取源；支持 `auto` / `github`，旧值按 GitHub 处理。 |
| `pull-release-channel` | string | `stable` | 更新通道 `stable` / `beta`；示例 YAML 未列出但代码支持。 |
| `fts-bleve-enabled` | bool | `true` | Bleve 全文搜索；32 位 ARM 自动禁用，其余架构遵循此项。 |
| `fts-bleve-store-raw` | bool | `false` | 是否在索引保存原文；`false` 节省空间，不能提供原文高亮存储。 |

## security

| 键 | 类型 | 默认值 | 作用与修改场景 |
| --- | --- | --- | --- |
| `auth-token-key` | string | `fast-note-sync-Auth-Token` | 用户令牌签名密钥组成部分；部署时必须更换。 |
| `token-expiry` | duration | `365d` | 用户令牌有效期；必须为正且不能溢出。 |
| `share-token-key` | string | `fns` | 分享令牌签名密钥组成部分；部署时必须更换。 |
| `share-token-expiry` | duration | `30d` | 分享令牌有效期。 |
| `webgui-login-token-expiry` | duration | `7d` | WebGUI 自动签发登录令牌有效期。 |
| `webgui-login-token-bind-ip` | bool | `true` | WebGUI 令牌绑定 IP；客户端 IP 随链路变化时可关。 |
| `webgui-login-token-bind-ua` | bool | `true` | WebGUI 令牌绑定 UA；UA 被网关改写导致 313 时可关。 |

## database 与 user-database

两段使用同一结构。`user-database.type` 为空时使用主数据库；非空时为每用户数据配置独立后端。MySQL/MariaDB 当前不受支持，请使用 SQLite 或 PostgreSQL。

| 键 | 类型 | database 默认 | user-database 默认 | 作用 |
| --- | --- | --- | --- | --- |
| `type` | string | `sqlite` | 空 | `sqlite` / `postgres`；`mysql` 代码可识别但不建议用于本版。 |
| `path` | path | `storage/database/db.sqlite3` | 空 | SQLite 文件路径。 |
| `host` | string | 空 | 空 | 网络数据库主机。 |
| `port` | int | `0` | `0` | 网络数据库端口。 |
| `username` / `password` | string | 空 | 空 | 数据库凭据。 |
| `name` | string | 空 | 空 | 数据库名。 |
| `ssl-mode` | string | 空 | 空 | PostgreSQL SSL 模式。 |
| `table-prefix` | string | 空 | 空 | 表前缀。 |
| `schema` | string | 空 | 空 | PostgreSQL schema。示例 YAML 中 user-database 的 `public` 是显式配置，不是代码默认。 |
| `auto-migrate` | bool | `true` | `true` | 自动建表/结构迁移。 |
| `charset` | string | 空 | 空 | 数据库字符集，按所用后端设置。 |
| `parse-time` | bool | `false` | `false` | MySQL 时间字段解析开关；示例 YAML 显式设为 `true`。 |
| `max-open-conns` | int | `100` | `100` | 最大打开连接；显式 `0` 不限制。 |
| `max-idle-conns` | int | `10` | `10` | 最大空闲连接；显式 `0` 不保留空闲连接。 |
| `conn-max-lifetime` | duration | `30m` | `30m` | 连接最大复用时长。 |
| `conn-max-idle-time` | duration | `10m` | `10m` | 连接最大空闲时长。 |
| `enable-write-queue` | bool | `true` | `true` | 异步写队列开关。 |
| `max-write-concurrency` | int | `0` | `0` | 关闭写队列时的最大并发写；`0` 或负数不限。 |

## 其余段落

### log / user / tracer

| 键 | 类型 | 默认值 | 作用与修改场景 |
| --- | --- | --- | --- |
| `log.level` | string | `warn` | `debug` / `info` / `warn` / `error`；排错可临时降为 `info` 或 `debug`。 |
| `log.file` | path | `storage/logs/log.log` | 日志文件路径。 |
| `log.production` | bool | `false` | `true` 使用 JSON 日志；示例 YAML 显式为 `true`。 |
| `user.register-is-enable` | bool | `false` | 开放注册开关；示例 YAML 显式为 `true`。不需要公开注册时应关闭。 |
| `user.admin-uid` | int | `0` | 管理员 UID；设为实际管理员 UID。 |
| `tracer.enabled` | bool | `true` | 请求链路追踪。 |
| `tracer.header` | string | `X-Trace-ID` | Trace ID 请求头名。 |

### short-link

| 键 | 类型 | 默认值 | 修改场景 |
| --- | --- | --- | --- |
| `base-url` | string | `https://sink.cool` | 自有短链服务地址。 |
| `api-key` | string | `SinkCool` | 短链服务 API Key，应按实际服务更换。 |
| `password` | string | 空 | 短链访问密码。 |
| `cloaking` | bool | `false` | 短链隐藏重定向。 |

### storage

| 键 | 类型 | 默认值 | 作用与修改场景 |
| --- | --- | --- | --- |
| `local-fs.is-enable` | bool | `false` | 本地文件存储开关。 |
| `local-fs.httpfs-is-enable` | bool | `true` | 本地文件 HTTP 直接访问。 |
| `local-fs.save-path` | path | `storage/uploads` | 本地文件保存目录。 |
| `aliyun-oss.is-enable` | bool | `true` | 允许配置阿里云 OSS 存储；具体连接参数在 WebGUI 存储配置中维护。 |
| `aws-s3.is-enable` | bool | `true` | 允许配置 AWS S3。 |
| `cloudflare-r2.is-enable` | bool | `true` | 允许配置 Cloudflare R2。 |
| `minio.is-enable` | bool | `true` | 允许配置 MinIO。 |
| `webdav.is-enable` | bool | `true` | 允许配置 WebDAV。 |

### git / webgui / cloudflare

| 键 | 类型 | 默认值 | 作用与修改场景 |
| --- | --- | --- | --- |
| `git.name` | string | `FNS Service` | Git 提交作者名。 |
| `git.email` | string | `fns@email.com` | Git 提交邮箱，按部署者身份修改。 |
| `git.allowed-private-hosts` | string[] | `[]` | 私网 HTTP(S) Git 仓库白名单；主机名与 IP/CIDR 的匹配规则见反代理/内网访问文档。 |
| `webgui.font-set` | string | 空 | 空用默认，`local` 用本地字体，也可填字体 URL；示例 YAML 显式为 `local`。 |
| `cloudflare.enabled` | bool | `false` | 启用内置 Cloudflare Tunnel。 |
| `cloudflare.token` | string | 空 | Tunnel Token；启用时必填。 |
| `cloudflare.log-enabled` | bool | `false` | 额外写 `storage/logs/cloudflared_tunnel.log`。 |

### oauth（MCP OAuth）

| 键 | 类型 | 默认值 | 作用与修改场景 |
| --- | --- | --- | --- |
| `enabled` | bool | `false` | MCP OAuth 开关。 |
| `resource` | string | 空 | 受保护资源标识；启用时必填。 |
| `authorization-servers` | string[] | 空 | 授权服务器列表；启用时至少一个。 |
| `jwks-url` / `issuer` | string | 空 | JWT 公钥地址与签发者；启用时必填。 |
| `audience` | string[] | 空 | 允许的受众。 |
| `scopes-supported` | string[] | `notes:read`, `notes:write`, `files:read`, `files:write`, `vaults:read` | 支持的作用域。 |
| `required-scopes` | string[] | `notes:read`, `files:read`, `vaults:read` | 必需作用域。 |
| `resource-name` | string | 空 | 资源显示名。 |
| `allow-static-fns-token` | bool | `true` | 是否也接受静态 FNS 令牌。 |
| `default-client` / `default-client-name` | string | `ChatGPT` | 默认客户端 ID/名称。 |
| `default-client-version` | string | 空 | 默认客户端版本。 |
| `default-vault-name` | string | 空 | 默认映射笔记库。 |
| `default-fns-scope` | string | 空 | 默认 FNS 权限范围。 |
| `subject-mapping.mode` | string | `email_or_fixed_uid` | `email` / `fixed_uid` / `email_or_fixed_uid`。 |
| `subject-mapping.claim` | string | `email` | 邮箱映射 Claim。 |
| `subject-mapping.fixed-uid` | int64 | `0` | 固定 UID 映射目标。 |
| `stytch.enabled` | bool | `false` | Stytch 托管认证开关。 |
| `stytch.kind` | string | `consumer` | `consumer` / `b2b`。 |
| `stytch.domain` / `project-id` / `secret` | string | 空 | Stytch 连接参数；启用时必填。 |
| `stytch.user-id` | string | 空 | Stytch 用户 ID。 |
| `stytch.user-id-prefix` | string | `fns:` | 自动创建用户 ID 前缀。 |
| `stytch.organization-id` / `member-id` | string | 空 | B2B 必填的组织/成员 ID。 |

### oidc（WebGUI SSO）

OIDC 配置同时支持历史顶层单 Provider 与 `providers[]`。两者共用以下键：`display-name` 默认 `Login with OIDC`，`callback-path` 默认 `/api/user/auth/oidc/callback`，`scopes` 默认 `openid, profile, email`，`auto-register` 默认 `false`，`issuer`、`client-id`、`client-secret`、`redirect-url` 默认空且启用时必填。`oidc.enabled` 默认 `false`。Provider 的 `id` 默认 `default`，多 Provider 必须显式设置且只能含字母、数字、下划线、连字符；非 default 的默认回调路径末尾加 `/id`。

`user-mapping`（顶层或 Provider 级）默认：`subject-claim: sub`、`email-claim: email`、`username-claim: preferred_username`、`display-name-claim: name`。完整示例与配置方式见 [OIDC 运行手册](OIDC.zh-CN.md)。

### attachment-static

| 键 | 类型 | 默认值 | 作用与修改场景 |
| --- | --- | --- | --- |
| `is-enable` | bool | `false` | 免 Token 附件静态入口；有明确静态代理需求才开启。 |
| `allowed-vaults` | map[string][]string | 空 | UID 到允许库名的白名单；未配置不会开启访问。 |
| `allowed-types` | string[] | 空 | 允许的文件后缀白名单；未配置不会开启访问。 |
