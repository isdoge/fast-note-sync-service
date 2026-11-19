# 远程部署（快速部署）

用途：本文说明如何在自己的电脑（macOS / Linux）上运行 `scripts/remote_deploy.sh`，只提供服务器地址和 SSH 账号，就把 Fast Note Sync 服务端部署到远程 Linux 服务器。原作者路线图中的「快速部署计划：只需要提供服务器地址（公网）、账号密码，即可完成 FNS 服务端的部署」即由该脚本实现。

相关脚本：

- `scripts/remote_deploy.sh` —— 在本地运行，负责连接远端并完成部署。
- `scripts/quest_install.sh` —— 被上传到远端以非交互方式执行，实际完成安装。

## 快速开始

```bash
# 密钥认证（推荐）
./scripts/remote_deploy.sh 203.0.113.10 -u root -i ~/.ssh/id_ed25519

# 密码认证：直接运行，ssh 会提示输密码，整个流程只输一次
./scripts/remote_deploy.sh 203.0.113.10 -u root

# 用本地构建的安装包安装未发布版本（如 3.7.0 尚未发 Release）
./scripts/remote_deploy.sh 192.168.1.5 --tarball ./fast-note-sync-service-3.7.0-linux-amd64.tar.gz

# Docker 方式部署
./scripts/remote_deploy.sh example.com --mode docker
```

## 参数

| 参数 | 说明 | 默认值 |
| --- | --- | --- |
| `<host>` | 服务器地址（必填） | — |
| `-u, --user <user>` | SSH 用户 | `root` |
| `-p, --port <port>` | SSH 端口 | `22` |
| `-i, --key <path>` | SSH 私钥路径 | ssh 默认配置 |
| `-v, --version <ver>` | 要安装的版本（binary 与 Docker 均生效） | `latest` |
| `--http-port <port>` | 服务端 HTTP 端口（数字，1–65535） | 沿用服务端配置 `:9000` |
| `--mode <binary\|docker>` | 部署方式 | `binary` |
| `--tarball <path>` | 本地 tar.gz 安装包（仅 binary 模式） | 从 GitHub Release 下载 |
| `--force` | 确认在 binary 与 Docker 间切换 | 关 |

## 认证与密码安全

- **优先使用密钥**（`-i` 或本机 ssh 默认配置），无需输入任何密码。
- 密码认证时，脚本把认证完全交给 `ssh` 自身的交互提示：密码**不会**出现在命令行参数、进程列表或任何文件中；并通过 SSH 连接复用（ControlMaster）让整个部署流程只需输入一次密码。
- 只有当你本机安装了 `sshpass` 并且**自己**通过环境变量 `SSHPASS` 显式提供密码时，才走非交互密码认证；脚本从不主动读取或记录密码。
- 首次连接使用 `StrictHostKeyChecking=accept-new` 自动接受新主机指纹；对安全要求更高的环境请先手动确认指纹。

## 两种部署方式

### binary + systemd（默认）

流程：SSH 连通性与权限检查（root 或可 sudo）→ 探测远端系统与架构 → 上传 `quest_install.sh`（及可选的本地安装包）→ 非交互执行 `install <version> --yes` → 安装器在有 systemd 时注册服务并开机自启 → 健康检查；指定具体版本时核对健康接口返回的运行版本，不一致即报错。

- 远端没有 systemd（如部分容器环境）时，服务以后台进程方式启动并给出警告，重启机器后不会自启。
- `--http-port` 会修改远端 `config/config.yaml` 的 `http-port` 并重启服务。

### Docker

流程：检查远端 `docker` 与 `docker compose`（任一缺失直接报错提示安装，脚本不会擅自安装）→ 在 `/opt/fast-note-docker` 写入 compose 文件并启动 → 健康检查。

compose 要点：镜像 `ghcr.io/isdoge/fast-note-sync-service`；数据与配置挂载到 `/opt/fast-note/storage` 与 `/opt/fast-note/config`；`restart: unless-stopped`。

## 已安装时的行为

- 远端已存在 `/opt/fast-note/fast-note-sync-service` 时，默认执行**升级**：替换二进制、保留 `config/` 与 `storage/` 数据（安装脚本对 config 只补缺不覆盖）。
- 已有二进制安装又指定 `--mode docker`，或已有同名 Docker 容器又指定 binary 时，需 `--force` 确认后才继续。
- 如果远端装有 Docker，脚本会先检查守护进程和同名容器；无法检查时中止，请先修复 Docker 权限或守护进程状态。
- 切换到 Docker 前，会禁用并停止旧 systemd 服务、停止残留后台进程并确认退出；切换到 binary 前，会关闭旧容器的自动重启、停止容器并确认停止。停止或状态确认失败就中止，避免两种实例同时操作数据目录。
- 切换保留旧程序、容器和数据。新部署失败后旧实例保持停止，不自动回滚；如需恢复，先确认新实例已停止，再在远端手动执行 `sudo systemctl enable --now fast-note.service`（后台部署用 `sudo fns start`），或 `sudo docker update --restart=unless-stopped fast-note-sync-service && sudo docker start fast-note-sync-service`。
- 任何一步失败脚本都会报错退出；退出时只清理本次创建的远端临时目录。

## 部署后必做的安全配置

1. 首次访问 `http://<host>:9000` 注册管理员账号。
2. 注册完成后在 `config/config.yaml` 中关闭开放注册：`register-is-enable: false`，然后重启服务。
3. 在服务器防火墙 / 云安全组只放行需要的 TCP 端口（默认 9000）。
4. 生产环境强烈建议配置反向代理并启用 HTTPS（参考 `docs/runbook/` 与仓库自带 nginx 示例）。
5. SSH 侧建议禁用密码登录、改用密钥。

## 健康检查

脚本会循环探测远端 `http://127.0.0.1:<port>/api/health`，最多约 60 秒。失败时打印远端最近日志（`/var/log/fast-note.log` 或 `journalctl`）后退出。
