# 对接 Firefly 博客：回环 Git 网关部署与回滚

用途：让容器内的 Fast Note Sync 服务端把笔记推送到 NAS 宿主机上的 bare 仓库 `/opt/git/blog.git`，由宿主机原生触发 `post-receive` 构建链，最终发布到 Astro / Firefly 博客。网关只监听回环地址，不改变现有 `post-receive`、`settings-publish-nas.sh` 与 `settings-sync-nas.py` 的保护逻辑。

本文只写路径、步骤与校验方法，不记录任何密码、令牌或私钥内容；令牌文件内容不得粘贴到工单、日志、聊天或 Git 仓库。

## 1. 架构与前置条件

数据流：

```text
Obsidian 插件 / WebGUI
        │  WebSocket 同步、REST POST /api/git-sync/trigger
        ▼
fast-note-sync-service 容器（network_mode: host）
        │  git push（HTTP BasicAuth）
        ▼
127.0.0.1:4323  firefly-git-http.service
        │  git-http-backend → git-receive-pack
        ▼
/opt/git/blog.git  ── post-receive ──► /opt/firefly/settings-publish-nas.sh
                                             └─ 校验设置、三方合并、构建、原子切换
```

宿主机前置条件（只读核对项）：

- `/opt/git/blog.git` 已存在，默认分支为 `master`，并配置了 `receive.denynonfastforwards=true`。
- `/usr/lib/git-core/git-http-backend` 可执行（或 `git-http-backend` 在 `PATH` 中）。
- `/usr/bin/python3` 可用；网关只依赖 Python 3 标准库。
- `/opt/firefly/settings-publish-nas.sh` 与 `/opt/firefly/settings-sync-nas.py` 保持原样，本方案不改动它们。
- FNS 容器为 `network_mode: host`，因此容器内可直接访问宿主机 `127.0.0.1` 的端口。

不要做的事：

- 不要把 `/opt/git/blog.git` 或 `/opt/firefly` 挂载进容器后使用 `file://`：`post-receive` 依赖宿主机的 `systemctl`、`runuser`、`/opt/node-v24` 与 `firefly-settings` 用户，在容器内执行会失败。
- 不要把网关绑定到 `0.0.0.0`、局域网地址或公网地址，也不要使用 `--allow-non-loopback`。
- 不要用强制推送；`blog.git` 只接受快进更新。

## 2. 部署网关

以下命令在 NAS 宿主机以 root 执行。`<时间戳>` 用 `date +%Y%m%d-%H%M%S` 的实际值替换。

### 2.1 备份

```sh
ts=$(date +%Y%m%d-%H%M%S)
mkdir -p "/vol1/1000/docker/backup/fns-firefly-git-http-pre-$ts"
cp -a /vol1/1000/docker/config/config.yaml \
      "/vol1/1000/docker/backup/fns-firefly-git-http-pre-$ts/config.yaml"
sha256sum /opt/git/blog.git/hooks/post-receive \
      > "/vol1/1000/docker/backup/fns-firefly-git-http-pre-$ts/post-receive.sha256"
```

如果机器上已经存在旧版网关文件，再分别备份 `/opt/firefly/firefly-git-http.py` 与 `/etc/systemd/system/firefly-git-http.service`。令牌文件不需要复制到备份目录；如需保留，必须连权限一起保留并确保 `0600`。

### 2.2 安装网关脚本

从服务端仓库复制 `scripts/firefly-git-http.py` 到宿主机：

```sh
install -m 0755 scripts/firefly-git-http.py /opt/firefly/firefly-git-http.py
```

脚本特性（与本方案一致）：

- 仅监听回环地址；非回环绑定必须显式加 `--allow-non-loopback`，部署时不要加。
- HTTP BasicAuth 使用常量时间比较。
- `--repo` 白名单限定可访问的 bare 仓库；未列入白名单的路径返回 404。
- 支持 `Content-Length` 与 `Transfer-Encoding: chunked` 两种请求体（go-git 的推送默认用 chunked）；请求体上限 100 MiB，单个请求 60 秒超时。
- 解码后的子路径含 NUL、反斜杠或 `..` 段时返回 400，不交给 `git-http-backend`。
- 原样转发请求与响应，`post-receive` 的 sideband 输出能回到 FNS 的推送进度里。
- 可选 `POST /__republish`，用于在 NAS 设置中心确认重建后手动重试一次构建。

### 2.3 创建令牌文件

令牌文件格式为一行 `用户名:密码`，权限必须是 `0600`；脚本会拒绝组可读或全局可读的文件。

```sh
umask 077
printf 'fns:%s\n' "$(openssl rand -hex 32)" > /opt/firefly/.git-http-token
chmod 600 /opt/firefly/.git-http-token
```

