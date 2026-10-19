#!/usr/bin/env bash
# Public, noninteractive bootstrap. Release payloads are verified before execution.
set -Eeuo pipefail

die() { printf 'Docklane: %s\n' "$*" >&2; exit 1; }
log() { printf 'Docklane: %s\n' "$*"; }
progress() { printf 'Docklane: %s\n' "$*" >&2; }
fetch_url() {
  local url=$1 output=$2 effective_file=${3:-} timeout=${4:-1800}
  local prefix effective status=1 attempt=0
  local progress_options=(--silent --show-error)
  if [[ -t 2 ]]; then progress_options=(--progress-bar --show-error); fi
  local prefixes=('')
  if [[ ${download_source:-mirror} == mirror ]]; then
    case "$url" in
      https://api.github.com/*) prefixes=('https://gh-proxy.com/' '') ;;
      https://github.com/*|https://raw.githubusercontent.com/*)
        prefixes=('https://gh-proxy.com/' 'https://ghfast.top/' '') ;;
    esac
  fi
  for prefix in "${prefixes[@]}"; do
    attempt=$((attempt+1))
    progress "下载 / Download: ${output##*/} via ${prefix:-direct HTTPS} (attempt $attempt/${#prefixes[@]})"
    # Never expose a partial download to validation or a subsequent installation.
    rm -f -- "$output.part"
    if effective=$(curl --fail "${progress_options[@]}" --location --proto '=https' --proto-redir '=https' \
      --connect-timeout 10 --max-time "$timeout" --speed-limit 1024 --speed-time 30 --retry 1 \
      --output "$output.part" --write-out '%{url_effective}' "$prefix$url"); then
      mv -- "$output.part" "$output" || return $?
      progress "下载完成 / Downloaded: ${output##*/}"
      if [[ -n $effective_file ]]; then
        # Only remove the exact transport prefix used for this request. Release
        # selection still validates the canonical repository and stable tag.
        printf '%s' "${effective#"$prefix"}" > "$effective_file" || return $?
      fi
      return 0
    else
      status=$?
    fi
    rm -f -- "$output.part"
    printf 'Docklane: download attempt failed via %s.\n' "${prefix:-direct HTTPS}" >&2
  done
  return "$status"
}
fetch() { fetch_url "$1" "$2"; }
local_docker() {
  env -u DOCKER_HOST -u DOCKER_CONTEXT -u DOCKER_TLS_VERIFY -u DOCKER_CERT_PATH \
    docker --host unix:///var/run/docker.sock "$@"
}
default_install_listen() {
  # Apply the default only to a fresh installation, preserving saved listeners.
  [[ -z $listen && -z $migrate ]] || return 0
  local path configured
  for path in /etc/docklane/config.json /var/lib/docklane-installer/release.json \
    /usr/local/bin/docklane /etc/systemd/system/docklane.service; do
    [[ ! -e $path && ! -L $path ]] || return 0
  done
  if [[ -n $config ]]; then
    configured=$(python3 - "$config" <<'PY'
import json,sys
with open(sys.argv[1]) as stream: value=json.load(stream)
if not isinstance(value,dict):raise SystemExit('Configuration must be a JSON object')
print('yes' if 'listen' in value else 'no')
PY
) || die 'Cannot read --config; supply a valid JSON object.'
    [[ $configured == no ]] || return 0
  fi
  listen=:9999
}
check_legacy_installation() {
  local path root state field value load_state= load_count=0 fragment= drop_ins=
  for path in /etc/docker-manager /var/lib/docker-manager /var/backups/docker-manager \
    /usr/local/bin/docker-manager /etc/systemd/system/multi-user.target.wants/docker-manager.service; do
    [[ ! -e $path && ! -L $path ]] || die "Legacy docker-manager deployment detected at $path. Back it up and remove it manually before a new Docklane installation; nothing was changed."
  done
  for root in /etc/systemd/system /run/systemd/system /usr/lib/systemd/system /lib/systemd/system; do
    for path in "$root/docker-manager.service" "$root/docker-manager.service.d"; do
      [[ ! -e $path && ! -L $path ]] || die "Legacy docker-manager deployment detected at $path. Back it up and remove it manually before a new Docklane installation; nothing was changed."
    done
  done
  # systemctl may return nonzero for a missing unit. Only its explicit
  # not-found state proves absence; failed/empty queries must stop preflight.
  state=$(systemctl show docker-manager.service --property=LoadState,FragmentPath,DropInPaths 2>/dev/null) || true
  while IFS='=' read -r field value; do
    case "$field" in
      LoadState) load_state=$value; load_count=$((load_count+1)) ;;
      FragmentPath) fragment=$value ;;
      DropInPaths) drop_ins=$value ;;
    esac
  done <<< "$state"
  [[ $load_count == 1 && -n $load_state ]] || die 'Cannot verify the legacy docker-manager.service state; no installation changes were made.'
  [[ $load_state == not-found && -z $fragment && -z $drop_ins ]] || die 'Legacy docker-manager systemd service is still loaded. Back it up and remove it manually before a new Docklane installation; nothing was changed.'
}
usage() {
  cat <<'EOF'
Usage: install-online.sh [--channel stable|beta] [--version vX.Y.Z[-beta.N]] [--listen :9999]
                         [--migrate-from /absolute/data] [--config /absolute/config.json]
                         [--download-source mirror|direct] [--dry-run] [--help]
Requires root, systemd, Ubuntu 24.04 or Debian 12/13, amd64 or arm64.
Defaults to the latest beta GitHub Release (testing only; do not use in production).
Use --channel stable to request a formal release explicitly. Existing Docker is never upgraded.
GitHub downloads use public mirrors with direct fallback; use --download-source direct to bypass mirrors.
Legacy docker-manager deployments are refused; no automatic renaming is performed.
Other manual deployments require explicit --migrate-from. Source data is retained.
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
write_release_selection() {
  python3 - "$work" "${requested_version:-$version}" "${channel:-beta}" "$arch" "$@" > "$work/release-selection" <<'PY'
import json,pathlib,re,sys
root=pathlib.Path(sys.argv[1]); requested,channel,arch=sys.argv[2:5]
pattern=re.compile(r'v(\d+)\.(\d+)\.(\d+)(?:-beta\.(\d+))?')
def key(tag):
    match=pattern.fullmatch(tag)
    return tuple(map(int,match.group(1,2,3)))+(1 if match[4] is None else 0,int(match[4] or 0))
releases=[]
for name in sys.argv[5:]:
    value=json.load(open(name)); releases.extend(value if isinstance(value,list) else [value])
valid=[]
for release in releases:
    tag=release.get('tag_name',''); match=pattern.fullmatch(tag)
    if not match or release.get('draft') is not False or not isinstance(release.get('published_at'),str) or not release['published_at']:continue
    beta=match[4] is not None
    if release.get('prerelease') is not beta:continue
    if requested:
        if tag!=requested:continue
    elif beta != (channel=='beta'):continue
    valid.append(release)
if not valid:
    detail='No published release matches the requested version/channel'
    if (requested and '-beta.' not in requested) or (not requested and channel=='stable'):
        detail+='\nNo stable release could be verified. A stable release may be unpublished, or GitHub metadata access may be unavailable. Retry access or use --channel beta explicitly; no channel switch was performed.'
    raise SystemExit(detail)
r=max(valid,key=lambda release:key(release['tag_name']));tag=r['tag_name']
slim={'RELEASE-SHA256SUMS','install-online.sh','docklane-images.tar.gz','docklane-installation.tar.gz',
      f'docklane-{tag}-linux-amd64.tar.gz',f'docklane-{tag}-linux-arm64.tar.gz'}
assets=r.get('assets',[]);names=[asset.get('name') for asset in assets]
if set(names)==slim and len(names)==6:
    required=['RELEASE-SHA256SUMS','docklane-installation.tar.gz','docklane-images.tar.gz',f'docklane-{tag}-linux-{arch}.tar.gz']
    validate=slim
    layout='slim'
else:raise SystemExit('Release must provide exactly the six supported assets')
for name in validate:
    found=[asset for asset in assets if asset.get('name')==name]
    expected=f'https://github.com/Funnyrz/docklane/releases/download/{tag}/{name}'
    if len(found)!=1 or found[0].get('state')!='uploaded' or found[0].get('browser_download_url')!=expected:
        raise SystemExit('Missing or invalid release asset: '+name)
(root/'layout').write_text(layout)
print(tag);print('\n'.join(required))
PY
}
resolve_release() {
  local endpoint=latest page=1 count
  local pages=()
  requested_version=${requested_version:-$version}
  if [[ -z $requested_version && ${channel:-beta} == beta ]]; then
    while :; do
      fetch "https://api.github.com/repos/Funnyrz/docklane/releases?per_page=100&page=$page" "$work/releases-$page.json" || die 'Beta release metadata is unavailable; no web inference is allowed.'
      count=$(python3 - "$work/releases-$page.json" <<'PY'
import json,sys
releases=json.load(open(sys.argv[1]))
if not isinstance(releases,list) or len(releases)>100:raise SystemExit('Invalid release pagination response')
print(len(releases))
PY
)
      pages+=("$work/releases-$page.json")
      ((count==100)) || break
      ((page<100)) || die 'Release pagination limit reached; cannot safely select the newest beta.'
      page=$((page+1))
    done
    write_release_selection "${pages[@]}"
  else
    [[ -z $requested_version ]] || endpoint="tags/$requested_version"
    if ! fetch "https://api.github.com/repos/Funnyrz/docklane/releases/$endpoint" "$work/release.json"; then
      [[ $requested_version != *-beta.* && ( ${channel:-beta} != beta || -n $requested_version ) ]] || die 'Beta release metadata is unavailable; no web inference is allowed.'
      log 'GitHub API unavailable; checking the public latest formal release.'
      resolve_release_web
      return
    fi
    write_release_selection "$work/release.json"
  fi
  version=$(head -n 1 "$work/release-selection")
  download_release_assets
}
download_release_assets() {
  local name
  while IFS= read -r name; do
    if [[ $name == RELEASE-SHA256SUMS && ${release_manifest_downloaded:-false} == true ]]; then continue; fi
    fetch "https://github.com/Funnyrz/docklane/releases/download/$version/$name" "$work/$name"
  done < <(tail -n +2 "$work/release-selection")
}
fetch_release_page() {
  fetch_url 'https://github.com/Funnyrz/docklane/releases/latest' "$work/release-page.html" "$work/release-page-url" 120
}
stable_selection_error() {
  die 'No stable release could be verified. A stable release may be unpublished, or GitHub API/web access may be unavailable. Retry access or use --channel beta explicitly; no channel switch was performed.'
}
resolve_release_web() {
  fetch_release_page || stable_selection_error
  python3 - "$work/release-page-url" "${requested_version:-$version}" > "$work/web-tag" <<'PY' || stable_selection_error
import pathlib,re,sys
url=pathlib.Path(sys.argv[1]).read_text().strip()
match=re.fullmatch(r'https://github\.com/Funnyrz/docklane/releases/tag/(v\d+\.\d+\.\d+)',url)
if not match:raise SystemExit('Public latest did not resolve to a stable Docklane release')
if sys.argv[2] and sys.argv[2]!=match[1]:raise SystemExit('GitHub API unavailable: only the current formal latest can be verified.')
print(match[1])
PY
  version=$(cat "$work/web-tag")
  fetch "https://github.com/Funnyrz/docklane/releases/download/$version/RELEASE-SHA256SUMS" "$work/RELEASE-SHA256SUMS"
  release_manifest_downloaded=true
  python3 - "$work" "$version" "$arch" > "$work/release-selection" <<'PY'
import pathlib,re,sys
root=pathlib.Path(sys.argv[1]);tag,arch=sys.argv[2:]
names=set()
for line in (root/'RELEASE-SHA256SUMS').read_text().splitlines():
    match=re.fullmatch(r'[a-fA-F0-9]{64} [ *]([A-Za-z0-9][A-Za-z0-9._-]*)',line)
    if not match or match[1] in names:raise SystemExit('Unsafe checksum manifest')
    names.add(match[1])
slim={'install-online.sh','docklane-installation.tar.gz','docklane-images.tar.gz',f'docklane-{tag}-linux-amd64.tar.gz',f'docklane-{tag}-linux-arm64.tar.gz'}
required=['RELEASE-SHA256SUMS','docklane-installation.tar.gz','docklane-images.tar.gz',f'docklane-{tag}-linux-{arch}.tar.gz']
layout='slim'
if names!=slim:raise SystemExit('Unsupported release asset layout')
(root/'layout').write_text(layout)
print(tag);print('\n'.join(required))
PY
  download_release_assets
}
verify_release() {
  python3 - "$work" "$version" "$arch" <<'PY'
import hashlib,json,pathlib,re,struct,sys,tarfile
root=pathlib.Path(sys.argv[1]);version,arch=sys.argv[2:]
def digest(p):
    h=hashlib.sha256()
    with p.open('rb') as f:
        for b in iter(lambda:f.read(1024*1024),b''):h.update(b)
    return h.hexdigest()
def manifest(path):
    result={}
    for line in path.read_text().splitlines():
        m=re.fullmatch(r'([a-fA-F0-9]{64}) [ *]([A-Za-z0-9][A-Za-z0-9._-]*)',line)
        if not m or m[2] in result:raise SystemExit('Unsafe checksum manifest: '+path.name)
        result[m[2]]=m[1].lower()
    if not result:raise SystemExit('Empty checksum manifest')
    return result
sums=manifest(root/'RELEASE-SHA256SUMS')
expected={'install-online.sh','docklane-installation.tar.gz','docklane-images.tar.gz',f'docklane-{version}-linux-amd64.tar.gz',f'docklane-{version}-linux-arm64.tar.gz'}
if set(sums)!=expected:raise SystemExit('Six-asset checksum manifest must cover exactly the other five assets')
for name in (root/'release-selection').read_text().splitlines()[2:]:
    if name not in sums or digest(root/name)!=sums[name]:raise SystemExit('Checksum mismatch: '+name)
def unpack(name,destination):
    destination.mkdir()
    with tarfile.open(root/name,'r:gz') as archive:
        seen=set()
        for item in archive.getmembers():
            p=pathlib.PurePosixPath(item.name)
            if p.is_absolute() or '..' in p.parts or not p.parts or str(p) in seen or not(item.isfile() or item.isdir()):raise SystemExit('Unsafe archive entry: '+item.name)
            seen.add(str(p))
        archive.extractall(destination)
    return seen
materials=root/'materials';unpack('docklane-installation.tar.gz',materials)
for name in ['scripts/install-support.py','scripts/install.sh','scripts/upgrade.sh','scripts/uninstall.sh','deployments/docklane.service','deployments/config.example.json','LICENSE']:
    if not(materials/name).is_file():raise SystemExit('Missing installation material: '+name)
for name in ('VERSION','SHA256SUMS','IMAGE-SHA256SUMS','IMAGES.txt','TOOL-VERSIONS.txt','release-ci-evidence.json','SECURITY-NOTES.md'):
    if not(materials/name).is_file():raise SystemExit('Missing installation metadata: '+name)
if (materials/'VERSION').read_text().strip()!=version.removeprefix('v'):raise SystemExit('Material VERSION mismatch')
evidence=json.loads((materials/'release-ci-evidence.json').read_text())
if evidence.get('version')!=version[1:] or evidence.get('tag')!=version or not re.fullmatch(r'[a-fA-F0-9]{40}',evidence.get('head_sha','')):raise SystemExit('Release CI version/commit metadata mismatch')
if json.loads((materials/'deployments/config.example.json').read_text()).get('backup_worker_image')!='docklane:'+version:raise SystemExit('Configuration worker version mismatch')
image_lines=(materials/'IMAGES.txt').read_text().splitlines()
image_arches=[]
for line in image_lines:
    match=re.fullmatch(r'\[docklane:'+re.escape(version)+r'-(amd64|arm64)\] sha256:[a-f0-9]{64} linux/\1',line)
    if not match:raise SystemExit('Worker image metadata version/architecture mismatch')
    image_arches.append(match[1])
if sorted(image_arches)!=['amd64','arm64']:raise SystemExit('Worker image metadata must identify both architectures once')
if not(materials/'TOOL-VERSIONS.txt').read_text().strip() or not(materials/'SECURITY-NOTES.md').read_text().strip():raise SystemExit('Empty required release metadata')
for name in ('SHA256SUMS','IMAGE-SHA256SUMS','IMAGES.txt','LICENSE'):(root/name).write_bytes((materials/name).read_bytes())
image_sums=manifest(root/'IMAGE-SHA256SUMS')
if image_sums.get('docklane-images.tar.gz')!=sums['docklane-images.tar.gz']:raise SystemExit('Inconsistent manifest: IMAGE-SHA256SUMS')
binary_name=f'docklane-{version}-linux-{arch}.tar.gz'
binary=root/'binary';members=unpack(binary_name,binary)
if members!={'docklane','LICENSE'}:raise SystemExit('Unexpected binary archive layout')
header=(binary/'docklane').read_bytes()[:20]
if len(header)!=20 or header[:6]!=b'\x7fELF\x02\x01' or struct.unpack('<H',header[18:20])[0]!={'amd64':62,'arm64':183}[arch]:raise SystemExit('Binary architecture mismatch')
(binary/'docklane').chmod(0o755)
binary_sums=manifest(root/'SHA256SUMS')
if binary_sums.get('docklane-linux-'+arch)!=digest(binary/'docklane') or binary_sums.get('LICENSE')!=digest(root/'LICENSE'):raise SystemExit('Binary checksum manifest disagrees with archive')
if (binary/'LICENSE').read_bytes()!=(materials/'LICENSE').read_bytes() or (materials/'LICENSE').read_bytes()!=(root/'LICENSE').read_bytes():raise SystemExit('License mismatch')
(root/'binary.sha256').write_text(digest(binary/'docklane'))
PY
}
check_selected_downgrade() {
  python3 - "$version" "${explicit_version:-false}" <<'PY'
import json,pathlib,re,sys
state=pathlib.Path('/var/lib/docklane-installer/release.json')
def key(tag):
    match=re.fullmatch(r'v?(\d+)\.(\d+)\.(\d+)(?:-beta\.(\d+))?',tag)
    if not match:raise SystemExit('Cannot safely compare installed/selected release version')
    return tuple(map(int,match.group(1,2,3)))+(1 if match[4] is None else 0,int(match[4] or 0))
if state.exists() and sys.argv[2]!='true':
    installed=json.loads(state.read_text()).get('version','')
    if key(sys.argv[1])<key(installed):raise SystemExit('Implicit downgrade refused; use an explicit --version after reviewing a matching backup/recovery plan')
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
  local native="docklane:$version-$arch"
  local_docker load --input "$work/docklane-images.tar.gz"
  local_docker image inspect "$native" > "$work/worker.json"
  local_docker image save --output "$work/worker-loaded.tar" "$native"
  python3 - "$work/IMAGES.txt" "$work/worker.json" "$native" "$version" "$arch" "$work/docklane-images.tar.gz" "$work/worker-loaded.tar" <<'PY'
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
with tarfile.open(sys.argv[7], 'r:') as loaded:
    manifests=json.load(loaded.extractfile('manifest.json'))
    native=[m for m in manifests if sys.argv[3] in (m.get('RepoTags') or [])]
    if len(native)!=1:raise SystemExit('Missing loaded native image manifest')
    loaded_config=loaded.extractfile(native[0]['Config']).read()
    if 'sha256:'+hashlib.sha256(loaded_config).hexdigest()!=config_id:
        raise SystemExit('Loaded worker content differs from verified release')
if entries[0][1]!=sys.argv[5] or not re.fullmatch(r'sha256:[a-f0-9]{64}',i.get('Id','')) or i.get('Architecture')!=sys.argv[5] or i.get('Os')!='linux':
    raise SystemExit('Worker image identity/architecture mismatch')
if 'DM_BACKUP_WORKER_IMAGE=docklane:'+sys.argv[4] not in i.get('Config',{}).get('Env',[]):
    raise SystemExit('Worker application version mismatch')
PY
  local_docker tag "$native" "docklane:$version"
}
main() {
  version=; listen=; migrate=; config=; dry_run=false; download_source=mirror; channel=beta; explicit_version=false
  while (($#)); do
    case "$1" in
      --help|-h) usage; return ;;
      --dry-run) dry_run=true; shift ;;
      --version|--listen|--migrate-from|--config|--download-source|--channel)
        (($#>=2)) && [[ -n $2 && $2 != --* ]] || die "Missing value for $1."
        case "$1" in --version) version=$2; explicit_version=true ;; --channel) channel=$2 ;; --listen) listen=$2 ;; --migrate-from) migrate=$2 ;; --config) config=$2 ;; --download-source) download_source=$2 ;; esac
        shift 2 ;;
      *) die "Unknown argument: $1" ;;
    esac
  done
  [[ $download_source == mirror || $download_source == direct ]] || die '--download-source must be mirror or direct.'
  [[ -z $version || $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-beta\.[0-9]+)?$ ]] || die 'Use vX.Y.Z or vX.Y.Z-beta.N.'
  [[ -z $migrate || $migrate == /* ]] || die '--migrate-from must be an absolute path.'
  [[ -z $config || $config == /* ]] || die '--config must be an absolute path.'
  [[ $channel == stable || $channel == beta ]] || die 'Use --channel stable or beta.'
  requested_version=$version
  platform
  log '本程序尚未经过充分、严格的测试，仅供学习与测试使用，请勿用于生产环境。使用前请自行评估风险并备份重要数据。'
  log 'This program has not undergone sufficient, rigorous testing. It is for learning and testing only; do not use it in production. Assess the risks yourself and back up important data before use.'
  export DOCKLANE_NOTICE_SHOWN=1
  if $dry_run; then
    log "Plan: $ID $VERSION_ID $arch; release ${version:-latest $channel}; listen ${listen:-preserve existing or :9999}; migration ${migrate:-none}."
    log "GitHub download source: $download_source; Docker apt repository: official (unchanged)."
    log 'Verify release assets; prepare missing dependencies; load matching worker; install/upgrade systemd service; enable and check /healthz. No changes made.'
    return
  fi
  check_legacy_installation
  umask 077
  work=$(mktemp -d /tmp/docklane-install.XXXXXXXX)
  trap 'rm -rf -- "$work"' EXIT
  trap 'status=$?; printf "Docklane: installation failed (exit %s). Review the preceding error; existing source data and recovery backups are retained.\n" "$status" >&2; exit "$status"' ERR
  progress '[1/8] 准备基础依赖 / Preparing base dependencies'
  bootstrap_tools
  progress "[2/8] 查询版本并下载安装包 / Resolving ${version:-latest $channel} and downloading release assets"
  resolve_release
  progress '[3/8] 校验安装包、架构与升级条件 / Verifying assets, architecture and upgrade eligibility'
  verify_release
  log "Verified $version for $arch."
  check_selected_downgrade
  progress '[4/8] 检查或安装 Docker / Checking or installing Docker'
  install_docker
  progress '[5/8] 准备 Compose 和 Restic / Preparing Compose and Restic'
  install_tools
  progress '[6/8] 加载并校验备份镜像 / Loading and verifying backup worker image'
  install_worker
  export DOCKER_HOST=unix:///var/run/docker.sock
  export DOCKER_CONFIG="$work/docker-config"
  unset DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH
  default_install_listen
  local args=(deploy --binary "$work/binary/docklane" --source-dir "$work/materials" --version "$version" --sha256 "$(cat "$work/binary.sha256")")
  if $explicit_version; then args+=(--explicit-version); fi
  [[ -z $listen ]] || args+=(--listen "$listen")
  [[ -z $migrate ]] || args+=(--migrate-from "$migrate")
  [[ -z $config ]] || args+=(--config "$config")
  progress '[7/8] 安装服务并检查自启和健康状态 / Installing service and checking boot enablement and health'
  python3 "$work/materials/scripts/install-support.py" "${args[@]}"
  progress '[8/8] 安装完成 / Installation complete'
  log 'Installation complete. Open http://SERVER_IP:PORT to create the administrator on first access.'
  log 'Config: /etc/docklane/config.json; status: systemctl status docklane; logs: journalctl -u docklane -f'
}
if [[ ${BASH_SOURCE[0]:-} == "$0" || -z ${BASH_SOURCE[0]:-} ]]; then main "$@"; fi
