# Fast Note Sync Service - REST API Documentation

This document is generated from `swagger.json` and provides the latest API definitions.

---

## General Information

### Base URL
```
http://{host}:9000/api
```

### Authentication
Endpoints requiring authentication must include the Token in the request header:
```
Authorization: {token}
```
The Token is obtained via the login interface.

### Standard Response Structure
```typescript
interface Response<T> {
  code: number;      // Status code (0=fail, 1+=success)
  status: boolean;   // Operation status
  message: string;   // Status message
  data: T;           // Business data
  details?: string[]; // Error details (optional)
}
```

### Paginated Response Structure
```typescript
interface ListResponse<T> {
  code: number;
  status: boolean;
  message: string;
  data: {
    list: T[];
    pager: {
      page: number;
      pageSize: number;
      totalRows: number;
    }
  }
}
```

### Pagination Parameters
| Parameter | Type | Description | Default |
|-----------|------|-------------|---------|
| page | number | Page number | 1 |
| page_size | number | Items per page | 10 (Max 100) |

### Conditional Writes (Optimistic Concurrency)

`POST /api/note`, `DELETE /api/note`, `POST /api/file` and `DELETE /api/file` accept
optional baseline fields. Sending none of them keeps the legacy unconditional
behavior; sending any of them turns the request into a guarded compare-and-set:

| Field | Type | Description |
|-------|------|-------------|
| baseRevision | string | Server revision token of the version the caller last saw. Read it from the `revision` field returned by note/file reads, lists and writes. |
| baseHash | string | Content hash of that version (`contentHash` for attachments, content hash for notes). |
| baseHashMissing | boolean | Set `true` when there is no trusted content baseline; the revision check still applies. |
| isConflictResolved | boolean | Marks an explicit user choice. It never bypasses the revision check; it only allows reviving a server tombstone when `baseRevision` still matches. |

Rules:

- Updating an existing note or attachment requires `baseRevision`. Without it, a
  conditional update is rejected with `530` (`revision_changed`).
- A conditional delete requires **both** `baseRevision` and `baseHash`. Supplying
  only one is rejected with `530` (`local_deleted`).
- Deleting a row that is already deleted or missing is an idempotent success; it is
  not broadcast to other clients.
- A rejected write returns HTTP 200 with `code: 530`, message
  `Sync conflict detected`, and a structured `data` body. No write is performed, no
  conflict copy is created and no realtime event is broadcast.

Conflict body (`data`):

```json
{
  "conflict": true,
  "kind": "note",
  "path": "Notes/ReadMe.md",
  "pathHash": "hash123",
  "reason": "both_modified",
  "serverRevision": "0123456789abcdef0123456789abcdef",
  "serverHash": "chash456",
  "serverDeleted": false,
  "baseHash": "bhash789",
  "baseMissing": true,
  "serverContent": "# current note body"
}
```

| Field | Description |
|-------|-------------|
| conflict | Always `true`. |
| kind | `note` or `file`. |
| reason | `both_modified`, `revision_changed`, `remote_deleted`, `local_deleted` or `server_missing`. |
| serverRevision / serverHash | Current server version token and content hash. |
| serverDeleted | `true` when the server row is a tombstone. |
| baseHash | Baseline used for this decision. |
| baseMissing | Always `true` for REST: the common-ancestor body is not resolved. |
| serverContent | Live note body for note conflicts; omitted for attachments and deleted rows. |

A guarded write never restores a deleted item. When `serverDeleted` is `true` (or
the reason is `remote_deleted` / `server_missing`), use `PUT /api/note/restore` or
`PUT /api/file/restore`, or retry without baseline fields to recreate the item.

### System Junk File Filtering

When `app.sync-exclude-system-junk` is `true` (default), REST write entry points
reject paths containing system junk names. Matching is per path segment:

- `.DS_Store`, `Thumbs.db` and `desktop.ini` match as a whole segment,
  case-insensitively.
- Any segment starting with `._` (macOS AppleDouble files) matches.
- Names that merely contain these strings (`my._notes.md`) do not match.

Rejected requests return `code: 444` (`Invalid path`) with
`details: "system junk file is not synced"`.

Affected endpoints: `POST /api/note`, `PATCH /api/note/frontmatter`,
`POST /api/note/append`, `POST /api/note/prepend`, `POST /api/note/replace`,
`POST /api/note/rename` (target path), `POST /api/file`, `POST /api/file/rename`
(target path), `POST /api/folder`, `POST /api/setting` and
`POST /api/setting/rename` (target path).

