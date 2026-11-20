# Publishing to a Firefly Blog: Loopback Git Gateway Deployment and Rollback

Purpose: let the containerised Fast Note Sync server push notes into a bare repository on the NAS host (`/opt/git/blog.git`), so the host runs its native `post-receive` build chain and publishes to an Astro / Firefly blog. The gateway binds to loopback only and leaves the existing `post-receive`, `settings-publish-nas.sh` and `settings-sync-nas.py` protection logic untouched.

This guide only documents paths, steps and verification. It never records passwords, tokens or private keys; token file contents must not be pasted into tickets, logs, chat or Git repositories.

## 1. Architecture and prerequisites

Data flow:

```text
Obsidian plugin / WebGUI
        │  WebSocket sync, REST POST /api/git-sync/trigger
        ▼
fast-note-sync-service container (network_mode: host)
        │  git push (HTTP BasicAuth)
        ▼
127.0.0.1:4323  firefly-git-http.service
        │  git-http-backend → git-receive-pack
        ▼
/opt/git/blog.git  ── post-receive ──► /opt/firefly/settings-publish-nas.sh
                                             └─ validate settings, 3-way merge, build, atomic switch
```

Host prerequisites (read-only checks):

- `/opt/git/blog.git` exists, its default branch is `master`, and `receive.denynonfastforwards=true` is set.
- `/usr/lib/git-core/git-http-backend` is executable (or `git-http-backend` is on `PATH`).
- `/usr/bin/python3` is available; the gateway only needs the Python 3 standard library.
- `/opt/firefly/settings-publish-nas.sh` and `/opt/firefly/settings-sync-nas.py` stay as they are; this scheme does not modify them.
- The FNS container uses `network_mode: host`, so it can reach the host's `127.0.0.1` ports directly.

Do not:

- Mount `/opt/git/blog.git` or `/opt/firefly` into the container and use `file://`: `post-receive` depends on the host's `systemctl`, `runuser`, `/opt/node-v24` and the `firefly-settings` user, which fail inside the container.
- Bind the gateway to `0.0.0.0`, a LAN address or a public address, and never pass `--allow-non-loopback`.
- Force-push; `blog.git` only accepts fast-forward updates.

## 2. Deploy the gateway

Run the following as root on the NAS host. Replace `<timestamp>` with the actual `date +%Y%m%d-%H%M%S` value.

### 2.1 Back up

```sh
ts=$(date +%Y%m%d-%H%M%S)
mkdir -p "/vol1/1000/docker/backup/fns-firefly-git-http-pre-$ts"
cp -a /vol1/1000/docker/config/config.yaml \
      "/vol1/1000/docker/backup/fns-firefly-git-http-pre-$ts/config.yaml"
sha256sum /opt/git/blog.git/hooks/post-receive \
      > "/vol1/1000/docker/backup/fns-firefly-git-http-pre-$ts/post-receive.sha256"
```

If an older gateway file already exists, also back up `/opt/firefly/firefly-git-http.py` and `/etc/systemd/system/firefly-git-http.service`. The token file does not need copying into the backup directory; if you keep a copy, preserve its permissions and keep it `0600`.

### 2.2 Install the gateway script

Copy `scripts/firefly-git-http.py` from the server repository to the host:

```sh
install -m 0755 scripts/firefly-git-http.py /opt/firefly/firefly-git-http.py
```

Script properties (matching this scheme):

- Binds to loopback only; a non-loopback bind requires an explicit `--allow-non-loopback`, which deployment must not use.
- HTTP BasicAuth is checked in constant time.
- The `--repo` allowlist restricts which bare repositories are reachable; anything else returns 404.
- Accepts both `Content-Length` and `Transfer-Encoding: chunked` request bodies (go-git pushes use chunked by default); the body is capped at 100 MiB and each request times out after 60 seconds.
- Decoded sub-paths containing NUL, backslashes or `..` segments return 400 and are never handed to `git-http-backend`.
- Streams requests and responses verbatim, so `post-receive` sideband output reaches FNS push progress.
- Optional `POST /__republish` retries one build after confirming a rebuild in the NAS settings centre.

### 2.3 Create the token file

The token file holds one line, `user:password`, and must be `0600`; the script refuses group- or world-readable files.

