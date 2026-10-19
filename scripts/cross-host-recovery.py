#!/usr/bin/env python3
"""Restore an encrypted repository on a different, fresh Linux installation.

Place this file beside integration.py. Run as root with Python >= 3.11:
  cross-host-recovery.py --binary /path/docklane --root /private/recovery \
    --offline /private/recovery/offline.json --expected /private/recovery/expected.json \
    --report /path/cross-host-report.json

The root may already contain the transferred encrypted repository, but root/state
and root/restored must not exist. No original SQLite or application key is read.
The offline JSON must be owned by root, mode 0600, and contain location/password;
location must point to the transferred repository inside --root.
Expected JSON: {"payload_sha256":"<64hex>",
  "source_snapshot_path":"/snapshot/source/payload.bin","mode":"0600",
  "uid":1001,"gid":1001,"snapshot_id":"<optional full verified ID>",
  "source":{"os":"Ubuntu 24.04","arch":"arm64","engine":"29.5.2",
  "binary_sha256":"<origin binary SHA256>"}}.
The snapshot path names the payload file, not merely its containing source.
--docker, --docker-endpoint and --restic also accept DM_TEST_* defaults.
Generated login credentials/cookies are memory-only, redacted, and revoked during
cleanup; the private destination state/restored files remain for inspection.
"""
import argparse
import datetime
import hashlib
import json
import os
import pathlib
import platform
import re
import secrets
import sqlite3
import stat
import sys
import tempfile
import urllib.parse

sys.dont_write_bytecode = True
import integration as harness
from integration import Client, Server, check, command, executable


def private_root(value):
    path = pathlib.Path(value).absolute()
    check(path != pathlib.Path('/'), 'root must be a dedicated private directory')
    check(path.resolve() == path, 'root must not traverse symbolic links')
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = path.lstat()
    check(stat.S_ISDIR(info.st_mode) and info.st_uid == os.geteuid(),
          'root must be an owned directory')
    check(stat.S_IMODE(info.st_mode) == 0o700, 'root must have mode 0700')
    for name in ('state', 'restored', 'state-config.json', 'state-server.log'):
        check(not os.path.lexists(path / name), 'private destination namespace already exists: ' + name)
    return path


def load_object(path, *, secret=False):
    path = pathlib.Path(path).absolute()
    info = path.lstat()
    check(stat.S_ISREG(info.st_mode), 'input must be a regular file, not a symbolic link')
    if secret:
        check(info.st_uid == os.geteuid() and stat.S_IMODE(info.st_mode) == 0o600,
              'offline recovery material must be owned by root with mode 0600')
    value = json.loads(path.read_text())
    check(isinstance(value, dict), 'input JSON must be an object')
    return value


def full_id(value):
    return isinstance(value, str) and re.fullmatch(r'[0-9a-f]{64}', value) is not None


def payload_expectation(value):
    sha = value.get('payload_sha256', '')
    check(isinstance(sha, str) and re.fullmatch(r'[0-9a-fA-F]{64}', sha) is not None,
          'expected payload_sha256 must be a full SHA256')
    raw_path = value.get('source_snapshot_path', '')
    check(isinstance(raw_path, str) and raw_path.startswith('/') and '\0' not in raw_path,
          'source_snapshot_path must be an absolute snapshot payload path')
    path = pathlib.PurePosixPath(raw_path)
    check(path != pathlib.PurePosixPath('/') and '..' not in path.parts,
          'source_snapshot_path must identify a payload without parent traversal')
    mode = value.get('mode')
    if isinstance(mode, str):
        mode = int(mode, 8) if mode.startswith(('0', '0o')) or re.fullmatch(r'[0-7]{3,4}', mode) else int(mode)
    check(type(mode) is int and 0 <= mode <= 0o7777, 'expected mode must be Unix permission bits')
    for key in ('uid', 'gid'):
        check(type(value.get(key)) is int and 0 <= value[key] < 2**32 - 1,
              'expected ' + key + ' must be a nonnegative numeric ID')
    source = value.get('source', {})
    check(isinstance(source, dict) and all(isinstance(source.get(key), str) and source[key]
                                        for key in ('os', 'arch', 'engine')),
          'expected source must include os, arch and engine provenance')
    check(isinstance(source.get('binary_sha256'), str) and re.fullmatch(r'[0-9a-fA-F]{64}', source['binary_sha256']) is not None,
          'expected source.binary_sha256 must record the origin binary SHA256')
    pinned = value.get('snapshot_id')
    check(pinned is None or full_id(pinned), 'expected snapshot_id must be a full immutable ID')
    return {'payload_sha256': sha.lower(), 'source_snapshot_path': str(path), 'mode': mode,
            'uid': value['uid'], 'gid': value['gid'], 'snapshot_id': pinned,
            'source': {key: source[key] for key in ('os', 'arch', 'engine', 'binary_sha256')}}


