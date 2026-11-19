[简体中文](https://github.com/isdoge/fast-note-sync-service/blob/master/docs/README.zh-CN.md) / [English](https://github.com/isdoge/fast-note-sync-service/blob/master/README.md) / [日本語](https://github.com/isdoge/fast-note-sync-service/blob/master/docs/README.ja.md) / [한국어](https://github.com/isdoge/fast-note-sync-service/blob/master/docs/README.ko.md) / [繁體中文](https://github.com/isdoge/fast-note-sync-service/blob/master/docs/README.zh-TW.md)

> [!IMPORTANT]
> **本倉庫是獨立維護的修復版。** 上游 [haierkeys/fast-note-sync-service](https://github.com/haierkeys/fast-note-sync-service) 自 2026 年 8 月起沒有再更新。本倉庫由 [isdoge](https://github.com/isdoge) 從上游 v3.6.1 分出，合併了上游積壓的 PR 並修復已知問題，此後獨立演進，版本號不再跟隨上游。映像檔與下載位址都已變更，詳見下文「關於本倉庫」。

有問題或需求，請在本倉庫新建 [issue](https://github.com/isdoge/fast-note-sync-service/issues/new)。


<h1 align="center">Fast Note Sync Service</h1>

<p align="center">
    <a href="https://github.com/isdoge/fast-note-sync-service/releases"><img src="https://img.shields.io/github/release/isdoge/fast-note-sync-service?style=flat-square" alt="release"></a>
    <a href="https://github.com/isdoge/fast-note-sync-service/releases"><img src="https://img.shields.io/github/v/tag/isdoge/fast-note-sync-service?label=release-alpha&style=flat-square" alt="alpha-release"></a>
    <a href="https://github.com/isdoge/fast-note-sync-service/blob/master/LICENSE"><img src="https://img.shields.io/github/license/isdoge/fast-note-sync-service?style=flat-square" alt="license"></a>
    <img src="https://img.shields.io/badge/Language-Go-00ADD8?style=flat-square" alt="Go">
</p>

<p align="center">
  <strong>高性能、低延遲的筆記同步，線上管理，遠端 REST API 服務平台</strong>
  <br>
  <em>基於 Golang + Websocket + React 建構</em>
</p>

<p align="center">
  數據提供需配合用戶端外掛程式使用：<a href="https://github.com/isdoge/obsidian-fast-note-sync">Obsidian Fast Note Sync Plugin</a>
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

## 📌 關於本倉庫

本倉庫是 [haierkeys/fast-note-sync-service](https://github.com/haierkeys/fast-note-sync-service) 的獨立維護版本，維護者 [isdoge](https://github.com/isdoge)。感謝原作者 HaierKeys 建立了這個專案。

- **來源**：從上游 v3.6.1 分出，合併了上游當時未處理的 13 個 PR，並修復了一批已知問題。完整清單見[更新日誌](/docs/CHANGELOG.zh-TW.md)。
- **版本號**：自 v3.7.0 起獨立遞增，不再跟隨上游。
- **發布管道**：二進位檔只在本倉庫的 [GitHub Releases](https://github.com/isdoge/fast-note-sync-service/releases) 發布，映像檔只發布到 `ghcr.io/isdoge/fast-note-sync-service`。程式內的更新檢查與下載位址也已指向本倉庫。
- **配套外掛**：請搭配同樣獨立維護的 [isdoge/obsidian-fast-note-sync](https://github.com/isdoge/obsidian-fast-note-sync)（v2.5.0 及以上）。部分修復需要服務端和外掛都升級才生效。
- **保持不變**：原作者的版權署名與贊助資訊、Go module 路徑（`github.com/haierkeys/fast-note-sync-service`）；原有的設定檔和資料目錄可以直接沿用。
- **意見回饋**：問題和需求請提到本倉庫的 [Issues](https://github.com/isdoge/fast-note-sync-service/issues)，不要提到上游倉庫。

### 已移除的內容

下面這些在獨立維護版裡已經移除。**如果你需要其中某一項，請[提 issue](https://github.com/isdoge/fast-note-sync-service/issues/new) 告訴我**，說明使用情境，我會評估是否恢復。

| 已移除 | 說明 | 現在請用 |
| --- | --- | --- |
| Docker Hub 映像檔 `haierkeys/fast-note-sync-service` | 本倉庫不向 Docker Hub 發布，該位址上仍是上游的舊版本 | `ghcr.io/isdoge/fast-note-sync-service` |
| 騰訊 `cnb.cool` 映像檔倉庫與下載源 | 不再同步到 cnb；一鍵指令碼的 `--cnb` 參數和映像檔切換選單、管理面板裡的 cnb 選項一併移除 | GitHub |
| 飛牛 fnOS `.fpk` 安裝包 | 不再建置 | Docker 或二進位部署 |
| Git 同步的 `ssh://`、`git://`、`user@host:path` 儲存庫位址 | 這幾種位址此前就無法完成同步，現在儲存和驗證時直接提示 | `http` / `https` 位址 |

另外，**MySQL / MariaDB 後端在目前版本不建議使用**（仍有未解決的建表問題），請使用 SQLite 或 PostgreSQL。

### 從上游版本升級

- **先備份**資料目錄與設定。首次啟動 v3.7.0 會執行三個一次性資料遷移（標籤索引回填、筆記大小回填、秒級時間戳轉毫秒）。v3.8.0 首次啟動還會執行一次路徑規範化遷移，把歷史資料裡帶反斜線的路徑改成正斜線；目標路徑已被佔用時，帶反斜線的那一條會重新命名為 `*.conflict.<時間戳>.*` 保留，不覆蓋、不刪除。
- **Docker 部署**：把映像檔位址換成 `ghcr.io/isdoge/fast-note-sync-service:latest`，資料卷和設定不用動。
- **二進位部署**：從本倉庫的 Releases 下載對應平台的套件替換原程式，或重新執行下文的一鍵指令碼。
- **行為變化**（節選，完整內容見更新日誌的「變更」與「安全」部分）：
  - `trusted-proxies` 預設只信任回環位址。同機 Docker + Nginx 部署且權杖綁定了 IP 時，需要把閘道位址加入 `trusted-proxies`。
  - 限定了筆記庫的 API 權杖，請求裡必須帶上筆記庫參數。
  - Git 同步的儲存庫在內網或 `100.64.0.0/10`（例如透過 Tailscale 存取的 Gitea）時，需要把位址或網段加入 `git.allowed-private-hosts`。
  - 完全沒有回應的 WebSocket 連線約 100 秒後斷開（原來約 75 秒）。經 Cloudflare Tunnel 等鏈路頻繁斷線時，可以調大 `app.ws-ping-wait`。

## 🎯 核心功能

* **🧰 MCP (Model Context Protocol) 原生支援**：
  * `FNS` 可以作為 MCP 服務端接入 `Cherry Studio`、`Cursor` 等相容的 AI 用戶端，即可讓 AI 具備讀寫私人筆記與附件的能力，且所有變更即時同步到各端。
  * 新增 7 個知識查詢工具：全文搜尋、失效連結、孤立筆記、標籤、最近修改、按前綴列出、庫統計；`note_list`、`file_list` 支援分頁。
  * 支援透過 OpenAI Secure MCP Tunnel 串接 ChatGPT 連接器。
* **🚀 REST API 支援**：
  * 提供標準的 REST API 介面，支援透過程式化方式（如自動化指令碼、AI 助手整合）對 Obsidian 筆記進行增刪改查。
  * 詳情請參閱 [RESTful API 文件](/docs/REST_API.md) 或 [OpenAPI 文件](/docs/swagger.yaml)。
* **💻 Web 管理面板**：
  * 內建現代化管理介面，輕鬆建立使用者、產生外掛組態、管理倉庫及筆記內容。
  * 支援 WebGUI OIDC 登入。請參閱 [OIDC 登入執行手冊](/docs/runbook/OIDC.zh-TW.md)。
  * 過期的登入權杖每天自動清理。
* **🔄 多端筆記同步**：
  * 支援 `Vault (倉庫)` 自動建立。
  * 支援筆記管理（增、刪、改、查），變更毫秒級即時分發至所有線上裝置。
* **🖼️ 附件同步支援**：
  * 完美支援圖片等非筆記檔案同步。
  * 支援大附件分片上傳下載，分片大小可配置，提升同步效率。
* **⚙️ 組態同步**：
  * 支援 `.obsidian` 設定檔的同步。
  * 支援 `PDF` 閱讀進度狀態同步。
* **📝 筆記歷史**：
  * 可以在 Web 頁面、用戶端外掛查看每一個筆記的歷史修改版本。
  * （需服務端 v1.2+ ）
* **🗑️ 資源回收桶**：
  * 支援筆記刪除後，自動進入資源回收桶。
  * 支援從資源回收桶復原筆記。（後續會陸續新增附件復原功能）

* **🚫 離線同步策略**：
  * 支援筆記離線編輯自動合併。（需要外掛端設定）
  * 離線刪除，重新連線之後自動補全或刪除同步。（需要外掛端設定）

* **🔗 分享功能**：
  * 可以 建立/取消 筆記分享。
  * 自動解析分享筆記中參照的圖片、音視訊等附件。
  * 提供分享存取統計功能。
  * 可以設定分享筆記的存取密碼。
  * 可以對分享筆記產生短連結。
* **📂 目錄同步**：
  * 支援資料夾的 建立/重新命名/移動/刪除 同步。

* **🌳 Git 自動化**：
  * 當附件和筆記發生變更時，自動更新並推送到遠端 Git 倉庫。
  * 儲存庫位址支援 http / https；內網儲存庫需要加入白名單 `git.allowed-private-hosts`。
  * 任務結束後自動釋放系統記憶體。

* **☁️ 多儲存備份與單向鏡像同步**：
  * 適配 S3/OSS/R2/WebDAV/本機 等多種儲存協定。
  * 支援全量/增量 ZIP 定時封存備份。
  * 支援 Vault 資源單向鏡像同步至遠端儲存。
  * 自動清理過期備份，支援自訂保留天數。

* **🗄️ 多資料庫支援**：
  * 支援 SQLite 與 PostgreSQL，滿足從個人到團隊的不同部署需求。MySQL / MariaDB 在目前版本不建議使用。

## ☕ 贊助與支援

- 本專案由 HaierKeys 建立。如果它對你有幫助，可以透過以下方式支持原作者（款項歸原作者，與本倉庫的維護無關）：

  | Ko-fi *非中國地區*                                                                               |    | 微信掃碼打賞 *中國地區*                        |
  |--------------------------------------------------------------------------------------------------|----|------------------------------------------------|
  | [<img src="/docs/images/kofi.png" alt="BuyMeACoffee" height="150">](https://ko-fi.com/haierkeys) | 或 | <img src="/docs/images/wxds.png" height="150"> |

  - 已支持名單：
    - <a href="https://github.com/isdoge/fast-note-sync-service/blob/master/docs/Support.zh-TW.md">Support.zh-TW.md</a>

## ⏱️ 更新日誌

- ♨️ [存取查看更新日誌](/docs/CHANGELOG.zh-TW.md)

## 🗺️ 路線圖 (Roadmap)

- [x] 增加 WebSocket `Protobuf` 傳輸格式的支援，強化同步傳輸效率。
- [x] 對現有授權機制進行隔離以及優化，提升整體安全性。
- [x] 增加 Web UI 筆記即時更新。
- [ ] 增加用戶端點對點訊息傳送（非筆記與附件，類似 LocalSend 功能，不支援用戶端儲存，可儲存到服務端）。
- [x] 各類說明文件完善。
- [ ] 更多的內網穿透（中繼閘道器）的支援。
- [x] 快速部署計劃：
  * 只需要提供伺服器位址（公網）、帳號密碼即可完成 FNS 服務端的部署。
- [ ] 優化現有的離線筆记合併方案，增加衝突處理機制。

路線圖沿用原作者的規劃。獨立維護版會按這份清單逐項推進，完成一項勾選一項。

> **如果您有改進建議或新想法，歡迎透過提交 issue 與我們分享——我們會認真評估並採納合適的建議。**

## 🚀 快速部署

我們提供多種安裝方式，推薦使用 **一鍵指令碼** 或 **Docker**。

### 方式一：一鍵指令碼（推薦）

自動偵測系統環境並完成安裝、服務註冊。

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/isdoge/fast-note-sync-service/master/scripts/quest_install.sh)
```


**指令碼主要行為：**

  * 自動下載適配目前系統的 Release 二進位檔案。
  * 預設安裝至 `/opt/fast-note`，並在 `/usr/local/bin/fns` 建立全域快捷命令 `fns`。
  * 設定並啟動 Systemd（Linux）或 Launchd（macOS）服務，實作開机自啟。
  * **互動選單**：直接執行 `fns` 可進入互動選單，支援安裝/升級、服務控制、開機自啟設定。
  * **管理命令**：`fns [install|uninstall|start|stop|status|update|menu]`

-----

### 方式二：Docker 部署

映像檔發布在 GitHub Container Registry，支援 linux/amd64、linux/arm64、linux/arm/v7。Docker Hub 上的 `haierkeys/fast-note-sync-service` 是上游舊版本，請不要混用。

#### Docker Run

```bash
# 1. 拉取鏡像
docker pull ghcr.io/isdoge/fast-note-sync-service:latest

# 2. 啟動容器
docker run -tid --name fast-note-sync-service \
    -p 9000:9000 \
    -v /data/fast-note-sync/storage/:/fast-note-sync/storage/ \
    -v /data/fast-note-sync/config/:/fast-note-sync/config/ \
    ghcr.io/isdoge/fast-note-sync-service:latest
```

#### Docker Compose

建立 `docker-compose.yaml` 檔案：

```yaml
version: '3'
services:
  fast-note-sync-service:
    image: ghcr.io/isdoge/fast-note-sync-service:latest
    container_name: fast-note-sync-service
    restart: always
    ports:
      - "9000:9000"  # RESTful API & WebSocket 連接埠 （其中 /api/user/sync 為 WebSocket 介面位址）
    volumes:
      - ./storage:/fast-note-sync/storage  # 資料儲存
      - ./config:/fast-note-sync/config    # 設定檔
```

啟動服務：

```bash
docker compose up -d
```

-----

### 方式三：手動二進位安裝

從 [Releases](https://github.com/isdoge/fast-note-sync-service/releases) 下載對應系統的最新版本，解壓後執行：

```bash
./fast-note-sync-service run -c config/config.yaml
```

### 方式四：遠端部署指令碼

在本機執行 `scripts/remote_deploy.sh`，提供伺服器位址與 SSH 帳號即可部署到遠端 Linux 伺服器，支援「二進位 + systemd」與 Docker Compose 兩種方式，詳見[遠端部署文件（簡體中文）](/docs/runbook/remote-deploy.zh-CN.md)。

```bash
./scripts/remote_deploy.sh 203.0.113.10 -u root
```

## 📖 使用指南

1.  **存取管理面板**：
    在瀏覽器開啟 `http://{伺服器IP}:9000`。
2.  **初始化設定**：
    首次存取需註冊帳號。*(如需關閉註冊功能，請在設定檔中設定 `user.register-is-enable: false`)*
3.  **配置用戶端**：
    登入管理面板，點擊 **「複製 API 組態」**。
4.  **連線 Obsidian**：
    開啟 Obsidian 外掛設定頁面，貼上剛才複製的組態資訊即可。


## ⚙️ 組態說明

預設設定檔為 `config.yaml`，程式會自動在 **根目錄** 或 **config/** 目錄下尋找。

查看完整組態範例：[config/config.yaml](https://github.com/isdoge/fast-note-sync-service/blob/master/config/config.yaml)

更多文件（簡體中文）：
- [組態項目參考](/docs/runbook/configuration.zh-CN.md)
- [反向代理與內網存取](/docs/runbook/reverse-proxy.zh-CN.md)
- [升級指南](/docs/runbook/upgrade.zh-CN.md)
- [排錯與錯誤碼](/docs/runbook/troubleshooting.zh-CN.md)
- [遠端部署](/docs/runbook/remote-deploy.zh-CN.md)

## 🌐 Nginx 反向代理組態範例

查看完整組態範例：[https-nginx-example.conf](https://github.com/isdoge/fast-note-sync-service/blob/master/scripts/https-nginx-example.conf)

## 🧰 MCP (模型上下文協定) 支援

FNS 現已原生支援 **MCP (Model Context Protocol)**，並同時提供 **SSE** 和 **StreamableHTTP** 兩種傳輸協定。

您可以將 FNS 作為 MCP 服務端直接接入 Cherry Studio、Cursor、Claude Code、hermes-agent 等相容的 AI 用戶端。接入後，AI 即可具備讀寫私人筆記和附件的能力。同時，所有由 MCP 產生的修改，都會透過 WebSocket 即時同步到您的各個裝置終端。

如需使用 Stytch 設定受 OAuth 保護的 MCP 部署，請閱讀 [MCP OAuth 與 Stytch 運行手冊](runbook/mcp-oauth-stytch.zh-TW.md)。

### 通用請求標頭參數

無論使用哪種傳輸模式，均支援以下請求標頭：

- **鑑權 Header**：`Authorization: Bearer <您的 API Token>`（在 WebGUI 的複製 API 組態中取得）
- **選用 Header**：`X-Default-Vault-Name: <筆記庫名稱>`（用於指定 MCP 操作的預設筆記庫，若工具呼叫時未指定 `vault` 參數，則使用此值）
- **選用 Header**：`X-Client: <用戶端類型>`（用於連線 MCP 的用戶端類型，如：`Cherry Studio` / `OpenClaw`）
- **選用 Header**：`X-Client-Version: <用戶端版本>`（用於連線 MCP 的用戶端版本，如：`1.1`）
- **選用 Header**：`X-Client-Name: <用戶端名稱>`（用於連線 MCP 的用戶端名稱，如：`Mac`）

---

### 接入組態：StreamableHTTP 模式（推薦）

StreamableHTTP 是 MCP 生態的標準傳輸協定，單個端點即可完成請求，對防火牆更友善，被較新的 MCP 用戶端（如 Claude Code、hermes-agent）原生支援。

- **介面位址**：`http://<您的伺服器IP或網域>:<連接埠>/api/mcp`
- **請求方式**：`POST`（傳送請求/通知）、`GET`（接聽服務端推送）、`DELETE`（終止會話）

#### 範例：Claude Code / hermes-agent / Cursor 等

*(註：請將 `<ServerIP>`、`<Port>`、`<Token>` 和 `<VaultName>` 替換為您自己的實際資訊)*

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

### 接入組態：SSE 模式（回溯相容）

SSE 模式為舊版傳輸協定，仍完整保留以保持回溯相容，適用於僅支援 SSE 的 MCP 用戶端（如 Cherry Studio）。

- **介面位址**：`http://<您的伺服器IP或網域>:<連接埠>/api/mcp/sse`

#### 範例：Cherry Studio / Cline 等

*(註：請將 `<ServerIP>`、`<Port>`、`<Token>` 和 `<VaultName>` 替換為您自己的實際資訊)*

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

## 🔗 用戶端 & 用戶端外掛程式 & 協作專案

* Obsidian Fast Note Sync 外掛程式
  * [Obsidian Fast Note Sync 外掛程式](https://github.com/isdoge/obsidian-fast-note-sync)
* 第三方用戶端
  * [FastNodeSync-CLI](https://github.com/Go1c/FastNodeSync-CLI) 一款基於 Python 和 FNS WebSocket 同步協定的雙向即時同步命令列用戶端，適用於無 GUI 的 Linux 伺服器環境（如 OpenClaw），實現與 Obsidian 桌面/行動端等價的同步能力。
  * [go-fast-note-sync](https://github.com/erichll/go-fast-note-sync) 一款基於 Go 和 FNS WebSocket 同步協定的 Go CLI 背景同步精靈程序，主要面向 Linux 無頭 (headless) 環境，同時也支援 macOS 和 Windows。
  * [Fast-note-sync-docker](https://github.com/youpingfang/obsidian-note-sync-docker) 一款基於 Docker、Python、FNS WebSocket 同步協定的快速容器化部署方案，實現筆記庫和設定檔到遠端伺服器。
* 協作專案
  * [Share to Save](https://github.com/chenxiccc/Obsidian-Share-to-Save) 是一款 Obsidian 外掛程式，將分享的網頁 URL 自動下載為 Markdown 筆記。
