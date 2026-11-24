# Troubleshooting and FAQ

Keep service logs, client error codes, and timestamps before changing settings. Temporarily set `log.level` to `info` or `debug` for more context. Never paste complete tokens or configured secrets into a public issue.

## Common symptoms

### Sync makes no progress

- **Cause**: Upstream has two races fixed in 3.7.0: the first message after Protobuf negotiation can be lost, and the first page acknowledgement can arrive before the page queue is registered. A plugin-initiated disconnect can also leave a round without resetting its state.
- **Action**: Upgrade the service to 3.7.0 and plugin to 2.5.0, then fully reconnect or restart the plugin and run sync manually. A round left by a disconnect may wait for the plugin's five-minute stagnation detector; older versions can remain stuck indefinitely. If it persists, record the last Action, error code, and matching server Trace ID.
- **Expected retry behavior**: A file that continuously fails to download causes that type's next round to restart from its last successful baseline, deliberately preferring extra downloads over missed changes.

### Devices repeatedly disconnect and reconnect

- **Cause**: Links such as Cloudflare Tunnel can lose pongs. Incorrect `trusted-proxies` can also make an IP-bound WebGUI token observe changing client addresses. A completely unresponsive connection closes after about 100 seconds by default.
- **Action**: Check proxy Upgrade headers and `trusted-proxies`. For a Tunnel, try `app.ws-ping-wait: 180` and increase `ws-ping-interval` if needed. Disable `security.webgui-login-token-bind-ip` if WebGUI logouts follow client-IP changes.
- **Limit**: Increasing timeouts only delays reconnection when pongs are continuously lost; the current plugin has no idle application-level heartbeat. Inbound data frames refresh liveness, so a slow upload should not itself be treated as a dead connection.

### File times show 1970

- **Cause**: Some clients report second-scale ctime/mtime where milliseconds are expected.
- **Action**: Upgrade the service to 3.7.0 and plugin to 2.5.0. Write entry points normalize seconds, and first startup migrates existing `note/file/folder` values.
- **Limit**: A 1970 time already written on a plugin device is not corrected automatically; the next file modification restores it. Do not delete and re-download local files because the deletion syncs. Normalization treats `0 < v < 1e11` as seconds, so genuine millisecond values from 1970–1973 are also multiplied incorrectly.

### All tokens become invalid after moving hosts or recreating a container

- **Cause**: Without a stable signing key, the user JWT HMAC key is `security.auth-token-key + "_" + machine identity`; share tokens also include that identity. The identity prefers the system machine ID, then motherboard serial, and only then `config/.server_uuid`. Preserving just the UUID file cannot cover a change in either higher-priority source.
- **Action**: Upgrade to 3.8.0. Set `security.token-signing-key`, or leave it empty so first startup generates `storage/token-signing.key`, and keep that setting or key file across migration or reinstallation. Tokens issued after that no longer become invalid when the machine identity changes.
- **Limits**: Tokens issued under the machine-ID scheme before the stable key was enabled still become invalid on a machine with a different ID; log in again and reissue them. In-place upgrades on the same machine are unaffected. `.server_uuid` is still not a general migration solution. If upgrading is impossible, preserve the original machine-identity source and secrets; otherwise log in to WebGUI again, issue and configure new plugin/API tokens, and recreate invalid shares.

### Protobuf decode failed and sync cannot recover

- **Cause**: Client/server frame formats or negotiation states differ. Upstream also forces JSON fallback frames through the Protobuf decoder after Protobuf has been negotiated.
- **Action**: Upgrade both ends to plugin 2.5.0 and service 3.7.0. The service recognizes `Action|{...}` JSON text frames even on a negotiated Protobuf connection, and the plugin falls back to JSON for that connection after decode failure. Reconnect afterward. Custom clients should check consistency among `pv`, `pb`, `protocol=protobuf`, and `ClientInfo.protobuf`; see `docs/ws_api.md`.

### Restricted-token calls rejected after upgrading (vault management, recycle-bin purge, recursive folder delete, MCP)

