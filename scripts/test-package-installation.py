#!/usr/bin/env python3
"""Exercise release packaging with isolated, fake build outputs (no Docker/GitHub writes)."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import textwrap
import unittest


class InstallationPackageTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.output = self.root / 'release'
        self.output.mkdir()
        paths = [
            'scripts/install-online.sh', 'scripts/install-support.py', 'scripts/install.sh',
            'scripts/upgrade.sh', 'scripts/uninstall.sh', 'deployments/docker-manager.service',
            'README.md', 'README.en.md', 'LICENSE', 'docker-manager-prd-v0.3.md',
            'docs/security-notes.md', 'docs/images/zh/promo.png',
            'web/qa/release-browser-results.json', '.tool-versions',
        ]
        for name in paths:
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(('source ' + name + '\n').encode())
        (self.root / 'web/package.json').write_text(json.dumps({'version': '0.3.0'}))
        shutil.copyfile(Path(__file__).with_name('package-installation.sh'),
                        self.root / 'scripts/package-installation.sh')
        for arch in ('amd64', 'arm64'):
            name = 'docklane-linux-' + arch
            (self.output / name).write_bytes(('binary ' + arch).encode())
            archive = self.output / f'docklane-v0.3.0-linux-{arch}.tar.gz'
            archive.write_bytes(('archive ' + arch).encode())
            self.manifest(archive.name + '.sha256', [archive.name])
        (self.output / 'LICENSE').write_bytes((self.root / 'LICENSE').read_bytes())
        self.manifest('SHA256SUMS', ['docklane-linux-amd64', 'docklane-linux-arm64', 'LICENSE'])
        (self.output / 'docker-manager-images.tar.gz').write_bytes(b'fake docker archive')
        self.manifest('IMAGE-SHA256SUMS', ['docker-manager-images.tar.gz'])
        (self.output / 'IMAGES.txt').write_text('version-matched images\n')
        (self.output / 'release-ci-evidence.json').write_text('{"head_sha":"fixture"}\n')

    def manifest(self, name, names):
        (self.output / name).write_text(''.join(
            hashlib.sha256((self.output / item).read_bytes()).hexdigest() + '  ' + item + '\n'
            for item in names))

    def package(self, ok=True):
        result = subprocess.run(['sh', str(self.root / 'scripts/package-installation.sh')],
                                env={**os.environ, 'RELEASE_OUTPUT_DIR': str(self.output)},
                                text=True, capture_output=True)
        self.assertEqual(result.returncode == 0, ok, result.stdout + result.stderr)
        return result

    def test_package_preserves_sources_and_covers_every_asset(self):
        (self.root / 'docs/.DS_Store').write_bytes(b'excluded')
        self.package()
        with tarfile.open(self.output / 'docker-manager-installation.tar.gz') as bundle:
            names = bundle.getnames()
            self.assertIn('scripts/install-support.py', names)
            self.assertIn('deployments/docker-manager.service', names)
            self.assertNotIn('docs/.DS_Store', names)
            for member in bundle.getmembers():
                self.assertFalse(member.name.startswith('/') or '..' in Path(member.name).parts)
                if member.isfile():
                    self.assertEqual(bundle.extractfile(member).read(), (self.root / member.name).read_bytes())
        manifest = self.output / 'RELEASE-SHA256SUMS'
        listed = set()
        for line in manifest.read_text().splitlines():
            digest, name = line.split('  ')
            listed.add(name)
            self.assertEqual(digest, hashlib.sha256((self.output / name).read_bytes()).hexdigest())
        self.assertEqual(listed, {path.name for path in self.output.iterdir()} - {manifest.name})
        self.assertEqual((self.output / 'install-online.sh').read_bytes(),
                         (self.root / 'scripts/install-online.sh').read_bytes())
        self.package()  # Repackaging the same candidate is supported.

    def test_missing_source_fails_before_archive_creation(self):
        (self.root / 'scripts/install-support.py').unlink()
        self.package(ok=False)
        self.assertFalse((self.output / 'docker-manager-installation.tar.gz').exists())

    def test_symlink_source_rejected(self):
        (self.root / 'docs/external').symlink_to(self.root / 'LICENSE')
        self.package(ok=False)

    def test_checksum_mismatch_rejected(self):
        (self.output / 'docklane-linux-amd64').write_bytes(b'corrupt binary')
        self.package(ok=False)
        self.assertFalse((self.output / 'docker-manager-installation.tar.gz').exists())

    def test_unsafe_checksum_path_rejected(self):
        (self.output / 'SHA256SUMS').write_text('0' * 64 + '  ../LICENSE\n')
        self.package(ok=False)

    def test_unsafe_asset_rejected(self):
        (self.output / 'external').symlink_to(self.root / 'LICENSE')
        self.package(ok=False)
        self.assertFalse((self.output / 'RELEASE-SHA256SUMS').exists())

    def test_unknown_release_version_rejected(self):
        (self.root / 'web/package.json').write_text('{"version":"0.4.0"}')
        self.package(ok=False)

    def publish(self, **overrides):
        sha = 'a' * 40
        (self.output / 'release-ci-evidence.json').write_text(json.dumps({
            'head_sha': sha, 'ci_run_url': 'https://github.com/example/project/actions/runs/1'}))
        self.package()
        shutil.copytree(self.output, self.root / 'candidate')
        workflow = Path(__file__).resolve().parents[1] / '.github/workflows/release-prepare.yml'
        source = workflow.read_text().split('        shell: python\n        run: |\n', 1)[1]
        publisher = self.root / 'publish.py'
        publisher.write_text(textwrap.dedent(source))
        tools = self.root / 'tools'
        tools.mkdir()
        mock = tools / 'gh'
        mock.write_text('#!/usr/bin/env python3\n' + textwrap.dedent('''\
            import json, os, pathlib, shutil, sys
            root = pathlib.Path(os.environ['FIXTURE_ROOT'])
            args = sys.argv[1:]
            with (root / 'gh-calls.jsonl').open('a') as stream:
                stream.write(json.dumps(args) + '\\n')
            if args[0] == 'api':
                endpoint = args[-1]
                if '/git/ref/tags/' in endpoint:
                    counter = root / 'tag-checks'
                    count = int(counter.read_text()) + 1 if counter.exists() else 1
                    counter.write_text(str(count))
                    changed = os.environ.get('MOVED_TAG') or (os.environ.get('MOVED_AFTER_UPLOAD') and count > 1)
                    print(json.dumps({'object': {'type': 'commit', 'sha': ('b' if changed else 'a') * 40}}))
                elif endpoint.endswith('/releases?per_page=100'):
                    releases = [{'tag_name': 'v0.3.0'}] if os.environ.get('EXISTING_RELEASE') else []
                    print(json.dumps([releases]))
                elif '/releases/tags/' in endpoint:
                    print(json.dumps({'draft': False, 'prerelease': False, 'html_url': 'https://example.invalid/release'}))
                else:
                    raise SystemExit('Unexpected API call: ' + endpoint)
            elif args[:2] == ['release', 'create']:
                (root / 'draft-created').touch()
            elif args[:2] == ['release', 'upload']:
                remote = root / 'remote'
                remote.mkdir()
                for name in args[5:]:
                    shutil.copyfile(name, remote / pathlib.Path(name).name)
                if os.environ.get('CORRUPT_UPLOAD'):
                    (remote / 'docklane-linux-amd64').write_bytes(b'corrupt')
            elif args[:2] == ['release', 'download']:
                target = pathlib.Path(args[args.index('--dir') + 1])
                for path in (root / 'remote').iterdir():
                    shutil.copyfile(path, target / path.name)
            elif args[:2] == ['release', 'edit']:
                (root / 'published').touch()
            else:
                raise SystemExit('Unexpected gh call: ' + str(args))
            '''))
        mock.chmod(0o755)
        return subprocess.run(['python3', str(publisher)], cwd=self.root, text=True,
                              capture_output=True, env={**os.environ, 'GH_REPO': 'example/project',
                              'RELEASE_TAG': 'v0.3.0', 'SOURCE_SHA': sha,
                              'RUNNER_TEMP': str(self.root), 'FIXTURE_ROOT': str(self.root),
                              'PATH': str(tools) + os.pathsep + os.environ['PATH'], **overrides})

    def test_publish_verifies_upload_before_formal_release(self):
        result = self.publish()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue((self.root / 'draft-created').exists())
        self.assertTrue((self.root / 'published').exists())
        calls = [json.loads(line) for line in (self.root / 'gh-calls.jsonl').read_text().splitlines()]
        self.assertLess(next(i for i, args in enumerate(calls) if args[:2] == ['release', 'download']),
                        next(i for i, args in enumerate(calls) if args[:2] == ['release', 'edit']))

    def test_publish_refuses_existing_release(self):
        result = self.publish(EXISTING_RELEASE='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Release already exists', result.stderr)
        self.assertFalse((self.root / 'draft-created').exists())

    def test_publish_refuses_moved_tag(self):
        result = self.publish(MOVED_TAG='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / 'draft-created').exists())

    def test_failed_upload_verification_retains_draft(self):
        result = self.publish(CORRUPT_UPLOAD='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Checksum mismatch', result.stderr)
        self.assertTrue((self.root / 'draft-created').exists())
        self.assertFalse((self.root / 'published').exists())

    def test_tag_moved_after_upload_is_not_published(self):
        result = self.publish(MOVED_AFTER_UPLOAD='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue((self.root / 'draft-created').exists())
        self.assertFalse((self.root / 'published').exists())


if __name__ == '__main__':
    unittest.main()
