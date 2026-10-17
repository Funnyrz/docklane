#!/usr/bin/env sh
set -eu
# Install a locally downloaded binary whose release checksum has already been verified.
[ "$(id -u)" -eq 0 ] || { echo 'Run as root to install the systemd service.' >&2; exit 1; }
[ "$#" -eq 1 ] || { echo 'Usage: scripts/install.sh /path/to/verified-linux-binary' >&2; exit 1; }
[ -f "$1" ] && [ -r "$1" ] || { echo 'Verified binary must be an existing readable file.' >&2; exit 1; }
command -v systemctl >/dev/null
command -v docker >/dev/null
compose_version=$(docker compose version --short)
case "$compose_version" in
  2.*|v2.*) ;;
  *) echo 'Docker Compose v2 is required; install the pinned v2.40.3 CLI plugin before installing Docklane.' >&2; exit 1 ;;
esac
command -v restic >/dev/null
restic_version=$(restic version)
case "$restic_version" in
  'restic 0.19.1 '*) ;;
  *) echo 'Restic 0.19.1 is required; install the pinned release and verify its upstream checksum first.' >&2; exit 1 ;;
esac
docker info >/dev/null
if ! getent group docker-manager >/dev/null; then groupadd --system docker-manager; fi
if ! id docker-manager >/dev/null 2>&1; then useradd --system --gid docker-manager --home-dir /var/lib/docker-manager --shell /usr/sbin/nologin docker-manager; fi
install -d -o docker-manager -g docker-manager -m 0700 /var/lib/docker-manager
install -d -o root -g docker-manager -m 0750 /etc/docker-manager
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
if [ ! -f /etc/docker-manager/config.json ]; then install -o root -g docker-manager -m 0640 "$script_dir/../deployments/config.example.json" /etc/docker-manager/config.json; fi
install -o root -g root -m 0755 "$1" /usr/local/bin/docklane
install -o root -g root -m 0644 "$script_dir/../deployments/docker-manager.service" /etc/systemd/system/docker-manager.service
systemctl daemon-reload
systemctl enable --now docker-manager
echo 'Installed. Review authorized paths in /etc/docker-manager/config.json; access port 8080 through your trusted network or reverse proxy.'
