#!/usr/bin/env bash
# Public, noninteractive bootstrap. Release payloads are verified before execution.
set -Eeuo pipefail

die() { printf 'Docklane: %s\n' "$*" >&2; exit 1; }
log() { printf 'Docklane: %s\n' "$*"; }
fetch() {
  curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
    --connect-timeout 20 --max-time 1800 --retry 3 "$1" --output "$2"
}
local_docker() {
  env -u DOCKER_HOST -u DOCKER_CONTEXT -u DOCKER_TLS_VERIFY -u DOCKER_CERT_PATH \
    docker --host unix:///var/run/docker.sock "$@"
}
usage() {
  cat <<'EOF'
Usage: install-online.sh [--version vX.Y.Z] [--listen :8080]
                         [--migrate-from /absolute/data] [--config /absolute/config.json]
                         [--dry-run] [--help]
Requires root, systemd, Ubuntu 24.04 or Debian 12/13, amd64 or arm64.
Defaults to the latest stable GitHub Release. Existing Docker is never upgraded.
Unknown manual deployments require explicit --migrate-from. Source data is retained.
EOF
}
platform() {
  [[ $(uname -s) == Linux ]] || die 'Only Linux is supported.'
  # os-release is a root-owned operating-system file, not a downloaded payload.
  . /etc/os-release
  case "${ID:-}:${VERSION_ID:-}" in
    ubuntu:24.04|debian:12|debian:13) ;;
    *) die "Unsupported system: ${ID:-unknown} ${VERSION_ID:-unknown}." ;;
  esac
  case "$(uname -m)" in
    x86_64) arch=amd64; docker_arch=x86_64 ;;
    aarch64|arm64) arch=arm64; docker_arch=aarch64 ;;
    *) die 'Only amd64 and arm64 are supported.' ;;
  esac
  [[ $(id -u) == 0 ]] || die 'Run this installer as root (sudo bash -s --).'
  command -v systemctl >/dev/null && [[ -d /run/systemd/system ]] || die 'A running systemd is required.'
}
bootstrap_tools() {
  local packages=() item
  for item in curl:curl python3:python3 tar:tar gzip:gzip bzip2:bzip2 sha256sum:coreutils runuser:util-linux useradd:passwd; do
    command -v "${item%%:*}" >/dev/null || packages+=("${item#*:}")
  done
  [[ -s /etc/ssl/certs/ca-certificates.crt ]] || packages+=(ca-certificates)
  if ((${#packages[@]})); then
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "${packages[@]}"
  fi
}
resolve_release() {
  local endpoint=latest
  [[ -z $version ]] || endpoint="tags/$version"
  fetch "https://api.github.com/repos/Funnyrz/docklane/releases/$endpoint" "$work/release.json"
  python3 - "$work/release.json" "$version" "$arch" > "$work/release-selection" <<'PY'
import json, re, sys
r=json.load(open(sys.argv[1])); tag=r.get('tag_name','')
if not re.fullmatch(r'v\d+\.\d+\.\d+', tag) or r.get('draft') or r.get('prerelease'):
    raise SystemExit('A published stable release is required')
if sys.argv[2] and sys.argv[2] != tag: raise SystemExit('Release version mismatch')
required=['RELEASE-SHA256SUMS','LICENSE','SHA256SUMS','IMAGE-SHA256SUMS','IMAGES.txt',
          'docker-manager-images.tar.gz','docker-manager-installation.tar.gz',
          f'docklane-{tag}-linux-{sys.argv[3]}.tar.gz',
          f'docklane-{tag}-linux-{sys.argv[3]}.tar.gz.sha256']
assets=r.get('assets',[])
for name in required:
    found=[a for a in assets if a.get('name')==name]
    expected=f'https://github.com/Funnyrz/docklane/releases/download/{tag}/{name}'
    if len(found)!=1 or found[0].get('state')!='uploaded' or found[0].get('browser_download_url')!=expected:
        raise SystemExit('Missing or invalid release asset: '+name)
print(tag)
print('\n'.join(required))
PY
  version=$(head -n 1 "$work/release-selection")
  while IFS= read -r name; do
    fetch "https://github.com/Funnyrz/docklane/releases/download/$version/$name" "$work/$name"
  done < <(tail -n +2 "$work/release-selection")
}
verify_release() {
  python3 - "$work" "$version" "$arch" <<'PY'
import hashlib, pathlib, re, struct, sys, tarfile
root=pathlib.Path(sys.argv[1]); version,arch=sys.argv[2:]
def digest(p):
    h=hashlib.sha256()
    with p.open('rb') as f:
        for b in iter(lambda:f.read(1024*1024),b''): h.update(b)
    return h.hexdigest()
def manifest(path):
    result={}
    for line in path.read_text().splitlines():
        m=re.fullmatch(r'([a-fA-F0-9]{64}) [ *]([A-Za-z0-9][A-Za-z0-9._-]*)',line)
        if not m or m[2] in result: raise SystemExit('Unsafe checksum manifest: '+path.name)
        result[m[2]]=m[1].lower()
    if not result: raise SystemExit('Empty checksum manifest')
    return result
sums=manifest(root/'RELEASE-SHA256SUMS')
for name in (root/'release-selection').read_text().splitlines()[2:]:
    if name not in sums or digest(root/name)!=sums[name]: raise SystemExit('Checksum mismatch: '+name)
binary_name=f'docklane-{version}-linux-{arch}.tar.gz'
for file,required in [('IMAGE-SHA256SUMS','docker-manager-images.tar.gz'),(binary_name+'.sha256',binary_name)]:
    sub=manifest(root/file)
    if sub.get(required)!=sums[required]: raise SystemExit('Inconsistent manifest: '+file)
def unpack(name,destination):
    destination.mkdir()
    with tarfile.open(root/name,'r:gz') as archive:
        seen=set()
        for item in archive.getmembers():
            p=pathlib.PurePosixPath(item.name)
            if p.is_absolute() or '..' in p.parts or not p.parts or str(p) in seen or not (item.isfile() or item.isdir()):
                raise SystemExit('Unsafe archive entry: '+item.name)
            seen.add(str(p))
        # Links and special files have been rejected; destination is private and empty.
        archive.extractall(destination)
    return seen
binary=root/'binary'; members=unpack(binary_name,binary)
if members!={'docklane','LICENSE'}: raise SystemExit('Unexpected binary archive layout')
header=(binary/'docklane').read_bytes()[:20]
if len(header)!=20 or header[:6]!=b'\x7fELF\x02\x01' or struct.unpack('<H',header[18:20])[0]!={'amd64':62,'arm64':183}[arch]:
    raise SystemExit('Binary architecture mismatch')
(binary/'docklane').chmod(0o755)
binary_sums=manifest(root/'SHA256SUMS')
if binary_sums.get('docklane-linux-'+arch)!=digest(binary/'docklane') or binary_sums.get('LICENSE')!=digest(root/'LICENSE'):
    raise SystemExit('Binary checksum manifest disagrees with archive')
materials=root/'materials'; unpack('docker-manager-installation.tar.gz',materials)
for name in ['scripts/install-support.py','scripts/install.sh','scripts/upgrade.sh','scripts/uninstall.sh','deployments/docker-manager.service','deployments/config.example.json','LICENSE']:
    if not (materials/name).is_file(): raise SystemExit('Missing installation material: '+name)
for license in [binary/'LICENSE',materials/'LICENSE']:
    if license.read_bytes()!=(root/'LICENSE').read_bytes(): raise SystemExit('License mismatch')
(root/'binary.sha256').write_text(digest(binary/'docklane'))
PY
}
install_docker() {
  local package installed version_candidate key source
  if command -v docker >/dev/null; then
    installed=$(local_docker version --format '{{.Server.Version}}') || die 'Existing Docker is unavailable; repair it before installation.'
    case "$installed" in 28.*|29.*) log "Preserving existing Docker $installed."; return ;; *) die "Unsupported existing Docker $installed; it has been left unchanged." ;; esac
  fi
  for package in docker.io docker-ce docker-ce-cli docker-engine podman-docker containerd containerd.io runc; do
    if dpkg-query -W -f='${Status}' "$package" 2>/dev/null | grep -qx 'install ok installed'; then
      die "Conflicting installation $package; resolve it manually before continuing."
    fi
  done
  key=/etc/apt/keyrings/docklane-docker.asc
  source=/etc/apt/sources.list.d/docklane-docker.sources
  fetch "https://download.docker.com/linux/$ID/gpg" "$work/docker.asc"
  cat > "$work/docker.sources" <<EOF
Types: deb
URIs: https://download.docker.com/linux/$ID
Suites: ${VERSION_CODENAME:?}
Components: stable
Architectures: $arch
Signed-By: $key
EOF
  [[ ! -e $source ]] || cmp -s "$source" "$work/docker.sources" || die 'Conflicting Docker apt source; left unchanged.'
  [[ ! -e $key ]] || cmp -s "$key" "$work/docker.asc" || die 'Conflicting Docker apt key; left unchanged.'
  install -d -m 0755 /etc/apt/keyrings
  install -m 0644 "$work/docker.asc" "$key"
  install -m 0644 "$work/docker.sources" "$source"
  apt-get update
  # Drain apt-cache fully: early awk exit can cause SIGPIPE under pipefail.
  version_candidate=$(apt-cache madison docker-ce | awk '!found && $3 ~ /^5:29\./ {print $3; found=1}')
  [[ -n $version_candidate ]] || die 'No supported stable Docker 29.x package is available.'
  DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    "docker-ce=$version_candidate" "docker-ce-cli=$version_candidate" containerd.io
  systemctl enable --now docker
  local_docker info >/dev/null || die 'New Docker Engine did not become available.'
}
verify_tool() {
  python3 - "$1" "$2" "$3" <<'PY'
import hashlib,re,sys
matches=[]
for line in open(sys.argv[1]):
    m=re.fullmatch(r'([a-fA-F0-9]{64})\s+\*?(.+?)\s*',line)
    if m and m[2]==sys.argv[2]: matches.append(m[1].lower())
if len(matches)!=1: raise SystemExit('Missing or duplicate tool checksum')
h=hashlib.sha256()
with open(sys.argv[3],'rb') as f:
    for b in iter(lambda:f.read(1024*1024),b''):h.update(b)
if h.hexdigest()!=matches[0]:raise SystemExit('Tool checksum mismatch')
PY
}
install_tools() {
  local compose_name="docker-compose-linux-$docker_arch" restic_name="restic_0.19.1_linux_$arch.bz2" compose
  mkdir "$work/docker-config"
  compose=$(DOCKER_CONFIG="$work/docker-config" local_docker compose version --short 2>/dev/null || true)
  if [[ ! $compose =~ ^v?2\. ]]; then
    [[ -z $compose ]] || die "Existing system Compose $compose is incompatible; left unchanged."
    [[ ! -e /usr/local/lib/docker/cli-plugins/docker-compose ]] || die 'Existing Compose plugin cannot be replaced automatically.'
    fetch "https://github.com/docker/compose/releases/download/v2.40.3/$compose_name" "$work/compose"
    fetch 'https://github.com/docker/compose/releases/download/v2.40.3/checksums.txt' "$work/compose-sums"
    verify_tool "$work/compose-sums" "$compose_name" "$work/compose"
    install -d -m 0755 /usr/local/lib/docker/cli-plugins
    install -m 0755 "$work/compose" /usr/local/lib/docker/cli-plugins/docker-compose
  fi
  if [[ ! -x /usr/local/lib/docklane/bin/restic ]] || ! /usr/local/lib/docklane/bin/restic version | grep -q '^restic 0\.19\.1 '; then
    fetch "https://github.com/restic/restic/releases/download/v0.19.1/$restic_name" "$work/restic.bz2"
    fetch 'https://github.com/restic/restic/releases/download/v0.19.1/SHA256SUMS' "$work/restic-sums"
    verify_tool "$work/restic-sums" "$restic_name" "$work/restic.bz2"
    bzip2 -dc "$work/restic.bz2" > "$work/restic"
    install -d -m 0755 /usr/local/lib/docklane/bin
    install -m 0755 "$work/restic" /usr/local/lib/docklane/bin/restic
  fi
  DOCKER_CONFIG="$work/docker-config" local_docker compose version --short | grep -Eq '^v?2\.' || die 'Compose v2 verification failed.'
  /usr/local/lib/docklane/bin/restic version | grep -q '^restic 0\.19\.1 ' || die 'Restic version verification failed.'
}
install_worker() {
  local native="docker-manager:$version-$arch"
  local_docker load --input "$work/docker-manager-images.tar.gz"
  local_docker image inspect "$native" > "$work/worker.json"
  python3 - "$work/IMAGES.txt" "$work/worker.json" "$native" "$version" "$arch" "$work/docker-manager-images.tar.gz" <<'PY'
import hashlib,json,re,sys,tarfile
lines=open(sys.argv[1]).read().splitlines(); entries=[]
for line in lines:
    m=re.fullmatch(r'\[([^]]+)\] (sha256:[a-f0-9]{64}) linux/(amd64|arm64)',line)
    if m and m[1]==sys.argv[3]: entries.append((m[2],m[3]))
images=json.load(open(sys.argv[2]))
if len(entries)!=1 or len(images)!=1:raise SystemExit('Missing worker image identity')
i=images[0]
with tarfile.open(sys.argv[6], 'r:gz') as archive:
    manifests=json.load(archive.extractfile('manifest.json'))
    native=[m for m in manifests if sys.argv[3] in (m.get('RepoTags') or [])]
    if len(native)!=1:raise SystemExit('Missing native image archive manifest')
    config=archive.extractfile(native[0]['Config']).read()
    config_id='sha256:'+hashlib.sha256(config).hexdigest()
    # Classic Docker stores expose the config digest as .Id; containerd stores
    # expose the OCI index digest recorded by build-images.sh. Both must come
    # from this verified archive, never an unrelated locally tagged image.
    expected=entries[0][0]
    if expected!=config_id:
        def contains_config(identity, depth=0):
            if depth>8 or not re.fullmatch(r'sha256:[a-f0-9]{64}',identity):
                raise SystemExit('Invalid worker descriptor')
            blob=archive.extractfile('blobs/sha256/'+identity[7:]).read()
            if 'sha256:'+hashlib.sha256(blob).hexdigest()!=identity:
                raise SystemExit('Worker archive identity mismatch')
            descriptor=json.loads(blob)
            if descriptor.get('config',{}).get('digest')==config_id:return True
            return any(contains_config(child['digest'],depth+1) for child in descriptor.get('manifests',[]))
        if not contains_config(expected):raise SystemExit('Worker index does not reference the native config')
if entries[0][1]!=sys.argv[5] or i.get('Id') not in (expected,config_id) or i.get('Architecture')!=sys.argv[5] or i.get('Os')!='linux':
    raise SystemExit('Worker image identity/architecture mismatch')
if 'DM_BACKUP_WORKER_IMAGE=docker-manager:'+sys.argv[4] not in i.get('Config',{}).get('Env',[]):
    raise SystemExit('Worker application version mismatch')
PY
  local_docker tag "$native" "docker-manager:$version"
}
main() {
  version=; listen=; migrate=; config=; dry_run=false
  while (($#)); do
    case "$1" in
      --help|-h) usage; return ;;
      --dry-run) dry_run=true; shift ;;
      --version|--listen|--migrate-from|--config)
        (($#>=2)) && [[ -n $2 && $2 != --* ]] || die "Missing value for $1."
        case "$1" in --version) version=$2 ;; --listen) listen=$2 ;; --migrate-from) migrate=$2 ;; --config) config=$2 ;; esac
        shift 2 ;;
      *) die "Unknown argument: $1" ;;
    esac
  done
  [[ -z $version || $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die 'Use a stable version such as v0.3.0.'
  [[ -z $migrate || $migrate == /* ]] || die '--migrate-from must be an absolute path.'
  [[ -z $config || $config == /* ]] || die '--config must be an absolute path.'
  platform
  if $dry_run; then
    log "Plan: $ID $VERSION_ID $arch; release ${version:-latest stable}; listen ${listen:-preserve existing or :8080}; migration ${migrate:-none}."
    log 'Verify release assets; prepare missing dependencies; load matching worker; install/upgrade systemd service; enable and check /healthz. No changes made.'
    return
  fi
  umask 077
  work=$(mktemp -d /tmp/docklane-install.XXXXXXXX)
  trap 'rm -rf -- "$work"' EXIT
  trap 'status=$?; printf "Docklane: installation failed (exit %s). Review the preceding error; existing source data and recovery backups are retained.\n" "$status" >&2; exit "$status"' ERR
  bootstrap_tools
  resolve_release
  verify_release
  log "Verified $version for $arch."
  install_docker
  install_tools
  install_worker
  export DOCKER_HOST=unix:///var/run/docker.sock
  export DOCKER_CONFIG="$work/docker-config"
  unset DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH
  local args=(deploy --binary "$work/binary/docklane" --source-dir "$work/materials" --version "$version" --sha256 "$(cat "$work/binary.sha256")")
  [[ -z $listen ]] || args+=(--listen "$listen")
  [[ -z $migrate ]] || args+=(--migrate-from "$migrate")
  [[ -z $config ]] || args+=(--config "$config")
  python3 "$work/materials/scripts/install-support.py" "${args[@]}"
  log 'Installation complete. Open http://SERVER_IP:PORT to create the administrator on first access.'
  log 'Config: /etc/docker-manager/config.json; status: systemctl status docker-manager; logs: journalctl -u docker-manager -f'
}
if [[ ${BASH_SOURCE[0]:-} == "$0" || -z ${BASH_SOURCE[0]:-} ]]; then main "$@"; fi