- **Cause**: This release splits vault-level destructive operations out of note/file write permission into dedicated function points: `vault_w` (create/rename/delete vault), `note_purge` / `file_purge` (purging the whole recycle bin), recursive folder delete requires both note and file write, MCP tools require the token's protocol dimension to include `mcp` explicitly (newly issued tokens no longer gain it implicitly), and REST share endpoints require note read/write. `f:*` (unrestricted) tokens are unaffected.
- **Action**: Error 315 details name the missing function point (for example `vault_w` or `note_purge`) or vault. For manual tokens, add the capability in WebGUI token management or use `*`; add `mcp` to the protocol dimension of MCP tokens; for OAuth connectors, request the matching scope (`vaults:write`, `notes:purge`, `files:purge`). A restricted-token "missing vault" message on WebSocket means a message omitted the vault field; include it.
- **Do not loosen the checks for old tokens**: these denials are exactly the privilege-escalation surface being fixed; re-issue tokens when the old granularity is genuinely required.

### Attachment downloads or WebGUI requests return 313

- **Cause**: A WebGUI login token is bound to the User-Agent at issuance, and the browser or proxy-rewritten UA no longer matches.
- **Action**: First check whether the proxy rewrites UA unnecessarily. If rewriting is required, set `security.webgui-login-token-bind-ua: false` and log in again. This disables UA checks. Error 312 is an IP mismatch and cannot be fixed with the UA switch.

### Git reports unsupported git protocol / only http and https

- **Cause**: Only HTTP(S) repositories are accepted. `ssh://`, `git://`, `user@host:path`, and server-local paths are rejected.
- **Action**: Use the repository's `http://` or `https://` URL and matching HTTP credentials. Validation has a 20-second timeout.

### Git reports private network access not allowed

- **Cause**: The target or a DNS result is in a blocked private range. `100.64.0.0/10`, including Tailscale, is covered.
- **Action**: Configure the smallest necessary `git.allowed-private-hosts` entries. A hostname entry still needs an IP/CIDR covering every private DNS result, and actual connection addresses are checked too. See the reverse-proxy/private-network guide.

### MySQL / MariaDB schema or runtime errors

- **Cause**: Release 3.8.0 fixes MySQL / MariaDB table creation, concurrent first-time table creation, reserved-word columns, case-sensitive paths (`varbinary(1024)` / `varbinary(255)`), and migration issues. Remaining failures are typically caused by missing configuration, insufficient database privileges, or the upgrade byte-length precheck.
- **Action**:
  1. If user data still goes to local SQLite or per-user database creation fails, confirm `user-database.type: mysql` and its connection parameters are explicitly configured (leaving `user-database.type` empty still writes to local SQLite files by default), and verify the database account has `CREATE DATABASE` privileges to create `user_<uid>` databases automatically.
  2. If upgrading from `<=3.7.0` fails on startup with `mysql upgrade precheck failed` (path exceeding 1024 UTF-8 bytes or vault name exceeding 255 bytes), the schema has not been altered yet; shorten the offending path or vault name using the database, table, column, and sample ID printed in the error message, then restart.
  3. The repository does not provide an automatic cross-database migration tool; changing `database.type` or `user-database.type` alone does not migrate existing data between backends.

### Full-text search is unavailable on 32-bit ARM

- **Cause**: On `GOARCH=arm`, Bleve is disabled automatically to avoid a zapx unaligned 64-bit atomic-operation crash.
- **Action**: This is the current protective behavior and logs a startup warning; `fts-bleve-enabled: true` cannot override it. Other sync features continue. A non-32-bit-ARM build follows the configuration.

## Error-code reference

Business codes normally appear in an HTTP 200 response body and are not HTTP status codes. This table covers codes users can encounter directly; `details` provides operation-specific context.

