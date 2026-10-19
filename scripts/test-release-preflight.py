#!/usr/bin/env python3
"""Read-only publication readiness tests; all GitHub and git calls are mocked."""
import base64
import contextlib
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


spec = importlib.util.spec_from_file_location('release_preflight', Path(__file__).with_name('release-preflight.py'))
preflight = importlib.util.module_from_spec(spec)
spec.loader.exec_module(preflight)


class ReleasePreflightTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        (self.root / 'web').mkdir()
        (self.root / 'web/package.json').write_text('{"version":"0.3.0"}')
        self.artifacts = self.root / 'candidate'
        self.artifacts.mkdir()
        self.sha = 'a' * 40
        self.tag_sha = self.sha
        self.workflow = (Path(__file__).resolve().parents[1] / '.github/workflows/release-prepare.yml').read_text()
        self.private = False
        self.releases = [[]]
        self.ci = [{'id': 123, 'head_sha': self.sha, 'status': 'completed',
                    'conclusion': 'success', 'event': 'push', 'html_url': 'https://example.invalid/ci'}]
        for name in preflight.release_assets('v0.3.0'):
            (self.artifacts / name).write_text('asset ' + name + '\n')
        for arch, machine in [('amd64', 62), ('arm64', 183)]:
            header = bytearray(20)
            header[:6] = b'\x7fELF\x02\x01'
            header[18:20] = machine.to_bytes(2, 'little')
            (self.artifacts / ('docklane-linux-' + arch)).write_bytes(header)
            archive = f'docklane-v0.3.0-linux-{arch}.tar.gz'
            self.manifest(archive + '.sha256', [archive])
        (self.artifacts / 'release-ci-evidence.json').write_text(json.dumps({
            'head_sha': self.sha, 'ci_run_id': 123, 'ci_run_url': 'https://example.invalid/ci'}))
        self.manifest('SHA256SUMS', ['docklane-linux-amd64', 'docklane-linux-arm64', 'LICENSE'])
        self.manifest('IMAGE-SHA256SUMS', ['docker-manager-images.tar.gz'])
        self.refresh()

    def manifest(self, name, names):
        (self.artifacts / name).write_text(''.join(
            hashlib.sha256((self.artifacts / item).read_bytes()).hexdigest() + '  ' + item + '\n'
            for item in sorted(names)))

    def refresh(self):
        self.manifest('RELEASE-SHA256SUMS', preflight.release_assets('v0.3.0'))

    def run_command(self, args, root, timeout=30):
        if args[:2] == ['git', 'rev-parse']:
            output = self.sha
        elif args[:2] == ['git', 'status']:
            output = ''
        elif args[:2] == ['git', 'remote']:
            output = 'https://github.com/example/project.git'
        elif args[0] == '/mock/actionlint':
            output = ''
        elif args[:2] == ['/mock/gh', 'api']:
            endpoint = args[-1]
            if endpoint == 'user':
                value = {'login': 'maintainer'}
            elif endpoint == 'repos/example/project':
                value = {'full_name': 'example/project', 'default_branch': 'main',
                         'private': self.private, 'archived': False, 'permissions': {'push': True}}
            elif endpoint.endswith('/actions/permissions'):
                value = {'enabled': True}
            elif endpoint.endswith('/actions/workflows/release-prepare.yml'):
                value = {'state': 'active'}
            elif '/contents/.github/workflows/release-prepare.yml?' in endpoint:
                value = {'encoding': 'base64', 'content': base64.b64encode(self.workflow.encode()).decode()}
            elif '/actions/workflows/ci.yml/runs?' in endpoint:
                value = {'workflow_runs': self.ci}
            elif '/git/ref/tags/' in endpoint:
                value = {'object': {'type': 'commit', 'sha': self.tag_sha}}
            elif endpoint.endswith('/releases?per_page=100'):
                value = self.releases
            else:
                raise AssertionError('Unexpected endpoint: ' + endpoint)
            output = json.dumps(value)
        else:
            raise AssertionError('Unexpected external command: ' + str(args))
        return subprocess.CompletedProcess(args, 0, output, '')

    def inspect(self, *options):
        args = ['release-preflight.py', '--root', str(self.root), '--artifacts', str(self.artifacts),
                '--tag', 'v0.3.0', '--actionlint', '/mock/actionlint', *options]
        stream = io.StringIO()
        with mock.patch.object(sys, 'argv', args), mock.patch.object(preflight, 'run', self.run_command), \
                mock.patch.object(preflight.shutil, 'which', return_value='/mock/gh'), contextlib.redirect_stdout(stream):
            code = preflight.main()
        return code, json.loads(stream.getvalue())

    def test_archive_release_is_ready_without_registry(self):
        code, report = self.inspect('--online')
        self.assertEqual(code, 0, report)
        self.assertEqual(report['distribution'], 'github-release-archive')
        self.assertIsNone(report['registry'])
        self.assertFalse(report['published'])
        self.assertTrue(all(value == 'passed' for value in report['gate_status'].values()))

    def test_optional_future_registry_does_not_require_registry_login(self):
        code, report = self.inspect('--online', '--registry', 'ghcr.io/example/docklane')
        self.assertEqual(code, 0, report)

    def test_invalid_optional_registry_fails(self):
        code, _ = self.inspect('--online', '--registry', 'not-a-registry')
        self.assertEqual(code, 1)

    def test_offline_mode_cannot_claim_remote_readiness(self):
        code, report = self.inspect()
        self.assertEqual(code, 2)
        self.assertIn('blocked', report['gate_status'].values())

    def test_private_repository_blocks_public_installer(self):
        self.private = True
        code, _ = self.inspect('--online')
        self.assertEqual(code, 2)

    def test_exact_commit_ci_is_required(self):
        self.ci[0]['head_sha'] = 'b' * 40
        code, _ = self.inspect('--online')
        self.assertEqual(code, 2)

    def test_old_prepare_only_workflow_is_not_publishable(self):
        self.workflow = 'name: no publication\n'
        code, _ = self.inspect('--online')
        self.assertEqual(code, 2)

    def test_existing_release_is_never_overwritten(self):
        for draft in (True, False):
            self.releases = [[{'tag_name': 'v0.3.0', 'draft': draft}]]
            code, _ = self.inspect('--online')
            self.assertEqual(code, 2)

    def test_wrong_tag_is_blocked(self):
        self.tag_sha = 'b' * 40
        code, _ = self.inspect('--online')
        self.assertEqual(code, 2)

    def test_wrong_candidate_source_fails(self):
        (self.artifacts / 'release-ci-evidence.json').write_text('{"head_sha":"wrong"}')
        self.refresh()
        code, _ = self.inspect('--online')
        self.assertEqual(code, 1)

    def test_missing_installer_is_a_failed_candidate(self):
        (self.artifacts / 'install-online.sh').unlink()
        code, _ = self.inspect('--online')
        self.assertEqual(code, 1)

    def test_unlisted_asset_fails(self):
        (self.artifacts / 'unexpected').write_bytes(b'unlisted')
        code, _ = self.inspect('--online')
        self.assertEqual(code, 1)

    def test_symlink_checksum_manifest_is_rejected(self):
        destination = self.root / 'external-checksums'
        shutil_manifest = self.artifacts / 'RELEASE-SHA256SUMS'
        shutil_manifest.rename(destination)
        shutil_manifest.symlink_to(destination)
        code, _ = self.inspect('--online')
        self.assertEqual(code, 1)


if __name__ == '__main__':
    unittest.main()