用户名可自行替换，但必须与 FNS 里配置仓库地址时使用的用户名一致。不要把该文件内容打印到终端历史、日志或文档；FNS 的 Git 同步配置里填写同一组凭据。

### 2.4 创建 systemd 单元

```sh
cat > /etc/systemd/system/firefly-git-http.service <<'EOF'
[Unit]
Description=Firefly loopback git-http-backend gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/bin/python3 /opt/firefly/firefly-git-http.py --bind 127.0.0.1 --port 4323 --root /opt/git --repo blog.git --token-file /opt/firefly/.git-http-token --republish-script /opt/firefly/settings-publish-nas.sh
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
chmod 0644 /etc/systemd/system/firefly-git-http.service
```

说明：

- `--root /opt/git` 与 `--repo blog.git` 对应仓库地址 `http://127.0.0.1:4323/blog.git`。
- 网关以 root 运行，`git-receive-pack` 才能在宿主机上下文触发原有 `post-receive`；`/__republish` 也按同样身份执行发布脚本。
- 如需额外仓库，重复传入 `--repo <名称>.git`；不要放宽 `--root` 之外的路径。

### 2.5 启动并校验

```sh
systemctl daemon-reload
systemctl enable --now firefly-git-http.service
systemctl is-active firefly-git-http.service
ss -ltnp | grep ':4323'
journalctl -u firefly-git-http.service -n 30 --no-pager
```

预期：服务为 `active`，只监听 `127.0.0.1:4323`。

未认证请求应返回 401：

```sh
curl -sS -o /dev/null -w '%{http_code}\n' \
  'http://127.0.0.1:4323/blog.git/info/refs?service=git-upload-pack'
```

带凭据验证仓库可读时，使用交互式 `git ls-remote`，让 Git 自行提示输入凭据，不要把密码写进命令行：

```sh
git ls-remote http://127.0.0.1:4323/blog.git
```

输入令牌文件里的用户名与密码后，应能看到 `master` 的引用列表。

## 3. 允许 FNS 访问回环地址

编辑 `/vol1/1000/docker/config/config.yaml`，在 `git:` 段落下增加回环白名单（保留原有 `name`、`email` 等字段）：

```yaml
git:
    name: FNS Service
    email: <原有值保持不变>
    allowed-private-hosts:
        - 127.0.0.1
```

只放行 `127.0.0.1`，不要添加整个私网网段。修改后重启容器使配置生效：

```sh
docker restart fast-note-sync-service
```

校验：

```sh
curl -s http://127.0.0.1:9000/api/version
docker logs --since 5m fast-note-sync-service 2>&1 | grep -i "git\|error" | tail -30
```

预期：容器 healthy，日志中没有 `private network access not allowed` 或 `git-http-backend` 相关错误。

## 4. 在 WebGUI 新建博客同步配置

在 FNS WebGUI 的 Git 自动化页面新增一条配置，保留原有整库备份配置不动：

| 字段 | 建议值 | 说明 |
| --- | --- | --- |
| `RepoURL` | `http://127.0.0.1:4323/blog.git` | 回环网关地址 |
| `Branch` | `master` | 与 bare 仓库一致 |
| `SourceDir` | `Blog`（或博客库中的 `src/content/posts`） | 留空表示导出整个库；填写后只导出该目录并剥离前缀 |
| `TargetSubdir` | `src/content/posts` | 导出严格限制在该仓库子目录内 |
| `RequireFrontmatter` | `type=share` | 只导出带该标记的笔记；取消标记后自动从仓库撤下 |
| `ValidateFrontmatter` | 开启 | 校验 `title`、`published`/`updated` 合法日期与本地相对图片存在 |
| `BundleAssets` | 开启 | 把库内图片汇聚到目标文章同目录 `./assets/` 并改写导出副本 |
| `Delay` | `60` | 连续编辑合并为一次提交与一次构建 |
| `WebhookURL` | `http://127.0.0.1:4323/__republish` | 手动触发时额外让远端重试一次构建；后台无变更的定时同步不会打它 |

这些字段全部默认关闭；不填时行为与原有整库镜像一致。配置表单里的「一键填入 Firefly 博客预设」可以填入上述推荐值。

插件侧可以使用「博客：立即同步并发布到 NAS Firefly 博客」命令，或调用 `POST /api/git-sync/trigger` 立即触发；查询状态使用 `GET /api/git-sync/status`。这两个接口只返回同步状态，不返回仓库地址、账号、密码或 Webhook 地址。

## 5. 发布验证

一次成功发布的链路：

1. 笔记变更后，FNS 按 `Delay` 合并并推送。
2. 网关接收推送，`git-receive-pack` 在宿主机触发 `/opt/git/blog.git/hooks/post-receive`。
3. `post-receive` 快进 `/opt/firefly/publish-source`，调用 `settings-publish-nas.sh`。
4. 构建成功后 `site/current` 原子切换，钩子输出 `Preview deployed:` 或 `Unified preview deployed:`。
5. FNS 的同步状态显示成功，并展示上述部署回执。