def counts(state):
    uri = 'file:' + urllib.parse.quote(str(state / 'docker-manager.db')) + '?mode=ro'
    with sqlite3.connect(uri, uri=True) as db:
        return {table: db.execute('SELECT count(*) FROM ' + table).fetchone()[0]
                for table in ('users', 'backup_repositories', 'backup_snapshots', 'backup_policies')}


def contains_payload(sources, payload):
    for source in sources:
        raw = source.get('snapshot_path', '')
        if not isinstance(raw, str) or not raw.startswith('/'):
            continue
        root = pathlib.PurePosixPath(raw)
        if '..' not in root.parts and (payload == root or root in payload.parents):
            return True
    return False


def choose_snapshot(client, repository_id, expected):
    prefix = '/backup/repositories/' + repository_id + '/snapshots'
    snapshots = client.call('GET', prefix)
    check(isinstance(snapshots, list), 'repository discovery did not return snapshots')
    verified = [item for item in snapshots if item.get('verification_status') == 'verified'
                and full_id(item.get('id'))]
    if expected['snapshot_id']:
        verified = [item for item in verified if item['id'] == expected['snapshot_id']]
    payload = pathlib.PurePosixPath(expected['source_snapshot_path'])
    matches = []
    for snapshot in verified:
        metadata = client.call('GET', prefix + '/' + snapshot['id'] + '/metadata')
        if metadata.get('verification_status') == 'verified' and contains_payload(metadata.get('sources', []), payload):
            matches.append((snapshot, metadata))
    check(matches, 'no repository-verified snapshot contains the expected payload source')
    check(len(matches) == 1, 'multiple verified snapshots match; supply expected.snapshot_id')
    return matches[0], len(snapshots)


