# Configuration reference

This reference follows the 3.7.0 implementation in `internal/config/*.go`, `internal/app/config.go`, and `config/config.yaml`. Defaults below are the values actually applied by code, not inferred from comments. Relative paths start at the service working directory. Durations support `s`, `m`, `h`, and this project's `d` extension.

## Security checks before deployment

- Generate a key with `openssl rand -base64 32` and set `security.auth-token-key`; do not keep the default. Changing it invalidates existing tokens. Existing user and share tokens also become invalid when the machine identity changes, even if this key stays unchanged.
- Set `security.share-token-key` to a separate random value as well.
- Set `user.register-is-enable: false` unless public registration is required.
- Set `user.admin-uid` to the intended administrator UID during deployment. **The implementation treats `0` as unrestricted administrator access: every authenticated user with a nonzero UID passes the administrator check, so the default is unsafe.** With `admin-uid: 0`, registration also stops accepting new users once any user exists, even when registration is enabled.
- `server.trusted-proxies` trusts only loopback proxies by default. For a proxy on another host or a container gateway, add the source IP/CIDR actually observed by the application. Do not use `0.0.0.0/0`.
- WebGUI login tokens bind to IP and UA by default. Disable `webgui-login-token-bind-ip` if the access path changes client IPs, or `webgui-login-token-bind-ua` if a proxy rewrites UA and causes error 313. Each switch disables its corresponding check.

## server

| Key | Type | Default | Purpose and when to change |
| --- | --- | --- | --- |
| `run-mode` | string | `release` | Gin mode: `release` or `debug`; use `debug` for debugging. |
| `http-port` | string | `:9000` | Main HTTP/WebSocket listen address; change for port conflicts or a specific interface. |
| `read-timeout` | int seconds | `60` | HTTP read timeout. |
| `write-timeout` | int seconds | `60` | HTTP write timeout. |
| `private-http-listen` | string | Empty, disabled | Private monitoring/metrics listener, for example `:9090`. |
| `mcp-sse-ping-interval` | int seconds | `30` | MCP SSE keepalive interval. |
| `mcp-disable-localhost-protection` | bool | `false` | Disable MCP localhost DNS-rebinding protection only for a trusted local proxy/Tunnel preserving the public Host, when MCP is not exposed to untrusted local clients. |
| `webgui-port` | string | Empty, disabled | Independent WebGUI listener, for example `:9001`. |
| `share-port` | string | Empty, disabled | Independent share-page listener, for example `:9002`. |
| `share-base-url` | string | Empty | Public share base URL when independent WebGUI and share ports are both used. |
| `ext-api-url` | string | Empty | Public API URL used by WebGUI when it differs from the internal listener. |
| `cors-allowed-origins` | string[] | Empty; inferred from `ext-api-url` | Explicit CORS Origin allowlist. |
| `trusted-proxies` | string[] | `[]`; loopback only | Proxy IPs/CIDRs trusted for forwarded host, scheme, and client IP. |
| `custom-response-headers` | map | Empty | Custom headers added to every response. |

## app

