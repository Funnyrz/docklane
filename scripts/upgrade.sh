#!/usr/bin/env sh
set -eu
[ "$(id -u)" -eq 0 ] || { echo 'Run as root.' >&2; exit 1; }
[ "$#" -eq 1 ] || { echo 'Usage: scripts/upgrade.sh /path/to/verified-linux-binary' >&2; exit 1; }
[ -f "$1" ] && [ -r "$1" ] || { echo 'Verified upgrade binary must be an existing readable file.' >&2; exit 1; }
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
unit_source="$script_dir/../deployments/docker-manager.service"
[ -f "$unit_source" ] || { echo 'Upgrade requires the matching deployments/docker-manager.service from this release.' >&2; exit 1; }
[ -f /usr/local/bin/docklane ] || [ -f /usr/local/bin/docker-manager ] || { echo 'No installed Docklane binary found.' >&2; exit 1; }
backup_dir="/var/backups/docker-manager/$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 0700 "$backup_dir"
systemctl stop docker-manager
cp -a /var/lib/docker-manager "$backup_dir/data"
cp -a /etc/docker-manager "$backup_dir/config"
for name in docklane docker-manager; do
  if [ -f "/usr/local/bin/$name" ]; then cp -a "/usr/local/bin/$name" "$backup_dir/$name"; fi
done
cp -a /etc/systemd/system/docker-manager.service "$backup_dir/docker-manager.service"
install -o root -g root -m 0755 "$1" /usr/local/bin/docklane
install -o root -g root -m 0644 "$unit_source" /etc/systemd/system/docker-manager.service
systemctl daemon-reload
if ! systemctl start docker-manager; then
  systemctl stop docker-manager || true
  echo "Upgrade could not start. Consistent backup and old binary: $backup_dir. Restore the matching binary, service unit, config, database and master.key as described in docs/operations.md." >&2
  exit 1
fi
sleep 3
if ! systemctl is-active --quiet docker-manager; then
  echo "Upgrade failed. Service stopped; consistent backup and old binary: $backup_dir. Follow docs/operations.md before restoring." >&2
  systemctl stop docker-manager || true
  exit 1
fi
echo "Upgrade started; inspect journalctl -u docker-manager and /healthz. Backup: $backup_dir"