```sh
umask 077
printf 'fns:%s\n' "$(openssl rand -hex 32)" > /opt/firefly/.git-http-token
chmod 600 /opt/firefly/.git-http-token
```

You may change the user name, but it must match the credentials configured for the repository URL in FNS. Never print the file contents into shell history, logs or documentation; use the same credentials in the FNS Git sync configuration.

### 2.4 Create the systemd unit

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

Notes:

- `--root /opt/git` plus `--repo blog.git` map to the URL `http://127.0.0.1:4323/blog.git`.
- The gateway runs as root so `git-receive-pack` can trigger the existing `post-receive` in the host context; `/__republish` runs the publish script under the same identity.
- For additional repositories, pass `--repo <name>.git` repeatedly; never widen the path beyond `--root`.

### 2.5 Start and verify

```sh
systemctl daemon-reload
systemctl enable --now firefly-git-http.service
systemctl is-active firefly-git-http.service
ss -ltnp | grep ':4323'
journalctl -u firefly-git-http.service -n 30 --no-pager
```

Expected: the service is `active` and only listens on `127.0.0.1:4323`.

An unauthenticated request must return 401:

```sh
curl -sS -o /dev/null -w '%{http_code}\n' \
  'http://127.0.0.1:4323/blog.git/info/refs?service=git-upload-pack'
```

To verify the repository is readable with credentials, use an interactive `git ls-remote` and let Git prompt for credentials; never put the password on the command line:

```sh
git ls-remote http://127.0.0.1:4323/blog.git
```

After entering the user name and password from the token file, you should see the `master` refs.

## 3. Allow FNS to reach the loopback address

Edit `/vol1/1000/docker/config/config.yaml` and add a loopback allowlist entry under the `git:` section (keep the existing `name`, `email` and other fields):

```yaml
git:
    name: FNS Service
    email: <keep the existing value>
    allowed-private-hosts:
        - 127.0.0.1
```

Allow only `127.0.0.1`; do not add whole private ranges. Restart the container for the change to take effect:

```sh
docker restart fast-note-sync-service
```

Verify:

```sh
curl -s http://127.0.0.1:9000/api/version
docker logs --since 5m fast-note-sync-service 2>&1 | grep -i "git\|error" | tail -30
```

Expected: the container is healthy and the logs contain no `private network access not allowed` or `git-http-backend` errors.

## 4. Create the blog sync config in the WebGUI

On the Git automation page in the FNS WebGUI, add one config and leave any existing whole-vault backup config alone:

| Field | Suggested value | Notes |
| --- | --- | --- |
| `RepoURL` | `http://127.0.0.1:4323/blog.git` | Loopback gateway address |
| `Branch` | `master` | Matches the bare repository |
| `SourceDir` | `Blog` (or `src/content/posts` in a blog vault) | Empty exports the whole vault; otherwise only that directory is exported with the prefix stripped |
| `TargetSubdir` | `src/content/posts` | Export is strictly confined to this repository subdirectory |
| `RequireFrontmatter` | `type=share` | Only notes carrying the marker are exported; removing the marker withdraws the file |
| `ValidateFrontmatter` | on | Validates `title`, legal `published`/`updated` dates and that local relative images exist |
| `BundleAssets` | on | Copies vault images into `./assets/` next to the target post and rewrites the exported copy |
| `Delay` | `60` | Merges consecutive edits into one commit and one build |
| `WebhookURL` | `http://127.0.0.1:4323/__republish` | A manual trigger also asks the remote to retry a build; scheduled runs with no changes never fire it |

All of these fields default to off; leaving them empty keeps the previous whole-vault mirror behaviour. The "fill Firefly blog preset" button in the config form fills in the recommended values above.

From the plugin you can run the "blog: sync and publish to the NAS Firefly blog now" command, or call `POST /api/git-sync/trigger` to trigger immediately; query status with `GET /api/git-sync/status`. These two endpoints only return sync status, never the repository URL, credentials or webhook address.

## 5. Verify a publish

A successful publish chain:

1. After a note change, FNS merges per `Delay` and pushes.
2. The gateway receives the push and `git-receive-pack` triggers `/opt/git/blog.git/hooks/post-receive` on the host.
3. `post-receive` fast-forwards `/opt/firefly/publish-source` and calls `settings-publish-nas.sh`.
4. On a successful build, `site/current` switches atomically and the hook prints `Preview deployed:` or `Unified preview deployed:`.
5. The FNS sync status shows success and surfaces that deploy receipt.

