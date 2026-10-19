#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/.."
command -v docker >/dev/null
command -v gzip >/dev/null
docker buildx version >/dev/null 2>&1 || { echo 'Multi-architecture image builds require the Docker Buildx CLI plugin.' >&2; exit 1; }
release_dir=${RELEASE_OUTPUT_DIR:-bin/release}
mkdir -p "$release_dir"
release_dir=$(CDPATH='' cd -- "$release_dir" && pwd)
for arch in amd64 arm64; do
  docker buildx build --load --platform "linux/$arch" --tag "docker-manager:v0.3.0-$arch" .
done
archive="$release_dir/.docker-manager-images.tar"
compressed="$release_dir/.docker-manager-images.tar.gz"
trap 'rm -f "$archive" "$compressed"' EXIT HUP INT TERM
docker save --output "$archive" docker-manager:v0.3.0-amd64 docker-manager:v0.3.0-arm64
gzip -n -c "$archive" > "$compressed"
mv "$compressed" "$release_dir/docker-manager-images.tar.gz"
{
  for arch in amd64 arm64; do
    printf '[docker-manager:v0.3.0-%s] ' "$arch"
    docker image inspect --format '{{.Id}} {{.Os}}/{{.Architecture}}' "docker-manager:v0.3.0-$arch"
  done
} > "$release_dir/IMAGES.txt"
cd "$release_dir"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum docker-manager-images.tar.gz > IMAGE-SHA256SUMS
else
  shasum -a 256 docker-manager-images.tar.gz > IMAGE-SHA256SUMS
fi