| Code | Meaning | Common cause and action |
| --- | --- | --- |
| 0 | Failure | Unclassified failure; inspect details and logs. |
| 1 / 2 / 3 / 4 / 5 / 6 | Success / created / updated / deleted / password updated / no update | Success codes, not failures. |
| 300 | Internal server error | Unclassified internal error; inspect logs at that time. |
| 301 | Database query failed | Database connectivity, permission, SQL, or schema issue. |
| 302 | Server busy | Worker pool full or closed; retry and inspect load. |
| 303 | Too many requests | Login/request throttling; reduce retry rate. |
| 304 | API not found | Path, version, or proxy route mismatch. |
| 305 | Invalid parameters | Invalid field, path, vault, or format; inspect details. |
| 306 | Invalid access token | Invalid signature, token state/type, or binding. |
| 307 | Login required | Authenticate again. |
| 308 | Login state invalid | Log in again. |
| 309 | User token missing | Supply a user token. |
| 310 | Token expired | Log in or issue a new token. |
| 311 | Token generation failed | Inspect issuance logs and lifetime configuration. |
| 312 | Token IP restricted | Client IP changed or trusted proxies are wrong. |
| 313 | Token UA restricted | UA changed or was rewritten. |
| 314 | Token client restricted | Token ClientType differs from the request client. |
| 315 | Token scope restricted | Missing protocol/client/function permission or vault access. |
| 400 | Registration failed | Inspect details; disabled registration has code 410. |
| 401 | Login failed | Inspect details. |
| 402 | Wrong username or password | Check credentials. |
| 403 | User not found | User deleted or UID/identity mapping wrong. |
| 404 | User already exists | Use another username or log in. |
| 405 | Email already exists | Use another email or log in. |
| 406 | Invalid username | Use 3–15 letters, digits, or underscores. |
| 407 | Invalid password | Set a password satisfying the rules. |
| 408 | Current password incorrect | Check the old password. |
| 409 | Passwords do not match | Re-enter them. |
| 410 | Registration disabled | Enable `user.register-is-enable` or have an administrator create the account. |
| 411 | Administrator required | Use the account identified by `user.admin-uid`. |
| 412 | Local filesystem disabled | Enable `storage.local-fs.is-enable`. |
| 413 | User update failed | Inspect fields and database logs. |
| 414 | Cannot block administrator | Choose a non-administrator. |
| 420 / 421 | Vault missing / already exists | Check vault name and state. |
| 422 / 423 | Invalid storage / cloud storage type | Use a supported type. |
| 430 / 431 | Note missing / already exists | Refresh state and retry. |
| 432 | Note retrieval failed | Inspect body files and database logs. |
| 433 / 434 | Note save / content update failed | Inspect path, storage, and database errors. |
| 435 | Note deletion failed | Inspect details and refresh deletion state. |
| 436 | Note list failed | Inspect database or index logs. |
| 437 / 438 | Rename failed / target exists | Choose another path or resolve the existing note. |
| 439 / 440 | Note sync / update check failed | Reconnect, sync, and inspect details. |
| 441 | Target note already exists | Resolve the conflicting target; distinct from the sync conflict reported as 530. |
| 442 | No match found | Change the search. |
| 443 | Invalid regex | Correct the expression. |
| 444 | Path contains `..`, or the write targets a system junk file | Use a vault-relative path without traversal. When the details read `system junk file is not synced`, a `.DS_Store`, `Thumbs.db`, `desktop.ini` or `._*` file was refused, which is expected; set `app.sync-exclude-system-junk` to `false` if you really need to sync them. |
| 445 / 446 | Folder missing / already exists | Refresh folder state. |
| 447–451 | Folder get/save/delete/list/rename failed | Inspect paths and storage logs. |
| 455 / 456 | Attachment missing / retrieval failed | Refresh attachment state and inspect storage. |
| 457–462 | Attachment save/content/delete/list/upload/check failed | Inspect session, storage, and details. |
| 463 | Upload session not found | Session expired or service recreated; restart upload. |
| 464 / 466 | File save / read failed | Inspect disk permission/space and object storage. |
| 465 | File move failed | Often a rename; inspect source, target, and storage. |
| 467 | File already exists | Choose another path or resolve the existing attachment. |
| 470–479 | Setting missing/conflict/read/write/sync/save failure | Refresh settings and inspect details. |
| 480–484 | Share missing/expired/revoked/password required/wrong | Check share state or recreate it. |
| 491 / 492 | History missing / restore failed | Refresh history and inspect storage. |
| 493–499 | Backup missing/failed/unknown type/invalid arguments/disabled | Check backup configuration, storage, and task state. |
| 500–502 | Storage missing/type disabled/validation failed | Check storage configuration and `storage.*.is-enable`. |
| 510–512 | Git configuration missing/task running/validation failed | Wait for the task; use details to fix URL, credentials, branch, or network. |
| 520 / 521 | cloudflared download failed / binary missing | Check GitHub connectivity or place the binary manually. |
| 530 | Sync conflict | Legacy realtime sync conflicts create a conflict copy; rejected conditional writes create none—handle the returned conflict information. |
| 531 | Stale revival ignored | Server already has a tombstone; refresh local state instead of re-uploading the identical old version. |
