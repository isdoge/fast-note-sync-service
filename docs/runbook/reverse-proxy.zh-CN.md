# 反向代理与内网访问

主端口默认是 `:9000`，WebSocket 路径是 `/api/user/sync`。代理必须保留 Host、协议和客户端 IP 转发头；服务端只从可信代理接受这些头。

## Nginx

以下配置在仓库示例 `scripts/https-nginx-example.conf` 基础上补了 WebSocket 超时。证书路径与上游地址按实际部署替换。

```nginx
server {
    listen 443 ssl;
    server_name notes.example.com;

    ssl_certificate /etc/nginx/cert.pem;
    ssl_certificate_key /etc/nginx/privkey.pem;

    client_max_body_size 50m;
    proxy_max_temp_file_size 0;
    proxy_buffering off;

    location /api/user/sync {
        proxy_pass http://127.0.0.1:9000;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }

    location / {
        proxy_pass http://127.0.0.1:9000;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

不要把上游写死为 `127.0.0.1` 后又假定应用看到回环地址：容器网络中应用看到的通常是网关或代理容器 IP。

## Caddy（未实测）

```caddyfile
notes.example.com {
    reverse_proxy 127.0.0.1:9000 {
        header_up Host {host}
        header_up X-Real-IP {remote_host}
        header_up X-Forwarded-Proto {scheme}
    }
}
```

Caddy 的 `reverse_proxy` 会处理 WebSocket 升级，无需另写 Upgrade/Connection 头。以上 Caddy 配置未实测；Nginx 配置来自仓库已有示例，额外超时值也未做链路实测。

## trusted-proxies

`server.trusted-proxies: []` 只信任 `127.0.0.1` 和 `::1`，适用于代理确实通过回环地址连接服务的情况。跨主机或容器代理应填写应用日志/连接中实际看到的代理 IP 或最小 CIDR，例如：

```yaml
server:
  trusted-proxies: ["192.168.1.20"]
```

可信代理会决定服务端是否采用 `X-Forwarded-Host`、`X-Forwarded-Proto`、`X-Forwarded-For` 和 `X-Real-IP`。代理必须覆盖客户端自行提交的这些头，不能原样透传。令牌绑定 IP 时，错误的可信代理配置会表现为 312 或反复掉线。

## 独立分享端口

主端口未配置独立端口时同时提供 API、WebGUI 和分享页面。设置 `server.share-port` 后，分享页面移到该端口；只配置分享端口且未配置 `ext-api-url` 时，链接会使用请求 Host 加分享端口。同时设置 `webgui-port` 和 `share-port` 时，配置公开地址：

```yaml
server:
  webgui-port: ":9001"
  share-port: ":9002"
  share-base-url: "https://share.example.com"
  ext-api-url: "https://api.example.com"
```

使用独立端口时，为对应域名/端口各建代理站点，并同样传递可信转发头。`share-base-url` 不应包含具体分享路径。

## 内置 Cloudflare Tunnel

配置：

```yaml
cloudflare:
  enabled: true
  token: "<your-token>"
  log-enabled: false
app:
  ws-ping-wait: 180
security:
  webgui-login-token-bind-ip: false
```

启动时服务端会：

1. 检查 `storage/cloudflared_tunnel/cloudflared-<GOOS>-<GOARCH>`（Windows 带 `.exe`）。
2. 文件不存在时，从 Cloudflare 官方 GitHub Releases 下载对应平台二进制；macOS 下载并解出 tgz 中的 `cloudflared`，非 Windows 平台赋予执行权限。
3. 执行 `cloudflared tunnel --no-autoupdate run --token <your-token>`。服务停止时终止子进程。
4. `log-enabled: true` 时，除标准输出/错误外写入 `storage/logs/cloudflared_tunnel.log`。

运行环境需要能访问 GitHub Releases；下载失败时按错误提示手动下载同平台文件并放到上述目录。 Tunnel 的外部入口、公共 Hostname 和回源地址仍在 Cloudflare 侧配置，本服务只负责启动 `cloudflared run`。

通过 Tunnel 后频繁断线可先把 `app.ws-ping-wait` 调到 `180`，必要时调大 `app.ws-ping-interval`。如果链路持续丢 pong，这只会延后重连；当前版本没有插件空闲应用层心跳来彻底消除该问题。WebGUI 因客户端 IP 变化掉线时可关闭 IP 绑定。可信本机 Tunnel 回源且保留公网 Host 时，如 MCP 被 localhost DNS 重绑定保护拦截，可在确认 MCP 未暴露给不可信本地客户端后开启 `server.mcp-disable-localhost-protection`。

## Tailscale 与其他内网访问

客户端通过 Tailscale 直接访问服务时，无需为 Tailscale 网段配置 `trusted-proxies`；只有反向代理才需要该配置。

Git 同步仓库是另一项安全边界：默认拒绝回环、链路本地、RFC1918 私网、`100.64.0.0/10`、`0.0.0.0/8`、未指定和多播地址。Tailscale 地址属于 `100.64.0.0/10`，需要显式放行：

```yaml
git:
  allowed-private-hosts:
    - "git.example.internal"
    - "100.64.0.10"
```

主机名条目只负责允许该名称；其 DNS 结果仍须全部被某个 IP/CIDR 条目覆盖。只写 CIDR 时，任何解析到该网段的仓库主机名都可访问，应按最小网段授权。实际连接地址也会重新校验，以防 DNS rebinding。

Git 仓库只接受 `http://` 和 `https://`。`ssh://`、`git://`、`user@host:path`、本地路径均被拒绝。