| Key | Type | Default | Purpose and when to change |
| --- | --- | --- | --- |
| `default-page-size` | int | `10` | Default page size. |
| `max-page-size` | int | `100` | Maximum page size. |
| `default-context-timeout` | int seconds | `60` | Default request context timeout. |
| `temp-path` | path | `storage/temp` | Temporary upload directory. |
| `is-return-sussess` | bool | `false` | Return success detail messages; the misspelled key name is intentional. |
| `soft-delete-retention-time` | duration | `90d` | Soft-deleted note retention; `0` keeps forever. |
| `sync-log-retention-time` | duration | `30d` | Sync log retention. |
| `history-keep-versions` | int | `100` | History versions per note; explicit `0` disables cleanup. |
| `history-save-delay` | duration | `10s` | Delay before saving history. |
| `upload-session-timeout` | duration | `1d` | Upload session recovery window. |
| `file-chunk-size` | size | `512KB` | File upload/download chunk size. |
| `download-session-timeout` | duration | `1h` | Download session timeout. |
| `sync-down-chunk-num` | int | `200` | Entries per download page. |
| `sync-up-chunk-num` | int | `100` | Entries per upload batch. |
| `pipeline-window-up` | int | `8` | pv2 upload pipeline, clamped to 0–32; `0` restores stop-and-wait. |
| `pipeline-window-down` | int | `4` | pv2 download pipeline, clamped to 0–16; `0` restores stop-and-wait. |
| `worker-pool-max-workers` | int | `100` | Maximum worker goroutines. |
| `worker-pool-queue-size` | int | `1000` | Worker queue capacity. |
| `write-queue-capacity` | int | `1000` | Write queue capacity per user. |
| `write-queue-timeout` | duration | `30s` | Write queue operation timeout. |
| `write-queue-idle-time` | duration | `10m` | Idle write queue cleanup interval. |
| `ws-read-max-payload-size` | size | `128MB` | Maximum inbound WebSocket payload. |
| `ws-write-max-payload-size` | size | `128MB` | Maximum outbound WebSocket payload. |
| `ws-parallel-enabled` | bool | `true` | Parallel WebSocket processing. |
| `ws-parallel-golimit` | int | `3` | Per-connection parallel goroutine limit. The sample YAML explicitly overrides this to `5`. |
| `ws-check-utf8-enabled` | bool | `true` | WebSocket UTF-8 validation. |
| `ws-compression-enabled` | bool | `true` | WebSocket compression. |
| `ws-compression-level` | int | `1` | Compression level 1–9. |
| `ws-compression-threshold` | int bytes | `512` | Minimum payload to compress. |
| `ws-write-timeout` | int seconds | `10` | Application write deadline; explicit `0` disables it. |
| `ws-ping-interval` | int seconds | `25` | WebSocket ping interval; code requires a positive value. Adjust with the next setting for idle Tunnel disconnects. |
| `ws-ping-wait` | int seconds | `60` | Inbound liveness timeout followed by a grace period; try `180` for Cloudflare Tunnel. This only delays reconnection when pongs are continuously lost. |
| `pull-source` | string | `auto` | Update source: `auto` or `github`; legacy values fall back to GitHub. |
| `pull-release-channel` | string | `stable` | Update channel `stable` or `beta`; supported by code but absent from the sample YAML. |
| `fts-bleve-enabled` | bool | `true` | Bleve full-text search; automatically disabled on 32-bit ARM. |
| `fts-bleve-store-raw` | bool | `false` | Store original text in the index; false uses less disk but does not store text for highlighting. |

## security

| Key | Type | Default | Purpose and when to change |
| --- | --- | --- | --- |
| `auth-token-key` | string | `fast-note-sync-Auth-Token` | Part of the user-token signing key; replace before deployment. |
| `token-expiry` | duration | `365d` | Default lifetime in the low-level JWT issuer. Manual tokens use requested `expiredDays`; WebGUI tokens use the dedicated setting below for their database expiry. Must be positive and representable. |
| `share-token-key` | string | `fns` | Part of the share-token signing key; replace before deployment. |
| `share-token-expiry` | duration | `30d` | Share token lifetime. |
| `webgui-login-token-expiry` | duration | `7d` | WebGUI login token lifetime. |
| `webgui-login-token-bind-ip` | bool | `true` | Bind WebGUI tokens to client IP. |
| `webgui-login-token-bind-ua` | bool | `true` | Bind WebGUI tokens to User-Agent. |

## database and user-database

Both sections use the same structure. An empty `user-database.type` inherits **the entire main database configuration**, so the other fields' code defaults are not used on this fallback path. A nonempty type selects the independent `user-database` configuration. MySQL/MariaDB is unsupported in this release; use SQLite or PostgreSQL.

