#!/usr/bin/env python3
"""Isolated six-asset packaging and draft-publication tests; no Docker or GitHub writes."""
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import textwrap
import unittest

SOURCE = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location('release_preflight', SOURCE / 'scripts/release-preflight.py')
HELPER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(HELPER)
VERSION = '0.3.2-beta.1'
SHA = 'a' * 40


def manifest(directory, name, names):
    (directory / name).write_text(''.join(f'{HELPER.file_digest(directory / item)}  {item}\n' for item in sorted(names)))


def tar_bytes(path, entries):
    with tarfile.open(path, 'w:gz') as archive:
        for name, content in entries.items():
            item = tarfile.TarInfo(name)
            item.size = len(content)
            item.mode = 0o755 if name == 'docklane' else 0o644
            archive.addfile(item, io.BytesIO(content))


def fixture(root, version=VERSION):
    build, output = root / 'internal', root / 'public'
    build.mkdir()
    output.mkdir()
    sources = ['scripts/install-online.sh', 'scripts/install-support.py', 'scripts/install.sh',
               'scripts/upgrade.sh', 'scripts/uninstall.sh', 'deployments/docklane.service',
               'deployments/config.example.json', 'README.md', 'README.en.md', 'LICENSE',
               'docker-manager-prd-v0.3.md', 'docs/security-notes.md', 'docs/images/zh/promo.png',
               'web/qa/release-browser-results.json', '.tool-versions']
    for name in sources:
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(('source ' + name + '\n').encode())
    (root / 'web/package.json').write_text(json.dumps({'version': version}))
    for name in ('package-installation.sh', 'release-preflight.py', 'release.sh', 'build-images.sh'):
        shutil.copyfile(SOURCE / 'scripts' / name, root / 'scripts' / name)
    license = (root / 'LICENSE').read_bytes()
    (build / 'LICENSE').write_bytes(license)
    for arch, machine in [('amd64', 62), ('arm64', 183)]:
        content = bytearray(64)
        content[:6] = b'\x7fELF\x02\x01'
        content[18:20] = machine.to_bytes(2, 'little')
        (build / ('docklane-linux-' + arch)).write_bytes(content)
        name = f'docklane-v{version}-linux-{arch}.tar.gz'
        tar_bytes(build / name, {'docklane': bytes(content), 'LICENSE': license})
        manifest(build, name + '.sha256', [name])
    manifest(build, 'SHA256SUMS', ['docklane-linux-amd64', 'docklane-linux-arm64', 'LICENSE'])
    (build / 'docklane-images.tar.gz').write_bytes(b'image fixture')
    manifest(build, 'IMAGE-SHA256SUMS', ['docklane-images.tar.gz'])
    (build / 'IMAGES.txt').write_text(''.join(f'[docklane:v{version}-{arch}] sha256:{"1" * 64} linux/{arch}\n' for arch in ('amd64', 'arm64')))
    (build / 'release-ci-evidence.json').write_text(json.dumps({'version': version, 'tag': 'v' + version,
        'head_sha': SHA, 'ci_run_id': 123, 'ci_run_url': 'https://github.com/example/project/actions/runs/123', 'publication': 'candidate'}))
    return build, output


def package(root, build, output):
    return subprocess.run(['sh', str(root / 'scripts/package-installation.sh')], cwd=root,
        env={**os.environ, 'RELEASE_BUILD_DIR': str(build), 'RELEASE_OUTPUT_DIR': str(output)}, text=True, capture_output=True)


class PackageTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.build, self.output = fixture(self.root)

    def pack(self, ok=True):
        result = package(self.root, self.build, self.output)
        self.assertEqual(result.returncode == 0, ok, result.stdout + result.stderr)
        return result

    def rewrite_metadata(self, name, value):
        path = self.output / 'docklane-installation.tar.gz'
        with tarfile.open(path) as archive:
            entries = {member.name: archive.extractfile(member).read() for member in archive.getmembers() if member.isfile()}
        if value is None:
            entries.pop(name)
        else:
            entries[name] = value
        tar_bytes(path, entries)
        manifest(self.output, 'RELEASE-SHA256SUMS', HELPER.release_assets('v' + VERSION))

    def test_exact_six_assets_and_internal_metadata(self):
        self.pack()
        self.assertEqual({p.name for p in self.output.iterdir()}, HELPER.release_assets('v' + VERSION) | {'RELEASE-SHA256SUMS'})
        result = HELPER.validate_candidate(self.output, 'v' + VERSION, SHA)
        self.assertTrue(result['prerelease'])
        with tarfile.open(self.output / 'docklane-installation.tar.gz') as archive:
            for name in ('VERSION', 'SHA256SUMS', 'IMAGE-SHA256SUMS', 'IMAGES.txt', 'TOOL-VERSIONS.txt',
                         'release-ci-evidence.json', 'SECURITY-NOTES.md', 'LICENSE'):
                self.assertTrue(archive.getmember(name).isfile())
            self.assertEqual(archive.extractfile('VERSION').read(), (VERSION + '\n').encode())
        self.assertEqual(len((self.output / 'RELEASE-SHA256SUMS').read_text().splitlines()), 5)
        self.pack()  # Replacing only the same known six assets is supported.

    def test_public_contamination_is_preserved_and_rejected(self):
        (self.output / 'unknown').write_bytes(b'preserve')
        self.pack(False)
        self.assertEqual((self.output / 'unknown').read_bytes(), b'preserve')

    def test_internal_contamination_is_rejected(self):
        (self.build / 'unexpected').write_bytes(b'preserve')
        self.pack(False)
        self.assertTrue((self.build / 'unexpected').exists())

    def test_missing_source_and_symlink_are_rejected(self):
        (self.root / 'scripts/install-support.py').unlink()
        self.pack(False)
        (self.root / 'scripts/install-support.py').symlink_to(self.root / 'LICENSE')
        self.pack(False)

    def test_corrupt_internal_binary_is_rejected(self):
        (self.build / 'docklane-linux-amd64').write_bytes(b'corrupt')
        self.pack(False)
        self.assertEqual(list(self.output.iterdir()), [])

    def test_stable_version_is_supported(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            build, output = fixture(root, '0.3.1')
            result = package(root, build, output)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertFalse(HELPER.validate_candidate(output, 'v0.3.1', SHA)['prerelease'])

    def test_file_directory_ancestor_collision_is_rejected(self):
        self.pack()
        self.rewrite_metadata('docs', b'file blocks docs/security-notes.md')
        with self.assertRaisesRegex(ValueError, 'ancestor'):
            HELPER.validate_candidate(self.output, 'v' + VERSION, SHA)

    def test_release_build_passes_source_version_to_both_architectures(self):
        tools = self.root / 'build-tools'
        tools.mkdir()
        fake = tools / 'go'
        fake.write_text('#!/usr/bin/env python3\nimport os,pathlib,sys,json\n'
            'with open(os.environ["BUILD_TRACE"],"a") as f:f.write(json.dumps(sys.argv[1:])+"\\n")\n'
            'path=pathlib.Path(sys.argv[sys.argv.index("-o")+1]);h=bytearray(64);h[:6]=bytes.fromhex("7f454c460201");'
            'h[18:20]=(62 if os.environ["GOARCH"]=="amd64" else 183).to_bytes(2,"little");path.write_bytes(h)\n')
        fake.chmod(0o755)
        trace = self.root / 'build-trace'
        result = subprocess.run(['sh', str(self.root / 'scripts/release.sh')], cwd=self.root,
            env={**os.environ, 'RELEASE_BUILD_DIR': str(self.build), 'BUILD_TRACE': str(trace),
                 'PATH': str(tools) + os.pathsep + os.environ['PATH']}, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = [json.loads(line) for line in trace.read_text().splitlines()]
        self.assertEqual(len(calls), 2)
        for args in calls:
            self.assertIn('github.com/docker-manager/docker-manager/internal/buildinfo.Version=' + VERSION,
                          next(value for value in args if value.startswith('-ldflags=')))
        self.assertFalse((self.output / 'docklane-linux-amd64').exists())

    def test_wrong_nested_version_is_rejected(self):
        self.pack()
        self.rewrite_metadata('VERSION', b'0.3.0\n')
        with self.assertRaisesRegex(ValueError, 'VERSION'):
            HELPER.validate_candidate(self.output, 'v' + VERSION, SHA)

    def test_missing_nested_metadata_is_rejected(self):
        self.pack()
        self.rewrite_metadata('SHA256SUMS', None)
        with self.assertRaisesRegex(ValueError, 'metadata'):
            HELPER.validate_candidate(self.output, 'v' + VERSION, SHA)

    def test_wrong_ci_source_is_rejected(self):
        evidence = json.loads((self.build / 'release-ci-evidence.json').read_text())
        evidence['head_sha'] = 'b' * 40
        (self.build / 'release-ci-evidence.json').write_text(json.dumps(evidence))
        self.pack()
        with self.assertRaisesRegex(ValueError, 'another commit'):
            HELPER.validate_candidate(self.output, 'v' + VERSION, SHA)

    def test_unlisted_public_asset_and_wrong_hash_rejected(self):
        self.pack()
        (self.output / 'install-online.sh').write_bytes(b'corrupt')
        with self.assertRaisesRegex(ValueError, 'Checksum'):
            HELPER.validate_candidate(self.output, 'v' + VERSION)
        (self.output / 'extra').write_bytes(b'extra')
        with self.assertRaisesRegex(ValueError, 'six'):
            HELPER.validate_candidate(self.output, 'v' + VERSION)

    def publish(self, version=VERSION, **overrides):
        self.pack()
        shutil.copytree(self.output, self.root / 'candidate')
        workflow = (SOURCE / '.github/workflows/release-prepare.yml').read_text()
        source = workflow.rsplit('        shell: python\n        run: |\n', 1)[1]
        (self.root / 'publish.py').write_text(textwrap.dedent(source))
        tools = self.root / 'tools'
        tools.mkdir()
        script = '''import json, os, pathlib, shutil, sys
root=pathlib.Path(os.environ['FIXTURE_ROOT']);args=sys.argv[1:];tag=os.environ['RELEASE_TAG']
with (root/'gh-calls.jsonl').open('a') as stream:stream.write(json.dumps(args)+'\\n')
if args[0]=='api':
    endpoint=args[-1]
    if '/git/ref/tags/' in endpoint:
        counter=root/'tag-checks';count=int(counter.read_text())+1 if counter.exists() else 1;counter.write_text(str(count))
        changed=os.environ.get('MOVED_TAG') or (os.environ.get('MOVED_AFTER_UPLOAD') and count>1)
        print(json.dumps({'object':{'type':'commit','sha':('b' if changed else 'a')*40}}))
    elif endpoint.endswith('/releases?per_page=100'):
        print(json.dumps([[{'tag_name':tag}] if os.environ.get('EXISTING_RELEASE') else []]))
    elif '/actions/runs/' in endpoint:
        print(json.dumps({'head_sha':'a'*40,'status':'completed','conclusion':'failure' if os.environ.get('FAILED_CI') else 'success','event':'push','path':'.github/workflows/ci.yml-extra' if os.environ.get('WRONG_CI_WORKFLOW') else '.github/workflows/ci.yml'}))
    elif endpoint.endswith('/releases/latest'):
        after=(root/'published').exists()
        status=os.environ.get('LATEST_POST_STATUS' if after else 'LATEST_STATUS','200')
        if os.environ.get('LATEST_MISSING') or (os.environ.get('LATEST_DISAPPEARS') and after):status='404'
        if os.environ.get('LATEST_APPEARS'):status='200' if after else '404'
        if os.environ.get('LATEST_TRANSPORT_ERROR'):
            print('transport failed, misleading HTTP 404 text',file=sys.stderr)
            raise SystemExit(1)
        latest='v0.3.0' if '-beta.' in tag else tag
        if os.environ.get('LATEST_CHANGED') and after:latest=tag
        payload={'tag_name':latest} if status=='200' else {'message':'Not Found (HTTP 404)'}
        if '--include' in args:print('HTTP/2.0 '+status+' Response\\ncontent-type: application/json\\n')
        print(json.dumps(payload))
        if status!='200':
            print('gh: Not Found (HTTP 404)',file=sys.stderr)
            raise SystemExit(1)
    elif '/releases/tags/' in endpoint:
        print(json.dumps({'draft':False,'prerelease':'-beta.' in tag,'html_url':'https://example.invalid/release'}))
    else:raise SystemExit('Unexpected API endpoint '+endpoint)
elif args[:2]==['release','create']:(root/'draft-created').touch()
elif args[:2]==['release','upload']:
    remote=root/'remote';remote.mkdir()
    for name in args[5:]:shutil.copyfile(name,remote/pathlib.Path(name).name)
    if os.environ.get('CORRUPT_UPLOAD'):(remote/'install-online.sh').write_bytes(b'corrupt')
elif args[:2]==['release','download']:
    target=pathlib.Path(args[args.index('--dir')+1])
    for path in (root/'remote').iterdir():shutil.copyfile(path,target/path.name)
elif args[:2]==['release','edit']:(root/'published').touch()
else:raise SystemExit('Unexpected gh call '+str(args))
'''
        mock = tools / 'gh'
        mock.write_text('#!/usr/bin/env python3\n' + script)
        mock.chmod(0o755)
        return subprocess.run(['python3', str(self.root / 'publish.py')], cwd=self.root, text=True, capture_output=True,
            env={**os.environ, 'GH_REPO': 'example/project', 'RELEASE_TAG': 'v' + version, 'SOURCE_SHA': SHA,
                 'RUNNER_TEMP': str(self.root), 'FIXTURE_ROOT': str(self.root),
                 'PATH': str(tools) + os.pathsep + os.environ['PATH'], **overrides})

    def test_beta_publish_is_prerelease_and_never_latest(self):
        result = self.publish()
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = [json.loads(line) for line in (self.root / 'gh-calls.jsonl').read_text().splitlines()]
        create = next(args for args in calls if args[:2] == ['release', 'create'])
        edit = next(args for args in calls if args[:2] == ['release', 'edit'])
        self.assertIn('--prerelease', create)
        self.assertIn('--prerelease=true', edit)
        self.assertIn('--latest=false', edit)
        self.assertIn('--channel beta', (self.root / 'release-notes.md').read_text())
        self.assertTrue((self.root / 'published').exists())

    def test_beta_publish_without_any_stable_release(self):
        result = self.publish(LATEST_MISSING='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = [json.loads(line) for line in (self.root / 'gh-calls.jsonl').read_text().splitlines()]
        self.assertEqual(sum(args[0] == 'api' and args[-1].endswith('/releases/latest') for args in calls), 2)
        edit = next(args for args in calls if args[:2] == ['release', 'edit'])
        self.assertIn('--prerelease=true', edit)
        self.assertIn('--latest=false', edit)

    def test_beta_refuses_auth_rate_limit_and_server_errors_for_latest(self):
        for status in ('401', '403', '429', '500'):
            with self.subTest(status=status):
                temporary = tempfile.TemporaryDirectory()
                self.addCleanup(temporary.cleanup)
                self.root = Path(temporary.name)
                self.build, self.output = fixture(self.root)
                result = self.publish(LATEST_STATUS=status)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((self.root / 'draft-created').exists())

    def test_latest_transport_failure_is_not_absence(self):
        result = self.publish(LATEST_TRANSPORT_ERROR='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / 'draft-created').exists())

    def test_beta_latest_absence_cannot_change_to_a_stable_release(self):
        result = self.publish(LATEST_APPEARS='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('changed the stable latest', result.stderr)

    def test_beta_existing_stable_release_cannot_disappear(self):
        result = self.publish(LATEST_DISAPPEARS='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('changed the stable latest', result.stderr)

    def test_latest_server_error_after_upload_still_fails(self):
        result = self.publish(LATEST_POST_STATUS='503')
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue((self.root / 'published').exists())

    def test_beta_latest_guard_detects_changed_stable_channel(self):
        result = self.publish(LATEST_CHANGED='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('changed the stable latest', result.stderr)

    def stable_fixture(self):
        # Replace only this test's temporary fixture source, never the real source.
        for path in list(self.root.iterdir()):
            if path.is_dir():shutil.rmtree(path)
            else:path.unlink()
        self.build, self.output = fixture(self.root, '0.3.1')

    def test_stable_publish_sets_latest(self):
        self.stable_fixture()
        result = self.publish('0.3.1')
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = [json.loads(line) for line in (self.root / 'gh-calls.jsonl').read_text().splitlines()]
        self.assertIn('--latest=true', next(args for args in calls if args[:2] == ['release', 'edit']))

    def test_stable_publication_requires_a_latest_release(self):
        self.stable_fixture()
        result = self.publish('0.3.1', LATEST_MISSING='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Stable release did not become latest', result.stderr)

    def test_existing_release_and_failed_ci_are_not_created(self):
        result = self.publish(EXISTING_RELEASE='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / 'draft-created').exists())

    def test_failed_ci_is_not_created(self):
        result = self.publish(FAILED_CI='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / 'draft-created').exists())

    def test_similarly_named_ci_workflow_is_not_accepted(self):
        result = self.publish(WRONG_CI_WORKFLOW='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('successful exact-commit CI', result.stderr)
        self.assertFalse((self.root / 'draft-created').exists())

    def test_corrupt_upload_and_moved_tag_never_publish(self):
        result = self.publish(CORRUPT_UPLOAD='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue((self.root / 'draft-created').exists())
        self.assertFalse((self.root / 'published').exists())

    def test_moved_tag_after_upload_never_publishes(self):
        result = self.publish(MOVED_AFTER_UPLOAD='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / 'published').exists())


if __name__ == '__main__':
    unittest.main()
