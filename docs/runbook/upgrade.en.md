# Upgrade from upstream 3.6.1 to 3.7.0

Release 3.7.0 is independently maintained by isdoge in `isdoge/fast-note-sync-service`. The compatible plugin release is 2.5.0. This guide covers only upgrades from upstream 3.6.1; earlier versions have not been verified.

## Before upgrading

1. Stop client writes and the service so the backup does not keep changing. If existing clients cannot remain paused, pause them only for the backup window before starting 3.7.0.
2. Back up all of `config/` and `storage/`. The former contains configuration, `lastVersion`, and possibly `.server_uuid`; the latter contains the default SQLite databases, note bodies, attachments, indexes, and logs.
3. If the database is outside `storage/`, take a separate consistent database backup. Keep a restorable snapshot of attachments in object storage too.
4. Record the current binary/image version, launch command, port mappings, and mounts.
5. Check secrets, proxy settings, and Git URLs. If you run an existing MySQL / MariaDB instance, take a full database backup before upgrading: on first startup, v3.8.0 converts path, path-hash, and vault-name columns to case-sensitive binary types (`varbinary(1024)` / `varbinary(255)`), which locks tables for a while on large datasets and runs a byte-length precheck beforehand (reporting the exact database, table, column, and row ID and aborting if any path exceeds 1024 UTF-8 bytes or any vault name exceeds 255 bytes).

Preserve the original `security.auth-token-key` and `security.share-token-key` during restoration. Token signatures also include the machine identity, so old tokens may become invalid after moving hosts or recreating the runtime; see the troubleshooting guide.

## Docker / Compose

Change the image to:

```yaml
services:
  fast-note-sync-service:
    image: ghcr.io/isdoge/fast-note-sync-service:3.7.0
```

Keep the original `config` and `storage` mounts, then pull and recreate:

```sh
docker compose pull fast-note-sync-service
docker compose up -d fast-note-sync-service
docker compose logs -f fast-note-sync-service
```

The container working directory in `docker/docker-compose.yaml` is `/fast-note-sync`, with persistent mounts at `/fast-note-sync/config` and `/fast-note-sync/storage`. Do not discard either directory to "reinitialize" an upgrade.

## Binary

1. Download the archive for the target OS/architecture from the 3.7.0 Release in `isdoge/fast-note-sync-service`.
2. Stop the service and back up the old binary.
3. Extract and replace the executable while retaining the working directory, `config/`, and `storage/`. Do not overwrite a locally modified `config/config.yaml`.
4. Start through the original service manager and inspect migration logs.

You can also run migrations explicitly while the service is stopped:

```sh
./fast-note-sync-service upgrade --config config/config.yaml
```

The command is repeatable; migrations already recorded are skipped. A normal `run` startup performs the same upgrade.

## One-command installer

The repository installer reads isdoge Releases:

```sh
curl -fsSL https://raw.githubusercontent.com/isdoge/fast-note-sync-service/3.7.0/scripts/quest_install.sh -o quest_install.sh
sudo sh quest_install.sh install 3.7.0
```

`install` stops the service, downloads the specified release, replaces the binary, installs/updates the manager, and starts the service. `update` always installs the latest version and is unsuitable when 3.7.0 must be pinned. An existing manager can run `sudo fns install 3.7.0`. Existing configuration files at the destination are preserved.

## One-time migrations on first startup

Startup runs automatic schema migration, then migrations newer than `config/lastVersion` and absent from `schema_version`. Upgrading from 3.6.1 adds these three in order:

| Migration version | Action |
| --- | --- |
| `3.6.2` `NoteTagIndexRebuildMigrate` | Traverses non-deleted vaults and rebuilds `note_tag` from each note's `content.txt`. |
| `3.6.3` `NoteSizeBackfillMigrate` | Reads `content.txt` for rows with `note.size = 0` and fills the body byte length; rows without a readable file are skipped. |
| `3.7.0` `TimestampSecondsToMillisMigrate` | Multiplies `ctime`/`mtime` by 1000 in per-user `note`, `file`, and `folder` tables where `0 < value < 100000000000`. |

These are migration identifiers, not releases that must be installed first. First startup can be slower for large datasets. Tag/size entries with unreadable body files may be skipped. If migration fails, retain the data and logs rather than repeatedly deleting and recreating databases.

The same timestamp normalization applies at later write entry points. Rare genuine millisecond values in `0 < v < 1e11` (1970–1973) are also treated as seconds. Server migration does not repair file times already displayed as 1970 on a plugin device; the next file modification restores them. Do not delete files in Obsidian to force a fresh download, because that deletion syncs to other devices.

## Behavior changes

- Update checks, downloads, and container images move to isdoge. Docker Hub, cnb, and fnOS `.fpk` releases are no longer produced.
- Vault-restricted tokens must include a matching `vault`; missing or cross-vault requests are rejected.
- Manual backup requests immediately return "accepted" and run in the background.
- Changing a Git branch or repository rebuilds the workspace and performs a full export on the next run. Remote history is unchanged and the old branch stops advancing.
- Git accepts HTTP(S) only. Private repositories, including Tailscale's `100.64.0.0/10`, are denied by default and require `git.allowed-private-hosts`.
- WebSocket heartbeat adds `app.ws-ping-interval` and `ws-ping-wait`. A completely unresponsive connection now closes after about 100 seconds by default; inbound data frames also refresh liveness.
- `trusted-proxies` trusts loopback only by default. Container gateways and remote proxies must be configured explicitly.
- Expired login tokens are cleaned at startup and daily.
- Bleve full-text search is disabled automatically on 32-bit ARM.
- Write APIs strip leading `/` and reject `..` path segments; MCP adds `.md` when creating or renaming notes without it.
- `security.webgui-login-token-bind-ua` is new; UA binding remains enabled by default.

## Rollback

1. Stop 3.7.0 and retain its logs plus a copy of the current directories.
2. Restore the pre-upgrade `config/`, `storage/`, and external database snapshots.
3. Restore the old binary, or point Docker back to the exact upstream 3.6.1 image previously used and recreate it.
4. Start clients afterward and first verify notes, attachments, and shares in a small vault.

**Do not simply run 3.6.1 against the upgraded data directories**: 3.7.0 writes millisecond timestamps, tag indexes, size backfills, and new schema state, and compatibility with the old release is unverified. Data mixed by continued writes from old and new clients during upgrade or rollback is also unverified. The reliable rollback point is the complete pre-upgrade snapshot.
