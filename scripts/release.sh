#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/.."
release_dir=${RELEASE_BUILD_DIR:-bin/release-internal}
mkdir -p "$release_dir"
release_dir=$(CDPATH='' cd -- "$release_dir" && pwd)
package_dir=$(mktemp -d)
trap 'rm -rf "$package_dir"' EXIT HUP INT TERM
version=$(python3 -c 'import json,re; v=json.load(open("web/package.json"))["version"]; assert re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:-beta\.[1-9][0-9]*)?",v), "Unsupported release version"; print(v)')
tag=v$version
printf '%s\n' "$version" > "$release_dir/VERSION"
cp LICENSE "$release_dir/LICENSE"
cp LICENSE "$package_dir/LICENSE"
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="-s -w -X github.com/docker-manager/docker-manager/internal/buildinfo.Version=$version" -o "$release_dir/docklane-linux-$arch" ./cmd/server
  cp "$release_dir/docklane-linux-$arch" "$package_dir/docklane"
  COPYFILE_DISABLE=1 tar -czf "$release_dir/docklane-$tag-linux-$arch.tar.gz" -C "$package_dir" docklane LICENSE
done
cd "$release_dir"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum docklane-linux-amd64 docklane-linux-arm64 LICENSE > SHA256SUMS
  for arch in amd64 arm64; do
    sha256sum "docklane-$tag-linux-$arch.tar.gz" > "docklane-$tag-linux-$arch.tar.gz.sha256"
  done
else
  shasum -a 256 docklane-linux-amd64 docklane-linux-arm64 LICENSE > SHA256SUMS
  for arch in amd64 arm64; do
    shasum -a 256 "docklane-$tag-linux-$arch.tar.gz" > "docklane-$tag-linux-$arch.tar.gz.sha256"
  done
fi
