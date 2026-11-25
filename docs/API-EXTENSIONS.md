# API Extensions for Fast Note Sync Service

This document describes the new API endpoints added in the `feature/api-extensions` branch.

## New Endpoints

### Health Check
```
GET /api/health
```
Returns server health status including database connectivity and uptime.

**Response:**
```json
{
  "status": "healthy",
  "version": "1.11.1",
  "uptime": 123.45,
  "database": "connected"
}
```

### Note Operations

#### Patch Frontmatter
```
PATCH /api/note/frontmatter?vault=<vault>&path=<path>
```
Update or remove YAML frontmatter fields without modifying note body.

**Body:**
```json
{
  "updates": {"title": "New Title", "tags": ["a", "b"]},
  "remove": ["oldField"]
}
```

#### Append Content
```
POST /api/note/append?vault=<vault>&path=<path>
```
Append content to the end of a note.

**Body:**
```json
{
  "content": "\n\n## New Section\nAppended content"
}
```

#### Prepend Content
```
POST /api/note/prepend?vault=<vault>&path=<path>
```
Prepend content after frontmatter (if present) or at the beginning.

**Body:**
```json
{
  "content": "Prepended content\n\n"
}
```

#### Find and Replace
```
POST /api/note/replace?vault=<vault>&path=<path>
```
Find and replace text in a note. Supports regex.

**Body:**
```json
{
  "find": "old text",
  "replace": "new text",
  "regex": false,
  "all": true,
  "failIfNoMatch": false
}
```

**Response includes `matchCount`:**
```json
{
  "matchCount": 3,
  "note": { ... }
}
```

#### Move Note
```
POST /api/note/move?vault=<vault>&path=<path>
```
Move/rename a note to a new path.

**Body:**
```json
{
  "destination": "new/path/note.md",
  "overwrite": false
}
```

### Link Operations

#### Get Backlinks
```
GET /api/note/backlinks?vault=<vault>&path=<path>
```
Get all notes that link TO this note.

**Response:**
```json
{
  "data": [
    {
      "path": "other-note.md",
      "linkText": "alias",
      "context": "...surrounding text [[link]]..."
    }
  ]
}
```

#### Get Outlinks
```
GET /api/note/outlinks?vault=<vault>&path=<path>
```
Get all links FROM this note.

**Response:**
```json
{
  "data": [
    {
      "path": "target-note",
      "linkText": "display text",
      "context": "...[[target-note|display text]]..."
    }
  ]
}
```

### Note Creation

#### Create Only (Don't Update)
```
POST /api/note?vault=<vault>&path=<path>
```
With `createOnly: true`, returns error 430 if note already exists.

**Body:**
```json
{
  "content": "...",
  "createOnly": true
}
```

## New Error Codes

| Code | Message |
|------|---------|
| 460 | Destination note already exists |
| 461 | No match found |
| 462 | Invalid regex pattern |

## Configuration

New config option in `config.yaml` (also editable via Admin Settings API):
```yaml
app:
  default-api-folder: ""  # Optional: prepend this folder to note paths without /
```

## Known Limitations

### Backlinks

1. **Exact path matching only**: Backlinks match the stored link path exactly. Obsidian's "shortest path when possible" resolution is not fully replicated server-side.

2. **Extension handling**: Links are stored without `.md` extension (e.g., `[[Note1]]` stores "Note1"). Queries with full paths (e.g., "Note1.md") are normalized by stripping `.md`.

3. **Heading anchors**: Links with `#heading` (e.g., `[[note#section]]`) are stored as-is. Querying backlinks for "note.md" won't find links to "note#section".

4. **Relative paths**: Links using relative paths (e.g., `[[../folder/note]]`) are stored as-is. Cross-folder resolution is not performed.

### Link Indexing

- Links are indexed when notes are saved via the API
- Existing notes need to be re-saved to populate the link index
- Link parsing uses regex: `\[\[([^\]|]+)(?:\|([^\]]+))?\]\]`

### Version History

- Version numbers increment on each content change
- History entries are created asynchronously (with delay)
- Move operations migrate history from source to destination note

## Testing

Run the test scripts:
```bash
# Basic API tests (41 tests)
./test-api.sh

# Edge case tests
./test-edge-cases.sh
```

## Files Changed

### New Files
- `internal/routers/api_router/handler_health.go`
- `internal/domain/domain_note_link.go`
- `internal/model/note_link.gen.go`
- `internal/dao/note_link_repository.go`
- `internal/service/note_link_service.go`
- `pkg/util/frontmatter.go`
- `pkg/util/link_parser.go`
- `pkg/util/path.go`
- `test-api.sh`
- `test-edge-cases.sh`

