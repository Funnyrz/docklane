#!/usr/bin/env sh
# Build exactly six public assets from a separate, version-bound staging directory.
set -eu
cd "$(dirname "$0")/.."
python3 - "${RELEASE_BUILD_DIR:-bin/release-internal}" "${RELEASE_OUTPUT_DIR:-bin/release}" <<'PY'
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import shutil
import sys
import tarfile
import tempfile

root = Path.cwd()
build, output = (Path(value).resolve() for value in sys.argv[1:])
if build == output or build in output.parents or output in build.parents:
    raise SystemExit('Internal staging and public output directories must be separate')
version = json.loads((root / 'web/package.json').read_text())['version']
if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+(?:-beta\.[1-9][0-9]*)?', version):
    raise SystemExit('Unsupported source release version')
tag = 'v' + version
spec = importlib.util.spec_from_file_location('release_preflight', root / 'scripts/release-preflight.py')
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)
public = helper.release_assets(tag) | {'RELEASE-SHA256SUMS'}
output.mkdir(parents=True, exist_ok=True)
for path in output.iterdir():
    if path.name not in public or not path.is_file() or path.is_symlink():
        raise SystemExit('Refusing contaminated public output directory: ' + str(path))
required = {'SHA256SUMS', 'IMAGE-SHA256SUMS', 'IMAGES.txt', 'release-ci-evidence.json',
            'docklane-linux-amd64', 'docklane-linux-arm64', 'LICENSE', 'docker-manager-images.tar.gz'}
for arch in ('amd64', 'arm64'):
    required |= {f'docklane-{tag}-linux-{arch}.tar.gz', f'docklane-{tag}-linux-{arch}.tar.gz.sha256'}
allowed = required | {'VERSION'}
if not build.is_dir():
    raise SystemExit('Internal build directory missing')
for path in build.iterdir():
    if path.name not in allowed or not path.is_file() or path.is_symlink():
        raise SystemExit('Refusing contaminated internal build directory: ' + str(path))
if not required.issubset({path.name for path in build.iterdir()}):
    raise SystemExit('Missing internal build output')
for manifest in ['SHA256SUMS', 'IMAGE-SHA256SUMS', *[
        f'docklane-{tag}-linux-{arch}.tar.gz.sha256' for arch in ('amd64', 'arm64')]]:
    for name, expected in helper.parsed_manifest((build / manifest).read_bytes()).items():
        path = build / name
        if name not in allowed or not path.is_file() or path.is_symlink() or helper.file_digest(path) != expected:
            raise SystemExit('Internal checksum mismatch: ' + name)
if (build / 'LICENSE').read_bytes() != (root / 'LICENSE').read_bytes():
    raise SystemExit('Internal license differs from source')
if (build / 'VERSION').exists() and (build / 'VERSION').read_text().strip() != version:
    raise SystemExit('Internal VERSION differs from source')
sources = ['scripts/install-online.sh', 'scripts/install-support.py', 'scripts/install.sh',
           'scripts/upgrade.sh', 'scripts/uninstall.sh', 'deployments', 'README.md', 'README.en.md',
           'LICENSE', 'docker-manager-prd-v0.3.md', 'docs', 'web/qa/release-browser-results.json']
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
metadata = {name: (build / name).read_bytes() for name in
            ('SHA256SUMS', 'IMAGE-SHA256SUMS', 'IMAGES.txt', 'release-ci-evidence.json')}
metadata.update({'VERSION': (version + '\n').encode(), 'TOOL-VERSIONS.txt': (root / '.tool-versions').read_bytes(),
                 'SECURITY-NOTES.md': (root / 'docs/security-notes.md').read_bytes()})
with tempfile.TemporaryDirectory(prefix='.docklane-candidate-', dir=output.parent) as temporary:
    candidate = Path(temporary)
    for name in helper.release_assets(tag) - {'docker-manager-installation.tar.gz', 'install-online.sh'}:
        shutil.copyfile(build / name, candidate / name)
    shutil.copyfile(root / 'scripts/install-online.sh', candidate / 'install-online.sh')
    with tarfile.open(candidate / 'docker-manager-installation.tar.gz', 'w:gz', format=tarfile.PAX_FORMAT) as archive:
        for entry in entries:
            archive.add(entry, arcname=entry.relative_to(root).as_posix(), recursive=False)
        for name, content in metadata.items():
            item = tarfile.TarInfo(name)
            item.size, item.mode = len(content), 0o644
            archive.addfile(item, io.BytesIO(content))
    manifest = ''.join(f'{helper.file_digest(candidate / name)}  {name}\n' for name in sorted(helper.release_assets(tag)))
    (candidate / 'RELEASE-SHA256SUMS').write_text(manifest)
    helper.validate_candidate(candidate, tag)
    for name in sorted(public):
        os.replace(candidate / name, output / name)
print(f'Prepared exactly six verified public assets for {tag}: {output}')
PY