Read, list, download, delete, restore and recycle-clear endpoints are not affected;
junk entries already stored remain readable and deletable. Set
`app.sync-exclude-system-junk: false` to restore the previous behavior.

---

## Error Codes Reference

| Code | Description |
|------|-------------|
| 0 | Failure |
| 1-6 | Success states |
| 400-446 | Business errors |
| 500-534 | System/Sync errors |


### Common Error Codes
| Code | Description |
|------|-------------|
| 405 | User registration is closed |
| 407 | Username does not exist |
| 408 | Username already exists |
| 414 | Note Vault does not exist |
| 428 | Note does not exist |
| 411 | This operation requires administrator privileges |
| 444 | Invalid path (also returned for system junk paths when filtering is enabled) |
| 445 | This operation requires administrator privileges |
| 505 | Invalid Params |
| 507 | Not logged in |
| 508 | Session expired |
| 530 | Sync conflict detected (conditional write or delete rejected) |

---

## Backup APIs

### Update backup configuration
**Endpoint**: `POST /api/backup/config`

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.BackupConfigRequest | ✓ | Backup Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Delete backup configuration
**Endpoint**: `DELETE /api/backup/config`

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| id | query | integer | - |  |

**Success Response (200)**:
Schema: `app.Res`

---

### Get backup configurations
**Endpoint**: `GET /api/backup/configs`

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |

**Success Response (200)**:
Schema: `app.Res`

---

### Trigger a backup manually
**Endpoint**: `POST /api/backup/execute`

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.BackupExecuteRequest | ✓ | Backup Execute Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Get backup history list
**Endpoint**: `GET /api/backup/historys`

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| configId | query | integer | ✓ |  |
| page | query | integer | - |  |
| pageSize | query | integer | - |  |

**Success Response (200)**:
Schema: `app.Res`

---

## Config APIs

### Get full admin config
**Endpoint**: `GET /api/admin/config`

Get full system configuration information, requires admin privileges

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |

**Success Response (200)**:
Schema: `app.Res`

---

### Update admin config
**Endpoint**: `POST /api/admin/config`

Modify full system configuration information, requires admin privileges

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | api_router.adminConfig | ✓ | Config Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Get Cloudflare config
**Endpoint**: `GET /api/admin/config/cloudflare`

Get Cloudflare tunnel configuration, requires admin privileges

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |

**Success Response (200)**:
Schema: `app.Res`

---

### Update Cloudflare config
**Endpoint**: `POST /api/admin/config/cloudflare`

Modify Cloudflare tunnel configuration, requires admin privileges

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | api_router.cloudflareConfig | ✓ | Config Parameters |

**Success Response (200)**:
Schema: `app.Res`

---



### Get WebGUI basic config
**Endpoint**: `GET /api/webgui/config`

Get non-sensitive configuration required for frontend display, such as font settings, registration status, etc.

**Parameters**:
None

**Success Response (200)**:
Schema: `app.Res`

---

## File APIs

### Get attachment content
**Endpoint**: `GET /api/file`

Get raw binary data of an attachment by path, supports strong cache control

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| isRecycle | query | boolean | - | Is in recycle bin // 是否在回收站 |
| path | query | string | ✓ | File path // 文件路径 |
| pathHash | query | string | - | Path hash // 路径哈希 |
| vault | query | string | ✓ | Vault name // 保险库名称 |

**Success Response (200)**:
Schema: `file`

---

### Upload attachment
**Endpoint**: `POST /api/file`