核验命令：

```sh
# 网关访问日志与错误
journalctl -u firefly-git-http.service -n 80 --no-pager

# 最近一次推送构建日志
ls -lt /opt/firefly/logs/push-*.log | head -3

# bare 仓库当前 head
git --git-dir=/opt/git/blog.git log -1 --oneline master
```

若 FNS 显示 `Preview deployment failed` 或 `Saved settings await confirmation`，本轮同步会被标记为失败，不会假装部署成功；按第 7 节排查。

## 6. 备份要点

- 每次改动前备份 `/vol1/1000/docker/config/config.yaml`。
- 记录 `/opt/git/blog.git/hooks/post-receive` 的 SHA-256，确认部署前后未变化；本方案不需要修改该钩子。
- 网关脚本与 systemd 单元改动前各自留一份带时间戳的副本。
- 令牌文件只保留一份 `0600` 原件；需要重建时直接重新生成，并同步更新 FNS 里的 Git 凭据。
- 本方案不改变 `/opt/git/blog.git` 的内容与 `/opt/firefly` 的发布逻辑，回滚网关不需要还原博客仓库或站点数据。

## 7. 排错

| 现象 | 可能原因 | 处理 |
| --- | --- | --- |
| 网关启动失败，日志提示 `permissions must be 0600` | 令牌文件权限过宽 | `chmod 600 /opt/firefly/.git-http-token` 后重启 |
| 未认证请求返回 401，带凭据仍 401 | 用户名或密码与令牌文件不一致 | 核对 FNS 里配置的凭据；必要时重新生成令牌并重启网关 |
| 返回 404 `repository not found` | `--repo` 名称与 URL 不一致，或仓库不在 `--root` 下 | 确认 `--root /opt/git --repo blog.git` 与地址 `/blog.git` 对应 |
| 返回 400 `invalid repository sub-path` | 请求子路径含 NUL、反斜杠或 `..` | 检查客户端地址，不要构造越界路径 |
| 返回 413 `request body too large` | 单次推送超过 100 MiB | 减小单次推送体积（提高 `Delay`、分批提交），或按需在脚本中调大上限 |
| 推送时报 `invalid content length` | 客户端 Content-Length 非法 | 检查中间代理是否改写请求头 |
| FNS 报 `private network access not allowed` | `allowed-private-hosts` 未加 `127.0.0.1` 或容器未重启 | 改配置并 `docker restart fast-note-sync-service` |
| FNS 报 `git-http-backend not found` | 宿主机未安装 git 或路径不在默认位置 | 安装 git，或用 `--git-http-backend` 指定实际路径 |
| 推送成功但同步标记失败，消息含 `Saved settings await confirmation` | NAS 设置中心有已保存但未确认重建的配置 | 先在 `/settings/` 确认重建，再对 `POST /__republish` 发起一次带 BasicAuth 的请求，或等下一次手动触发 |
| 消息含 `Preview deployment failed` | 构建失败 | 查看最新的 `/opt/firefly/logs/push-*.log`，修复后重试 |
| `POST /__republish` 返回 409 | 已有一次重试在运行 | 等待当前构建结束 |
| 端口被占用 | 已有进程监听 4323 | `ss -ltnp \| grep ':4323'` 定位；不要改用非回环地址 |
| 推送被拒绝 `non-fast-forward` | 远端 `master` 已有新提交 | 保持现有的非快进自动对齐逻辑，不要 `--force`；确认没有其它写入方 |

## 8. 一键停用与回滚

停用只影响网关与 FNS 的这条博客同步配置，不会删除 `blog.git`、已发布站点或 Firefly 的任何文件。

```sh
# 1) 停止并禁用网关
systemctl disable --now firefly-git-http.service

# 2) 恢复 FNS 配置（去掉 allowed-private-hosts 中的 127.0.0.1）
cp /vol1/1000/docker/backup/fns-firefly-git-http-pre-<时间戳>/config.yaml \
   /vol1/1000/docker/config/config.yaml
docker restart fast-note-sync-service

# 3) 在 WebGUI 里停用或删除这条博客 Git 同步配置
```

彻底移除时再执行：

```sh
rm -f /etc/systemd/system/firefly-git-http.service
systemctl daemon-reload
rm -f /opt/firefly/firefly-git-http.py /opt/firefly/.git-http-token
```

回滚后校验：

```sh
systemctl is-enabled firefly-git-http.service   # 应为 disabled / not-found
ss -ltnp | grep ':4323'                          # 应无输出
curl -s http://127.0.0.1:9000/api/version        # 服务端仍正常
```

原有整库 Git 备份配置与其它同步功能不受影响。