| Key | Type | database default | user-database default | Purpose |
| --- | --- | --- | --- | --- |
| `type` | string | `sqlite` | Empty | `sqlite` or `postgres`; code recognizes `mysql`, but it is not recommended in this release. |
| `path` | path | `storage/database/db.sqlite3` | Empty | SQLite file. |
| `host` | string | Empty | Empty | Network database host. |
| `port` | int | `0` | `0` | Network database port. |
| `username` / `password` | string | Empty | Empty | Database credentials. |
| `name` | string | Empty | Empty | Database name. |
| `ssl-mode` | string | Empty | Empty | PostgreSQL SSL mode. |
| `table-prefix` | string | Empty | Empty | Table prefix. |
| `schema` | string | Empty | Empty | PostgreSQL schema. The sample YAML explicitly sets user-database to `public`; this is not the code default. |
| `auto-migrate` | bool | `true` | `true` | Automatic schema migration. |
| `charset` | string | Empty | Empty | Backend character set. |
| `parse-time` | bool | `false` | `false` | MySQL time parsing; the sample YAML explicitly enables it. |
| `max-open-conns` | int | `100` | `100` | Maximum open connections; explicit `0` is unlimited. |
| `max-idle-conns` | int | `10` | `10` | Maximum idle connections; explicit `0` keeps none. |
| `conn-max-lifetime` | duration | `30m` | `30m` | Connection reuse lifetime. |
| `conn-max-idle-time` | duration | `10m` | `10m` | Connection idle lifetime. |
| `enable-write-queue` | bool | `true` | `true` | Asynchronous write queue. |
| `max-write-concurrency` | int | `0` | `0` | Concurrent writes when the queue is disabled; zero or negative is unlimited. |

## Remaining sections

### log / user / tracer

| Key | Type | Default | Purpose and when to change |
| --- | --- | --- | --- |
| `log.level` | string | `warn` | `debug`, `info`, `warn`, or `error`; temporarily lower it for diagnostics. |
| `log.file` | path | `storage/logs/log.log` | Log file path. |
| `log.production` | bool | `false` | JSON logs when true; the sample YAML explicitly enables it. |
| `user.register-is-enable` | bool | `false` | Registration switch; the sample YAML explicitly enables it. |
| `user.admin-uid` | int | `0` | Every nonzero UID passes admin checks when zero; set the intended administrator UID. Also limits registration once users exist. |
| `tracer.enabled` | bool | `true` | Request tracing. |
| `tracer.header` | string | `X-Trace-ID` | Trace ID request header. |

### short-link

| Key | Type | Default | When to change |
| --- | --- | --- | --- |
| `base-url` | string | `https://sink.cool` | Use another short-link service. |
| `api-key` | string | `SinkCool` | Set the actual service API key. |
| `password` | string | Empty | Short-link access password. |
| `cloaking` | bool | `false` | Short-link cloaking. |

### storage

| Key | Type | Default | Purpose and when to change |
| --- | --- | --- | --- |
| `local-fs.is-enable` | bool | `false` | Local filesystem storage. |
| `local-fs.httpfs-is-enable` | bool | `true` | Direct HTTP access to local files. |
| `local-fs.save-path` | path | `storage/uploads` | Local file directory. |
| `aliyun-oss.is-enable` | bool | `true` | Allow Aliyun OSS storage configurations; connection details are maintained in WebGUI. |
| `aws-s3.is-enable` | bool | `true` | Allow AWS S3 configurations. |
| `cloudflare-r2.is-enable` | bool | `true` | Allow Cloudflare R2 configurations. |
| `minio.is-enable` | bool | `true` | Allow MinIO configurations. |
| `webdav.is-enable` | bool | `true` | Allow WebDAV configurations. |

### git / webgui / cloudflare

