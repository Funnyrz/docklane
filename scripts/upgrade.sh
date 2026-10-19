#!/usr/bin/env sh
set -eu
# The common lifecycle helper backs up the effective data_dir, not a fixed path.
[ "$(id -u)" -eq 0 ] || { echo 'Run as root.' >&2; exit 1; }
[ "$#" -eq 1 ] || { echo 'Usage: scripts/upgrade.sh /path/to/verified-linux-binary' >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo 'Python 3 is required for safe installation.' >&2; exit 1; }
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
python3 "$script_dir/install-support.py" check-legacy
[ -f /etc/systemd/system/docklane.service ] || { echo 'No managed Docklane service found; use install.sh for a new installation.' >&2; exit 1; }
exec "$script_dir/install.sh" "$1"