Upload a file as an attachment (`multipart/form-data`). Sending any baseline field
turns the upload into a conditional write (see [Conditional Writes](#conditional-writes-optimistic-concurrency)).

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| vault | formData | string | ✓ | Vault name // 保险库名称 |
| path | formData | string | ✓ | File path // 文件路径 |
| pathHash | formData | string | - | Path hash // 路径哈希 |
| ctime | formData | int64 | - | Creation timestamp // 创建时间戳 |
| mtime | formData | int64 | - | Modification timestamp // 修改时间戳 |
| file | formData | file | ✓ | File to upload // 上传的文件 |
| baseRevision | formData | string | - | Server revision token of the baseline // 基线的服务端版本令牌 |
| baseHash | formData | string | - | Content hash baseline // 内容基线 |
| baseHashMissing | formData | boolean | - | No trusted content baseline // 没有可信内容基线 |
| isConflictResolved | formData | boolean | - | Marks a manual conflict resolution // 标记手动解决冲突 |

**Success Response (200)**:
Schema: `pkgapp.Res{data=dto.FileDTO}`. `data.revision` is the new server revision
token, usable as the next `baseRevision`.

**Conflict (200, code 530)**:
Schema: `pkgapp.Res{data=dto.BaselineConflictResponse}` — see
[Conditional Writes](#conditional-writes-optimistic-concurrency). Attachment bytes
are never inlined.

---

### Delete attachment
**Endpoint**: `DELETE /api/file`

Permanently delete a specific attachment record and its physical file. Sending both
`baseRevision` and `baseHash` turns the delete into a conditional delete.

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| path | query | string | ✓ | File path // 文件路径 |
| pathHash | query | string | ✓ | Path hash // 路径哈希 |
| vault | query | string | ✓ | Vault name // 保险库名称 |
| baseRevision | query | string | - | Server revision token of the baseline // 基线的服务端版本令牌 |
| baseHash | query | string | - | Content hash baseline // 内容基线 |
| baseHashMissing | query | boolean | - | No trusted content baseline // 没有可信内容基线 |
| isConflictResolved | query | boolean | - | Marks a manual conflict resolution // 标记手动解决冲突 |

**Success Response (200)**:
Schema: `pkgapp.Res{data=dto.FileDTO}`

**Conflict (200, code 530)**:
Schema: `pkgapp.Res{data=dto.BaselineConflictResponse}`. A conditional delete of an
already-deleted or missing row succeeds idempotently and is not broadcast.

---

### Get attachment info
**Endpoint**: `GET /api/file/info`

Get attachment metadata (FileDTO) by path

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| isRecycle | query | boolean | - | Is in recycle bin // 是否在回收站 |
| path | query | string | ✓ | File path // 文件路径 |
| pathHash | query | string | - | Path hash // 路径哈希 |
| vault | query | string | ✓ | Vault name // 保险库名称 |

**Success Response (200)**:
Schema: `app.Res`

---

### Clear recycle bin
**Endpoint**: `DELETE /api/file/recycle-clear`

Permanently clear selected files from recycle bin

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.FileRecycleClearRequest | ✓ | Clear Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Rename attachment
**Endpoint**: `POST /api/file/rename`

Rename an attachment to a new path

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.FileRenameRequest | ✓ | Rename Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Restore attachment
**Endpoint**: `PUT /api/file/restore`

Restore deleted attachment from trash

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.FileRestoreRequest | ✓ | Restore Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Get file list
**Endpoint**: `GET /api/files`

Get attachment list for current user with pagination, search, filter, and sort support

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| isRecycle | query | boolean | - | Is in recycle bin // 是否在回收站 |
| keyword | query | string | - | Search keyword // 搜索关键词 |
| sortBy | query | string | - | Sort by field // 排序字段 |
| sortOrder | query | string | - | Sort order // 排序顺序 |
| vault | query | string | ✓ | Vault name // 保险库名称 |
| page | query | integer | - | Page number // 页码 |
| pageSize | query | integer | - | Page size // 每页数量 |

**Success Response (200)**:
Schema: `app.Res`

---

## Folder APIs

### Get folder info
**Endpoint**: `GET /api/folder`

Get folder info for current user by path or pathHash

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| path | query | string | - | Folder path // 文件夹路径 |
| pathHash | query | string | - | Path hash // 路径哈希 |
| vault | query | string | ✓ | Vault name // 保险库名称 |

**Success Response (200)**:
Schema: `app.Res`

---

### Create folder
**Endpoint**: `POST /api/folder`

Create a new folder or restore a deleted one by path

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.FolderCreateRequest | ✓ | Create Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Delete folder
**Endpoint**: `DELETE /api/folder`

Soft delete a folder by path or pathHash

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.FolderDeleteRequest | ✓ | Delete Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### List files in folder
**Endpoint**: `GET /api/folder/files`

List non-deleted files in a specific folder with pagination and sorting

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| path | query | string | - | Folder path // 文件夹路径 |
| pathHash | query | string | - | Path hash // 路径哈希 |
| sortBy | query | string | - | Sort by field // 排序字段 |
| sortOrder | query | string | - | Sort order // 排序顺序 |
| vault | query | string | ✓ | Vault name // 保险库名称 |
| page | query | integer | - | Page number // 页码 |
| pageSize | query | integer | - | Page size // 每页数量 |

**Success Response (200)**:
Schema: `app.Res`

---

### List notes in folder
**Endpoint**: `GET /api/folder/notes`

List non-deleted notes in a specific folder with pagination and sorting

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| path | query | string | - | Folder path // 文件夹路径 |
| pathHash | query | string | - | Path hash // 路径哈希 |
| sortBy | query | string | - | Sort by field // 排序字段 |
| sortOrder | query | string | - | Sort order // 排序顺序 |
| vault | query | string | ✓ | Vault name // 保险库名称 |
| page | query | integer | - | Page number // 页码 |
| pageSize | query | integer | - | Page size // 每页数量 |

**Success Response (200)**:
Schema: `app.Res`

---

### Get folder tree
**Endpoint**: `GET /api/folder/tree`

Get the complete folder tree structure for a vault

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| depth | query | integer | - | Tree depth // 树深度 |
| vault | query | string | ✓ | Vault name // 保险库名称 |

**Success Response (200)**:
Schema: `app.Res`

---

### Get folder list
**Endpoint**: `GET /api/folders`

Get folder list for current user by parent path or pathHash

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| path | query | string | - | Folder path // 文件夹路径 |
| pathHash | query | string | - | Path hash // 路径哈希 |
| vault | query | string | ✓ | Vault name // 保险库名称 |

**Success Response (200)**:
Schema: `app.Res`

---

## GitSync APIs

### Update git sync configuration
**Endpoint**: `POST /api/git-sync/config`

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.GitSyncConfigRequest | ✓ | Git Sync Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Delete git sync configuration
**Endpoint**: `DELETE /api/git-sync/config`

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.GitSyncDeleteRequest | ✓ | Git Sync ID |

**Success Response (200)**:
Schema: `app.Res`

---

### Clean local git workspace
**Endpoint**: `DELETE /api/git-sync/config/clean`

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.GitSyncCleanRequest | ✓ | Clean Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Trigger a manual git sync
**Endpoint**: `POST /api/git-sync/config/execute`

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.GitSyncExecuteRequest | ✓ | Execute Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Get git sync configurations
**Endpoint**: `GET /api/git-sync/configs`

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |

**Success Response (200)**:
Schema: `app.Res`

---

### Get git sync histories
**Endpoint**: `GET /api/git-sync/histories`

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| configId | query | integer | - |  |
| page | query | integer | - |  |
| pageSize | query | integer | - |  |

**Success Response (200)**:
Schema: `app.Res`

---

### Validate git sync parameters
**Endpoint**: `POST /api/git-sync/validate`

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.GitSyncValidateRequest | ✓ | Validation Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

## Note APIs

### Get note details
**Endpoint**: `GET /api/note`

Get specific note content and metadata by path or path hash

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| isRecycle | query | boolean | - | Is in recycle bin // 是否在回收站 |
| path | query | string | ✓ | Note path // 笔记路径 |
| pathHash | query | string | - | Path hash // 路径哈希 |
| vault | query | string | ✓ | Vault name // 保险库名称 |

**Success Response (200)**:
Schema: `pkgapp.Res{data=dto.NoteWithFileLinksResponse}`. `data.revision` is the
current server revision token, usable as `baseRevision`.

---

### Create or update note
**Endpoint**: `POST /api/note`

Handle note creation, modification, or renaming (identified by path change)

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.NoteModifyOrCreateRequest | ✓ | Note Content |

The body may also carry `baseRevision`, `baseHash`, `baseHashMissing` and
`isConflictResolved`. Sending any of them turns the request into a conditional
write (see [Conditional Writes](#conditional-writes-optimistic-concurrency));
omitting all of them keeps the unconditional behavior.

**Success Response (200)**:
Schema: `pkgapp.Res{data=dto.NoteDTO}`. `data.revision` is the new server revision
token.

**Conflict (200, code 530)**:
Schema: `pkgapp.Res{data=dto.BaselineConflictResponse}`. No write or broadcast occurs.

---

### Delete note
**Endpoint**: `DELETE /api/note`

Move note to trash. Sending both `baseRevision` and `baseHash` turns the delete into a
conditional delete.

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| path | query | string | ✓ | Note path // 笔记路径 |
| pathHash | query | string | - | Path hash // 路径哈希 |
| vault | query | string | ✓ | Vault name // 保险库名称 |
| baseRevision | query | string | - | Server revision token of the baseline // 基线的服务端版本令牌 |
| baseHash | query | string | - | Content hash baseline // 内容基线 |
| baseHashMissing | query | boolean | - | No trusted content baseline // 没有可信内容基线 |
| isConflictResolved | query | boolean | - | Marks a manual conflict resolution // 标记手动解决冲突 |

**Success Response (200)**:
Schema: `pkgapp.Res{data=dto.NoteDTO}`

**Conflict (200, code 530)**:
Schema: `pkgapp.Res{data=dto.BaselineConflictResponse}`. A conditional delete of an
already-deleted or missing note succeeds idempotently and is not broadcast.

---

### Append content to note
**Endpoint**: `POST /api/note/append`

Append content to the end of a note

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.NoteAppendRequest | ✓ | Append Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Get backlinks
**Endpoint**: `GET /api/note/backlinks`

Get all other notes that link to the specified note

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| path | query | string | ✓ | Note path // 笔记路径 |
| pathHash | query | string | - | Path hash // 路径哈希 |
| vault | query | string | ✓ | Vault name // 保险库名称 |

**Success Response (200)**:
Schema: `app.Res`

---

### Modify note frontmatter
**Endpoint**: `PATCH /api/note/frontmatter`

Update or delete note frontmatter fields

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.NotePatchFrontmatterRequest | ✓ | Frontmatter Modification Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Move note
**Endpoint**: `POST /api/note/move`

Move a note to a new path

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.NoteMoveRequest | ✓ | Move Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Get outgoing links
**Endpoint**: `GET /api/note/outlinks`

Get other notes that the specified note links to

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| path | query | string | ✓ | Note path // 笔记路径 |
| pathHash | query | string | - | Path hash // 路径哈希 |
| vault | query | string | ✓ | Vault name // 保险库名称 |

**Success Response (200)**:
Schema: `app.Res`

---

### Prepend content to note
**Endpoint**: `POST /api/note/prepend`

Insert content at the beginning of a note (after frontmatter)

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.NotePrependRequest | ✓ | Prepend Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Clear recycle bin
**Endpoint**: `DELETE /api/note/recycle-clear`

Permanently clear selected notes from recycle bin

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.NoteRecycleClearRequest | ✓ | Clear Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Rename note
**Endpoint**: `POST /api/note/rename`

Rename a note to a new path

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.NoteRenameRequest | ✓ | Rename Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Find and replace in note
**Endpoint**: `POST /api/note/replace`

Perform find and replace operation in a note, supporting regular expressions

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.NoteReplaceRequest | ✓ | Find and Replace Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Restore note
**Endpoint**: `PUT /api/note/restore`

Restore deleted note from trash

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.NoteRestoreRequest | ✓ | Restore Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Get note list
**Endpoint**: `GET /api/notes`

Get note list for current user with pagination

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| isRecycle | query | boolean | - | Is in recycle bin // 是否在回收站 |
| keyword | query | string | - | Search keyword // 搜索关键词 |
| searchContent | query | boolean | - | Whether to search content // 是否搜索内容 |
| searchMode | query | string | - | Search mode (path, content, regex) // 搜索模式（路径、内容、正则） |
| sortBy | query | string | - | Sort by field // 排序字段 |
| sortOrder | query | string | - | Sort order // 排序顺序 |
| vault | query | string | ✓ | Vault name // 保险库名称 |
| page | query | integer | - | Page number // 页码 |
| pageSize | query | integer | - | Page size // 每页数量 |

**Success Response (200)**:
Schema: `pkgapp.Res{data={list: []dto.NoteNoContentDTO}}`. Every list item includes
`revision`, so a client can list then conditionally update without an extra read.

---

## Note History APIs

### Get note history list
**Endpoint**: `GET /api/note/histories`

Get all history records for a specific note with pagination

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| isRecycle | query | boolean | - | Is in recycle bin // 是否在回收站 |
| path | query | string | ✓ | Note path // 笔记路径 |
| pathHash | query | string | - | Path hash // 路径哈希 |
| vault | query | string | ✓ | Vault name // 保险库名称 |
| page | query | integer | - | Page number // 页码 |
| pageSize | query | integer | - | Page size // 每页数量 |

**Success Response (200)**:
Schema: `app.Res`

---

### Get note history details
**Endpoint**: `GET /api/note/history`

Get specific note history content by history record ID

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| id | query | integer | ✓ | History Record ID |

**Success Response (200)**:
Schema: `app.Res`

---

### Restore note from history
**Endpoint**: `PUT /api/note/history/restore`

Restore note content to a specific history version

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.NoteHistoryRestoreRequest | ✓ | Restore Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

## Setting APIs

### Get setting info
**Endpoint**: `GET /api/setting`

Get setting info for current user by path or pathHash

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| path | query | string | - | Setting path // 配置路径 |
| pathHash | query | string | - | Path hash // 路径哈希 |
| vault | query | string | ✓ | Vault name // 保险库名称 |

**Success Response (200)**:
Schema: `app.Res`

---

### Create or update setting
**Endpoint**: `POST /api/setting`

Create a new setting or update an existing one

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.SettingModifyOrCreateRequest | ✓ | Create/Update Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Delete setting
**Endpoint**: `DELETE /api/setting`

Soft delete a setting by path or pathHash

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.SettingDeleteRequest | ✓ | Delete Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Rename setting
**Endpoint**: `POST /api/setting/rename`

Rename a setting and update its path and pathHash

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.SettingRenameRequest | ✓ | Rename Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Get setting list
**Endpoint**: `GET /api/settings`

Get setting list for current user with pagination and keyword filtering

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| keyword | query | string | - | Keyword // 关键词 |
| vault | query | string | ✓ | Vault name // 保险库名称 |
| page | query | integer | - | Page number // 页码 |
| pageSize | query | integer | - | Page size // 每页数量 |

**Success Response (200)**:
Schema: `app.Res`

---

## Share APIs

### Query share by path
**Endpoint**: `GET /api/share`

Get share token and info by vault and path

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| path | query | string | ✓ | Resource path // 资源路径 |
| pathHash | query | string | ✓ | Resource path Hash // 资源路径哈希 |
| vault | query | string | ✓ | Vault name // 保险库名称 |

**Success Response (200)**:
Schema: `app.Res`

---

### Create resource share
**Endpoint**: `POST /api/share`

Create a share token for a specific note or attachment, automatically resolve attachment references and authorize

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.ShareCreateRequest | ✓ | Share Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Cancel share
**Endpoint**: `DELETE /api/share`

Cancel a share by ID or path parameters

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.ShareCancelRequest | ✓ | Cancel Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Get shared attachment content
**Endpoint**: `GET /api/share/file`

Get raw binary data of a specific attachment via share token

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| Share-Token | header | string | ✓ | Auth Token |
| id | query | integer | ✓ | Resource ID // 资源 ID |

**Success Response (200)**:
Schema: `file`

---

### Get shared note details
**Endpoint**: `GET /api/share/note`

Get specific note content (restricted read-only access) via share token

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| Share-Token | header | string | ✓ | Auth Token |
| id | query | integer | ✓ | Resource ID // 资源 ID |

**Success Response (200)**:
Schema: `app.Res`

---

### List shares
**Endpoint**: `GET /api/shares`

Get all active and inactive shares of the user, supports sorting and pagination

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| sort_by | query | string | - | Sort field: created_at, updated_at, expires_at (default: created_at) |
| sort_order | query | string | - | Sort direction: asc or desc (default: desc) |
| page | query | integer | - | Page number |
| pageSize | query | integer | - | Page size |

**Success Response (200)**:
Schema: `app.Res`

---

## Storage APIs

### Get storage configuration list
**Endpoint**: `GET /api/storage`

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |

**Success Response (200)**:
Schema: `app.Res`

---

### Create or update storage configuration
**Endpoint**: `POST /api/storage`

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.StoragePostRequest | ✓ | Storage Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Delete storage configuration
**Endpoint**: `DELETE /api/storage`

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| id | query | integer | ✓ | Storage ID |

**Success Response (200)**:
Schema: `app.Res`

---

### Get enabled storage types
**Endpoint**: `GET /api/storage/enabled_types`

Get list of enabled storage types. Possible values: localfs, oss, s3, r2, minio, webdav

**Parameters**:
None

**Success Response (200)**:
Schema: `app.Res`

---

### Validate storage connection
**Endpoint**: `POST /api/storage/validate`

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.StoragePostRequest | ✓ | Storage Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

## System APIs

### Download cloudflared binary
**Endpoint**: `GET /api/admin/cloudflared_tunnel_download`

Trigger the download of cloudflared binary for the current platform

**Parameters**:
None

**Success Response (200)**:
Schema: `app.Res`

---

### Trigger manual GC
**Endpoint**: `GET /api/admin/gc`

Manually run Go runtime GC and release memory to OS, requires admin privileges

**Parameters**:
None

**Success Response (200)**:
Schema: `app.Res`

---

### Trigger server restart
**Endpoint**: `GET /api/admin/restart`

Gracefully restart the server

**Parameters**:
None

**Success Response (200)**:
Schema: `app.Res`

---

### Get system and runtime info
**Endpoint**: `GET /api/admin/systeminfo`

Get system information and Go runtime data, requires admin privileges

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |

**Success Response (200)**:
Schema: `app.Res`

---

### Trigger server upgrade
**Endpoint**: `GET /api/admin/upgrade`

Download latest version and restart server

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| version | query | string | ✓ | Version to upgrade (e.g. 2.0.10 or latest) |

**Success Response (200)**:
Schema: `app.Res`

---

### Get effective share base URL
**Endpoint**: `GET /api/admin/share-base-url`

Return the currently effective share base URL and its source. Requires the effective
administrator; the static notifier token (see the update endpoint) cannot read it.

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |

**Success Response (200)**:
Schema: `pkgapp.Res{data=dto.ShareBaseURLResponse}`

```json
{
  "baseUrl": "https://notes.example.com",
  "source": "override"
}
```

`source` is `override` when a runtime value is set, or `config` when it falls back to
`server.share-base-url`. An empty `baseUrl` means no fixed share base URL is
configured; links are then built from the external API URL or the request address.

---

### Update runtime share base URL
**Endpoint**: `POST /api/admin/share-base-url`

Set or clear the runtime share base URL used for generated share links. Intended for
CGNAT or tunnel setups whose public address changes dynamically, so an external
notifier can push the new address without editing the config or restarting.

**Authentication**: either an effective administrator user token, or the static
token configured in `security.share-base-url-token` (sent as
`Authorization: Bearer <token>` or `X-Share-Base-Url-Token: <token>`). The static
token may only push or clear the value; reading still requires the administrator.

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Admin Auth Token, or static token (see above) |
| params | body | dto.ShareBaseURLUpdateRequest | ✓ | Base URL |

```json
{
  "baseUrl": "https://notes.example.com"
}
```

`baseUrl` must be an absolute `http` or `https` URL without credentials, control
characters, query string or fragment; trailing slashes are trimmed and a subpath is
preserved. An empty `baseUrl` clears the override and falls back to
`server.share-base-url`. The value is persisted atomically in `storage/share_base_url`
(mode 0600) and loaded on restart.

**Success Response (200)**:
Schema: `pkgapp.Res{data=dto.ShareBaseURLResponse}`

**Failure**:
`code: 411` (`This operation requires administrator privileges`) when the caller is
neither the effective administrator nor authorized by the static token; `code: 305`
(`Invalid Params`) for an invalid or empty body/URL; `code: 479` when persisting the
override fails.

---

### Get latest FastNodeSync-CLI release
**Endpoint**: `GET /api/upgrade/cli`

Relay the latest [Go1c/FastNodeSync-CLI](https://github.com/Go1c/FastNodeSync-CLI)
release so restricted intranet clients can upgrade without direct GitHub access. The
upstream repository is fixed server-side; clients cannot influence it.

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |

**Success Response (200)**:
Schema: `pkgapp.Res{data=dto.CLIUpgradeResponse}`

```json
{
  "repository": "Go1c/FastNodeSync-CLI",
  "version": "v1.0.0",
  "name": "v1.0.0",
  "publishedAt": "2026-11-24T00:00:00Z",
  "htmlUrl": "https://github.com/Go1c/FastNodeSync-CLI/releases/tag/v1.0.0",
  "body": "Release notes",
  "assets": [
    {
      "name": "FastNodeSync-CLI-linux-amd64",
      "downloadUrl": "https://github.com/.../download/...",
      "size": 12345678
    }
  ],
  "prerelease": false,
  "cached": true,
  "cachedAt": "2026-11-24T00:00:00Z",
  "stale": false
}
```

Results are cached in memory for 10 minutes. `cached` is `true` when the response came
from that cache; `stale` is `true` when an upstream refresh failed and an expired
cached payload was served instead. A request with no cache and a failed upstream
returns `code: 300` (`Server Internal Error`).

---

### Get connected WebSocket clients
**Endpoint**: `GET /api/admin/ws_clients`

Get a list of all current WebSocket connections, requires admin privileges

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |

**Success Response (200)**:
Schema: `app.Res`

---

### Health check
**Endpoint**: `GET /api/health`

Check service health status, including database connection

**Parameters**:
None

**Success Response (200)**:
Schema: `api_router.HealthResponse`

---

### Get support records
**Endpoint**: `GET /api/support`

Get support records for the specified language with pagination and sorting

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| lang | query | string | - | Language code (default: en) |
| sortBy | query | string | - | Sort by field (amount, time, name, item) |
| sortOrder | query | string | - | Sort order (asc, desc) |
| page | query | integer | - | Page number |
| pageSize | query | integer | - | Page size |

**Success Response (200)**:
Schema: `app.Res`

---

### Get server version info
**Endpoint**: `GET /api/version`

Get current server software version, Git tag, and build time

**Parameters**:
None

**Success Response (200)**:
Schema: `app.Res`

---

### Probe version source latency
**Endpoint**: `GET /api/version/probe`

Probe GitHub releases and return reachability, latency, recommended source and current mode

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |

**Success Response (200)**:
Schema: `app.Res{data=dto.SourceProbeDTO}`

```json
{
  "code": 1,
  "msg": "Success",
  "data": {
    "github": { "ok": true, "latencyMs": 280 },
    "recommended": "github",
    "selectedMode": "auto"
  }
}
```

---

## User APIs

### Change user password
**Endpoint**: `POST /api/user/change_password`

Handle password change request for current user, validate old password and update new password.
处理当前用户的修改密码请求，验证旧密码并更新新密码。

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.UserChangePasswordRequest | ✓ | Change Password Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Get user info
**Endpoint**: `GET /api/user/info`

Handle request to get current user info.
处理获取当前用户信息的请求。

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |

**Success Response (200)**:
Schema: `app.Res`

---

### Get token capabilities
**Endpoint**: `GET /api/user/token-capabilities`

Report the effective capabilities of the token that authenticated this request. Any
authenticated token may call it, including read-only and vault-restricted tokens; no
extra function point is required. Permissions are the union of the REST and WebSocket
channels, computed with the same verification logic used by request authentication.
The response never contains secrets, passwords or the raw token.

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |

**Success Response (200)**:
Schema: `pkgapp.Res{data=dto.TokenCapabilitiesResponse}`

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

| Field | Description |
|-------|-------------|
| isAdmin | Whether this token belongs to the effective administrator. |
| tokenId | Database token ID as a string (empty when not applicable). |
| protocol | Protocol dimension from the token scope (`rest`, `ws`, or both). |
| restricted | `true` when the token is vault-restricted or lacks full permissions. |
| vaults | Allowed vault names; an empty array means unrestricted. |
| permissions | Effective read/write permissions per resource. |
| readOnly | `true` when neither note nor attachment write is allowed. |

Clients can use `readOnly` and `permissions` to switch to a read-only UI.

---

### User login
**Endpoint**: `POST /api/user/login`

Handle user login HTTP request, validate parameters and return auth token.
处理用户登录 HTTP 请求，验证参数并返回认证 Token。

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| params | body | dto.UserLoginRequest | ✓ | Login Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### User registration
**Endpoint**: `POST /api/user/register`

Handle user registration HTTP request, validate parameters and call UserService. Registration may be disabled in server settings.
处理用户注册 HTTP 请求，验证参数并调用 UserService。注册功能可能在服务器设置中被禁用。

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| params | body | dto.UserCreateRequest | ✓ | Register Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

## Vault APIs

### Get vault list
**Endpoint**: `GET /api/vault`

Get all note vaults for current user

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |

**Success Response (200)**:
Schema: `app.Res`

---

### Create or update vault
**Endpoint**: `POST /api/vault`

Be used to create a new vault or update an existing vault configuration based on the ID in the request parameters

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| params | body | dto.VaultPostRequest | ✓ | Vault Parameters |

**Success Response (200)**:
Schema: `app.Res`

---

### Delete vault
**Endpoint**: `DELETE /api/vault`

Permanently delete a specific note vault and all associated notes and attachments

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| id | query | integer | ✓ | Vault ID // 保险库 ID |

**Success Response (200)**:
Schema: `app.Res`

---

### Get vault details
**Endpoint**: `GET /api/vault/get`

Get specific vault configuration details by vault ID

**Parameters**:
| Name | In | Type | Required | Description |
|------|----|------|----------|-------------|
| token | header | string | ✓ | Auth Token |
| id | query | integer | ✓ | Vault ID |

**Success Response (200)**:
Schema: `app.Res`

---

## Timestamp Format

All timestamp fields (`ctime`, `mtime`, `updatedTimestamp`, `lastTime`) are **Unix timestamps in milliseconds**. When writing notes or attachments, the server tolerates second-scale values: any `ctime`/`mtime` in the range (0, 1e11) is treated as seconds and normalized to milliseconds before storage (#461).

---

## Hash Algorithms

`pathHash` and `contentHash` use a 32-bit hash algorithm (e.g., FNV-1a). Clients can compute these automatically or receive them from the server.

---

## Full-Text Search (FTS)

The server includes a built-in full-text search engine based on SQLite FTS5 for efficient searching of note paths and content.