### Modified Files
- `config/config.yaml`
- `internal/app/app.go`
- `internal/app/config.go`
- `internal/dao/note_repository.go`
- `internal/domain/repository.go`
- `internal/dto/note_dto.go`
- `internal/model/model.go`
- `internal/routers/api_router/handler_note.go`
- `internal/routers/router.go`
- `internal/service/note_service.go`
- `pkg/code/common.go`

---

# Additions in v3.8.7 – v3.8.10

This section documents the API additions and changes made after the original
`feature/api-extensions` work. The REST details, including the full conflict body,
are in [REST_API.md](REST_API.md); this file summarizes both REST and MCP surfaces.

## Conditional Writes (REST and MCP)

Notes and attachments now support an optional compare-and-set mode so a client can
avoid overwriting a change made by another device.

### REST

`POST /api/note`, `DELETE /api/note`, `POST /api/file` and `DELETE /api/file` accept:

| REST field | MCP argument | Meaning |
|------------|--------------|---------|
| `baseRevision` | `base_revision` | Server revision token last seen by the caller. |
| `baseHash` | `base_hash` | Content hash of that version. |
| `baseHashMissing` | `base_hash_missing` | No trusted content baseline; revision is still checked. |
| `isConflictResolved` | — | Explicit user choice; never bypasses the revision check. |

- Omitting every baseline field keeps the previous unconditional behavior.
- Updates require `baseRevision`; deletes require both `baseRevision` and `baseHash`.
- A mismatch returns REST `code: 530` with a `dto.BaselineConflictResponse` body:
  `conflict`, `kind`, `path`, `pathHash`, `reason`, `serverRevision`, `serverHash`,
  `serverDeleted`, `baseHash`, `baseMissing` and (for live notes) `serverContent`.
  No write, conflict copy or broadcast is produced.
- A conditional delete of a missing/already-deleted row is an idempotent success and
  is not broadcast.
- `reason` is one of `both_modified`, `revision_changed`, `remote_deleted`,
  `local_deleted`, `server_missing`.

### MCP

The following tools accept the optional baseline arguments:

| Tool | Baseline arguments |
|------|--------------------|
| `note_create_or_update` | `base_revision`, `base_hash`, `base_hash_missing` |
| `file_write` | `base_revision`, `base_hash`, `base_hash_missing` |
| `note_delete` | `base_revision` **and** `base_hash` (both required for a guarded delete) |
| `file_delete` | `base_revision` **and** `base_hash` (both required for a guarded delete) |

A rejected conditional write returns a tool error result (`isError: true`) whose text
content is a JSON object:

```json
{
  "vault": "MyVault",
  "conflict": {
    "conflict": true,
    "kind": "note",
    "path": "Notes/ReadMe.md",
    "pathHash": "hash123",
    "reason": "both_modified",
    "serverRevision": "0123456789abcdef0123456789abcdef",
    "serverHash": "chash456",
    "serverDeleted": false,
    "baseHash": "bhash789"
  }
}
```

The conflict is deliberately carried in the error text rather than structured
content: these tools declare a different output schema, and spec-validating clients
would reject a schema mismatch as a protocol error, hiding the conflict. A guarded
write never restores a deleted item; use `note_restore` / `file_restore` when
`serverDeleted` is true (or the reason is `remote_deleted` / `server_missing`), or
retry without baseline arguments to recreate it.

### Revision Tokens in Responses

Notes and attachments now expose `revision` in reads, lists and write results, both in
REST (`dto.NoteDTO`, `dto.NoteNoContentDTO`, `dto.NoteWithFileLinksResponse`,
`dto.FileDTO`) and in the MCP equivalents (`dto.McpNoteDTO`,
`dto.McpNoteNoContentDTO`, `dto.McpFileDTO`). Use it directly as `baseRevision` /
`base_revision`.

## New MCP Tools

### `note_history_list`

List the stored version history of one note, newest first, metadata only.

| Argument | Type | Required | Description |
|----------|------|----------|-------------|
| `vault` | string | - | Vault name; omitted or `default` uses the client default vault. |
| `path` | string | ✓ | Exact vault-relative note path. |
| `isRecycle` | boolean | - | Look the note up in the recycle bin (default `false`). |
| `page` | number | - | Page number. |
| `pageSize` | number | - | Page size. |

Output: `vault`, `path`, `count`, `page`, `pageSize`, `totalCount`, `hasMore`,
`histories[]` (`id`, `noteId`, `path`, `version`, `clientName`, `clientType`,
`clientVersion`, `createdAt`).

### `note_history_get`

| Argument | Type | Required | Description |
|----------|------|----------|-------------|
| `id` | number | ✓ | History version ID from `note_history_list`. |

