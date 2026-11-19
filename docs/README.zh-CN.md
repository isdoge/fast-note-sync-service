[简体中文](https://github.com/isdoge/fast-note-sync-service/blob/master/docs/README.zh-CN.md) / [English](https://github.com/isdoge/fast-note-sync-service/blob/master/README.md) / [日本語](https://github.com/isdoge/fast-note-sync-service/blob/master/docs/README.ja.md) / [한국어](https://github.com/isdoge/fast-note-sync-service/blob/master/docs/README.ko.md) / [繁體中文](https://github.com/isdoge/fast-note-sync-service/blob/master/docs/README.zh-TW.md)

> [!IMPORTANT]
> **本仓库是独立维护的修复版。** 上游 [haierkeys/fast-note-sync-service](https://github.com/haierkeys/fast-note-sync-service) 自 2026 年 8 月起没有再更新。本仓库由 [isdoge](https://github.com/isdoge) 从上游 v3.6.1 分出，合并了上游积压的 PR 并修复已知问题，此后独立演进，版本号不再跟随上游。镜像与下载地址都已变更，详见下文「关于本仓库」。

有问题或需求，请在本仓库新建 [issue](https://github.com/isdoge/fast-note-sync-service/issues/new)。


<h1 align="center">Fast Note Sync Service</h1>

<p align="center">
    <a href="https://github.com/isdoge/fast-note-sync-service/releases"><img src="https://img.shields.io/github/release/isdoge/fast-note-sync-service?style=flat-square" alt="release"></a>
    <a href="https://github.com/isdoge/fast-note-sync-service/releases"><img src="https://img.shields.io/github/v/tag/isdoge/fast-note-sync-service?label=release-alpha&style=flat-square" alt="alpha-release"></a>
    <a href="https://github.com/isdoge/fast-note-sync-service/blob/master/LICENSE"><img src="https://img.shields.io/github/license/isdoge/fast-note-sync-service?style=flat-square" alt="license"></a>
    <img src="https://img.shields.io/badge/Language-Go-00ADD8?style=flat-square" alt="Go">
</p>

<p align="center">
  <strong>高性能、低延迟的笔记同步, 在线管理, 远端 REST API 服务平台</strong>
  <br>
  <em>基于 Golang + Websocket + React 构建</em>
</p>

<p align="center">
  数据提供需配合客户端插件使用：<a href="https://github.com/isdoge/obsidian-fast-note-sync">Obsidian Fast Note Sync Plugin</a>
</p>

<div align="center">
  <div align="center">
    <a href="/docs/images/vault.png"><img src="/docs/images/vault.png" alt="fast-note-sync-service-preview" width="400" /></a>
    <a href="/docs/images/attach.png"><img src="/docs/images/attach.png" alt="fast-note-sync-service-preview" width="400" /></a>
    </div>
  <div align="center">
    <a href="/docs/images/note.png"><img src="/docs/images/note.png" alt="fast-note-sync-service-preview" width="400" /></a>
    <a href="/docs/images/setting.png"><img src="/docs/images/setting.png" alt="fast-note-sync-service-preview" width="400" /></a>
  </div>
</div>

---

## 📌 关于本仓库

本仓库是 [haierkeys/fast-note-sync-service](https://github.com/haierkeys/fast-note-sync-service) 的独立维护版本，维护者 [isdoge](https://github.com/isdoge)。感谢原作者 HaierKeys 创建了这个项目。

- **来源**：从上游 v3.6.1 分出，合并了上游当时未处理的 13 个 PR，并修复了一批已知问题。完整清单见[更新日志](/docs/CHANGELOG.zh-CN.md)。
- **版本号**：自 v3.7.0 起独立递增，不再跟随上游。
- **发布渠道**：二进制只在本仓库的 [GitHub Releases](https://github.com/isdoge/fast-note-sync-service/releases) 发布，镜像只发布到 `ghcr.io/isdoge/fast-note-sync-service`。程序内的更新检查与下载地址也已指向本仓库。
- **配套插件**：请搭配同样独立维护的 [isdoge/obsidian-fast-note-sync](https://github.com/isdoge/obsidian-fast-note-sync)（v2.5.0 及以上）。部分修复需要服务端和插件都升级才生效。
- **保持不变**：原作者的版权署名与赞助信息、Go module 路径（`github.com/haierkeys/fast-note-sync-service`）；原有的配置文件和数据目录可以直接沿用。
- **反馈**：问题和需求请提到本仓库的 [Issues](https://github.com/isdoge/fast-note-sync-service/issues)，不要提到上游仓库。

### 已移除的内容

下面这些在独立维护版里已经移除。**如果你需要其中某一项，请[提 issue](https://github.com/isdoge/fast-note-sync-service/issues/new) 告诉我**，说明使用场景，我会评估是否恢复。

| 已移除 | 说明 | 现在请用 |
| --- | --- | --- |
| Docker Hub 镜像 `haierkeys/fast-note-sync-service` | 本仓库不向 Docker Hub 发布，该地址上仍是上游的旧版本 | `ghcr.io/isdoge/fast-note-sync-service` |
| 腾讯 `cnb.cool` 镜像仓库与下载源 | 不再同步到 cnb；一键脚本的 `--cnb` 参数和镜像切换菜单、管理面板里的 cnb 选项一并移除 | GitHub |
| 飞牛 fnOS `.fpk` 安装包 | 不再构建 | Docker 或二进制部署 |
| Git 同步的 `ssh://`、`git://`、`user@host:path` 仓库地址 | 这几种地址此前就无法完成同步，现在保存和验证时直接提示 | `http` / `https` 地址 |

另外，**MySQL / MariaDB 后端在当前版本不建议使用**（仍有未解决的建表问题），请使用 SQLite 或 PostgreSQL。

### 从上游版本升级

- **先备份**数据目录与配置。首次启动 v3.7.0 会执行三个一次性数据迁移（标签索引回填、笔记大小回填、秒级时间戳转毫秒）。v3.8.0 首次启动还会执行一次路径规范化迁移，把历史数据里带反斜杠的路径改成正斜杠；目标路径已被占用时，带反斜杠的那一条会改名为 `*.conflict.<时间戳>.*` 保留，不覆盖、不删除。
- **Docker 部署**：把镜像地址换成 `ghcr.io/isdoge/fast-note-sync-service:latest`，数据卷和配置不用动。
- **二进制部署**：从本仓库的 Releases 下载对应平台的包替换原程序，或重新执行下文的一键脚本。
- **行为变化**（节选，完整内容见更新日志的「变更」与「安全」部分）：
  - `trusted-proxies` 默认只信任回环地址。同机 Docker + Nginx 部署且令牌绑定了 IP 时，需要把网关地址加入 `trusted-proxies`。
  - 限定了笔记库的 API 令牌，请求里必须带上笔记库参数。
  - Git 同步的仓库在内网或 `100.64.0.0/10`（例如通过 Tailscale 访问的 Gitea）时，需要把地址或网段加入 `git.allowed-private-hosts`。
  - 完全没有响应的 WebSocket 连接约 100 秒后断开（原来约 75 秒）。经 Cloudflare Tunnel 等链路频繁断线时，可以调大 `app.ws-ping-wait`。

## 🎯 核心功能

* **🧰 MCP (Model Context Protocol) 原生支持**：
  * `FNS` 可以作为 MCP 服务端接入 `Cherry Studio`、`Cursor` 等兼容的 AI 客户端，即可让 AI 具备读写私人笔记与附件的能力，且所有变更实时同步到各端。
  * 新增 7 个知识查询工具：全文搜索、失效链接、孤立笔记、标签、最近修改、按前缀列出、库统计；`note_list`、`file_list` 支持分页。
  * 支持通过 OpenAI Secure MCP Tunnel 接入 ChatGPT 连接器。
* **🚀 REST API 支持**：
  * 提供标准的 REST API 接口，支持通过编程方式（如自动化脚本、AI 助手集成）对 Obsidian 笔记进行增删改查。
  * 详情请参阅 [RESTful API 文档](/docs/REST_API.md) 或 [OpenAPI 文档](/docs/swagger.yaml)。
* **💻 Web 管理面板**：
  * 内置现代化管理界面，轻松创建用户、生成插件配置、管理仓库及笔记内容。
  * 支持 WebGUI OIDC 登录。请参阅 [OIDC 登录运行手册](/docs/runbook/OIDC.zh-CN.md)。
  * 过期的登录令牌每天自动清理。
* **🔄 多端笔记同步**：
  * 支持 **Vault (仓库)** 自动创建。
  * 支持笔记管理（增、删、改、查），变更毫秒级实时分发至所有在线设备。
* **🖼️ 附件同步支持**：
  * 完美支持图片等非笔记文件同步。
  * 支持大附件 分片上传下载，分片大小可配置，提升同步效率。
* **⚙️ 配置同步**：
  * 支持 `.obsidian` 配置文件的同步。
  * 支持 `PDF` 进度状态同步。
* **📝 笔记历史**：
  * 可以在 Web 页面，插件端查看每一个笔记的 历史修改版本。
  * (需服务端 v1.2+ )
* **🗑️ 回收站**：
  * 支持笔记删除后，自动进入回收站。
  * 支持从回收站恢复笔记。(后续会陆续新增附件恢复功能)

* **🚫 离线同步策略**：
  * 支持笔记离线编辑自动合并。(需要插件端设置)
  * 离线删除，重连之后自动补全或删除同步。(需要插件端设置)

* **🔗 分享功能**：
  * 可以 创建/取消 笔记分享。
  * 自动解析分享笔记中引用的图片、音视频等附件。
  * 提供分享访问统计功能。
  * 可以设置分享笔记的访问密码。
  * 可以对分享笔记生成短链接。
* **📂 目录同步**：
  * 支持文件夹的 创建/重命名/移动/删除 同步。

* **🌳 Git 自动化**：
  * 当附件和笔记发生变更时，自动更新并推送至远程 Git 仓库。
  * 仓库地址支持 http / https；内网仓库需要加入白名单 `git.allowed-private-hosts`。
  * 任务结束后自动释放系统内存。

* **☁️ 多存储备份与单向镜像同步**：
  * 适配 S3/OSS/R2/WebDAV/本地 等多种存储协议。
  * 支持全量/增量 ZIP 定时归档备份。
  * 支持 Vault 资源单向镜像同步至远程存储。
  * 自动清理过期备份，支持自定义保留天数。

* **🗄️ 多数据库支持**：
  * 支持 SQLite 与 PostgreSQL，满足从个人到团队的不同部署需求。MySQL / MariaDB 在当前版本不建议使用。

## ☕ 赞助与支持

- 本项目由 HaierKeys 创建。如果它对你有帮助，可以通过以下方式支持原作者（款项归原作者，与本仓库的维护无关）：

  | Ko-fi *非中国地区*                                                                               |    | 微信扫码打赏 *中国地区*                        |
  |--------------------------------------------------------------------------------------------------|----|------------------------------------------------|
  | [<img src="/docs/images/kofi.png" alt="BuyMeACoffee" height="150">](https://ko-fi.com/haierkeys) | 或 | <img src="/docs/images/wxds.png" height="150"> |

  - 已支持名单：
    - <a href="https://github.com/isdoge/fast-note-sync-service/blob/master/docs/Support.zh-CN.md">Support.zh-CN.md</a>

## ⏱️ 更新日志

- ♨️ [访问查看更新日志](/docs/CHANGELOG.zh-CN.md)

## 🗺️ 路线图 (Roadmap)

- [x] 增加 WebSocket `Protobuf` 传输格式的支持, 强化同步传输效率.
- [x] 对现有授权机制进行隔离以及优化,  提升整体安全性.
- [x] 增加 WebGui 笔记实时更新
- [ ] 增加客户端 点对点 消息传送(非笔记&附件,类似localsend功能,不支持客户端保存, 可保存到服务端)
- [x] 各类帮助文档完善
- [ ] 更多的内网穿透(中继网关)的支持
- [x] 快速部署计划
  * 只需要提供服务器地址(公网), 账号密码 即可完成 FNS 服务端的部署
- [ ] 优化现有的离线笔记合并方案, 增加冲突处理机制

路线图沿用原作者的规划。独立维护版会按这份清单逐项推进，完成一项勾选一项。

> **如果您有改进建议或新想法，欢迎通过提交 issue 与我们分享——我们会认真评估并采纳合适的建议。**

## 🚀 快速部署

我们提供多种安装方式，推荐使用 **一键脚本** 或 **Docker**。

### 方式一：一键脚本（推荐）

自动检测系统环境并完成安装、服务注册。

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/isdoge/fast-note-sync-service/master/scripts/quest_install.sh)
```


**脚本主要行为：**

  * 自动下载适配当前系统的 Release 二进制文件。
  * 默认安装至 `/opt/fast-note`，并在 `/usr/local/bin/fns` 创建全局快捷命令 `fns`。
  * 配置并启动 Systemd（Linux）或 Launchd（macOS）服务，实现开机自启。
  * **交互菜单**：直接运行 `fns` 可进入交互菜单，支持安装/升级、服务控制、开机自启配置。
  * **管理命令**：`fns [install|uninstall|start|stop|status|update|menu]`

-----

### 方式二：Docker 部署

镜像发布在 GitHub Container Registry，支持 linux/amd64、linux/arm64、linux/arm/v7。Docker Hub 上的 `haierkeys/fast-note-sync-service` 是上游旧版本，请不要混用。

#### Docker Run

```bash
# 1. 拉取镜像
docker pull ghcr.io/isdoge/fast-note-sync-service:latest

# 2. 启动容器
docker run -tid --name fast-note-sync-service \
    -p 9000:9000 \
    -v /data/fast-note-sync/storage/:/fast-note-sync/storage/ \
    -v /data/fast-note-sync/config/:/fast-note-sync/config/ \
    ghcr.io/isdoge/fast-note-sync-service:latest
```

#### Docker Compose

创建 `docker-compose.yaml` 文件：

```yaml
version: '3'
services:
  fast-note-sync-service:
    image: ghcr.io/isdoge/fast-note-sync-service:latest
    container_name: fast-note-sync-service
    restart: always
    ports:
      - "9000:9000"  # RESTful API & WebSocket 端口 其中 /api/user/sync 为 WebSocket 接口地址
    volumes:
      - ./storage:/fast-note-sync/storage  # 数据存储
      - ./config:/fast-note-sync/config    # 配置文件
```

启动服务：

```bash
docker compose up -d
```

-----

### 方式三：手动二进制安装

从 [Releases](https://github.com/isdoge/fast-note-sync-service/releases) 下载对应系统的最新版本，解压后运行：

```bash
./fast-note-sync-service run -c config/config.yaml
```

### 方式四：远程部署脚本

在本地执行 `scripts/remote_deploy.sh`，提供服务器地址与 SSH 账号即可部署到远端 Linux 服务器，支持“二进制 + systemd”与 Docker Compose 两种方式，详见[远程部署文档](/docs/runbook/remote-deploy.zh-CN.md)。

```bash
./scripts/remote_deploy.sh 203.0.113.10 -u root
```

## 📖 使用指南

1.  **访问管理面板**：
    在浏览器打开 `http://{服务器IP}:9000`。
2.  **初始化设置**：
    首次访问需注册账号。*(如需关闭注册功能，请在配置文件中设置 `user.register-is-enable: false`)*
3.  **配置客户端**：
    登录管理面板，点击 **“复制 API 配置”**。
4.  **连接 Obsidian**：
    打开 Obsidian 插件设置页面，粘贴刚才复制的配置信息即可。


## ⚙️ 配置说明

默认配置文件为 `config.yaml`，程序会自动在 **根目录** 或 **config/** 目录下查找。

查看完整配置示例：[config/config.yaml](https://github.com/isdoge/fast-note-sync-service/blob/master/config/config.yaml)

更多文档：
- [配置项参考](/docs/runbook/configuration.zh-CN.md)
- [反向代理与内网访问](/docs/runbook/reverse-proxy.zh-CN.md)
- [升级指南](/docs/runbook/upgrade.zh-CN.md)
- [排错与错误码](/docs/runbook/troubleshooting.zh-CN.md)
- [远程部署](/docs/runbook/remote-deploy.zh-CN.md)

## 🌐 Nginx 反代配置示例

查看完整配置示例：[https-nginx-example.conf](https://github.com/isdoge/fast-note-sync-service/blob/master/scripts/https-nginx-example.conf)

## 🧰 MCP (模型上下文协议) 支持

FNS 现已原生支持 **MCP (Model Context Protocol)**，并同时提供 **SSE** 和 **StreamableHTTP** 两种传输协议。

您可以将 FNS 作为 MCP 服务端直接接入 Cherry Studio、Cursor、Claude Code、hermes-agent 等兼容的 AI 客户端。接入后，AI 即可具备读写私人笔记和附件的能力。同时，所有由 MCP 产生的修改，都会通过 WebSocket 实时同步到您的各个设备终端。

如需使用 Stytch 配置受 OAuth 保护的 MCP 部署，请阅读 [MCP OAuth 与 Stytch 运行手册](runbook/mcp-oauth-stytch.zh-CN.md)。

### 通用请求头参数

无论使用哪种传输模式，均支持以下请求头：

- **鉴权 Header**：`Authorization: Bearer <您的 API Token>`（在 WebGUI 的复制 API 配置中获取）
- **可选 Header**：`X-Default-Vault-Name: <笔记库名称>`（用于指定 MCP 操作的默认笔记库，若工具调用时未指定 `vault` 参数，则使用此值）
- **可选 Header**：`X-Client: <客户端类型>`（用于连接 MCP 的客户端类型，如：Cherry Studio / OpenClaw）
- **可选 Header**：`X-Client-Version: <客户端版本>`（用于连接 MCP 的客户端版本，如：1.1）
- **可选 Header**：`X-Client-Name: <客户端名称>`（用于连接 MCP 的客户端名称，如：Mac）

---

### 接入配置：StreamableHTTP 模式（推荐）

StreamableHTTP 是 MCP 生态的标准传输协议，单个端点即可完成请求，对防火墙更友好，被较新的 MCP 客户端（如 Claude Code、hermes-agent）原生支持。

- **接口地址**：`http://<您的服务器IP或域名>:<端口>/api/mcp`
- **请求方式**：`POST`（发送请求/通知）、`GET`（监听服务端推送）、`DELETE`（终止会话）

#### 示例：Claude Code / hermes-agent / Cursor 等

*(注：请将 `<ServerIP>`、`<Port>`、`<Token>` 和 `<VaultName>` 替换为您自己的实际信息)*

```json
{
  "mcpServers": {
    "fns": {
      "url": "http://<ServerIP>:<Port>/api/mcp",
      "type": "http",
      "headers": {
        "Content-Type": "application/json",
        "Authorization": "Bearer <Token>",
        "X-Default-Vault-Name": "<VaultName>",
        "X-Client": "<Client>",
        "X-Client-Version": "<ClientVersion>",
        "X-Client-Name": "<ClientName>"
      }
    }
  }
}
```

---

### 接入配置：SSE 模式（向后兼容）

SSE 模式为旧版传输协议，仍完整保留以保持向后兼容，适用于仅支持 SSE 的 MCP 客户端（如 Cherry Studio）。

- **接口地址**：`http://<您的服务器IP或域名>:<端口>/api/mcp/sse`

#### 示例：Cherry Studio / Cline 等

*(注：请将 `<ServerIP>`、`<Port>`、`<Token>` 和 `<VaultName>` 替换为您自己的实际信息)*

```json
{
  "mcpServers": {
    "fns": {
      "url": "http://<ServerIP>:<Port>/api/mcp/sse",
      "type": "sse",
      "headers": {
        "Content-Type": "application/json",
        "Authorization": "Bearer <Token>",
        "X-Default-Vault-Name": "<VaultName>",
        "X-Client": "<Client>",
        "X-Client-Version": "<ClientVersion>",
        "X-Client-Name": "<ClientName>"
      }
    }
  }
}
```

## 🔗 客户端 & 客户端插件 & 协作项目

* Obsidian Fast Note Sync 插件
  * [Obsidian Fast Note Sync Plugin](https://github.com/isdoge/obsidian-fast-note-sync)
* 三方客户端
  * [FastNodeSync-CLI ](https://github.com/Go1c/FastNodeSync-CLI) 一款基于 Python 和 FNS WebSocket 同步协议实现的双向实时同步的命令行客户端, 适用于无 GUI 的 Linux 服务器环境（如 OpenClaw），实现与 Obsidian 桌面/移动端等价的同步能力。
  * [go-fast-note-sync](https://github.com/erichll/go-fast-note-sync) 一款基于 Go 和 FNS WebSocket 同步协议的 Go CLI 后台同步守护进程，主要面向 Linux 无头（headless）环境，同时也支持 macOS 和 Windows。
  * [Fast-note-sync-docker](https://github.com/youpingfang/obsidian-note-sync-docker) 一款基于 Docker ， Python ， FNS WebSocket 同步协议 的 快速容器化部署方案，实现 笔记库和配置文件到远程服务器。
* 协作项目
  * [Share to Save](https://github.com/chenxiccc/Obsidian-Share-to-Save) 是一款 Obsidian 插件，将分享的网页 URL 自动下载为 Markdown 笔记。
