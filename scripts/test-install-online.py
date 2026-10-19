#!/usr/bin/env python3
"""Exercise the public installer using isolated assets and mocked host commands.

Only fixture paths are substituted in a temporary copy before sourcing Bash.
Release validation and archive/ELF validation run unmodified. No network or host
package manager, Docker daemon, service manager, or privileged writes are used.
"""
import bz2
import hashlib
import io
import json
import os
from pathlib import Path
import shlex
import shutil
import struct
import subprocess
import tarfile
import tempfile
import unittest

SOURCE = Path(__file__).resolve().with_name('install-online.sh')
BASH = shutil.which('bash')
TAG = 'v0.3.0'


def quote(value):
    return shlex.quote(str(value))


def checksum(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def archive(path, members, *, compressed=True):
    with tarfile.open(path, 'w:gz' if compressed else 'w:') as output:
        for name, content in members.items():
            info = tarfile.TarInfo(name)
            if isinstance(content, tuple):
                info.type = content[0]
                info.linkname = content[1]
                output.addfile(info)
            else:
                info.size = len(content)
                output.addfile(info, io.BytesIO(content))


class InstallerTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='docklane-online-test-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.work = self.root / 'work'
        self.work.mkdir()
        self.assets = self.root / 'assets'
        self.assets.mkdir()
        self.trace = self.root / 'trace'
        self.os_release = self.root / 'os-release'
        self.os_release.write_text('ID=ubuntu\nVERSION_ID=24.04\nVERSION_CODENAME=noble\n')
        self.systemd = self.root / 'systemd'
        self.systemd.mkdir()
        self.script = self.root / 'install-online.sh'
        source = SOURCE.read_text()
        replacements = {
            '/etc/os-release': str(self.os_release),
            '/var/lib/docklane-installer': str(self.root / 'receipt'),
            '/run/systemd/system': str(self.systemd),
            '/usr/local/lib/docker': str(self.root / 'docker-tools'),
            '/usr/local/lib/docklane': str(self.root / 'docklane-tools'),
            '/etc/apt': str(self.root / 'apt'),
            '/etc/ssl/certs/ca-certificates.crt': str(self.root / 'ca-certificates.crt'),
        }
        for old, new in replacements.items():
            source = source.replace(old, new)
        self.script.write_text(source)
        (self.root / 'apt/sources.list.d').mkdir(parents=True)
        self.restic = self.root / 'docklane-tools/bin/restic'
        self.make_release()

    def bash(self, body, *, prelude='', ok=True):
        common = f'''
source {quote(self.script)}
work={quote(self.work)}
version=; channel=stable; arch=amd64; docker_arch=x86_64; ID=ubuntu; VERSION_ID=24.04; VERSION_CODENAME=noble
download_source=direct
trace={quote(self.trace)}
assets={quote(self.assets)}
uname() {{ case "$1" in -s) echo Linux ;; -m) echo x86_64 ;; esac; }}
id() {{ echo 0; }}
systemctl() {{ printf 'systemctl %s\\n' "$*" >> "$trace"; }}
apt-get() {{ printf 'apt-get %s\\n' "$*" >> "$trace"; }}
fetch() {{
  printf 'fetch %s\\n' "$1" >> "$trace"
  case "$1" in
    https://api.github.com/repos/Funnyrz/docklane/releases/*) cp "$assets/release.json" "$2" ;;
    https://github.com/Funnyrz/docklane/releases/download/*) cp "$assets/${{1##*/}}" "$2" ;;
    *) echo 'Unexpected fetch' >&2; return 80 ;;
  esac
}}
'''
        result = subprocess.run([BASH, '-c', common + prelude + '\n' + body], text=True,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                env={**os.environ, 'HOME': str(self.root), 'LC_ALL': 'C'})
        if ok:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        return result

    def make_release(self, arch='amd64', *, binary_members=None, material_members=None):
        self.binary_name = f'docklane-{TAG}-linux-{arch}.tar.gz'
        self.license = b'MIT License\nCopyright (c) 2026 Funnyrz\n'
        header = bytearray(64)
        header[:6] = b'\x7fELF\x02\x01'
        header[18:20] = struct.pack('<H', {'amd64': 62, 'arm64': 183}[arch])
        self.binary = bytes(header)
        archive(self.assets / self.binary_name,
                binary_members or {'docklane': self.binary, 'LICENSE': self.license})
        materials = {name: b'fixture' for name in (
            'scripts/install-support.py', 'scripts/install.sh', 'scripts/upgrade.sh',
            'scripts/uninstall.sh', 'deployments/docker-manager.service',
            'deployments/config.example.json')}
        materials['LICENSE'] = self.license
        archive(self.assets / 'docker-manager-installation.tar.gz',
                materials if material_members is None else material_members)
        (self.assets / 'LICENSE').write_bytes(self.license)
        (self.assets / 'docker-manager-images.tar.gz').write_bytes(b'image archive fixture')
        image_id = 'sha256:' + '1' * 64
        (self.assets / 'IMAGES.txt').write_text(f'[docker-manager:{TAG}-{arch}] {image_id} linux/{arch}\n')
        (self.assets / 'SHA256SUMS').write_text(
            f'{hashlib.sha256(self.binary).hexdigest()}  docklane-linux-{arch}\n'
            f'{hashlib.sha256(self.license).hexdigest()}  LICENSE\n')
        self.rehash()
        names = [p.name for p in self.assets.iterdir() if p.name != 'release.json']
        release = {'tag_name': TAG, 'draft': False, 'prerelease': False, 'published_at': '2026-10-19T00:00:00Z', 'assets': [
            {'name': name, 'state': 'uploaded', 'browser_download_url':
             f'https://github.com/Funnyrz/docklane/releases/download/{TAG}/{name}'}
            for name in names]}
        (self.assets / 'release.json').write_text(json.dumps(release))

    def rehash(self):
        name = self.binary_name
        (self.assets / (name + '.sha256')).write_text(f'{checksum(self.assets / name)}  {name}\n')
        image = 'docker-manager-images.tar.gz'
        (self.assets / 'IMAGE-SHA256SUMS').write_text(f'{checksum(self.assets / image)}  {image}\n')
        paths = sorted(p for p in self.assets.iterdir()
                       if p.name not in ('release.json', 'RELEASE-SHA256SUMS'))
        (self.assets / 'RELEASE-SHA256SUMS').write_text(''.join(
            f'{checksum(p)}  {p.name}\n' for p in paths))

    def make_slim_release(self, tag='v0.3.1-beta.1'):
        for path in self.assets.iterdir():
            path.unlink()
        self.slim_tag = tag
        self.license = b'MIT License\nCopyright (c) 2026 Funnyrz\n'
        sums = []
        for arch, machine in [('amd64', 62), ('arm64', 183)]:
            header = bytearray(64)
            header[:6] = b'\x7fELF\x02\x01'
            header[18:20] = struct.pack('<H', machine)
            if arch == 'amd64':
                self.binary = bytes(header)
            archive(self.assets / f'docklane-{tag}-linux-{arch}.tar.gz', {'docklane': bytes(header), 'LICENSE': self.license})
            sums.append(f'{hashlib.sha256(header).hexdigest()}  docklane-linux-{arch}\n')
        (self.assets / 'docker-manager-images.tar.gz').write_bytes(b'fixture worker')
        (self.assets / 'install-online.sh').write_bytes(SOURCE.read_bytes())
        sums.append(f'{hashlib.sha256(self.license).hexdigest()}  LICENSE\n')
        self.slim_materials = {name: b'fixture' for name in ('scripts/install-online.sh', 'scripts/install-support.py', 'scripts/install.sh', 'scripts/upgrade.sh', 'scripts/uninstall.sh', 'deployments/docker-manager.service')}
        self.slim_materials.update({
            'VERSION': (tag[1:] + '\n').encode(), 'LICENSE': self.license,
            'SHA256SUMS': ''.join(sums).encode(),
            'IMAGE-SHA256SUMS': f'{checksum(self.assets / "docker-manager-images.tar.gz")}  docker-manager-images.tar.gz\n'.encode(),
            'IMAGES.txt': ''.join(f'[docker-manager:{tag}-{arch}] sha256:{"1" * 64} linux/{arch}\n' for arch in ('amd64','arm64')).encode(),
            'TOOL-VERSIONS.txt': b'golang 1.27.2\nnodejs 22.22.0\npnpm 10.28.2\n',
            'SECURITY-NOTES.md': b'Test only; keep data backed up.',
            'release-ci-evidence.json': json.dumps({'version': tag[1:], 'tag': tag, 'head_sha': 'a' * 40, 'ci_run_id': 123, 'ci_run_url': 'https://github.com/Funnyrz/docklane/actions/runs/123', 'publication': 'candidate'}).encode(),
            'deployments/config.example.json': json.dumps({'version': 1, 'backup_worker_image': 'docker-manager:' + tag}).encode(),
        })
        self.repack_slim()
        names = [path.name for path in self.assets.iterdir() if path.name != 'release.json']
        self.slim_metadata = {'tag_name': tag, 'draft': False, 'prerelease': '-beta.' in tag,
                              'published_at': '2026-10-19T00:00:00Z', 'assets': [
            {'name': name, 'state': 'uploaded', 'browser_download_url': f'https://github.com/Funnyrz/docklane/releases/download/{tag}/{name}'} for name in names]}
        (self.assets / 'release.json').write_text(json.dumps(self.slim_metadata))

    def repack_slim(self):
        archive(self.assets / 'docker-manager-installation.tar.gz', self.slim_materials)
        names = ['install-online.sh', 'docker-manager-installation.tar.gz', 'docker-manager-images.tar.gz',
                 f'docklane-{self.slim_tag}-linux-amd64.tar.gz', f'docklane-{self.slim_tag}-linux-arm64.tar.gz']
        (self.assets / 'RELEASE-SHA256SUMS').write_text(''.join(f'{checksum(self.assets / name)}  {name}\n' for name in names))

    def test_beta_dry_run_describes_selected_channel(self):
        result = self.bash('main --channel beta --dry-run')
        self.assertIn('release latest beta', result.stdout)
        self.assertNotIn('release latest stable', result.stdout)
        self.assertFalse(self.trace.exists())

    def test_stable_web_fallback_detects_new_layout_from_manifest(self):
        self.make_slim_release('v0.3.1')
        prelude = '''
fetch() {
 if [[ $1 == https://api.github.com/* ]]; then return 22; fi
 printf 'fetch %s\\n' "$1" >> "$trace"
 cp "$assets/${1##*/}" "$2"
}
fetch_release_page() { printf '%s' 'https://github.com/Funnyrz/docklane/releases/tag/v0.3.1' > "$work/release-page-url"; }
'''
        self.bash('resolve_release; verify_release', prelude=prelude)
        self.assertEqual((self.work / 'layout').read_text(), 'slim')
        calls = self.trace.read_text()
        self.assertEqual(sum('/RELEASE-SHA256SUMS' in line for line in calls.splitlines()), 1)
        self.assertNotIn('/LICENSE', calls)

    def test_disclaimer_is_printed_before_dependency_mutation(self):
        result = self.bash('main --version v0.3.0', prelude='bootstrap_tools() { echo mutation-started; return 23; }', ok=False)
        chinese = '本程序尚未经过充分、严格的测试，仅供学习与测试使用，请勿用于生产环境。使用前请自行评估风险并备份重要数据。'
        self.assertIn(chinese, result.stdout)
        self.assertLess(result.stdout.index(chinese), result.stdout.index('mutation-started'))
        self.assertIn('do not use it in production', result.stdout)

    def test_invalid_channel_is_refused_without_mutation(self):
        self.bash('main --channel nightly', ok=False)
        self.assertFalse(self.trace.exists())

    def test_new_stable_six_asset_layout_and_explicit_stable_on_beta_channel(self):
        self.make_slim_release('v0.3.1')
        self.bash('channel=beta; version=v0.3.1; resolve_release; verify_release')
        shutil.rmtree(self.work)
        self.work.mkdir()
        self.bash('channel=stable; resolve_release; verify_release')

    def test_new_six_assets_use_hashed_internal_metadata(self):
        self.make_slim_release()
        self.bash('version=v0.3.1-beta.1; resolve_release; verify_release')
        calls = self.trace.read_text()
        self.assertEqual(sum('download/' in line for line in calls.splitlines()), 4)
        self.assertNotIn('linux-arm64.tar.gz', calls)
        self.assertNotIn('/install-online.sh', calls)
        self.assertEqual((self.work / 'VERSION').exists(), False)
        self.assertEqual((self.work / 'materials/VERSION').read_text().strip(), '0.3.1-beta.1')
        self.assertEqual((self.work / 'binary/docklane').read_bytes(), self.binary)

    def test_internal_version_ci_worker_license_and_archive_integrity(self):
        for name, value in [('VERSION', b'9.0.0'), ('release-ci-evidence.json', b'{"version":"0.3.1-beta.1","tag":"v0.3.0","head_sha":"bad"}'),
                            ('deployments/config.example.json', b'{"backup_worker_image":"docker-manager:v0.3.0"}'),
                            ('LICENSE', b'other license'), ('IMAGES.txt', b'[docker-manager:v0.3.0-amd64] sha256:' + b'1' * 64 + b' linux/amd64\n'), ('TOOL-VERSIONS.txt', b''), ('SECURITY-NOTES.md', b''),
                            ('unsafe-link', (tarfile.SYMTYPE, '/etc/passwd'))]:
            with self.subTest(metadata=name):
                shutil.rmtree(self.work)
                self.work.mkdir()
                self.make_slim_release()
                self.slim_materials[name] = value
                self.repack_slim()
                self.bash('version=v0.3.1-beta.1; resolve_release; verify_release', ok=False)
                self.assertFalse((self.work / 'binary.sha256').exists())

    def test_six_asset_manifest_must_list_both_archives_and_script(self):
        self.make_slim_release()
        manifest = self.assets / 'RELEASE-SHA256SUMS'
        manifest.write_text(''.join(line + '\n' for line in manifest.read_text().splitlines() if 'arm64' not in line))
        self.assertIn('other five assets', self.bash('version=v0.3.1-beta.1; resolve_release; verify_release', ok=False).stderr)

    def test_beta_pages_select_highest_numeric_published_beta(self):
        self.make_slim_release('v0.3.1-beta.10')
        latest = self.slim_metadata
        older = {**latest, 'tag_name': 'v0.3.1-beta.2'}
        draft = {**latest, 'tag_name': 'v99.0.0-beta.1', 'draft': True}
        (self.root / 'page1').write_text(json.dumps([older] * 99 + [draft]))
        (self.root / 'page2').write_text(json.dumps([{**latest, 'tag_name': 'v0.3.1-beta.99', 'published_at': None}, latest]))
        prelude = f"""
fetch() {{
 printf 'fetch %s\\n' "$1" >> "$trace"
 if [[ $1 == *'per_page=100&page=1' ]]; then cp {quote(self.root / 'page1')} "$2"
 elif [[ $1 == *'per_page=100&page=2' ]]; then cp {quote(self.root / 'page2')} "$2"
 else cp "$assets/${{1##*/}}" "$2"; fi
}}
"""
        self.bash('channel=beta; resolve_release; [[ $version == v0.3.1-beta.10 ]]; verify_release', prelude=prelude)
        self.assertIn('page=2', self.trace.read_text())

    def test_explicit_version_overrides_channel_but_checks_prerelease_status(self):
        self.make_slim_release()
        self.bash('channel=stable; version=v0.3.1-beta.1; resolve_release; verify_release')
        shutil.rmtree(self.work)
        self.work.mkdir()
        self.change_release(lambda release: release.update(prerelease=False))
        self.bash('channel=beta; version=v0.3.1-beta.1; resolve_release', ok=False)

    def test_nonboolean_or_missing_draft_is_never_a_published_release(self):
        for value in (None, 0, '', True, 'false', 'missing'):
            with self.subTest(draft=value):
                shutil.rmtree(self.work)
                self.work.mkdir()
                self.make_slim_release()
                if value == 'missing':
                    self.slim_metadata.pop('draft')
                else:
                    self.slim_metadata['draft'] = value
                (self.assets / 'release.json').write_text(json.dumps(self.slim_metadata))
                self.bash('version=v0.3.1-beta.1; resolve_release', ok=False)
                (self.assets / 'release.json').write_text(json.dumps([self.slim_metadata]))
                self.bash('channel=beta; resolve_release', prelude='fetch() { cp "$assets/release.json" "$2"; }', ok=False)

    def test_beta_metadata_failure_never_falls_back_to_web(self):
        result = self.bash('channel=beta; resolve_release', prelude='fetch() { return 22; }; fetch_release_page() { echo unwanted-web >&2; }', ok=False)
        self.assertIn('Beta release metadata is unavailable', result.stderr)
        self.assertNotIn('unwanted-web', result.stderr)
        result = self.bash('version=v0.3.1-beta.1; resolve_release', prelude='fetch() { return 22; }; fetch_release_page() { echo unwanted-web >&2; }', ok=False)
        self.assertNotIn('unwanted-web', result.stderr)

    def test_implicit_downgrade_is_rejected_for_legacy_bootstrap(self):
        receipt = self.root / 'receipt'
        receipt.mkdir()
        (receipt / 'release.json').write_text(json.dumps({'version': 'v0.3.1-beta.10'}))
        self.assertIn('downgrade', self.bash('version=v0.3.0; check_selected_downgrade', ok=False).stderr)
        self.bash('version=v0.3.0; explicit_version=true; check_selected_downgrade')
        self.bash('version=v0.3.1; check_selected_downgrade')
        self.assertIn('downgrade', self.bash('version=v0.3.1-beta.2; check_selected_downgrade', ok=False).stderr)

    def change_release(self, change):
        p = self.assets / 'release.json'
        data = json.loads(p.read_text())
        change(data)
        p.write_text(json.dumps(data))

    def test_supported_platform_matrix(self):
        for distro, version in [('ubuntu', '24.04'), ('debian', '12'), ('debian', '13')]:
            for machine, expected in [('x86_64', 'amd64'), ('aarch64', 'arm64'), ('arm64', 'arm64')]:
                with self.subTest(distro=distro, machine=machine):
                    self.os_release.write_text(f'ID={distro}\nVERSION_ID={version}\n')
                    out = self.bash('platform; echo "$arch"', prelude=
                        f'uname() {{ case "$1" in -s) echo Linux ;; -m) echo {machine} ;; esac; }}')
                    self.assertEqual(out.stdout.strip(), expected)

    def test_unsupported_platform_arch_root_and_systemd(self):
        for prelude, message in [
            ('uname() { echo Darwin; }', 'Only Linux'),
            ('uname() { case "$1" in -s) echo Linux;; -m) echo riscv64;; esac; }', 'Only amd64'),
            ('id() { echo 1000; }', 'root'),
        ]:
            with self.subTest(message=message):
                self.assertIn(message, self.bash('platform', prelude=prelude, ok=False).stderr)
        self.os_release.write_text('ID=ubuntu\nVERSION_ID=22.04\n')
        self.assertIn('Unsupported system', self.bash('platform', ok=False).stderr)
        self.os_release.write_text('ID=debian\nVERSION_ID=13\n')
        self.systemd.rmdir()
        self.assertIn('systemd', self.bash('platform', ok=False).stderr)

    def test_help_and_invalid_arguments_do_not_touch_system(self):
        self.assertIn('--migrate-from', self.bash('main --help').stdout)
        for args in ['--unknown', '--version', '--version latest', '--version v0.3.0-rc1',
                     '--migrate-from relative', '--config relative', '--listen --dry-run',
                     '--download-source', '--download-source https://evil.invalid']:
            with self.subTest(args=args):
                self.bash('main ' + args, ok=False)
        self.assertFalse(self.trace.exists())

    def test_dry_run_has_no_install_or_download(self):
        result = self.bash('main --dry-run --version v0.3.0 --listen :9090 '
                           '--migrate-from /old/data --config /old/config.json')
        self.assertIn(':9090', result.stdout)
        self.assertIn('/old/data', result.stdout)
        self.assertIn('No changes made', result.stdout)
        self.assertFalse(self.trace.exists())
        self.assertEqual(list(self.work.iterdir()), [])

    def test_download_source_dry_run_and_default(self):
        self.assertIn('source: mirror', self.bash('main --dry-run').stdout)
        self.assertIn('source: direct', self.bash('main --dry-run --download-source direct').stdout)
        self.assertFalse(self.trace.exists())

    def transport_mock(self, failures=0, effective=''):
        return f'''
download_source=mirror
attempt=0
curl() {{
  local output= url=${{!#}}
  printf '%s\\n' "$url" >> "$trace"
  while (($#)); do
    if [[ $1 == --output ]]; then output=$2; shift; fi
    shift
  done
  # Count via the trace: curl runs in a command substitution subshell.
  attempt=$(wc -l < "$trace")
  printf 'partial' > "$output"
  (( attempt > {failures} )) || return 22
  printf 'complete' > "$output"
  printf '%s' {quote(effective) if effective else '"$url"'}
}}
'''

    def test_release_compose_restic_and_raw_use_mirror(self):
        for url in [
            f'https://github.com/Funnyrz/docklane/releases/download/{TAG}/LICENSE',
            'https://github.com/docker/compose/releases/download/v2.40.3/checksums.txt',
            'https://github.com/restic/restic/releases/download/v0.19.1/SHA256SUMS',
            'https://raw.githubusercontent.com/Funnyrz/docklane/main/scripts/install-online.sh',
        ]:
            with self.subTest(url=url):
                self.trace.unlink(missing_ok=True)
                self.bash(f'fetch_url {quote(url)} "$work/payload"', prelude=self.transport_mock())
                self.assertEqual(self.trace.read_text().splitlines(), ['https://gh-proxy.com/' + url])
                self.assertEqual((self.work / 'payload').read_text(), 'complete')
                self.assertFalse((self.work / 'payload.part').exists())

    def test_mirror_fallback_replaces_partial_then_uses_direct(self):
        url = f'https://github.com/Funnyrz/docklane/releases/download/{TAG}/LICENSE'
        self.bash(f'fetch_url {quote(url)} "$work/payload"', prelude=self.transport_mock(failures=2))
        self.assertEqual(self.trace.read_text().splitlines(),
                         ['https://gh-proxy.com/' + url, 'https://ghfast.top/' + url, url])
        self.assertEqual((self.work / 'payload').read_text(), 'complete')
        self.assertFalse((self.work / 'payload.part').exists())

    def test_all_transport_failures_remove_partial_and_return_nonzero(self):
        self.bash('fetch_url https://github.com/a/b/releases/download/v1/file "$work/payload"',
                  prelude=self.transport_mock(failures=3), ok=False)
        self.assertEqual(len(self.trace.read_text().splitlines()), 3)
        self.assertFalse((self.work / 'payload').exists())
        self.assertFalse((self.work / 'payload.part').exists())

    def test_api_fallback_skips_unsupported_secondary_proxy(self):
        url = 'https://api.github.com/repos/Funnyrz/docklane/releases/latest'
        self.bash(f'fetch_url {quote(url)} "$work/payload"', prelude=self.transport_mock(failures=1))
        self.assertEqual(self.trace.read_text().splitlines(), ['https://gh-proxy.com/' + url, url])

    def test_direct_and_other_hosts_bypass_mirrors(self):
        for url, setting in [
            ('https://github.com/a/b/file', 'direct'),
            ('https://download.docker.com/linux/ubuntu/gpg', 'mirror'),
            ('https://github.com.evil.invalid/file', 'mirror'),
        ]:
            with self.subTest(url=url):
                self.trace.unlink(missing_ok=True)
                self.bash(f'download_source={setting}; fetch_url {quote(url)} "$work/payload"',
                          prelude=self.transport_mock())
                self.assertEqual(self.trace.read_text().splitlines(), [url])

    def test_mirror_latest_redirect_is_normalized_then_validated(self):
        redirect = f'https://ghfast.top/https://github.com/Funnyrz/docklane/releases/tag/{TAG}'
        prelude = self.transport_mock(failures=1, effective=redirect)
        self.bash('resolve_release_web; verify_release', prelude=prelude)
        self.assertEqual((self.work / 'release-page-url').read_text(),
                         f'https://github.com/Funnyrz/docklane/releases/tag/{TAG}')
        self.assertEqual((self.work / 'binary/docklane').read_bytes(), self.binary)

    def test_mirror_redirect_cannot_smuggle_external_release_host(self):
        result = self.bash('resolve_release_web', prelude=self.transport_mock(
            effective=f'https://gh-proxy.com/https://evil.invalid/Funnyrz/docklane/releases/tag/{TAG}'), ok=False)
        self.assertIn('did not resolve to a stable Docklane release', result.stderr)
        self.assertFalse((self.work / 'binary').exists())

    def test_nested_install_failure_has_clear_error_message(self):
        result = self.bash('main', prelude='platform() { :; }; bootstrap_tools() { false; }', ok=False)
        self.assertIn('installation failed', result.stderr)
        self.assertIn('source data and recovery backups are retained', result.stderr)

    def test_latest_resolves_once_and_downloads_pinned_assets(self):
        self.bash('resolve_release; [[ $version == v0.3.0 ]]; verify_release')
        calls = self.trace.read_text().splitlines()
        self.assertEqual(sum('api.github.com' in line for line in calls), 1)
        self.assertIn('/releases/latest', calls[0])
        self.assertTrue(all('/download/v0.3.0/' in line for line in calls[1:]))
        self.assertEqual((self.work / 'binary/docklane').read_bytes(), self.binary)
        self.assertEqual((self.work / 'binary.sha256').read_text(), hashlib.sha256(self.binary).hexdigest())

    def test_explicit_release_is_fixed(self):
        self.bash('version=v0.3.0; resolve_release; verify_release')
        self.assertIn('/releases/tags/v0.3.0', self.trace.read_text().splitlines()[0])

    def api_unavailable_prelude(self, redirect=None, *, failed_asset='', web_failure=False):
        redirect = redirect or f'https://github.com/Funnyrz/docklane/releases/tag/{TAG}'
        return f'''
failed_asset={quote(failed_asset)}
fetch() {{
  printf 'fetch %s\\n' "$1" >> "$trace"
  if [[ $1 == https://api.github.com/* ]]; then
    printf 'rate limited' > "$2"
    return 22
  fi
  [[ ${{1##*/}} != "$failed_asset" ]] || {{ printf partial > "$2"; return 22; }}
  [[ $1 == https://github.com/Funnyrz/docklane/releases/download/* ]] || return 90
  cp "$assets/${{1##*/}}" "$2"
}}
curl() {{
  printf 'web-curl %s\\n' "$*" >> "$trace"
  [[ ${{!#}} == https://github.com/Funnyrz/docklane/releases/latest ]] || return 91
  {'return 22' if web_failure else ':'}
  local output=
  while (($#)); do
    if [[ $1 == --output ]]; then output=$2; shift; fi
    shift
  done
  printf '<html>untrusted v9.9.9 prerelease draft</html>' > "$output"
  printf '%s' {quote(redirect)}
}}
'''

    def test_rate_limited_api_uses_one_web_redirect_and_pins_all_assets(self):
        self.bash('resolve_release; [[ $version == v0.3.0 ]]; verify_release',
                  prelude=self.api_unavailable_prelude())
        calls = self.trace.read_text().splitlines()
        self.assertEqual(sum('api.github.com' in line for line in calls), 1)
        self.assertEqual(sum(line.startswith('web-curl ') for line in calls), 1)
        self.assertIn('--write-out %{url_effective}', calls[1])
        self.assertIn("--proto =https --proto-redir =https", calls[1])
        self.assertTrue(all('/download/v0.3.0/' in line for line in calls[2:]))
        self.assertEqual((self.work / 'binary/docklane').read_bytes(), self.binary)
        self.assertNotIn('v9.9.9', (self.work / 'release-selection').read_text())

    def test_rate_limited_explicit_latest_is_allowed(self):
        self.bash('version=v0.3.0; resolve_release; verify_release',
                  prelude=self.api_unavailable_prelude())
        calls = self.trace.read_text().splitlines()
        self.assertIn('/releases/tags/v0.3.0', calls[0])
        self.assertEqual(sum(line.startswith('web-curl ') for line in calls), 1)

    def test_rate_limited_explicit_older_version_is_safely_rejected(self):
        result = self.bash('version=v0.2.0; resolve_release',
                           prelude=self.api_unavailable_prelude(), ok=False)
        self.assertIn('only the current formal latest can be verified', result.stderr)
        self.assertEqual(len(self.trace.read_text().splitlines()), 2)
        self.assertFalse((self.work / 'binary').exists())

    def test_web_redirect_rejects_untrusted_or_nonstable_release_urls(self):
        invalid = [
            'https://github.com/Other/docklane/releases/tag/v0.3.0',
            'https://github.com/Funnyrz/other/releases/tag/v0.3.0',
            'https://evil.invalid/Funnyrz/docklane/releases/tag/v0.3.0',
            'http://github.com/Funnyrz/docklane/releases/tag/v0.3.0',
            'https://github.com/Funnyrz/docklane/releases/tag/v0.3.0-rc1',
            'https://github.com/Funnyrz/docklane/releases/tag/draft',
            'https://github.com/Funnyrz/docklane/releases/new?draft=true',
            'https://github.com/Funnyrz/docklane/releases/latest',
            'https://github.com/Funnyrz/docklane/releases/tag/v0.3.0?prerelease=false',
            'https://github.com/Funnyrz/docklane/releases/tag/v0.3.0/extra',
        ]
        for url in invalid:
            with self.subTest(url=url):
                self.trace.unlink(missing_ok=True)
                result = self.bash('resolve_release', prelude=self.api_unavailable_prelude(url), ok=False)
                self.assertIn('did not resolve to a stable Docklane release', result.stderr)
                self.assertEqual(len(self.trace.read_text().splitlines()), 2)

    def test_web_fallback_failure_does_not_download_assets(self):
        self.bash('resolve_release', prelude=self.api_unavailable_prelude(web_failure=True), ok=False)
        self.assertEqual(len(self.trace.read_text().splitlines()), 2)
        self.assertFalse((self.work / 'binary').exists())

    def test_web_fallback_asset_download_failure_stops(self):
        self.bash('resolve_release; echo unexpected-verification',
                  prelude=self.api_unavailable_prelude(failed_asset='LICENSE'), ok=False)
        self.assertFalse((self.work / 'binary').exists())
        self.assertFalse((self.work / 'SHA256SUMS').exists())

    def test_web_fallback_keeps_checksum_verification(self):
        (self.assets / 'LICENSE').write_bytes(b'corrupted after release checksums')
        result = self.bash('resolve_release; verify_release',
                           prelude=self.api_unavailable_prelude(), ok=False)
        self.assertIn('Checksum mismatch: LICENSE', result.stderr)
        self.assertFalse((self.work / 'binary').exists())

    def test_valid_api_metadata_rejection_does_not_use_web_fallback(self):
        self.change_release(lambda release: release.update(draft=True))
        result = self.bash('resolve_release', prelude='''
fetch_release_page() { echo 'unexpected web fallback' >&2; exit 90; }
''', ok=False)
        self.assertIn('No published release matches', result.stderr)
        self.assertNotIn('unexpected web fallback', result.stderr)
        self.assertEqual(len(self.trace.read_text().splitlines()), 1)

    def test_arm64_binary(self):
        self.make_release('arm64')
        self.bash('arch=arm64; resolve_release; verify_release')

    def test_unstable_missing_duplicate_or_redirected_assets_rejected(self):
        changes = [lambda r: r.update(draft=True), lambda r: r.update(prerelease=True),
                   lambda r: r.update(tag_name='v0.3.0-rc1'),
                   lambda r: r['assets'].pop(),
                   lambda r: r['assets'].append(r['assets'][0].copy()),
                   lambda r: r['assets'][0].update(browser_download_url='https://evil.invalid/payload'),
                   lambda r: r['assets'][0].update(state='new')]
        original = (self.assets / 'release.json').read_text()
        for change in changes:
            with self.subTest(change=changes.index(change)):
                (self.assets / 'release.json').write_text(original)
                self.change_release(change)
                self.bash('resolve_release', ok=False)
        (self.assets / 'release.json').write_text(original)
        self.bash('version=v9.9.9; resolve_release', ok=False)

    def test_download_interruption_aborts_before_verification(self):
        self.bash('resolve_release; echo reached-verification', prelude='''
original_fetch=$(declare -f fetch)
eval "${original_fetch/fetch ()/fixture_fetch ()}"
fetch() { if [[ $1 == *LICENSE ]]; then printf partial > "$2"; return 22; fi; fixture_fetch "$@"; }
''', ok=False)
        self.assertFalse((self.work / 'binary').exists())

    def test_corrupt_asset_and_unsafe_manifest_rejected(self):
        self.bash('resolve_release; printf corrupt >> "$work/LICENSE"; verify_release', ok=False)
        self.assertFalse((self.work / 'binary').exists())
        shutil.rmtree(self.work)
        self.work.mkdir()
        self.bash('resolve_release; printf "bad checksum\\n" > "$work/RELEASE-SHA256SUMS"; verify_release', ok=False)

    def test_duplicate_manifest_and_inconsistent_sidecar(self):
        self.bash('resolve_release; cat "$work/RELEASE-SHA256SUMS" >> "$work/duplicate"; '
                  'cat "$work/duplicate" >> "$work/RELEASE-SHA256SUMS"; verify_release', ok=False)
        shutil.rmtree(self.work)
        self.work.mkdir()
        (self.assets / (self.binary_name + '.sha256')).write_text('0' * 64 + '  ' + self.binary_name + '\n')
        paths = sorted(p for p in self.assets.iterdir() if p.name not in ('release.json', 'RELEASE-SHA256SUMS'))
        (self.assets / 'RELEASE-SHA256SUMS').write_text(''.join(f'{checksum(p)}  {p.name}\n' for p in paths))
        result = self.bash('resolve_release; verify_release', ok=False)
        self.assertIn('Inconsistent manifest', result.stderr)

    def test_raw_binary_manifest_must_match_slim_archive_and_license(self):
        for bad_sums in [
            '0' * 64 + '  docklane-linux-amd64\n' + hashlib.sha256(self.license).hexdigest() + '  LICENSE\n',
            hashlib.sha256(self.binary).hexdigest() + '  docklane-linux-amd64\n' + '0' * 64 + '  LICENSE\n',
            hashlib.sha256(self.binary).hexdigest() + '  docklane-linux-arm64\n' + hashlib.sha256(self.license).hexdigest() + '  LICENSE\n',
        ]:
            with self.subTest(sums=bad_sums):
                shutil.rmtree(self.work)
                self.work.mkdir()
                (self.assets / 'SHA256SUMS').write_text(bad_sums)
                self.rehash()
                result = self.bash('resolve_release; verify_release', ok=False)
                self.assertIn('Binary checksum manifest disagrees with archive', result.stderr)
                self.assertFalse((self.work / 'binary.sha256').exists())

    def test_unsafe_binary_archives(self):
        for extra in [{'../escaped': b'bad'}, {'/absolute': b'bad'},
                      {'link': (tarfile.SYMTYPE, '/etc/passwd')},
                      {'hardlink': (tarfile.LNKTYPE, 'docklane')},
                      {'fifo': (tarfile.FIFOTYPE, '')}]:
            with self.subTest(extra=extra):
                shutil.rmtree(self.work)
                self.work.mkdir()
                self.make_release(binary_members={'docklane': self.binary, 'LICENSE': self.license, **extra})
                self.bash('resolve_release; verify_release', ok=False)
                self.assertFalse((self.root / 'escaped').exists())

    def test_unexpected_layout_and_wrong_elf(self):
        for members in [{'docklane': self.binary},
                        {'docklane': b'#!/bin/sh\nexit 0\n', 'LICENSE': self.license},
                        {'docklane': self.binary[:18] + struct.pack('<H', 183) + self.binary[20:], 'LICENSE': self.license},
                        {'docklane': self.binary, 'LICENSE': self.license, 'extra': b'file'}]:
            with self.subTest(members=list(members)):
                shutil.rmtree(self.work)
                self.work.mkdir()
                self.make_release(binary_members=members)
                self.bash('resolve_release; verify_release', ok=False)

    def test_missing_material_and_license_mismatch(self):
        self.make_release(material_members={'LICENSE': self.license})
        self.assertIn('Missing installation material', self.bash('resolve_release; verify_release', ok=False).stderr)
        shutil.rmtree(self.work)
        self.work.mkdir()
        self.make_release(binary_members={'docklane': self.binary, 'LICENSE': b'other license'})
        self.assertIn('License mismatch', self.bash('resolve_release; verify_release', ok=False).stderr)

    def test_existing_docker_preserved_without_packages_or_restart(self):
        for version in ['28.5.1', '29.5.2']:
            with self.subTest(version=version):
                result = self.bash('install_docker', prelude=f'''
docker() {{ :; }}
local_docker() {{ [[ "$*" == "version --format {{{{.Server.Version}}}}" ]] || return 91; echo {version}; }}
''')
                self.assertIn('Preserving existing Docker', result.stdout)
        self.assertFalse(self.trace.exists())

    def test_unhealthy_or_unsupported_existing_docker_fails(self):
        for implementation in ['return 1', 'echo 27.5.1']:
            self.bash('install_docker', prelude='docker() { :; }; local_docker() { ' + implementation + '; }', ok=False)
        self.assertFalse(self.trace.exists())

    def test_conflicting_dependency_does_not_mutate_host(self):
        result = self.bash('install_docker', prelude='''
command() { if [[ $1 == -v && $2 == docker ]]; then return 1; fi; builtin command "$@"; }
dpkg-query() { [[ ${!#} != docker.io ]] || echo 'install ok installed'; }
''', ok=False)
        self.assertIn('Conflicting installation docker.io', result.stderr)
        self.assertFalse(self.trace.exists())
        self.assertEqual(list((self.root / 'apt/sources.list.d').iterdir()), [])

    def test_bootstrap_installs_only_missing_base_dependencies(self):
        result = self.bash('bootstrap_tools', prelude='''
command() {
  if [[ $1 == -v && ( $2 == python3 || $2 == runuser ) ]]; then return 1; fi
  builtin command "$@"
}
''')
        self.assertEqual(result.stdout, '')
        calls = self.trace.read_text()
        self.assertIn('apt-get update', calls)
        self.assertIn('python3', calls)
        self.assertIn('util-linux', calls)
        self.assertIn('ca-certificates', calls)

    def test_new_docker_uses_official_source_and_pinned_29_candidate(self):
        self.bash('install_docker', prelude='''
command() { if [[ $1 == -v && $2 == docker ]]; then return 1; fi; builtin command "$@"; }
dpkg-query() { return 1; }
fetch() { [[ $1 == https://download.docker.com/linux/ubuntu/gpg ]] || return 90; echo 'fixture-key' > "$2"; }
apt-cache() { printf 'docker-ce | 5:30.0.0-1 | stable\\ndocker-ce | 5:29.5.2-1 | stable\\n'; }
local_docker() { [[ $1 == info ]]; }
''')
        calls = self.trace.read_text()
        self.assertIn('docker-ce=5:29.5.2-1 docker-ce-cli=5:29.5.2-1 containerd.io', calls)
        self.assertIn('systemctl enable --now docker', calls)
        source = (self.root / 'apt/sources.list.d/docklane-docker.sources').read_text()
        self.assertIn('URIs: https://download.docker.com/linux/ubuntu', source)
        self.assertIn('Suites: noble', source)
        self.assertIn('Architectures: amd64', source)

    def test_large_package_listing_is_drained_without_sigpipe(self):
        self.bash('install_docker', prelude='''
command() { if [[ $1 == -v && $2 == docker ]]; then return 1; fi; builtin command "$@"; }
dpkg-query() { return 1; }
fetch() { echo 'fixture-key' > "$2"; }
apt-cache() {
  python3 -c 'import sys; sys.stdout.write("docker-ce | 5:29.5.2-1 | stable\\n" + "docker-ce | 5:28.5.1-1 | stable\\n" * 25000)'
}
local_docker() { [[ $1 == info ]]; }
''')
        self.assertIn('docker-ce=5:29.5.2-1 docker-ce-cli=5:29.5.2-1 containerd.io', self.trace.read_text())

    def test_missing_supported_docker_version_never_installs_engine(self):
        self.bash('install_docker', prelude='''
command() { if [[ $1 == -v && $2 == docker ]]; then return 1; fi; builtin command "$@"; }
dpkg-query() { return 1; }
fetch() { echo 'fixture-key' > "$2"; }
apt-cache() { echo 'docker-ce | 5:30.0.0-1 | stable'; }
''', ok=False)
        calls = self.trace.read_text()
        self.assertNotIn('apt-get install', calls)
        self.assertNotIn('systemctl', calls)

    def test_missing_tools_download_and_verify_private_versions(self):
        compose_name = 'docker-compose-linux-x86_64'
        compose = self.assets / compose_name
        compose.write_bytes(b'#!/bin/sh\necho v2.40.3\n')
        (self.assets / 'checksums.txt').write_text(f'{checksum(compose)}  {compose_name}\n')
        restic_name = 'restic_0.19.1_linux_amd64.bz2'
        packed = self.assets / restic_name
        packed.write_bytes(bz2.compress(b'#!/bin/sh\necho "restic 0.19.1 fixture"\n'))
        (self.assets / 'RESTIC-SHA256SUMS').write_text(f'{checksum(packed)}  {restic_name}\n')
        plugin = self.root / 'docker-tools/cli-plugins/docker-compose'
        self.bash('install_tools', prelude=f'''
fetch() {{
  printf 'fetch %s\\n' "$1" >> "$trace"
  case "$1" in
    */v2.40.3/*) cp "$assets/${{1##*/}}" "$2" ;;
    */v0.19.1/SHA256SUMS) cp "$assets/RESTIC-SHA256SUMS" "$2" ;;
    */v0.19.1/*) cp "$assets/${{1##*/}}" "$2" ;;
    *) return 90 ;;
  esac
}}
local_docker() {{ [[ -x {quote(plugin)} ]] && {quote(plugin)}; }}
''')
        self.assertEqual(self.restic.read_bytes(), b'#!/bin/sh\necho "restic 0.19.1 fixture"\n')
        self.assertEqual(plugin.read_bytes(), compose.read_bytes())
        self.assertEqual(len(self.trace.read_text().splitlines()), 4)

    def test_tool_checksum_rejects_corruption_and_duplicates(self):
        tool = self.root / 'tool'
        tool.write_bytes(b'fixture executable')
        sums = self.root / 'sums'
        sums.write_text(f'{checksum(tool)}  tool-name\n')
        cmd = f'verify_tool {quote(sums)} tool-name {quote(tool)}'
        self.bash(cmd)
        sums.write_text(sums.read_text() * 2)
        self.bash(cmd, ok=False)
        sums.write_text('0' * 64 + '  tool-name\n')
        self.bash(cmd, ok=False)

    def test_existing_compose_and_private_restic_reused(self):
        self.restic.parent.mkdir(parents=True)
        self.restic.write_text('#!/bin/sh\necho "restic 0.19.1 fixture"\n')
        self.restic.chmod(0o755)
        self.bash('install_tools', prelude='local_docker() { echo v2.40.3; }')
        self.assertFalse(self.trace.exists())

    def test_incompatible_compose_is_not_overwritten(self):
        result = self.bash('install_tools', prelude='local_docker() { echo v1.29.0; }', ok=False)
        self.assertIn('incompatible', result.stderr)
        self.assertFalse(self.trace.exists())
        self.assertFalse(self.restic.exists())

    def worker_fixture(self, store='classic', *, graph='valid'):
        config = json.dumps({'architecture': 'amd64', 'os': 'linux',
                             'config': {'Env': [f'DM_BACKUP_WORKER_IMAGE=docker-manager:{TAG}']}}).encode()
        config_id = 'sha256:' + hashlib.sha256(config).hexdigest()
        legacy = store in ('classic-only', 'legacy-to-containerd')
        config_name = config_id[7:] + '.json' if legacy else 'blobs/sha256/' + config_id[7:]
        members = {config_name: config}
        native = f'docker-manager:{TAG}-amd64'
        members['manifest.json'] = json.dumps([{'Config': config_name,
                                              'RepoTags': [native], 'Layers': []}]).encode()
        referenced = 'sha256:' + 'f' * 64 if graph == 'wrong-config' else config_id
        descriptor = json.dumps({'config': {'digest': referenced}}).encode()
        descriptor_id = 'sha256:' + hashlib.sha256(descriptor).hexdigest()
        index = json.dumps({'manifests': [{'digest': descriptor_id}]}).encode()
        index_id = 'sha256:' + hashlib.sha256(index).hexdigest()
        if not legacy:
            members['blobs/sha256/' + descriptor_id[7:]] = descriptor
            members['blobs/sha256/' + index_id[7:]] = b'{}' if graph == 'corrupt-index' else index
        expected = config_id if legacy else index_id
        (self.work / 'IMAGES.txt').write_text(f'[{native}] {expected} linux/amd64\n')
        archive(self.work / 'docker-manager-images.tar.gz', members)
        # docker image save always produces a plain tar, and stores may rewrite
        # metadata filenames and inspect IDs while preserving exact config bytes.
        self.loaded_archive = self.root / 'loaded-worker.tar'
        self.loaded_config = config
        self.loaded_native = native
        self.write_loaded_worker(config)
        identity = index_id if store == 'containerd' else config_id
        if store == 'legacy-to-containerd':
            identity = 'sha256:' + '2' * 64
        worker = [{'Id': identity, 'Architecture': 'amd64', 'Os': 'linux',
                   'Config': {'Env': [f'DM_BACKUP_WORKER_IMAGE=docker-manager:{TAG}']}}]
        fixture = self.root / 'worker.json'
        fixture.write_text(json.dumps(worker))
        return fixture, worker

    def write_loaded_worker(self, config):
        config_name = 'exported-config.json'
        members = {config_name: config, 'manifest.json': json.dumps([
            {'Config': config_name, 'RepoTags': [self.loaded_native], 'Layers': []}
        ]).encode()}
        archive(self.loaded_archive, members, compressed=False)

    def worker_prelude(self, fixture):
        return f'''
version={TAG}
local_docker() {{
  printf 'docker %s\\n' "$*" >> "$trace"
  if [[ "$1 $2" == 'image inspect' ]]; then cat {quote(fixture)}; fi
  if [[ "$1 $2" == 'image save' ]]; then cp {quote(self.loaded_archive)} "$4"; fi
}}
'''

    def test_worker_classic_and_containerd_stores(self):
        for store in ('classic-only', 'classic', 'containerd', 'legacy-to-containerd'):
            with self.subTest(store=store):
                fixture, _ = self.worker_fixture(store)
                self.bash('install_worker', prelude=self.worker_prelude(fixture))
                self.assertIn(f'docker tag docker-manager:{TAG}-amd64 docker-manager:{TAG}', self.trace.read_text())
                self.trace.unlink()

    def test_worker_rejects_index_with_wrong_graph_or_corrupt_digest(self):
        for graph in ('wrong-config', 'corrupt-index'):
            with self.subTest(graph=graph):
                fixture, _ = self.worker_fixture('containerd', graph=graph)
                self.bash('install_worker', prelude=self.worker_prelude(fixture), ok=False)
                self.assertNotIn('docker tag', self.trace.read_text())
                self.trace.unlink()

    def test_worker_rejects_loaded_content_with_different_config(self):
        fixture, _ = self.worker_fixture('legacy-to-containerd')
        altered = json.loads(self.loaded_config)
        altered['config']['Labels'] = {'unexpected': 'different-content'}
        self.write_loaded_worker(json.dumps(altered).encode())
        result = self.bash('install_worker', prelude=self.worker_prelude(fixture), ok=False)
        self.assertIn('Loaded worker content differs from verified release', result.stderr)
        self.assertNotIn('docker tag', self.trace.read_text())

    def test_worker_identity_architecture_and_version(self):
        fixture, worker = self.worker_fixture()
        prelude = self.worker_prelude(fixture)
        for field, value in [('Id', 'not-a-valid-sha256'), ('Architecture', 'arm64'), ('Os', 'windows'),
                             ('Config', {'Env': ['DM_BACKUP_WORKER_IMAGE=docker-manager:v0.2.0']})]:
            with self.subTest(field=field):
                bad = json.loads(json.dumps(worker))
                bad[0][field] = value
                fixture.write_text(json.dumps(bad))
                self.bash('install_worker', prelude=prelude, ok=False)
                self.assertNotIn('docker tag', self.trace.read_text())
                self.trace.unlink()

    def test_local_docker_ignores_remote_context(self):
        commands = self.root / 'commands'
        commands.mkdir()
        docker = commands / 'docker'
        docker.write_text('#!/bin/sh\n'
                          '[ -z "${DOCKER_HOST:-}${DOCKER_CONTEXT:-}${DOCKER_TLS_VERIFY:-}${DOCKER_CERT_PATH:-}" ] || exit 91\n'
                          'printf "%s\\n" "$*"\n')
        docker.chmod(0o755)
        result = self.bash('local_docker info', prelude=f'''
export DOCKER_HOST=tcp://remote:2375 DOCKER_CONTEXT=remote DOCKER_TLS_VERIFY=1 DOCKER_CERT_PATH=/secret
export PATH={quote(commands)}:"$PATH"
''')
        self.assertEqual(result.stdout.strip(), '--host unix:///var/run/docker.sock info')


if __name__ == '__main__':
    unittest.main(verbosity=2)