Output: `vault` and `history` (`id`, `noteId`, `path`, `content`, `contentHash`,
`version`, `clientName`, `clientType`, `clientVersion`, `createdAt`). The version's
vault is checked against the token's vault allowlist before any content is returned.

### `note_history_restore`

| Argument | Type | Required | Description |
|----------|------|----------|-------------|
| `id` | number | ✓ | History version ID to restore. |

Restores the note body and returns `vault`, `operation: "history_restore"` and the
updated `note`. Requires note write permission and vault allowlist access; the change
is broadcast to other clients as `NoteSyncModify`.

### `folder_list`

| Argument | Type | Required | Description |
|----------|------|----------|-------------|
| `vault` | string | - | Vault name. |
| `path` | string | - | Parent path without a leading slash; empty or omitted lists the vault root. |

Output: `vault`, `path`, `count`, `folders[]` (immediate child folder paths). It
returns folder paths only; use `note_list_by_prefix` for notes inside a folder.

## System Junk File Filtering

When `app.sync-exclude-system-junk` is `true` (default), write entry points reject
`.DS_Store`, `Thumbs.db`, `desktop.ini` (whole segment, case-insensitive) and any
segment starting with `._`.

- REST: `code: 444` with `details: "system junk file is not synced"` for
  `POST /api/note`, `PATCH /api/note/frontmatter`, `POST /api/note/append`,
  `POST /api/note/prepend`, `POST /api/note/replace`, `POST /api/note/rename`
  (target), `POST /api/file`, `POST /api/file/rename` (target), `POST /api/folder`,
  `POST /api/setting` and `POST /api/setting/rename` (target).
- MCP: a tool error `system junk file is not synced: <path>` from the write tools
  (`note_create_or_update`, `note_rename` target, `note_patch_frontmatter`,
  `note_append`, `note_prepend`, `note_replace`, `file_write`, `file_rename` target).
- Realtime sync: WebSocket write entry points return `code: 444`; the full-sync
  comparison no longer requests uploads for junk files that do not exist on the
  server. Existing junk entries remain readable, downloadable and deletable.
- Set `app.sync-exclude-system-junk: false` to restore the previous behavior.

## Token Capabilities

`GET /api/user/token-capabilities` reports the effective scope of the calling token:

```json
{
  "uid": 1,
  "isAdmin": true,
  "tokenId": "12",
  "protocol": "rest,ws",
  "restricted": false,
  "vaults": [],
  "permissions": {
    "noteRead": true,
    "noteWrite": true,
    "fileRead": true,
    "fileWrite": true,
    "settingRead": true,
    "settingWrite": true,
    "vaultRead": true,
    "vaultWrite": true
  },
  "readOnly": false
}
```

Any authenticated token may call it, including read-only and vault-restricted
tokens. Permissions are the union of the REST and WebSocket channels and use the same
verification logic as request authentication. No secrets or raw token are returned.

## Runtime Share Base URL

`GET /api/admin/share-base-url` and `POST /api/admin/share-base-url` manage the base
URL embedded in generated share links without a config edit or restart.

- `GET` requires the effective administrator and returns `{ "baseUrl": "...",
  "source": "override" | "config" }`.
- `POST` accepts an effective administrator token **or** the static
  `security.share-base-url-token` (via `Authorization: Bearer <token>` or
  `X-Share-Base-Url-Token`). The static token can push/clear but not read.
- Body: `{ "baseUrl": "https://notes.example.com" }`. Empty clears the override. The
  URL must be absolute `http`/`https` with no credentials, control characters, query
  or fragment; trailing slashes are trimmed and subpaths are preserved.
- The override is persisted atomically in `storage/share_base_url` (mode 0600) and
  loaded on restart.

## CLI Upgrade Relay

`GET /api/upgrade/cli` requires an authenticated user token and returns the latest
`Go1c/FastNodeSync-CLI` release from a fixed upstream. Results are cached in memory
for 10 minutes; concurrent refreshes are collapsed into one upstream request.
`cached` marks a cache hit, `stale` marks an expired cache served after an upstream
failure. Assets include `name`, `downloadUrl` and `size`.

## Realtime Revision Fields

- `FileSyncDownloadMessage` (protobuf field 9 in `FileSyncDownloadMessage`) now
  carries `revision`, so a client can record the downloaded attachment as a confirmed
  baseline. Older clients ignore the unknown field.
- `FileSyncModifyMessage` now carries `revision` for attachment create/update
  broadcasts, including REST uploads and MCP `file_write`.

## New Configuration Options

```yaml
app:
  sync-exclude-system-junk: true   # reject system junk files at write entry points

security:
  share-base-url-token: ""         # static token for POST /api/admin/share-base-url
```
