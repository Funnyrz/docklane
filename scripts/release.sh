#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/.."
release_dir=${RELEASE_OUTPUT_DIR:-bin/release}
mkdir -p "$release_dir"
release_dir=$(CDPATH='' cd -- "$release_dir" && pwd)
package_dir=$(mktemp -d)
trap 'rm -rf "$package_dir"' EXIT HUP INT TERM
version=v0.3.0
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags='-s -w' -o "$release_dir/docklane-linux-$arch" ./cmd/server
  cp "$release_dir/docklane-linux-$arch" "$package_dir/docklane"
  COPYFILE_DISABLE=1 tar -czf "$release_dir/docklane-$version-linux-$arch.tar.gz" -C "$package_dir" docklane
done
cd "$release_dir"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum docklane-linux-amd64 docklane-linux-arm64 > SHA256SUMS
  for arch in amd64 arm64; do
    sha256sum "docklane-$version-linux-$arch.tar.gz" > "docklane-$version-linux-$arch.tar.gz.sha256"
  done
else
  shasum -a 256 docklane-linux-amd64 docklane-linux-arm64 > SHA256SUMS
  for arch in amd64 arm64; do
    shasum -a 256 "docklane-$version-linux-$arch.tar.gz" > "docklane-$version-linux-$arch.tar.gz.sha256"
  done
fi
