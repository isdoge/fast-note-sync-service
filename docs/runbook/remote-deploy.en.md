# Remote Deploy (Quick Deploy)

Purpose: This guide explains how to run `scripts/remote_deploy.sh` on your own machine (macOS / Linux) and deploy the Fast Note Sync service to a remote Linux server, providing only the server address and an SSH account. This implements the original author's roadmap item "Quick deploy plan: deploy the FNS server with just a public server address and credentials."

Related scripts:

- `scripts/remote_deploy.sh` — run locally; connects to the remote host and drives the deployment.
- `scripts/quest_install.sh` — uploaded to the remote host and executed non-interactively to perform the actual install.

## Quick Start

```bash
# Key auth (recommended)
./scripts/remote_deploy.sh 203.0.113.10 -u root -i ~/.ssh/id_ed25519

# Password auth: just run it; ssh prompts for the password once for the whole process
./scripts/remote_deploy.sh 203.0.113.10 -u root

# Install an unreleased version from a locally built package
./scripts/remote_deploy.sh 192.168.1.5 --tarball ./fast-note-sync-service-3.7.0-linux-amd64.tar.gz

# Docker deployment
./scripts/remote_deploy.sh example.com --mode docker
```

## Options

| Option | Description | Default |
| --- | --- | --- |
| `<host>` | Server address (required) | — |
| `-u, --user <user>` | SSH user | `root` |
| `-p, --port <port>` | SSH port | `22` |
| `-i, --key <path>` | SSH private key path | ssh default config |
| `-v, --version <ver>` | Version to install | `latest` |
| `--http-port <port>` | Service HTTP port | keep server default `:9000` |
| `--mode <binary\|docker>` | Deployment mode | `binary` |
| `--tarball <path>` | Local tar.gz package (binary mode only) | download from GitHub Release |
| `--force` | Force continue when an install already exists or conflicts with docker mode | off |

## Authentication and Password Safety

- **Key auth is preferred** (`-i` or your local ssh default config); no password involved.
- With password auth, the script delegates authentication entirely to `ssh`'s own interactive prompt: the password **never** appears in command lines, process lists, or any file. SSH connection reuse (ControlMaster) means you type it once for the whole deployment.
- Non-interactive password auth is only used when `sshpass` is installed locally **and** you explicitly provide the password via the `SSHPASS` environment variable. The script never reads or records passwords on its own.
- First connections use `StrictHostKeyChecking=accept-new` to auto-accept new host fingerprints. In stricter environments, verify the fingerprint manually first.

## Deployment Modes

### binary + systemd (default)

Flow: SSH connectivity and privilege check (root or sudo-capable) → probe remote OS and architecture → upload `quest_install.sh` (plus an optional local package) → run `install --yes` non-interactively → register the systemd service and enable auto-start when systemd is present → health check.

- Without systemd (e.g. some container environments), the service runs as a background process with a warning; it will not auto-start after reboot.
- `--http-port` rewrites `http-port` in the remote `config/config.yaml` and restarts the service.

### Docker

Flow: verify `docker` and `docker compose` on the remote host (missing tools abort with an install hint — the script never installs them itself) → write a compose file under `/opt/fast-note-docker` and start → health check.

Compose details: image `ghcr.io/isdoge/fast-note-sync-service`; data and config mounted from `/opt/fast-note/storage` and `/opt/fast-note/config`; `restart: unless-stopped`.

## Behavior When Already Installed

- If `/opt/fast-note/fast-note-sync-service` exists, the script performs an **upgrade** by default: the binary is replaced, while `config/` and `storage/` are preserved (missing config files are added, existing ones never overwritten).
- Selecting `--mode docker` while a binary install exists is treated as a conflict and requires `--force`.
- On any failure the script aborts with a clear error and tells you which steps already completed.

## Post-Deploy Security Checklist

1. Visit `http://<host>:9000` and register the administrator account.
2. After registering, disable open registration in `config/config.yaml` (`register-is-enable: false`) and restart the service.
3. Open only the required TCP port (default 9000) in the server firewall / cloud security group.
4. For production, put the service behind a reverse proxy with HTTPS (see `docs/runbook/` and the bundled nginx example).
5. Prefer SSH keys and disable SSH password logins.

## Health Check

The script polls the remote `http://127.0.0.1:<port>/api/health` for up to ~60 seconds. On failure it prints recent remote logs (`/var/log/fast-note.log` or `journalctl`) and exits.
