# 从上游 3.6.1 升级到 3.7.0

3.7.0 是 isdoge 独立维护版本，发布仓库为 `isdoge/fast-note-sync-service`。插件兼容版本为 2.5.0。本文只覆盖从上游 3.6.1 升级；更早版本未验证。

## 升级前

1. 停止客户端写入和服务端进程，避免备份过程中继续变化。
2. 备份完整 `config/` 与 `storage/`。前者包含配置、`lastVersion` 和可能的 `.server_uuid`，后者包含默认 SQLite 数据库、正文、附件、索引及日志。
3. 数据库不在 `storage/` 时，按所用数据库的一致性备份流程另行备份；对象存储中的附件也要保留可恢复快照。
4. 记录当前二进制或镜像版本、启动命令、端口映射和挂载目录。
5. 检查配置中的密钥、代理和 Git 仓库地址；MySQL/MariaDB 在本版不受支持，应先使用 SQLite 或 PostgreSQL。

恢复时保持原 `security.auth-token-key` 和 `security.share-token-key`。令牌签名还包含机器标识，换机或重建运行环境后旧令牌可能失效，详见排错文档。

## Docker / Compose

把镜像改为：

```yaml
services:
  fast-note-sync-service:
    image: ghcr.io/isdoge/fast-note-sync-service:3.7.0
```

保留原 `config` 和 `storage` 挂载，然后拉取并重建容器，例如：

```sh
docker compose pull fast-note-sync-service
docker compose up -d fast-note-sync-service
docker compose logs -f fast-note-sync-service
```

仓库 `docker/docker-compose.yaml` 中容器工作目录为 `/fast-note-sync`，持久化挂载分别是 `/fast-note-sync/config` 与 `/fast-note-sync/storage`。不要通过丢弃这两个目录来“重新初始化”升级。

## 二进制

1. 从 `isdoge/fast-note-sync-service` 的 3.7.0 Release 下载对应 OS/架构压缩包。
2. 停止服务，备份旧二进制。
3. 解压并替换可执行文件，保留原工作目录、`config/` 与 `storage/`。不要覆盖自己修改过的 `config/config.yaml`。
4. 用原启动方式启动并检查迁移日志。

也可在服务停止后显式执行迁移：

```sh
./fast-note-sync-service upgrade --config config/config.yaml
```

该命令可重复执行，已记录的迁移会跳过；正常 `run` 启动也会执行升级。

## 一键脚本

仓库脚本已改为读取 isdoge Releases：

```sh
curl -fsSL https://raw.githubusercontent.com/isdoge/fast-note-sync-service/v3.7.0/scripts/quest_install.sh -o quest_install.sh
sudo sh quest_install.sh install 3.7.0
```

脚本的 `install` 会先停止服务，下载指定版本，替换二进制，安装/更新管理器并启动服务；`update` 总是安装最新版本，不适合要求固定 3.7.0 的升级。已有管理器可运行 `sudo fast-note-installer install 3.7.0`。脚本会保留目标位置已有的配置文件。

## 首次启动的一次性迁移

启动过程先执行自动建表，再运行版本大于 `config/lastVersion` 且未在 `schema_version` 中登记的迁移。从 3.6.1 升级时，新增三项按以下顺序执行：

| 迁移版本 | 内容 |
| --- | --- |
| `3.6.2` `NoteTagIndexRebuildMigrate` | 遍历未删除笔记库，从每篇笔记的 `content.txt` 重建 `note_tag` 标签索引。 |
| `3.6.3` `NoteSizeBackfillMigrate` | 对 `note.size = 0` 的行读取 `content.txt`，回填正文字节长度；读不到文件的行跳过。 |
| `3.7.0` `TimestampSecondsToMillisMigrate` | 将各用户 `note`、`file`、`folder` 表中 `0 < ctime/mtime < 100000000000` 的值乘 1000，秒级时间转为毫秒。 |

这些版本号是迁移标识，不要求先安装 3.6.2/3.6.3。大库首次启动会较慢。正文文件无法读取的标签/大小数据可能被跳过；出现迁移错误时先保留现场与日志，不要反复删除数据库重试。

时间归一化同样作用于后续写入入口。极少数 `0 < v < 1e11` 的真实毫秒值（1970–1973 年）也会被当作秒乘 1000。插件本地已经显示为 1970 年的文件时间不会由服务端迁移自动改正；文件下次修改后恢复。不要通过在 Obsidian 中删除文件再重拉来修正，因为删除会同步到其他设备。

## 行为变化

- 更新检查、下载和容器镜像改到 isdoge；不再发布 Docker Hub、cnb 或 fnOS `.fpk`。
- 限定库的令牌必须带匹配的 `vault`；不带或越权会被拒绝。
- 手动备份接口立即返回“已受理”，任务转到后台。
- 修改 Git 分支或仓库地址后，下一轮重建工作区并完整重新导出；远端历史不变，旧分支不再推进。
- Git 仅支持 HTTP(S)，私网仓库默认拒绝，包括 Tailscale 的 `100.64.0.0/10`；按需在 `git.allowed-private-hosts` 放行。
- WebSocket 心跳新增 `app.ws-ping-interval` / `ws-ping-wait`，默认完全无响应连接约 100 秒后断开；上行数据帧也刷新存活时间。
- `trusted-proxies` 默认仅信任回环地址。容器网关或跨主机代理必须显式配置。
- 启动时及每天清理过期登录令牌。
- 32 位 ARM 自动关闭 Bleve 全文搜索。
- 写接口去掉路径前导 `/`，拒绝 `..` 段；MCP 新建/改名笔记自动补 `.md`。
- 新增 `security.webgui-login-token-bind-ua`，默认仍绑定 UA。

## 回滚

1. 停止 3.7.0，保留其日志和当前目录副本。
2. 恢复升级前的 `config/`、`storage/` 以及外部数据库快照。
3. 恢复旧二进制，或把 Docker 镜像改回原来实际使用的上游 3.6.1 标签并重建。
4. 再启动客户端，先检查一个小库的笔记、附件和分享。

**不建议只把程序换回 3.6.1 而继续使用已经升级的数据目录**：3.7.0 已写入毫秒时间戳、标签索引、大小回填和新结构，旧版对这些数据的兼容性未验证。升级过程中或回滚后由新旧客户端继续写入造成的数据混杂也未验证，因此可靠回滚点是升级前的完整快照。
