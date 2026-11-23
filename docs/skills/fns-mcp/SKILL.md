---
name: fns-mcp
description: Fast Note Sync Service MCP SSE Skill (Bilingual). Allows agents to access and manage notes, files, and vaults via the MCP protocol.
version: 1.0.0
---

# Fast Note Sync (FNS) MCP Skill (Bilingual/双语版)

This skill allows agents to interact with the Fast Note Sync Service using the Model Context Protocol (MCP).
本技能允许 Agent 通过 Model Context Protocol (MCP) 与 Fast Note Sync Service 交互。

## Core Capabilities / 核心能力

- **Note Management / 笔记管理**: Listing, searching, CRUD, moving, renaming, restoring, and clearing recycle bin. (列表、检索、增删改查、移动、重命名、恢复、清理回收站)
- **Note Enhancement / 笔记增强**: Frontmatter patching, appending/prepending, find & replace, and backlinks/outlinks. (Frontmatter 修补、追加/预置、查找替换、双链/外链)
- **File Management / 文件管理**: Attachment listing, metadata retrieval, reading content (Base64), renaming, and deletion. (附件列表、元数据、读取内容、重命名、删除)
- **Vault Management / 库管理**: Vault listing, creating/updating, and deletion. (库列表、创建/更新、删除)

## Configuration Guide / 配置指南

Before using this skill, the agent needs to connect to the MCP SSE interface.
在使用此技能前，Agent 需要连接到 MCP SSE 接口。

### Interface Details / 接口信息
- **SSE Endpoint**: `http://<YOUR_DOMAIN>:9000/api/mcp/sse`
- **Message Endpoint**: `http://<YOUR_DOMAIN>:9000/api/mcp/message`
- **Authentication**: Requires `Authorization: Bearer <YOUR_AUTH_TOKEN>` in headers.
- **Client Identity (Optional)**: 
    - `X-Client`: `<ClientType>` (e.g., `CherryStudio`, `OpenClaw`)
    - `X-Client-Name`: `<ClientName>` (e.g., `MyAgent`)
    - `X-Client-Version`: `<Version>`
- **Default Vault (Optional)**: `X-Default-Vault-Name: <VAULT_NAME>`

### Platform Specific Config / 平台配置示例
- [OpenClaw Configuration](configs/openclaw.json)
- [Hermes Agent Configuration](configs/hermes.yaml)
- [Cherry Studio Configuration Guide / 配置指南](configs/cherry-studio.md)

---

## Available Tools / 可用工具说明

### 1. Note Tools / 笔记工具

| Tool Name / 工具 | Description / 描述 | Arguments / 参数 |
| :--- | :--- | :--- |
| `note_list` | List notes in a vault / 列出笔记 | `vault`, `keyword` |
| `note_get` | Get note content / 获取笔记内容 | `vault`, `path` |
| `note_create_or_update` | Create/Update note / 创建或更新笔记 | `vault`, `path`, `content`; optional `base_revision`, `base_hash`, `base_hash_missing` for a guarded compare-and-set write |
| `note_delete` | Delete note / 删除笔记 | `vault`, `path`; a guarded delete requires both `base_revision` and `base_hash` |
| `note_rename` | Rename note / 重命名笔记 | `vault`, `oldPath`, `newPath` |
| `note_restore` | Restore note / 恢复笔记 | `vault`, `path` |
| `note_append` | Append content / 追加内容 | `vault`, `path`, `content` |
| `note_prepend` | Prepend content / 预置内容 | `vault`, `path`, `content` |
| `note_replace` | Find & Replace / 查找替换 | `vault`, `path`, `find`, `replace`, `regex`, `all` |
| `note_patch_frontmatter` | Patch Frontmatter / 修补前置参数 | `vault`, `path`, `updates` (JSON), `remove` (JSON) |
| `note_get_backlinks` | Get backlinks / 获取反链 | `vault`, `path` |
| `note_get_outlinks` | Get outlinks / 获取出链 | `vault`, `path` |

### 2. File Tools / 文件工具

