#!/usr/bin/env sh
set -eu
[ "$(id -u)" -eq 0 ] || { echo 'Run as root.' >&2; exit 1; }
systemctl disable --now docker-manager || true
rm -f /etc/systemd/system/docker-manager.service /usr/local/bin/docklane /usr/local/bin/docker-manager
systemctl daemon-reload
echo 'Service removed. Configuration, application data, account and backup repositories were preserved.'