Verification commands:

```sh
# Gateway access log and errors
journalctl -u firefly-git-http.service -n 80 --no-pager

# Most recent push build log
ls -lt /opt/firefly/logs/push-*.log | head -3

# Current head of the bare repository
git --git-dir=/opt/git/blog.git log -1 --oneline master
```

If FNS shows `Preview deployment failed` or `Saved settings await confirmation`, that run is marked failed instead of pretending the deploy succeeded; troubleshoot using section 7.

## 6. Backup notes

- Back up `/vol1/1000/docker/config/config.yaml` before every change.
- Record the SHA-256 of `/opt/git/blog.git/hooks/post-receive` and confirm it is unchanged across the deployment; this scheme does not modify that hook.
- Keep a timestamped copy of the gateway script and the systemd unit before each change.
- Keep exactly one `0600` token file; to rotate it, regenerate and update the Git credentials in FNS.
- This scheme does not change the contents of `/opt/git/blog.git` or the publish logic in `/opt/firefly`, so rolling back the gateway never requires restoring the blog repository or site data.

## 7. Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| Gateway fails to start, log says `permissions must be 0600` | Token file permissions too broad | `chmod 600 /opt/firefly/.git-http-token` and restart |
| Unauthenticated request returns 401, authenticated still 401 | User or password differs from the token file | Check the credentials in FNS; regenerate the token and restart the gateway if needed |
| 404 `repository not found` | `--repo` name does not match the URL, or the repo is not under `--root` | Confirm `--root /opt/git --repo blog.git` matches the `/blog.git` address |
| 400 `invalid repository sub-path` | Request sub-path contains NUL, a backslash or `..` | Check the client URL; never construct escaping paths |
| 413 `request body too large` | A single push exceeds 100 MiB | Reduce push size (raise `Delay`, split commits), or raise the limit in the script if needed |
| Push fails with `invalid content length` | Client sent an invalid Content-Length | Check whether an intermediate proxy rewrites request headers |
| FNS reports `private network access not allowed` | `allowed-private-hosts` lacks `127.0.0.1`, or the container was not restarted | Update the config and `docker restart fast-note-sync-service` |
| FNS reports `git-http-backend not found` | Git is not installed on the host, or it is not in the default location | Install git, or point `--git-http-backend` at the actual path |
| Push succeeds but the run is marked failed with `Saved settings await confirmation` | The NAS settings centre has saved but unconfirmed changes | Confirm the rebuild in `/settings/`, then send one BasicAuth `POST /__republish` or wait for the next manual trigger |
| Message contains `Preview deployment failed` | The build failed | Inspect the newest `/opt/firefly/logs/push-*.log`, fix, then retry |
| `POST /__republish` returns 409 | A retry is already running | Wait for the current build to finish |
| Port already in use | Another process listens on 4323 | Locate it with `ss -ltnp \| grep ':4323'`; do not switch to a non-loopback address |
| Push rejected as `non-fast-forward` | The remote `master` already has new commits | Keep the existing non-fast-forward realign logic; never `--force`; confirm no other writer |

## 8. Disable and roll back

Disabling only affects the gateway and this one blog sync config in FNS; it never deletes `blog.git`, published sites or any Firefly file.

```sh
# 1) Stop and disable the gateway
systemctl disable --now firefly-git-http.service

# 2) Restore the FNS config (drop 127.0.0.1 from allowed-private-hosts)
cp /vol1/1000/docker/backup/fns-firefly-git-http-pre-<timestamp>/config.yaml \
   /vol1/1000/docker/config/config.yaml
docker restart fast-note-sync-service

# 3) Disable or delete this blog Git sync config in the WebGUI
```

To remove everything, then run:

```sh
rm -f /etc/systemd/system/firefly-git-http.service
systemctl daemon-reload
rm -f /opt/firefly/firefly-git-http.py /opt/firefly/.git-http-token
```

Verify the rollback:

```sh
systemctl is-enabled firefly-git-http.service   # expect disabled / not-found
ss -ltnp | grep ':4323'                          # expect no output
curl -s http://127.0.0.1:9000/api/version        # the server still works
```

The existing whole-vault Git backup config and other sync features are unaffected.
