#!/usr/bin/env sh
# Package only version-matched source materials; run after release.sh/build-images.sh.
set -eu
cd "$(dirname "$0")/.."
python3 - "${RELEASE_OUTPUT_DIR:-bin/release}" <<'PY'
import hashlib
import json
import os
from pathlib import Path
import re
import tarfile
import tempfile
import shutil
import sys

root = Path.cwd()
output = Path(sys.argv[1]).resolve()
output.mkdir(parents=True, exist_ok=True)
version = 'v' + json.loads((root / 'web/package.json').read_text())['version']
if version != 'v0.3.0':
    raise SystemExit('Update the release contract before packaging another version')
sources = [
    'scripts/install-online.sh', 'scripts/install-support.py', 'scripts/install.sh',
    'scripts/upgrade.sh', 'scripts/uninstall.sh', 'deployments', 'README.md',
    'README.en.md', 'LICENSE', 'docker-manager-prd-v0.3.md', 'docs',
    'web/qa/release-browser-results.json',
]
entries = []
for source in sources:
    path = root / source
    if not path.exists() or path.is_symlink():
        raise SystemExit('Missing source or symlink: ' + source)
    for entry in [path, *sorted(path.rglob('*'))] if path.is_dir() else [path]:
        if entry.name == '.DS_Store' or '__pycache__' in entry.parts:
            continue
        if entry.is_symlink() or not (entry.is_file() or entry.is_dir()):
            raise SystemExit('Unsafe installation source: ' + str(entry))
        entries.append(entry)

# Check all build outputs before replacing any previously prepared materials.
required = ['SHA256SUMS', 'IMAGE-SHA256SUMS', 'docker-manager-images.tar.gz',
            'IMAGES.txt', 'release-ci-evidence.json']
for arch in ('amd64', 'arm64'):
    required += ['docklane-linux-' + arch, f'docklane-{version}-linux-{arch}.tar.gz',
                 f'docklane-{version}-linux-{arch}.tar.gz.sha256']
for name in required:
    if not (output / name).is_file() or (output / name).is_symlink():
        raise SystemExit('Missing build output or symlink: ' + name)

def digest(path):
    result = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1 << 20), b''):
            result.update(block)
    return result.hexdigest()

for name in ['SHA256SUMS', 'IMAGE-SHA256SUMS', *[
        f'docklane-{version}-linux-{arch}.tar.gz.sha256' for arch in ('amd64', 'arm64')]]:
    seen = set()
    for line in (output / name).read_text().splitlines():
        match = re.fullmatch(r'([a-fA-F0-9]{64}) [ *]([A-Za-z0-9][A-Za-z0-9._-]*)', line)
        if not match or match[2] in seen:
            raise SystemExit('Malformed checksum manifest: ' + name)
        seen.add(match[2])
        payload = output / match[2]
        if not payload.is_file() or payload.is_symlink() or digest(payload) != match[1].lower():
            raise SystemExit('Checksum mismatch: ' + str(payload))
    if not seen:
        raise SystemExit('Empty checksum manifest: ' + name)

with tempfile.TemporaryDirectory(prefix='.installation-', dir=output) as temporary:
    archive = Path(temporary) / 'docker-manager-installation.tar.gz'
    with tarfile.open(archive, 'w:gz', format=tarfile.PAX_FORMAT) as bundle:
        for entry in entries:
            bundle.add(entry, arcname=entry.relative_to(root).as_posix(), recursive=False)
    os.replace(archive, output / archive.name)
for source, destination in [('scripts/install-online.sh', 'install-online.sh'),
                            ('LICENSE', 'LICENSE'), ('.tool-versions', 'TOOL-VERSIONS.txt'),
                            ('docs/security-notes.md', 'SECURITY-NOTES.md')]:
    shutil.copyfile(root / source, output / destination)
payloads = sorted(path for path in output.iterdir() if path.name != 'RELEASE-SHA256SUMS')
for path in payloads:
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', path.name) or not path.is_file() or path.is_symlink():
        raise SystemExit('Unsafe release asset: ' + str(path))
manifest = ''.join(f'{digest(path)}  {path.name}\n' for path in payloads)
with tempfile.NamedTemporaryFile(mode='w', dir=output, prefix='.checksums-', delete=False) as stream:
    stream.write(manifest)
    temporary = stream.name
os.replace(temporary, output / 'RELEASE-SHA256SUMS')
print(f'Prepared installation archive and {len(payloads)} verified release assets in {output}')
PY