| Tool Name / 工具 | Description / 描述 | Arguments / 参数 |
| :--- | :--- | :--- |
| `file_list` | List files / 列出文件 | `vault`, `keyword` |
| `file_get_info` | Get metadata / 获取元数据 | `vault`, `path` |
| `file_read` | Read content (Base64) / 读取内容 | `vault`, `path` |
| `file_delete` | Delete file / 删除文件 | `vault`, `path`; a guarded delete requires both `base_revision` and `base_hash` |
| `file_rename` | Rename file / 重命名文件 | `vault`, `oldPath`, `newPath` |
| `file_write` | Create/update file from Base64 / 以 Base64 新建或更新附件 | `vault`, `path`, `content` (Base64); optional `base_revision`, `base_hash`, `base_hash_missing` for a guarded write |

### 3. Query Tools / 查询工具

These answer structural questions the note CRUD surface cannot express. They
return metadata plus a short snippet instead of note bodies — call `note_get`
with the returned path when you need the full text.

这些工具回答笔记 CRUD 接口无法表达的结构性问题。它们只返回元数据与短片段而非正文 ——
确认路径后再用 `note_get` 读全文。

| Tool Name / 工具 | Description / 描述 | Arguments / 参数 |
| :--- | :--- | :--- |
| `note_search` | Full-text search with snippets / 全文搜索并返回片段 | `vault`, `keyword`, `mode` (`fulltext`\|`path`), `page`, `pageSize` |
| `note_list_broken_links` | Wikilinks whose target is missing / 目标不存在的双链 | `vault`, `limit` |
| `note_list_orphans` | Notes nothing links to / 没有任何入链的笔记 | `vault`, `limit` |
| `note_list_tags` | Tag list with counts / 标签清单及次数 | `vault`, `prefix`, `scope` (`all`\|`frontmatter`\|`inline`), `limit` |
| `note_list_recent` | Newest notes first, metadata only / 最近修改的笔记 | `vault`, `limit` |
| `note_list_by_prefix` | Notes under a folder prefix / 某目录下的笔记 | `vault`, `prefix` |
| `vault_stats` | Note/file/link/tag counts and sizes / 库的统计信息 | `vault` |
| `folder_list` | Immediate child folders under a path / 某路径下的直接子目录 | `vault`, `path` |

### 3.1 History Tools / 历史版本工具

| Tool Name / 工具 | Description / 描述 | Arguments / 参数 |
| :--- | :--- | :--- |
| `note_history_list` | List a note's stored versions / 列出笔记的历史版本 | `vault`, `path`, `isRecycle`, `page`, `pageSize` |
| `note_history_get` | Full content of one version / 读取某个历史版本的完整内容 | `id` |
| `note_history_restore` | Restore a note to a version / 把笔记恢复到某个历史版本 | `id` |

History tools address a version by ID, so the version's vault is checked against the token's vault allowlist before any content is returned or restored.
历史工具按 ID 寻址版本，返回内容或恢复之前都会按令牌的库白名单校验该版本所属的库。

### 4. Vault Tools / 库工具

| Tool Name / 工具 | Description / 描述 | Arguments / 参数 |
| :--- | :--- | :--- |
| `vault_list` | List vaults / 列出库 | None |
| `vault_get` | Get vault details / 获取详情 | `id` |
| `vault_create_or_update` | Create/Update vault / 创建或更新 | `vault`, `id` |
| `vault_delete` | Delete vault / 删除库 | `id` |

---

## Best Practices / 最佳实践

1. **Vault Selection**: Explicitly specify `vault` when possible. (尽可能明确指定 `vault` 参数)
2. **Path Handling**: Paths are relative to vault root. (路径相对于库根目录)
3. **Encoding**: `file_read` returns Base64. (文件读取返回 Base64 编码)
4. **Consistency**: Be aware of path changes after rename/move. (重命名后注意路径变更)
5. **Search first, then read**: `note_search` returns snippets, not bodies — narrow the candidates first and call `note_get` only for the notes you actually need. (先用 `note_search` 拿片段缩小范围，再用 `note_get` 读全文)
6. **Guard your writes**: reads and lists return a `revision` token. Pass it as `base_revision` (plus the `contentHash` as `base_hash`) to `note_create_or_update`, `note_delete`, `file_write`, or `file_delete` to avoid overwriting a concurrent change; `note_delete` and `file_delete` require both fields in guarded mode. A conflict comes back as a structured error with `conflict: true` and the current `serverRevision`. (读和列表都会返回 `revision`；把它作为 `base_revision` 传给写入工具即可避免覆盖并发改动，`note_delete` 与 `file_delete` 的条件删除必须两个字段都给；冲突会以带 `conflict: true` 与当前 `serverRevision` 的结构化错误返回)