def write_report(path, report):
    path = pathlib.Path(path).absolute()
    check(not path.is_symlink(), 'report destination must not be a symbolic link')
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile('w', dir=path.parent, prefix='.cross-host-', delete=False) as output:
            temporary = pathlib.Path(output.name)
            os.chmod(temporary, 0o600)
            json.dump(report, output, ensure_ascii=False, indent=2)
            output.write('\n')
        os.replace(temporary, path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def run(args):
    report = {'status': 'failed', 'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
              'source_sqlite_used': False, 'source_application_key_used': False,
              'service_recreation_requested': False, 'checks': [], 'cleanup': {}}
    server = client = None
    logged_in = False
    failure = None
    try:
        check(sys.platform == 'linux' and os.geteuid() == 0, 'run on Linux as root to verify arbitrary restored UID/GID')
        check(sys.version_info >= (3, 11), 'Python 3.11 or newer is required')
        root = private_root(args.root)
        offline = load_object(args.offline, secret=True)
        password = offline.get('password')
        check(isinstance(password, str) and password, 'offline JSON requires a nonempty password')
        harness.SECRETS.append(password)
        location = offline.get('location')
        check(isinstance(location, str) and pathlib.Path(location).is_absolute(),
              'offline JSON requires a local absolute repository location')
        repository = pathlib.Path(location).resolve(strict=True)
        check(repository.is_dir() and repository != root and repository.is_relative_to(root),
              'transferred repository must be inside the private root')
        check(not any(repository.is_relative_to(root / name) for name in ('state', 'restored')),
              'repository conflicts with private state or restore namespace')
        expected = payload_expectation(load_object(args.expected))
        binary = executable(args.binary, 'Docklane binary')
        docker = executable(args.docker, 'Docker CLI')
        restic = executable(args.restic, 'Restic')
        endpoint = args.docker_endpoint
        if not endpoint:
            endpoint = command([docker, 'context', 'inspect', '--format', '{{.Endpoints.docker.Host}}']).stdout.decode().strip()
        check(endpoint.startswith(('unix://', 'tcp://')), 'Docker endpoint must be explicit Unix or TCP')
        engine = json.loads(command([docker, '--host', endpoint, 'version', '--format', '{{json .Server}}']).stdout)
        check(engine.get('Version'), 'real Docker Engine version is unavailable')
        operating_system = platform.freedesktop_os_release()
        report.update({'root': str(root), 'source': expected['source'],
                       'destination': {'os': operating_system.get('PRETTY_NAME'), 'id': operating_system.get('ID'),
                                       'version': operating_system.get('VERSION_ID'), 'arch': platform.machine(),
                                       'engine': {key: engine.get(key) for key in ('Version', 'ApiVersion', 'Os', 'Arch')},
                                       'binary_sha256': harness.digest(binary),
                                       'restic_version': command([restic, 'version']).stdout.decode().strip()},
                       'new_database': {'path': str(root / 'state/docker-manager.db'), 'preexisting': False},
                       'repository_location': str(repository)})
        server = Server(binary, root, 'state', endpoint, docker, restic, None)
        server.start()
        client = Client(server.base)
        initial = counts(server.state)
        report['new_database']['initial_counts'] = initial
        check(all(number == 0 for number in initial.values()), 'fresh database unexpectedly contains user/repository/backup records')
        status = client.call('GET', '/auth/status')
        check(not status['initialized'] and not status['authenticated'], 'fresh server already has an administrator or session')
        report['checks'].append('fresh SQLite has zero users, repositories, policies and snapshot index records')
        login_password = secrets.token_urlsafe(36)
        harness.SECRETS.append(login_password)
        client.call('POST', '/auth/setup', {'username': 'cross-host-' + secrets.token_hex(6), 'password': login_password})
        logged_in = True
        harness.SECRETS.extend(cookie.value for cookie in client.cookies)
        login_password = None
        check(client.call('GET', '/backup/repositories') == [], 'fresh API already lists a repository')
        dashboard = client.call('GET', '/hosts/local/dashboard')
        check(dashboard.get('docker', {}).get('connected') is True, 'fresh service cannot reach the real Docker Engine')
        imported = client.call('POST', '/backup/repositories', {'name': 'cross-host-offline-import',
                               'location': str(repository), 'password': password, 'import': True}, expected=201)
        (snapshot, metadata), discovered = choose_snapshot(client, imported['id'], expected)
        snapshot_id = snapshot['id']
        target = root / 'restored'
        body = {'repository_id': imported['id'], 'snapshot': snapshot_id, 'target': str(target),
                'overwrite': False, 'recreate': False, 'container_ids': [], 'project_ids': []}
        preview = client.call('POST', '/backup/restore/preview', body)
        check(preview.get('verification_status') == 'verified', 'restore preview did not verify repository completion marker')
        manifest = preview.get('manifest', {})
        check(type(manifest.get('version')) is int and manifest['version'] >= 1, 'encrypted recovery manifest is unavailable')
        check(contains_payload(manifest.get('sources', []), pathlib.PurePosixPath(expected['source_snapshot_path'])),
              'manifest does not include the expected source path')
        check(preview.get('revision'), 'restore preview lacks a bound revision')
        report['snapshot'] = {'id': snapshot_id, 'verification_status': 'verified', 'discovered': discovered,
                              'manifest_version': manifest['version'], 'sources': metadata['sources']}
        report['checks'].append('fresh installation discovers verified snapshot and encrypted manifest from repository only')
        accepted = client.call('POST', '/backup/restore', dict(body, revision=preview['revision'], confirm=True), expected=202)
        task = client.task(accepted)
        restored = target.joinpath(*pathlib.PurePosixPath(expected['source_snapshot_path']).parts[1:])
        info = restored.lstat()
        check(stat.S_ISREG(info.st_mode) and restored.resolve().is_relative_to(target.resolve()),
              'restored payload is not a regular file inside the new target')
        actual = {'payload_sha256': harness.digest(restored), 'mode': stat.S_IMODE(info.st_mode),
                  'uid': info.st_uid, 'gid': info.st_gid}
        for key in actual:
            check(actual[key] == expected[key], 'independent restored payload verification differs: ' + key)
        report['payload'] = {'snapshot_path': expected['source_snapshot_path'], 'restored_path': str(restored),
                             'expected': {key: expected[key] for key in actual}, 'actual': actual,
                             'content_and_metadata_match': True}
        report['task'] = {key: task[key] for key in ('id', 'status', 'stage', 'progress')}
        report['checks'].append('independent SHA256, Unix permissions and UID/GID match source expectation')
        final_counts = counts(server.state)
        check(final_counts['users'] == 1 and final_counts['backup_repositories'] == 1
              and final_counts['backup_snapshots'] == 0 and final_counts['backup_policies'] == 0,
              'fresh recovery unexpectedly used a source snapshot index or policy database')
        report['new_database']['final_counts'] = final_counts
        report['checks'].append('fresh SQLite snapshot index stays empty after completed file-only recovery')
        report['status'] = 'passed'
    except BaseException as error:
        failure = harness.clean_message(error) or type(error).__name__
        report['failure'] = failure
    finally:
        cleanup_errors = []
        if client is not None:
            if logged_in:
                try:
                    harness.SECRETS.extend(cookie.value for cookie in client.cookies)
                    client.call('POST', '/auth/logout')
                    report['cleanup']['temporary_session_revoked'] = True
                except Exception as error:
                    cleanup_errors.append(harness.clean_message(error))
            client.cookies.clear()
        report['cleanup']['plaintext_login_file_created'] = False
        report['cleanup']['cookie_jar_cleared'] = True
        if server is not None:
            process = server.process
            try:
                server.stop()
            except Exception as error:
                cleanup_errors.append(harness.clean_message(error))
            report['cleanup']['private_server_stopped'] = process is None or process.poll() is not None
        else:
            report['cleanup']['private_server_stopped'] = True
        if cleanup_errors:
            report['cleanup']['errors'] = cleanup_errors
            report['status'] = 'failed'
        report['finished_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        write_report(args.report, report)
    print(('PASS' if report['status'] == 'passed' else 'FAIL') + ' cross-host recovery; report: ' + str(pathlib.Path(args.report).absolute()))
    if failure:
        print(failure, file=sys.stderr)
    return 0 if report['status'] == 'passed' else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    for key in ('binary', 'root', 'offline', 'expected', 'report'):
        parser.add_argument('--' + key, required=True)
    parser.add_argument('--docker', default=os.getenv('DM_TEST_DOCKER', 'docker'))
    parser.add_argument('--restic', default=os.getenv('DM_TEST_RESTIC', 'restic'))
    parser.add_argument('--docker-endpoint', default=os.getenv('DM_TEST_DOCKER_ENDPOINT') or os.getenv('DOCKER_HOST'))
    return run(parser.parse_args())


if __name__ == '__main__':
    raise SystemExit(main())
