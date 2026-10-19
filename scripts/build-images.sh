#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/.."
command -v docker >/dev/null
command -v gzip >/dev/null
docker buildx version >/dev/null 2>&1 || { echo 'Multi-architecture image builds require the Docker Buildx CLI plugin.' >&2; exit 1; }
version=$(python3 -c 'import json,re; v=json.load(open("web/package.json"))["version"]; assert re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:-beta\.[1-9][0-9]*)?",v), "Unsupported release version"; print(v)')
release_dir=${RELEASE_BUILD_DIR:-bin/release-internal}
mkdir -p "$release_dir"
release_dir=$(CDPATH='' cd -- "$release_dir" && pwd)
for arch in amd64 arm64; do
  docker buildx build --load --platform "linux/$arch" --build-arg "DOCKLANE_VERSION=$version" --tag "docklane:v$version-$arch" .
done
temporary=$(mktemp -d)
archive="$temporary/docklane-images.tar"
compressed="$temporary/docklane-images.tar.gz"
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
docker save --output "$archive" "docklane:v$version-amd64" "docklane:v$version-arm64"
gzip -n -c "$archive" > "$compressed"
mv "$compressed" "$release_dir/docklane-images.tar.gz"
{
  for arch in amd64 arm64; do
    printf '[docklane:v%s-%s] ' "$version" "$arch"
    docker image inspect --format '{{.Id}} {{.Os}}/{{.Architecture}}' "docklane:v$version-$arch"
  done
} > "$release_dir/IMAGES.txt"
cd "$release_dir"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum docklane-images.tar.gz > IMAGE-SHA256SUMS
else
  shasum -a 256 docklane-images.tar.gz > IMAGE-SHA256SUMS
fi
