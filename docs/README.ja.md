[简体中文](https://github.com/isdoge/fast-note-sync-service/blob/master/README.md) / [English](https://github.com/isdoge/fast-note-sync-service/blob/master/docs/README.en.md) / [日本語](https://github.com/isdoge/fast-note-sync-service/blob/master/docs/README.ja.md) / [한국어](https://github.com/isdoge/fast-note-sync-service/blob/master/docs/README.ko.md) / [繁體中文](https://github.com/isdoge/fast-note-sync-service/blob/master/docs/README.zh-TW.md)

> [!IMPORTANT]
> **本リポジトリは独立メンテナンスの修正版です。** アップストリーム [haierkeys/fast-note-sync-service](https://github.com/haierkeys/fast-note-sync-service) は 2026 年 8 月以降更新されていません。本リポジトリは [isdoge](https://github.com/isdoge) がアップストリーム v3.6.1 からフォークし、未処理だった PR をマージして既知の問題を修正したもので、以後は独立して進化し、バージョン番号はアップストリームに追随しません。イメージとダウンロード先も変更されています。詳細は下記の「本リポジトリについて」をご覧ください。

問題やご要望がある場合は、本リポジトリで [issue](https://github.com/isdoge/fast-note-sync-service/issues/new) を作成してください。


<h1 align="center">Fast Note Sync Service</h1>

<p align="center">
    <a href="https://github.com/isdoge/fast-note-sync-service/releases"><img src="https://img.shields.io/github/release/isdoge/fast-note-sync-service?style=flat-square" alt="release"></a>
    <a href="https://github.com/isdoge/fast-note-sync-service/releases"><img src="https://img.shields.io/github/v/tag/isdoge/fast-note-sync-service?label=release-alpha&style=flat-square" alt="alpha-release"></a>
    <a href="https://github.com/isdoge/fast-note-sync-service/blob/master/LICENSE"><img src="https://img.shields.io/github/license/isdoge/fast-note-sync-service?style=flat-square" alt="license"></a>
    <img src="https://img.shields.io/badge/Language-Go-00ADD8?style=flat-square" alt="Go">
</p>

<p align="center">
  <strong>高性能・低遅延のノート同期、オンライン管理、リモート REST API サービスプラットフォーム</strong>
  <br>
  <em>Golang + Websocket + React ベース</em>
</p>

<p align="center">
  データ提供はクライアントプラグインと組み合わせる必要があります：<a href="https://github.com/isdoge/obsidian-fast-note-sync">Obsidian Fast Note Sync Plugin</a>
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

## 📌 本リポジトリについて

本リポジトリは [haierkeys/fast-note-sync-service](https://github.com/haierkeys/fast-note-sync-service) の独立メンテナンス版で、メンテナーは [isdoge](https://github.com/isdoge) です。このプロジェクトを作成してくださった原作者 HaierKeys 氏に感謝します。

- **由来**：アップストリーム v3.6.1 からフォークし、当時未処理だった 13 件の PR をマージし、一連の既知の問題を修正しています。完全な一覧は[更新履歴](/docs/CHANGELOG.ja.md)をご覧ください。
- **バージョン番号**：v3.7.0 以降は独立して採番され、アップストリームには追随しません。
- **リリースチャネル**：バイナリは本リポジトリの [GitHub Releases](https://github.com/isdoge/fast-note-sync-service/releases) のみで、コンテナイメージは `ghcr.io/isdoge/fast-note-sync-service` のみで公開されます。アプリ内のアップデート確認とダウンロード先も本リポジトリを指すようになりました。
- **併用するプラグイン**：同じく独立メンテナンスされている [isdoge/obsidian-fast-note-sync](https://github.com/isdoge/obsidian-fast-note-sync)（v2.5.0 以上）と併用してください。一部の修正はサーバーとプラグインの両方をアップグレードしないと有効になりません。
- **変更しないもの**：原作者の著作権表記と寄付情報、Go module パス（`github.com/haierkeys/fast-note-sync-service`）。既存の設定ファイルとデータディレクトリはそのまま使えます。
- **フィードバック**：問題やご要望は本リポジトリの [Issues](https://github.com/isdoge/fast-note-sync-service/issues) までお寄せください。アップストリームには投稿しないでください。

### 削除されたもの

以下は独立メンテナンス版では削除されています。**必要なものがあれば、[issue](https://github.com/isdoge/fast-note-sync-service/issues/new) を立てて使用場面を教えてください。** 復元するかどうかを検討します。

| 削除されたもの | 説明 | 代わりに使うもの |
| --- | --- | --- |
| Docker Hub イメージ `haierkeys/fast-note-sync-service` | 本リポジトリは Docker Hub に公開しません。同アドレスには引き続き古いアップストリーム版が置かれています | `ghcr.io/isdoge/fast-note-sync-service` |
| Tencent `cnb.cool` イメージリポジトリとダウンロードソース | cnb への同期を廃止。ワンクリックスクリプトの `--cnb` オプションとイメージ切替メニュー、管理パネルの cnb 選択肢も削除しました | GitHub |
| 飛牛 fnOS `.fpk` インストーラー | ビルドされなくなりました | Docker またはバイナリでのデプロイ |
| Git 同期の `ssh://`、`git://`、`user@host:path` 形式のリポジトリ URL | これらの形式は以前から同期できず、保存・検証時にその旨を表示するようになりました | `http` / `https` URL |

### アップストリーム版からのアップグレード

- まずデータディレクトリと設定を**バックアップ**してください。v3.7.0 の初回起動時に 3 つの一回限りのデータ移行（タグインデックスのバックフィル、ノートサイズのバックフィル、秒→ミリ秒タイムスタンプ変換）が実行されます。v3.8.0 では初回起動時にパス正規化の移行も実行され、履歴データ内のバックスラッシュを含むパスがスラッシュに変換されます。変換先のパスがすでに使用されている場合、バックスラッシュを含む項目は `*.conflict.<タイムスタンプ>.*` に名前を変更して保持され、上書きや削除は行われません。既存の MySQL / MariaDB 環境を v3.8.0 へアップグレードする場合、初回起動時にパスとノート庫名の列が大文字・小文字を区別するバイナリ型（`varbinary(1024)` / `varbinary(255)`）へ変更され、変更前にバイト長の事前チェックが自動実行されます。履歴データに 1024 UTF-8 バイトを超える長いパス（または 255 バイトを超えるノート庫名）がある場合、事前チェックは該当するテーブル名とレコード ID を表示して移行を中止しますので、短くしてから再起動してください。
- **Docker デプロイ**：イメージアドレスを `ghcr.io/isdoge/fast-note-sync-service:latest` に変更してください。ボリュームと設定はそのままで構いません。
- **v3.8.4 の初回起動**では、既存のすべてのノート・添付ファイル・設定・フォルダの行に 128 ビットのランダムな `revision` トークンがバックフィルされます（SQLite、PostgreSQL、MySQL / MariaDB すべて対象）。手作業は不要ですが、初回起動時に各行を一度ずつ書き込むため、データ量の多い環境では事前にデータベースをバックアップしてください。
- **バイナリデプロイ**：本リポジトリの Releases から該当プラットフォームのパッケージをダウンロードして置き換えるか、下記のワンクリックスクリプトを再実行してください。
- **動作の変更**（抜粋。完全な内容は更新履歴の「変更」と「セキュリティ」の節を参照）：
  - `trusted-proxies` のデフォルトはループバックアドレスのみを信頼します。同一ホストの Docker + Nginx 構成でトークンを IP にバインドしている場合は、ゲートウェイアドレスを `trusted-proxies` に追加してください。
  - ノート庫を制限した API トークンは、リクエストに必ずノート庫パラメータを含める必要があります。
  - Git 同期のリポジトリがイントラネットまたは `100.64.0.0/10`（例：Tailscale 経由の Gitea）にある場合、アドレスまたはサブネットを `git.allowed-private-hosts` に追加してください。
  - 全く応答のない WebSocket 接続は約 100 秒で切断されます（以前は約 75 秒）。Cloudflare Tunnel などで頻繁に切断される場合は `app.ws-ping-wait` を大きくしてください。

## 🎯 主な機能

* **🧰 MCP (Model Context Protocol) ネイティブサポート**:
  * `FNS` はMCPサーバーとして、`Cherry Studio`や`Cursor`などの互換性のあるAIクライアントに接続でき、AIにプライベートなノートや添付ファイルの読み書き機能を提供します。すべての変更は各クライアントにリアルタイムで同期されます。
  * 7 つのナレッジ検索ツールを追加：全文検索、壊れたリンク、孤立ノート、タグ、最近の変更、プレフィックス付き一覧、ノート庫統計。`note_list`、`file_list` はページングに対応。
  * OpenAI Secure MCP Tunnel 経由で ChatGPT コネクタとして接続できます。
* **🚀 REST API サポート**:
  * 标准のREST APIインターフェースを提供し、プログラム（自動化スクリプトやAIアシスタントの統合など）を介したObsidianノートのCRUD操作をサポートします。
  * 詳細は、[RESTful API ドキュメント](/docs/REST_API.md) または [OpenAPI ドキュメント](/docs/swagger.yaml) を参照してください。
* **💻 Web管理パネル**:
  * 最新の管理画面を内蔵し、ユーザーの作成、プラグイン設定の生成、リポジトリやノート内容の管理を簡単に行えます。
  * 期限切れのログイントークンを毎日自動的にクリーンアップします。
* **🔄 複数デバイスのノート同期**:
  * Vault（リポジトリ）の自動作成をサポート。
  * ノート管理（追加、削除、編集、検索）をサポートし、変更はミリ秒単位でリアルタイムにすべてのオンラインデバイスに配信されます。
* **🖼️ 添付ファイル同期サポート**:
  * 画像などのノート以外のファイルの同期を完全にサポート。
  * 大容量の添付ファイルの分割アップロード・ダウンロードをサポートし、分割サイズを設定して同期効率を向上できます。
* **⚙️ 設定同期**:
  * `.obsidian` 設定ファイルの同期をサポート。
  * `PDF` の閲覧進捗ステータスの同期をサポート。
* **📝 ノート履歴**:
  * Webページやクライアントプラグイン側で、各ノートの修正履歴バージョンを確認できます。
  * （サーバー側 v1.2+ が必要）
* **🗑️ ゴミ箱**:
  * ノート削除後、自動的にゴミ箱に送られます。
  * ゴミ箱からのノート復元をサポート。（添付ファイルの復元機能は今後順次追加予定です）

* **🚫 オフライン同期ポリシー**:
  * ノートのオフライン編集時の自動マージをサポート。（プラグイン側の設定が必要です）
  * オフラインでの削除後、再接続時に自動的に補完または同期削除されます。（プラグイン側の設定が必要です）

* **🔗 共有機能**:
  * ノート共有の作成/キャンセルが可能。
  * 共有ノート内で引用されている画像、音声、動画などの添付ファイルを自動的に解析します。
  * 共有アクセス統計機能を提供。
  * 共有ノートのアクセスパスワードを設定可能。
  * 共有ノートの短縮リンクを生成可能。
* **📂 フォルダ同期**:
  * フォルダの作成/名前変更/移動/削除の同期をサポート。

* **🌳 Git自動化**:
  * 添付ファイルやノートに変更があった場合、自動的に更新してリモートのGitリポジトリにプッシュします。
  * リポジトリ URL は http / https に対応。イントラネットのリポジトリは `git.allowed-private-hosts` のホワイトリストへの追加が必要です。
  * タスク終了後、自動的にシステムメモリを解放します。
  * サブディレクトリのサンドボックスと静的ブログ公開（Astro / Firefly）に対応：指定したソースディレクトリのみをリポジトリの対象サブディレクトリへエクスポートし、frontmatter で絞り込み、エクスポート前に記事を検証し、参照画像を `./assets/` に集約したうえで、実際のプッシュ後にデプロイ Webhook を呼び出します。

* **☁️ マルチストレージバックアップと一方向ミラー同期**:
  * S3/OSS/R2/WebDAV/ローカルなどの複数のストレージプロトコルに対応。
  * フル/増分ZIP定期アーカイブバックアップをサポート。
  * Vaultリソースのリモートストレージへの一方向ミラー同期をサポート。
  * 期限切れバックアップの自動クリーンアップをサポートし、カスタム保持日数を設定可能。

* **🗄️ マルチデータベースサポート**:
  * SQLite、PostgreSQL、MySQL / MariaDB（MySQL 8.4、MariaDB 11.4 で検証済み）に対応し、個人からチームまでのさまざまなデプロイ要件を満たします。ノートや添付ファイルなどのユーザーデータを PostgreSQL または MySQL / MariaDB に保存するには、`user-database.type` と接続パラメータも明示的に設定する必要があります（空欄のままでは既定どおりローカルの SQLite ファイルに書き込まれます。MySQL / MariaDB アカウントには `CREATE DATABASE` 権限が必要です）。

## ☕ スポンサーとサポート

- 本プロジェクトは HaierKeys によって作成されました。役に立ったと思われた場合は、以下の方法で原作者を支援できます（寄付は原作者のものであり、本リポジトリのメンテナンスには関しません）:

  | Ko-fi *中国以外の地域*                                                                          |    | WeChat Pay *中国国内*                          |
  |--------------------------------------------------------------------------------------------------|----|------------------------------------------------|
  | [<img src="/docs/images/kofi.png" alt="BuyMeACoffee" height="150">](https://ko-fi.com/haierkeys) | or | <img src="/docs/images/wxds.png" height="150"> |

  - 支援者リスト：
    - <a href="https://github.com/isdoge/fast-note-sync-service/blob/master/docs/Support.ja.md">Support.ja.md</a>

## ⏱️ 更新履歴

- ♨️ [更新履歴を表示する](/docs/CHANGELOG.ja.md)

## 🗺️ ロードマップ

- [x] WebSocketの `Protobuf` 転送フォーマットのサポートを追加し、同期伝送効率を強化。
- [x] 既存の認可メカニズムを分離および最適化し、全体のセキュリティを向上。
- [x] Web UIにおけるノートのリアルタイム更新を追加。
- [ ] クライアント間のピアツーピア（P2P）メッセージ送信を追加（ノートや添付ファイル以外、LocalSendのような機能。クライアント側での保存はサポートせず、サーバー側での保存が可能）。
- [x] 各種ヘルプドキュメントの充実。
- [ ] より多くのイントラネット浸透（リレーゲートウェイ）のサポート。
- [x] 迅速なデプロイ計画：
  * サーバーアドレス（パブリックIP）とアカウント名・パスワードを提供するだけで、FNSサーバー側のデプロイが完了します。
- [x] 既存のオフラインノートマージスキームを最適化し、競合処理メカニズムを追加。

ロードマップは原作者の計画を引き継いでいます。独立メンテナンス版はこのリストに沿って 1 項目ずつ進め、完了したものからチェックしていきます。

> **改善の提案や新しいアイデアがございましたら、issueを提出して共有してください。適切な提案は慎重に評価し採用いたします。**

## 🚀 クイックデプロイ

複数のインストール方法を提供しています。**ワンクリックスクリプト** または **Docker** の使用を推奨します。

### 方法1：ワンクリックスクリプト（推奨）

システム環境を自动検出し、インストールとサービス登録を完了します。

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/isdoge/fast-note-sync-service/master/scripts/quest_install.sh)
```


**スクリプトの主な動作：**

  * 現在のシステムに適したReleaseバイナリファイルを自動的にダウンロードします。
  * デフォルトで `/opt/fast-note` にインストールされ、`/usr/local/bin/fns` にグローバルなショートカットコマンド `fns` を作成します。
  * Systemd（Linux）または Launchd（macOS）サービスを設定・起動し、自動起動を有効にします。
  * **対話メニュー**：`fns` を直接実行すると対話メニューが開き、インストール/アップグレード、サービス制御、自動起動の設定が行えます。
  * **管理コマンド**: `fns [install|uninstall|start|stop|status|update|menu]`

-----

### 方法2：Dockerデプロイ

イメージは GitHub Container Registry で公開されており、linux/amd64、linux/arm64、linux/arm/v7 に対応しています。Docker Hub の `haierkeys/fast-note-sync-service` は古いアップストリーム版なので、混用しないでください。

#### Docker Run

```bash
# 1. イメージのプル
docker pull ghcr.io/isdoge/fast-note-sync-service:latest

# 2. コンテナの起動
docker run -tid --name fast-note-sync-service \
    -p 9000:9000 \
    -v /data/fast-note-sync/storage/:/fast-note-sync/storage/ \
    -v /data/fast-note-sync/config/:/fast-note-sync/config/ \
    ghcr.io/isdoge/fast-note-sync-service:latest
```

#### Docker Compose

docker-compose.yaml ファイルを作成：

```yaml
version: '3'
services:
  fast-note-sync-service:
    image: ghcr.io/isdoge/fast-note-sync-service:latest
    container_name: fast-note-sync-service
    restart: always
    ports:
      - "9000:9000"  # RESTful API & WebSocket ポート（うち /api/user/sync が WebSocket インターフェースアドレス）
    volumes:
      - ./storage:/fast-note-sync/storage  # 数据ストレージ
      - ./config:/fast-note-sync/config    # 設定ファイル
```

サービスの起動：

```bash
docker compose up -d
```

-----

### 方法3：手動バイナリインストール

[Releases](https://github.com/isdoge/fast-note-sync-service/releases) から対応する系统的最新バージョンをダウンロードし、解凍して実行します：

```bash
./fast-note-sync-service run -c config/config.yaml
```

### 方法4：リモートデプロイスクリプト

ローカルで `scripts/remote_deploy.sh` を実行し、サーバーアドレスと SSH アカウントを指定するだけでリモート Linux サーバーにデプロイできます。バイナリ + systemd と Docker Compose の両方式に対応しています。詳細は[リモートデプロイドキュメント](/docs/runbook/remote-deploy.en.md)を参照してください。

```bash
./scripts/remote_deploy.sh 203.0.113.10 -u root
```

## 📖 使用ガイド

1.  **管理パネルにアクセス**:
    ブラウザで `http://{サーバーIP}:9000` を開きます。
2.  **初期設定**:
    初回アクセス時はアカウント登録が必要です。（登録機能を無効にする場合は、設定ファイルで `user.register-is-enable: false` と設定してください）
3.  **クライアントの設定**:
    管理パネルにログインし、「API設定をコピー」をクリックします。
4.  **Obsidianへの接続**:
    Obsidianのプラグイン設定ページを開き、コピーした設定情報を貼り付けます。


## ⚙️ 設定の説明

デフォルトの設定ファイルは `config.yaml` です。プログラムは自動的に**ルートディレクトリ**または **config/** ディレクトリ内を検索します。

完全な設定例を表示：[config/config.yaml](https://github.com/isdoge/fast-note-sync-service/blob/master/config/config.yaml)

その他のドキュメント：
- [設定項目リファレンス](/docs/runbook/configuration.en.md)
- [リバースプロキシとプライベートネットワークアクセス](/docs/runbook/reverse-proxy.en.md)
- [アップグレードガイド](/docs/runbook/upgrade.en.md)
- [トラブルシューティングとエラーコード](/docs/runbook/troubleshooting.en.md)
- [リモートデプロイ](/docs/runbook/remote-deploy.en.md)
- [Firefly ブログへの公開（ループバック Git ゲートウェイ、英語）](/docs/runbook/firefly-blog.en.md)

## 🌐 Nginx リバースプロキシ設定例

完全な設定例を表示：[https-nginx-example.conf](https://github.com/isdoge/fast-note-sync-service/blob/master/scripts/https-nginx-example.conf)

## 🧰 MCP (Model Context Protocol) サポート

FNSは現在、**MCP (Model Context Protocol)** をネイティブサポートしており、**SSE** と **StreamableHTTP** の両方の転送プロトコルを提供しています。

FNSをMCPサーバーとしてCherry Studio、Cursor、Claude Code、hermes-agentなどの互換性のあるAIクライアントに直接接続できます。接続後、AIはプライベートなノートや添付ファイルを読み書きできるようになります。また、MCPによる変更はWebSocketを通じてリアルタイムに各デバイス端末に同期されます。

### 共通リクエストヘッダーパラメータ

どの転送モードを使用する場合でも、以下のリクエストヘッダーがサポートされます：

- **認証用Header**: `Authorization: Bearer <APIトークン>` （WebGUIの「API設定をコピー」から取得）
- **オプションのHeader**: `X-Default-Vault-Name: <ノート庫名>` （MCP操作のデフォルトのノート庫を指定。ツール呼び出し時に `vault` パラメータが指定されていない場合、この値が使用されます）
- **オプションのHeader**: `X-Client: <クライアントタイプ>` （MCPに接続するクライアントのタイプ。例：`Cherry Studio`、`OpenClaw`）
- **オプションのHeader**: `X-Client-Version: <クライアントバージョン>` （接続するクライアントのバージョン。例：`1.1`）
- **オプションのHeader**: `X-Client-Name: <クライアント名>` （接続するクライアントの名前。例：`Mac`）

---

### 接続設定: StreamableHTTP モード（推奨）

StreamableHTTPはMCPエコシステムの標準的な転送プロトコルです。単一のエンドポイントでリクエストを完了でき、ファイアウォールに優しく、新しいMCPクライアント（Claude Codeやhermes-agentなど）でネイティブにサポートされています。

- **インターフェースアドレス**: `http://<サーバーIPまたはドメイン>:<ポート>/api/mcp`
- **リクエスト方法**: `POST`（リクエスト/通知送信）、`GET`（サーバーからのプッシュ監視）、`DELETE`（セッション終了）

#### 例：Claude Code / hermes-agent / Cursor など

*(注: `<ServerIP>`、`<Port>`、`<Token>`、`<VaultName>` をご自身の実際の情報に置き換えてください)*

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

### 接続設定: SSE モード（後方互換）

SSEモードは従来の転送プロトコルです。後方互換性を維持するために完全に保持されており、SSEのみをサポートするMCPクライアント（Cherry Studioなど）に適しています。

- **インターフェースアドレス**: `http://<サーバーIPまたはドメイン>:<ポート>/api/mcp/sse`

#### 例：Cherry Studio / Cline など

*(注: `<ServerIP>`、`<Port>`、`<Token>`、`<VaultName>` をご自身の実際の情報に置き換えてください)*

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

## 🔗 クライアント ＆ クライアントプラグイン ＆ 共同プロジェクト

* Obsidian Fast Note Sync プラグイン
  * [Obsidian Fast Note Sync プラグイン](https://github.com/isdoge/obsidian-fast-note-sync)
* サードパーティ製クライアント
  * [FastNodeSync-CLI](https://github.com/Go1c/FastNodeSync-CLI) PythonおよびFNS WebSocket同期プロトコルに基づき実装された、双方向リアルタイム同期のコマンドラインクライアント。GUIのないLinuxサーバー環境（OpenClawなど）に適しており、Obsidianデスクトップ/モバイル端と同等の同期機能を実現します。
  * [go-fast-note-sync](https://github.com/erichll/go-fast-note-sync) GoおよびFNS WebSocket同期プロトコルに基づいたGo CLIバックグラウンド同期デーモン。主にLinuxヘッドレス環境向けであり、macOSとWindowsもサポートしています。
  * [Fast-note-sync-docker](https://github.com/youpingfang/obsidian-note-sync-docker) Docker、Python、FNS WebSocket同期プロトコルに基づいた迅速なコンテナ化デプロイソリューション。ノートライブラリと設定ファイルをリモートサーバーに同期します。
* 共同プロジェクト
  * [Share to Save](https://github.com/chenxiccc/Obsidian-Share-to-Save) 共有されたウェブページのURLを自動的にMarkdownノートとしてダウンロードするObsidianプラグイン。
