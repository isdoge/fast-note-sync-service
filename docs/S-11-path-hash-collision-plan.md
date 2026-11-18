# S-11 路径哈希碰撞止损方案（不实现，仅方案）

关联：haierkeys/fast-note-sync-service#342 S-11（32 位滚动哈希 PathHash 碰撞）

## 问题确认（当前代码实测）

- `pkg/util/hash.go` 的 `EncodeHash32` 是 Java 风格 31 进制滚动哈希，32 位。
  实测 `EncodeHash32("Aa.md") == EncodeHash32("BB.md") == 62966277`。
- DAO 三仓储均只按 `(vaultID, pathHash)` 取行、不核对 `Path`：
  - `internal/dao/note_repository.go:271` `GetByPathHash`（含 `GetAllByPathHash:308`、`GetByPathHashIncludeRecycle:286`）
  - `internal/dao/file_repository.go:182` `GetByPathHash`
  - `internal/dao/folder_repository.go:53` `GetByPathHash`、`:62` `GetAllByPathHash`
- 后果：同库中存在哈希碰撞的两条路径 A、B 时，写入 B 命中 A 的记录行，A 的内容/元数据被 B 的覆盖；改名探测、删除幂等、回收站恢复同样拿错行。

## 为什么这一版不实现止损（判定依据）

任务规则：止损改动超过约 150 行，或会影响同步主流程（扫描比较、改名）的正常情况，则只写方案。实测两点都命中：

1. **改动面远超 150 行。** 服务层按 PathHash 读的写入相关调用共约 27 处
   （note_service 8、file_service 10、folder_service 8、note_link_service 1），
   每处都要加"完整路径不同则拒绝"分支；DAO 层需要新增/改造带 Path 条件的查询
   与 domain 接口、3 个 mock 同步改；错误码与 i18n 各加一条。合计 > 300 行。
2. **无法绕开同步主流程。** 每次正常写入的前置读就是碰撞点本身：
   - `noteService.ModifyOrCreate`（note_service.go:367）先按 PathHash 查行再决定
     覆盖或新建，闸门若在这里，等于所有同步写入都要过"hash 行的 Path 是否等于
     请求 Path"的检查，而改名（Rename 复用墓碑行）、删除（幂等，同 hash 多行时
     优先取活跃行，见 note_service.go:570 注释）恰恰依赖"hash 命中但 Path 不同
     也要继续"的既有语义，闸门会破坏这些正常情况。
   - WS 同步比较（`UpdateCheckWithNote`/`evalUpdateCheck`、`ExistsBatch`、
     `IsStaleRevive`、doNoteSync/doFileSync/doFolderSync 的逐条处理）同样以
     PathHash 为主键做差量判定，任何"拒绝碰撞"策略都会把普通同步流量变成错误。

## 完整修复方向（下一版）

完整修复要改协议（PathHash 升级 SHA-256），两端（Go 服务端 + TS 插件）必须同版本
切换，属于跨仓协议变更，单独排期。步骤：

1. 插件端 `src/lib/utils/helpers.ts` 的 `hashContent`/`hashContentAsync` 改
   `crypto.subtle.digest("SHA-256", ...)`；服务端 `EncodeHash32` 换
   `crypto/sha256` 十六进制。字段宽度同步放宽（DB varchar 足够容纳 64 hex）。
2. 兼容期方案：新列 `path_hash2`（SHA-256）双写，读时优先 `path_hash2`、
   回落旧列；迁移完成后删旧列。碰撞概率从 2^16 量级降到可忽略。
3. 若要先做纯止损（不迁协议）：最小方案是"写入前按 (vaultID, pathHash) 命中
   行后核对 `note.Path == params.Path`，不等则返回明确错误并记日志"。需要：
   - 涉及函数：`noteService.ModifyOrCreate/Rename/Delete/Restore`、
     `fileService.UpdateOrCreate/Rename/Delete/Restore`、
     `folderService.UpdateOrCreate(EnsurePathFID)/Rename/DeleteTree/Delete`；
   - 新错误码（如 532 `路径哈希冲突，写入被拒绝`）；
   - 风险：改名（复用墓碑行需跳过核对）、幂等删除（不存在的行返回成功）、
     "删除后在原路径重建"（命中墓碑行时 Path 必然不同，必须放行）都要白名单，
     正是这些特例让它一旦做错就把同步打挂；
   - 测试点：碰撞两路径后写被拒且先写内容不变；正常改名/删除/原路径重建不受影响。

## 本版交付物

本文档 + 碰撞复现证据（`Aa.md`/`BB.md` 哈希同为 62966277）+ DAO 只按
(vaultID, PathHash) 查询的定位。完整修复与止损均待下一批与协议升级一起做。
