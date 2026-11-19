# Reverse proxy and private-network access

The main listener defaults to `:9000`, and the WebSocket endpoint is `/api/user/sync`. A proxy must preserve the Host, scheme, and client-IP forwarding headers; the service accepts them only from trusted proxies.

## Nginx

`GET /api/webgui/events` is a long-lived SSE event stream with a heartbeat every 20 seconds. Disable proxy buffering for this endpoint and set the proxy read timeout above the heartbeat interval. The server ends each connection after 30 minutes; automatic client reconnection is normal.

This adds WebSocket timeouts to the repository example `scripts/https-nginx-example.conf`. Replace certificate paths and upstream addresses for the actual deployment.

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

Using `127.0.0.1` as the upstream does not prove that the application sees a loopback peer: in a container network it usually sees a gateway or proxy-container IP.

## Caddy (not tested)

```caddyfile
notes.example.com {
    reverse_proxy 127.0.0.1:9000 {
        header_up Host {host}
        header_up X-Real-IP {remote_host}
        header_up X-Forwarded-Proto {scheme}
    }
}
```

Caddy's `reverse_proxy` handles WebSocket upgrades without explicit Upgrade/Connection headers. This Caddy configuration has not been tested. The Nginx configuration comes from the repository example; its added timeout values have not been link-tested either.

## trusted-proxies

`server.trusted-proxies: []` trusts only `127.0.0.1` and `::1`, suitable when the proxy actually connects over loopback. For a proxy on another host or in a container, add the proxy IP or smallest CIDR actually observed by the application, for example:

```yaml
server:
  trusted-proxies: ["192.168.1.20"]
```

Trust determines whether the service accepts `X-Forwarded-Host`, `X-Forwarded-Proto`, `X-Forwarded-For`, and `X-Real-IP`. The proxy must overwrite client-supplied forwarding headers rather than pass them through. Incorrect trust settings can cause error 312 or repeated disconnects when tokens bind to IP.

## Independent share port

Without independent listeners, the main port serves the API, WebGUI, and share pages. Setting `server.share-port` moves share pages to that listener. With only a share port configured and no `ext-api-url`, links use the request Host plus that port. When both WebGUI and share listeners are configured, set public URLs:

```yaml
server:
  webgui-port: ":9001"
  share-port: ":9002"
  share-base-url: "https://share.example.com"
  ext-api-url: "https://api.example.com"
```

Create a proxy site for each public name/port and preserve the same trusted forwarding headers. Do not append a specific share path to `share-base-url`.

## Built-in Cloudflare Tunnel

Configuration:

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

At startup, the service:

1. Checks `storage/cloudflared_tunnel/cloudflared-<GOOS>-<GOARCH>` (with `.exe` on Windows).
2. If absent, downloads the platform binary from Cloudflare's official GitHub Releases. On macOS it extracts `cloudflared` from the tgz; on non-Windows systems it sets executable permission.
3. Runs `cloudflared tunnel --no-autoupdate run --token <your-token>` and stops the child process during service shutdown.
4. With `log-enabled: true`, also writes stdout/stderr to `storage/logs/cloudflared_tunnel.log`.

The runtime environment must reach GitHub Releases. If download fails, follow the error message to manually download the matching platform file into that directory. The Tunnel's public hostname and origin remain configured in Cloudflare; this service only launches `cloudflared run`.

For frequent disconnects over a Tunnel, first try `app.ws-ping-wait: 180` and, if needed, increase `app.ws-ping-interval`. If the link continuously loses pongs, this only delays reconnection; the current plugin has no idle application-level heartbeat to eliminate the underlying issue. Disable WebGUI IP binding if changing client IPs cause logouts. If MCP is blocked by localhost DNS-rebinding protection behind a trusted local Tunnel preserving the public Host, `server.mcp-disable-localhost-protection` may be enabled only after ensuring MCP is not exposed to untrusted local clients.

## Tailscale and other private-network access

Clients connecting directly to the service over Tailscale do not require a Tailscale entry in `trusted-proxies`; that setting is only for reverse proxies.

Git synchronization has a separate boundary. By default it rejects loopback, link-local, RFC1918 private, `100.64.0.0/10`, `0.0.0.0/8`, unspecified, and multicast addresses. Tailscale addresses fall in `100.64.0.0/10` and require explicit permission:

```yaml
git:
  allowed-private-hosts:
    - "git.example.internal"
    - "100.64.0.10"
```

A hostname entry permits that name, but all its private DNS results must still be covered by an IP/CIDR entry. With only a CIDR configured, any repository hostname resolving into it is allowed, so grant the smallest necessary range. Actual connection addresses are checked again to prevent DNS rebinding.

Git repositories accept only `http://` and `https://`. `ssh://`, `git://`, `user@host:path`, and local paths are rejected.
