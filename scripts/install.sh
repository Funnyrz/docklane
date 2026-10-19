#!/usr/bin/env sh
set -eu
# Offline installation requires Python 3 and release-matched installation materials.
[ "$(id -u)" -eq 0 ] || { echo 'Run as root to install the systemd service.' >&2; exit 1; }
[ "$#" -eq 1 ] || { echo 'Usage: scripts/install.sh /path/to/verified-linux-binary' >&2; exit 1; }
[ -f "$1" ] && [ -r "$1" ] || { echo 'Verified binary must be an existing readable file.' >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo 'Python 3 is required for safe installation.' >&2; exit 1; }
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
binary=$(python3 -c 'import os,sys; print(os.path.abspath(sys.argv[1]))' "$1")
checksum=$(python3 -c 'import hashlib,sys; print(hashlib.sha256(open(sys.argv[1], "rb").read()).hexdigest())' "$binary")
version=$(python3 "$script_dir/install-support.py" version --source-dir "$script_dir/..")
exec python3 "$script_dir/install-support.py" deploy --binary "$binary" --source-dir "$script_dir/.." --version "$version" --sha256 "$checksum" --explicit-version