| Key | Type | Default | Purpose and when to change |
| --- | --- | --- | --- |
| `git.name` | string | `FNS Service` | Git commit author name. |
| `git.email` | string | `fns@email.com` | Git commit email; set the deployer's identity. |
| `git.allowed-private-hosts` | string[] | `[]` | Private HTTP(S) Git allowlist; matching rules are in the reverse-proxy/private-network guide. |
| `webgui.font-set` | string | Empty | Empty uses the default, `local` uses local fonts, or set a font URL; the sample YAML explicitly uses `local`. |
| `cloudflare.enabled` | bool | `false` | Built-in Cloudflare Tunnel. |
| `cloudflare.token` | string | Empty | Tunnel token; required when enabled. |
| `cloudflare.log-enabled` | bool | `false` | Also write `storage/logs/cloudflared_tunnel.log`. |

### oauth (MCP OAuth)

| Key | Type | Default | Purpose and when to change |
| --- | --- | --- | --- |
| `enabled` | bool | `false` | MCP OAuth switch. |
| `resource` | string | Empty | Protected resource identifier; required when enabled. |
| `authorization-servers` | string[] | Empty | Authorization servers; at least one when enabled. |
| `jwks-url` / `issuer` | string | Empty | JWT public-key URL and issuer; required when enabled. |
| `audience` | string[] | Empty | Allowed audiences. |
| `scopes-supported` | string[] | `notes:read`, `notes:write`, `files:read`, `files:write`, `vaults:read` | Supported scopes. |
| `required-scopes` | string[] | `notes:read`, `files:read`, `vaults:read` | Required scopes. |
| `resource-name` | string | Empty | Resource display name. |
| `allow-static-fns-token` | bool | `true` | Also accept static FNS tokens. |
| `default-client` / `default-client-name` | string | `ChatGPT` | Default client ID/name. |
| `default-client-version` | string | Empty | Default client version. |
| `default-vault-name` | string | Empty | Default mapped vault. |
| `default-fns-scope` | string | Empty | Default FNS scope. |
| `subject-mapping.mode` | string | `email_or_fixed_uid` | `email`, `fixed_uid`, or `email_or_fixed_uid`. |
| `subject-mapping.claim` | string | `email` | Email mapping claim. |
| `subject-mapping.fixed-uid` | int64 | `0` | Fixed mapping UID. |
| `stytch.enabled` | bool | `false` | Stytch hosted authentication. |
| `stytch.kind` | string | `consumer` | `consumer` or `b2b`. |
| `stytch.domain` / `project-id` / `secret` | string | Empty | Required when Stytch is enabled. |
| `stytch.user-id` | string | Empty | Stytch user ID. |
| `stytch.user-id-prefix` | string | `fns:` | Prefix for automatically created users. |
| `stytch.organization-id` / `member-id` | string | Empty | Required for B2B. |

### oidc (WebGUI SSO)

OIDC supports legacy top-level single-provider keys and `providers[]`. Shared keys default as follows: `display-name: Login with OIDC`, `callback-path: /api/user/auth/oidc/callback`, `scopes: openid, profile, email`, and `auto-register: false`. `issuer`, `client-id`, `client-secret`, and `redirect-url` default to empty and are required when enabled. `oidc.enabled` defaults to `false`. Provider `id` defaults to `default`; multiple providers need explicit IDs containing only letters, digits, underscores, or hyphens. A non-default provider's default callback path ends in `/id`.

`user-mapping`, globally or per provider, defaults to `subject-claim: sub`, `email-claim: email`, `username-claim: preferred_username`, and `display-name-claim: name`. See the [OIDC runbook](OIDC.en.md) for full examples.

### attachment-static

| Key | Type | Default | Purpose and when to change |
| --- | --- | --- | --- |
| `is-enable` | bool | `false` | Token-free static attachment entry; enable only for an explicit static-proxy requirement. |
| `allowed-vaults` | map[string][]string | Empty | UID-to-vault allowlist; access remains disabled without it. |
| `allowed-types` | string[] | Empty | Allowed file extensions; access remains disabled without it. |
