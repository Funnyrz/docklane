#!/usr/bin/env sh
# Real-engine integration checks. Uses only unique, labelled fixtures and temp state.
set -eu
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH='' cd -- "$script_dir/.." && pwd)
if ! command -v python3 >/dev/null 2>&1; then
  echo 'ERROR: python3 is required for the integration harness' >&2
  exit 1
fi
binary=${1:-${DM_TEST_BINARY:-}}
build_dir=''
cleanup_build() { if [ -n "$build_dir" ]; then rm -rf -- "$build_dir"; fi; }
trap cleanup_build EXIT HUP INT TERM
if [ -z "$binary" ]; then
  if [ ! -f "$repo/web/dist/index.html" ]; then
    echo 'ERROR: frontend assets are missing; run pnpm --dir web install --frozen-lockfile and pnpm --dir web build first' >&2
    exit 1
  fi
  go_bin=${DM_TEST_GO:-go}
  if ! command -v "$go_bin" >/dev/null 2>&1; then
    echo 'ERROR: Go is required when no compiled binary is provided' >&2
    exit 1
  fi
  build_dir=$(mktemp -d "${TMPDIR:-/tmp}/dm-e2e-build.XXXXXX")
  binary="$build_dir/docker-manager"
  (cd "$repo" && "$go_bin" build -trimpath -o "$binary" ./cmd/server)
fi
if [ ! -x "$binary" ]; then
  echo 'ERROR: integration server binary must exist and be executable' >&2
  exit 1
fi
python3 "$script_dir/integration.py" --binary "$binary"
