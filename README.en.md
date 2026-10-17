# Docklane

**Your containers. Under control.**

A single-host Docker management and local backup platform. Manage containers, Compose projects and Docker resources, follow tasks, and preview and verify local backups and restores through a bilingual web console.

The Go service embeds the Vue interface and runs as a Linux binary or Docker container. Docker Engine provides live resource state; SQLite stores management metadata only.

[简体中文](README.md) · [Features](#features) · [Screenshots](#screenshots) · [Deployment](#deployment) · [Development](#development-and-verification) · [Documentation](#documentation) · [MIT License](LICENSE)

![Docklane English promotional image](docs/images/en/promo.png)

> Promotional concept image. Refer to the text and actual screenshots for the current interface and supported features.

## Features

| Area | Available today |
| --- | --- |
| Overview | Local host connection status, resource counts, backup summary and recent tasks |
| Containers | Start, stop, restart, delete, details, logs, terminal, statistics, configuration preflight and controlled rebuilds |
| Images | Filter local lists, inspect, pull, import/export tar archives and delete individual images; encrypted registry credentials and authentication tests |
| Networks and storage | Create, inspect and delete networks and volumes; view container references and mounts; scan authorized directories for capacity |
| Compose | Discover and import existing projects, edit multiple indexed Compose configuration files, validate and deploy with checks; environment files and profiles supported |
| Local backups | Create/import local Restic repositories; file/directory, local-volume, container and Compose backups; Cron schedules with time zones and retention of the latest N snapshots |
| Restore | Snapshot preflight, new-directory restore by default, explicitly confirmed overwrite and controlled service reconstruction |
| Image acceleration | TCP/Registry connectivity diagnostics and explicit image pull tests; preview and apply mirror configuration when authorization requirements are met |
| Operations | Task progress, logs and cancellation, audit records and host diagnostics |
| Bilingual interface | Simplified Chinese `zh-CN` and English `en`, instant switching and a saved browser preference |

Docklane currently manages a single host. See [Current limitations](#current-limitations) for feature boundaries.

## Screenshots

These screenshots use the latest local Docklane frontend connected to the test environment’s real APIs through a read-only proxy; the frontend was not deployed to the test server. Empty lists show actual empty states. Logs, terminal output, raw configuration and audit details are excluded.

![Docklane English overview](docs/images/en/overview.jpg)

<details>
<summary>View containers, Compose, images, backups and settings</summary>

### Containers

![Container management](docs/images/en/containers.jpg)

### Compose

![Compose projects](docs/images/en/compose.jpg)

### Images

![Image management](docs/images/en/images.jpg)

### Backups and restore

![Backups and restore](docs/images/en/backups.jpg)

### Settings

![System settings](docs/images/en/settings.jpg)

</details>

## Current limitations

- Multi-host management, RBAC, remote backup repositories, Host Helper and logical database exports are not yet delivered.
- Standalone container creation, registry search, image builds/tagging/bulk cleanup, network connection management/topology and volume file browsing/editing shown in the promotional image are not implemented. Image search filters the local list; directory scans report capacity rather than providing a file manager.
- Compose editing is limited to indexed configuration files, rather than arbitrary project files or `.env` files. Compose deployments in container mode require identical host/container project paths.
- Backups do not include image archives or provide arbitrary point-in-time recovery or logical database consistency guarantees. Schedules missed while offline are recorded without automatic catch-up. Local-volume backups support only the `local` driver without extra driver options.
- Automatic mirror configuration requires explicitly authorized Linux binary mode with the necessary local Socket, tools and permissions. Docker mode provides manual instructions. Connectivity tests do not establish that a pull used a particular mirror or guarantee faster downloads.

## Supported environments

| Component | Requirements or pinned versions |
| --- | --- |
| Operating system | Linux; installation targets Ubuntu 24.04 and Debian 12/13 |
| Release architectures | `linux/amd64`, `linux/arm64` |
| Docker Engine | 28/29; deploy a version with current security fixes |
| Compose / Restic | Compose v2 (pinned validation version v2.40.3), Restic 0.19.1 |
| Build tools | Go 1.27.2, Node.js 22.22.0, pnpm 10.28.2 |

The Docker image includes Docker CLI, Compose and Restic. Binary installations need these tools installed separately. Cross-compiling an architecture does not establish acceptance on native hardware; see the [verification record](docs/verification.md).

## Deployment

### Product name and upgrade compatibility

**The product is Docklane, and its executable is `docklane`.** For upgrade compatibility, the systemd service and account, image tags, configuration and data directories, Go module, `DM_*` environment variables and browser storage keys retain their existing technical identifiers. Installation/upgrade scripts install the new binary as `docklane` while retaining the `docker-manager` service and existing data.

### Choose a download

As of 2026-10-17, no version has been published on [GitHub Releases](https://github.com/Funnyrz/docklane/releases). The package-based instructions below require a matching release bundle from the maintainer. If you do not have one, use the source build instructions below. The listed filenames are not live download links.

| Download | Use case |
| --- | --- |
| `docklane-v0.3.0-<timestamp>.tar.gz` | Complete bundle: offline Docker images, both binary architectures, installation scripts, deployment examples and bilingual documentation/screenshots; extracts to `docklane-v0.3.0/` |
| `docklane-v0.3.0-linux-amd64.tar.gz` | Slim binary package for x86-64 Linux |
| `docklane-v0.3.0-linux-arm64.tar.gz` | Slim binary package for ARM64 Linux |

Each slim package has a matching `.tar.gz.sha256` sidecar. Newly built packages contain the `docklane` executable and the MIT `LICENSE` file. The frontend is embedded, so no separate frontend installation is needed. Choose the complete bundle for offline images, systemd installation materials or bundled documentation.

### Slim binary package: run directly

Download the package and checksum for your architecture into an empty directory. This example targets amd64; replace `amd64` with `arm64` for an ARM64 host:

```sh
sha256sum -c docklane-v0.3.0-linux-amd64.tar.gz.sha256
tar -xzf docklane-v0.3.0-linux-amd64.tar.gz
DM_DATA_DIR=./data DM_LISTEN=127.0.0.1:8080 ./docklane
```

On macOS, use `shasum -a 256 -c` for verification; the executable still requires Linux. Open `http://127.0.0.1:8080` to initialize the administrator.

Slim packages do not bundle Docker CLI, Compose or Restic. Install Docker Engine and grant the current account access before managing containers. Compose features additionally need Compose v2; local backup/restore needs Restic 0.19.1. Volume backup/restore also needs an available matching worker image; see the configuration notes below.

### Docker: offline installation

Extract the complete release package, enter `docklane-v0.3.0/`, then run the following. This example targets amd64; on arm64, replace `amd64` with `arm64` in the image tag.

```sh
sha256sum -c RELEASE-SHA256SUMS
tar -xzf docker-manager-installation.tar.gz
docker load -i docker-manager-images.tar.gz
docker tag docker-manager:v0.3.0-amd64 docker-manager:v0.3.0
export DM_BACKUP_HOST_DIR=/srv/docker-manager/backups
export DM_RESTORE_HOST_DIR=/srv/docker-manager/restores
mkdir -p "$DM_BACKUP_HOST_DIR" "$DM_RESTORE_HOST_DIR"
docker compose -f deployments/docker-compose.yml up -d --no-build
```

The archive contains both architectures. Tag only the matching architecture with the common tag before startup. Temporary backup/restore containers use this same local image; the worker image is not pulled automatically by default.

### Docker: build from source

Install Git, Docker Engine, Docker Buildx (for BuildKit builds) and Compose v2, then run on a Linux host:

```sh
git clone https://github.com/Funnyrz/docklane.git
cd docklane
docker build -t docker-manager:v0.3.0 .
export DM_BACKUP_HOST_DIR=/srv/docker-manager/backups
export DM_RESTORE_HOST_DIR=/srv/docker-manager/restores
mkdir -p "$DM_BACKUP_HOST_DIR" "$DM_RESTORE_HOST_DIR"
docker compose -f deployments/docker-compose.yml up -d --no-build
```

The example Compose file binds only to `127.0.0.1:8080`. Access it locally, through a restricted network tunnel or an HTTPS reverse proxy. The image needs no `--privileged` flag and does not mount the host root by default. Docker mode cannot write host daemon configuration or restart systemd directly.

### Linux binary and systemd

The complete bundle provides `docklane-linux-amd64` and `docklane-linux-arm64`. Installation places the executable at `/usr/local/bin/docklane`; the service remains `docker-manager`. Existing installations should use the new package’s `scripts/upgrade.sh`, which handles switching the old executable path to `docklane` while retaining configuration and data locations.

Install Docker, Compose v2 and Restic 0.19.1, then run inside the extracted complete package’s `docklane-v0.3.0/` directory. Skip extracting the installation materials if already done. On arm64, change the binary filename suffix:

```sh
sha256sum -c RELEASE-SHA256SUMS
tar -xzf docker-manager-installation.tar.gz
sudo ./scripts/install.sh ./docklane-linux-amd64
sudo journalctl -u docker-manager -f
```

The installer creates a dedicated account and systemd service. Configuration lives at `/etc/docker-manager/config.json`; application data lives at `/var/lib/docker-manager`. Docker group access still grants high privileges. Grant backup-source read access and repository/restore-directory write access separately. Daemon writes and service restarts are not granted by default.

### First visit and language

Open your deployment address and create the administrator with a password of at least 12 bytes. Initialization can succeed only once. Login and diagnostics remain available while Docker is unavailable. Keep the management port behind access controls rather than exposing it directly to the public internet.

Switch languages on the initialization page, login page or console header. The first visit matches the browser's Chinese/English preference and falls back to Simplified Chinese. Manual choices use `localStorage` key `docker-manager:locale`. Switching preserves the page, connections and unsaved forms. Logs, terminal output, resource names and raw diagnostics retain their original content.

## Configuration and data protection

- Load JSON configuration with `DM_CONFIG`; environment variables override supported options. See the [example configuration](deployments/config.example.json) and [operations guide](docs/operations.md).
- `DM_ALLOWED_PATHS` authorizes explicit absolute paths. Container mode also needs host/container mappings in `DM_PATH_MAPPINGS`. The filesystem root `/` cannot be authorized.
- Use a dedicated, writable application data directory for one instance only. Back up its database together with the matching `master.key`. Preserve the Restic repository and password separately offline; recovery exports contain secrets.
- Docker Socket access grants high host privileges. Temporary backup containers mount only selected sources and the repository, without the Socket. Mount directory backup sources separately as read-only, and give repository and restore directories their own write permissions.
- Keep the running image and `DM_BACKUP_WORKER_IMAGE` (`backup_worker_image` in binary configuration) on the same version. Verify a backup and a restore to a new directory after deployment.
- File verification does not establish logical database consistency. Stop-mode backups must cover every known writer. Configuration rollback cannot undo data already written by an application.
- Restore defaults to a new directory. Overwriting the original location requires separate confirmation. A failed or interrupted overwrite may leave writers stopped; verify the data before explicitly starting them.
- Before upgrading, stop the service and retain complete application data, configuration and the previous binary or image. Roll back with a matching database and key; do not open an upgraded database directly with an older application.

## Development and verification

Install the build tools listed above, Git and Make. These steps build and run from source on a Linux host. Docker, Compose and backup features also require their respective runtime tools and access permissions:

```sh
git clone https://github.com/Funnyrz/docklane.git
cd docklane
make setup
make build
DM_DATA_DIR=./data DM_LISTEN=127.0.0.1:8080 ./bin/docklane
```

For frontend development, run `pnpm --dir web dev`. Vite proxies `/api` to local port `:8080`. The interface uses real APIs and provides no simulated business data.

```sh
make check          # Formatting, static checks, Go/frontend tests and race checks
make integration    # Run only on an isolated Linux Docker test host
make release        # Both Linux binary architectures, slim tar.gz packages and checksums
make release-images # Both image architectures and offline archive; requires Docker Buildx
```

Source release commands write their artifacts to `bin/release/` by default; `RELEASE_OUTPUT_DIR` overrides the release script's output directory. To install from source, use `sudo ./scripts/install.sh ./bin/release/docklane-linux-amd64`.

Translations live in `web/src/i18n/`. Add both languages with matching interpolation parameters for new text. `pnpm --dir web test` covers dictionary consistency and language switching.

## Documentation

Detailed guides are currently primarily in Chinese; the resource API and internal implementation contract are in English.

- [Installation, permissions, upgrades and disaster recovery](docs/operations.md)
- [Public API](docs/api.md) · [Resource API](docs/resources-api.md) · [Workflow API](docs/workflows-api.md)
- [Security verification and known limitations](docs/security-notes.md)
- [Release acceptance reproduction](docs/release-acceptance.md) · [Acceptance matrix](docs/acceptance-matrix.md)
- [Verification report](docs/verification.md) · [Development plan](docs/development-plan-v0.3.md)

These records define the scope of testing and static checks. They do not establish a complete penetration test, permission validation across every distribution or acceptance under every failure condition.

## License

Docklane is licensed under the [MIT License](LICENSE), with `Copyright (c) 2026 Funnyrz`. Retain the copyright and permission notices when distributing copies.

Newly built slim binary packages, installation materials and release candidate directories include `LICENSE`. Docker images include it at `/usr/share/licenses/docklane/LICENSE`. Third-party dependencies and tools distributed with the image remain subject to their respective licenses.
