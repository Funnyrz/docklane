#!/usr/bin/env python3
"""Actual online migration acceptance, ONLY in a disposable systemd Linux VM.

Requires root and DM_PLATFORM_ISOLATED=1. Uses local --assets with the production
online verification/deploy path. No fixtures, old data, or failed services are
deleted; the private state contains recovery locations and generated credentials.
"""

import argparse
import importlib.util
import json
import os
from pathlib import Path
import secrets
import select
import socket
import subprocess
import sys
import tarfile
import tempfile
import urllib.request


def load_harness(source):
    spec = importlib.util.spec_from_file_location('online_systemd_harness', source / 'scripts/test-online-systemd.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def migration_class(harness):
    class Migration(harness.Acceptance):
        def verified_manual_binary(self, destination):
            machine = os.uname().machine
            arch = {'x86_64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}.get(machine)
            self.check(arch is not None, 'unsupported migration-test architecture')
            archive = self.args.assets / f'docklane-{harness.VERSION}-linux-{arch}.tar.gz'
            entries = {}
            for line in (self.args.assets / 'RELEASE-SHA256SUMS').read_text().splitlines():
                checksum, name = line.split(maxsplit=1)
                name = name.lstrip(' *')
                self.check(name not in entries, 'duplicate release manifest entry')
                entries[name] = checksum
            self.check(archive.name in entries and harness.digest(archive) == entries[archive.name],
                       'manual-source binary archive checksum mismatch')
            with tarfile.open(archive, 'r:gz') as package:
                members = package.getmembers()
                self.check({item.name for item in members} == {'docklane', 'LICENSE'} and
                           len(members) == 2 and all(item.isfile() for item in members),
                           'unexpected manual-source binary archive layout')
                with package.extractfile('docklane') as binary:
                    destination.write_bytes(binary.read())
            destination.chmod(0o755)
            return harness.digest(destination)

        def invoke_migration(self, source_data):
            wrapper = self.install_wrapper()
            result = self.helper.command(
                ['bash', str(wrapper), '--version', harness.VERSION, '--migrate-from', str(source_data)],
                env={**self.env, 'TEST_SOURCE': str(self.args.source),
                     'TEST_ASSETS': str(self.args.assets), 'TEST_VERSION': harness.VERSION},
                timeout=2400, allow_failure=True)
            log = self.state / ('migration-installer-' + secrets.token_hex(4) + '.log')
            log.write_bytes(result.stdout + b'\n' + result.stderr)
            log.chmod(0o600)
            self.check(result.returncode == 0, 'online migration failed; inspect private log: ' + str(log) + '\n' +
                       self.helper.clean_message(result.stderr.decode(errors='replace')[-2500:]))
            return log.name

        def migration(self):
            if self.args.resume:
                return self.resume_migration()
            self.check(not self.control, 'migration test already has state; inspect retained fixtures instead of overwriting')
            for path in (harness.DATA, Path('/etc/docker-manager'), harness.BINARY, harness.UNIT,
                         Path('/usr/local/bin/docker-manager'), Path('/var/lib/docklane-installer')):
                self.check(not path.exists() and not path.is_symlink(), 'refusing existing installation path: ' + str(path))
            self.check(self.unit().get('LoadState') == 'not-found', 'refusing an existing docker-manager service')
            self.check((self.args.assets / 'install-online.sh').read_bytes() ==
                       (self.args.source / 'scripts/install-online.sh').read_bytes(),
                       'candidate online entrypoint differs from supplied source')
            before_docker = self.docker()
            with socket.socket() as probe:
                probe.bind(('127.0.0.1', 0))
                port = probe.getsockname()[1]
            listen = f'127.0.0.1:{port}'
            self.base = 'http://' + listen
            fixture = Path(tempfile.mkdtemp(prefix='docklane-online-migration-', dir='/home'))
            fixture.chmod(0o700)
            manual_binary = fixture / 'docklane'
            binary_sha = self.verified_manual_binary(manual_binary)
            manual_log = fixture / 'manual.log'
            source_data = fixture / 'data'
            self.control = {'source': str(self.args.source), 'assets': str(self.args.assets),
                            'fixture': str(fixture), 'source_data': str(source_data),
                            'manual_log': str(manual_log), 'listen': listen,
                            'version': harness.VERSION, 'docker_before': before_docker}
            self.save()
            output = manual_log.open('wb')
            try:
                # No DM_CONFIG, --config, or DM_DATA_DIR: Go's ./data default
                # must be recovered from the verified process's real cwd.
                manual = subprocess.Popen([str(manual_binary)], cwd=fixture,
                                          env={**self.env, 'DM_LISTEN': listen},
                                          stdout=output, stderr=subprocess.STDOUT)
                self.control['manual_pid'] = manual.pid
                self.save()
                self.wait_manual()
                api = self.client()
                status = api.call('GET', '/auth/status')
                self.check(not status['initialized'], 'manual fixture was unexpectedly initialized')
                self.credentials = {'username': 'migration-' + secrets.token_hex(6),
                                    'password': secrets.token_urlsafe(36)}
                self.helper.SECRETS.extend(self.credentials.values())
                self.save()
                initialized = api.call('POST', '/auth/setup', self.credentials)
                expected_settings = {'allowed_paths': [], 'path_mappings': [], 'enable_daemon_write': False}
                api.call('POST', '/settings/capabilities',
                         {**expected_settings, 'confirm': True, 'password': self.credentials['password']})
                original = api.call('GET', '/settings')
                self.control.update(admin_id=initialized['user']['id'],
                                    master_key_sha256=harness.digest(source_data / 'master.key'),
                                    binary_sha256=binary_sha)
                self.save_cookies(api)
                self.save()
                self.check(original['listen'] == listen and original['allowed_paths'] == [],
                           'manual fixture does not use expected process defaults')
                self.record('manual instance uses Go defaults, no configuration file and no business-path grants',
                            source_data=str(source_data), cwd=str(fixture), pid=manual.pid,
                            listen=listen, administrator_id=self.control['admin_id'])
                backup_root = Path('/var/backups/docker-manager')
                old_backups = set(backup_root.iterdir()) if backup_root.exists() else set()
                installer_log = self.invoke_migration(source_data)
                try:
                    exit_code = manual.wait(timeout=10)
                except subprocess.TimeoutExpired as error:
                    raise RuntimeError('old manual process did not exit; retained PID ' + str(manual.pid)) from error
                self.check(exit_code == 0, 'manual process was not stopped normally')
                self.check(not Path('/proc', str(manual.pid)).exists(), 'old manual PID is still running')
                unit = self.healthy()
                self.check(unit.get('MainPID') != str(manual.pid), 'systemd reused the old manual process')
                self.check(self.docker() == before_docker, 'migration changed the existing Docker daemon')
                self.check(harness.digest(harness.DATA / 'master.key') == self.control['master_key_sha256'],
                           'migrated master.key differs from source')
                self.check(harness.digest(source_data / 'master.key') == self.control['master_key_sha256'],
                           'original source master.key was changed or removed')
                self.check(harness.digest(harness.BINARY) == binary_sha, 'installed binary differs from local candidate')
                # Use a fresh client to prove persisted administrator credentials,
                # then inspect current and DB-saved settings in the new process.
                self.fresh_login()
                current = self.client(existing=True).call('GET', '/settings')
                self.check(current['listen'] == listen and current['data_dir'] == str(harness.DATA),
                           'migration did not preserve listen and relocate application data')
                self.check(current['capability_settings']['saved'] == expected_settings and
                           current['capability_settings']['effective'] == expected_settings and
                           current['capability_settings']['pending_restart'] is False,
                           'database capability settings were lost or widened')
                new_backups = set(backup_root.iterdir()) - old_backups
                self.check(len(new_backups) == 1, 'migration must create one complete stopped backup group')
                snapshot = new_backups.pop()
                self.control['backup'] = str(snapshot)
                for required in ('data/docker-manager.db', 'data/master.key', 'startup-config.json',
                                 'effective-config.json', 'restore.json', 'data-sha256.json'):
                    self.check((snapshot / required).is_file(), 'missing backup material: ' + required)
                sums = json.loads((snapshot / 'data-sha256.json').read_text())
                for name, checksum in sums.items():
                    self.check(harness.digest(snapshot / 'data' / name) == checksum,
                               'stopped backup checksum mismatch: ' + name)
                self.check(harness.digest(snapshot / 'data/master.key') == self.control['master_key_sha256'],
                           'backup key is not the original key')
                self.check(harness.digest(snapshot / 'data/docker-manager.db') == harness.digest(source_data / 'docker-manager.db'),
                           'source database was changed after stopped migration')
                self.check(snapshot.stat().st_mode & 0o077 == 0, 'backup group is not private')
                self.control['completed'] = True
                self.save()
                self.record('real online migration preserves source, administrator, key and database settings',
                            old_pid=manual.pid, old_exit_code=exit_code, unit=unit,
                            source_data=str(source_data), installed_data=str(harness.DATA),
                            master_key_sha256=self.control['master_key_sha256'],
                            backup=str(snapshot), backup_files=len(sums),
                            private_installer_log=installer_log, docker_preserved=before_docker)
            finally:
                # A failed test must not kill a source that the installer refused
                # to migrate. Its PID and private logs remain in control.json.
                output.close()

        def resume_migration(self):
            self.check(self.control and not self.control.get('completed'),
                       '--resume requires a retained incomplete migration fixture')
            source_data = Path(self.control['source_data'])
            fixture = Path(self.control['fixture'])
            pid = self.control['manual_pid']
            proc = Path('/proc') / str(pid)
            resumed_process = None
            if not proc.exists():
                # The earlier production attempt may have gracefully stopped
                # this recorded fixture before a later pre-deploy check failed.
                # Restart only our unchanged original source, never any service.
                for target in (harness.DATA, Path('/etc/docker-manager'), harness.BINARY, harness.UNIT):
                    self.check(not target.exists() and not target.is_symlink(),
                               'target installation exists; inspect recovery before restarting the retained source')
                binary = fixture / 'docklane'
                self.check(not fixture.is_symlink() and fixture.stat().st_uid == 0 and
                           fixture.stat().st_mode & 0o077 == 0 and not binary.is_symlink(),
                           'retained manual fixture identity or ownership changed')
                self.check(harness.digest(binary) == self.control['binary_sha256'] and
                           harness.digest(source_data / 'master.key') == self.control['master_key_sha256'],
                           'retained source binary or key changed; refusing to restart it')
                with Path(self.control['manual_log']).open('ab') as output:
                    resumed_process = subprocess.Popen([str(binary)], cwd=fixture,
                                                       env={**self.env, 'DM_LISTEN': self.control['listen']},
                                                       stdout=output, stderr=subprocess.STDOUT)
                self.control.setdefault('previous_manual_pids', []).append(pid)
                pid = resumed_process.pid
                self.control['manual_pid'] = pid
                self.save()
                proc = Path('/proc') / str(pid)
            self.check(proc.is_dir() and Path(os.readlink(proc / 'exe')).resolve() == (fixture / 'docklane').resolve(),
                       'retained PID does not execute the recorded manual binary; refusing to resume')
            self.check(Path(os.readlink(proc / 'cwd')).resolve() == fixture.resolve(),
                       'retained manual process working directory changed')
            self.check(harness.digest(source_data / 'master.key') == self.control['master_key_sha256'],
                       'retained source key changed')
            self.check((self.args.assets / 'install-online.sh').read_bytes() ==
                       (self.args.source / 'scripts/install-online.sh').read_bytes(),
                       'retry candidate online entrypoint differs from supplied source')
            self.base = 'http://' + self.control['listen']
            self.wait_manual()
            source_api = self.client(existing=True)
            status = source_api.call('GET', '/auth/status')
            self.check(status['authenticated'] and status['user']['id'] == self.control['admin_id'],
                       'retained source administrator/session changed')
            expected = {'allowed_paths': [], 'path_mappings': [], 'enable_daemon_write': False}
            self.check(source_api.call('GET', '/settings')['capability_settings']['saved'] == expected,
                       'retained source settings changed')
            before_docker = self.docker()
            self.check(before_docker == self.control['docker_before'], 'existing Docker changed since initial attempt')
            target_sha = self.verified_manual_binary(self.state / ('retry-candidate-' + secrets.token_hex(4)))
            backup_root = Path('/var/backups/docker-manager')
            previous = set(backup_root.iterdir()) if backup_root.exists() else set()
            descriptor = os.pidfd_open(pid)
            try:
                log = self.invoke_migration(source_data)
                poller = select.poll()
                poller.register(descriptor, select.POLLIN)
                self.check(bool(poller.poll(10000)), 'retained manual process did not exit after migration')
            finally:
                os.close(descriptor)
            exit_status = 'unavailable: original harness parent exited after the earlier attempt'
            if resumed_process is not None:
                exit_status = resumed_process.wait(timeout=10)
                self.check(exit_status == 0, 'restarted manual fixture did not exit normally')
            unit = self.healthy()
            self.check(unit.get('MainPID') != str(pid), 'managed service is still the old manual process')
            self.check(self.docker() == before_docker, 'retry changed the existing Docker daemon')
            self.check(harness.digest(harness.BINARY) == target_sha, 'installed retry binary differs from candidate')
            for directory in (source_data, harness.DATA):
                self.check(harness.digest(directory / 'master.key') == self.control['master_key_sha256'],
                           'source or migrated key changed')
            self.fresh_login()
            settings = self.client(existing=True).call('GET', '/settings')
            self.check(settings['listen'] == self.control['listen'] and settings['data_dir'] == str(harness.DATA),
                       'retry did not preserve listen or relocate data')
            self.check(settings['capability_settings']['saved'] == expected and
                       settings['capability_settings']['effective'] == expected and
                       settings['capability_settings']['pending_restart'] is False,
                       'retry changed database capability settings')
            snapshots = set(backup_root.iterdir()) - previous
            self.check(len(snapshots) == 1, 'retry did not produce one stopped backup group')
            snapshot = snapshots.pop()
            for required in ('data/docker-manager.db', 'data/master.key', 'startup-config.json',
                             'effective-config.json', 'restore.json', 'data-sha256.json'):
                self.check((snapshot / required).is_file(), 'retry backup missing: ' + required)
            sums = json.loads((snapshot / 'data-sha256.json').read_text())
            self.check(all(harness.digest(snapshot / 'data' / name) == checksum for name, checksum in sums.items()),
                       'retry backup checksums differ')
            self.check(harness.digest(snapshot / 'data/docker-manager.db') == harness.digest(source_data / 'docker-manager.db'),
                       'retained source database changed after migration')
            self.control.update(completed=True, backup=str(snapshot), retry_assets=str(self.args.assets),
                                retry_binary_sha256=target_sha)
            self.save()
            self.record('retained no-config manual fixture migrates successfully after candidate correction',
                        old_pid=pid, old_pidfd_confirmed_exited=True,
                        old_exit_status=exit_status,
                        unit=unit, source_data=str(source_data), installed_data=str(harness.DATA),
                        master_key_sha256=self.control['master_key_sha256'], backup=str(snapshot),
                        backup_files=len(sums), private_installer_log=log, docker_preserved=before_docker)

        def wait_manual(self):
            import time
            deadline = time.monotonic() + 90
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            while time.monotonic() < deadline:
                try:
                    with opener.open(self.base + '/healthz', timeout=2) as response:
                        if response.status == 200:
                            return
                except OSError:
                    pass
                time.sleep(0.3)
            raise RuntimeError('manual fixture failed to become healthy; inspect ' + self.control['manual_log'])

        def run(self):
            try:
                super().run()
            finally:
                self.report['limitations'] = [
                    'Runs only inside an explicitly isolated Linux systemd guest.',
                    'Release selection and transport use local candidate assets; production verification, dependency preparation, worker loading and deployment remain unchanged.',
                    'Tests live no-config binary migration, with no external business-path grants; offline explicit-config cases use lifecycle unit tests.',
                    'The source fixture, backup group and managed installation remain for inspection or recovery; this harness performs no cleanup.',
                ]
                harness.private_json(self.args.report, self.report)

    return Migration


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--assets', type=Path, required=True)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--report', type=Path, required=True)
    parser.add_argument('--resume', action='store_true', help='retry only the recorded still-running incomplete manual fixture')
    args = parser.parse_args()
    if sys.platform != 'linux' or os.geteuid() != 0 or os.getenv('DM_PLATFORM_ISOLATED') != '1':
        parser.error('Run only as root inside an isolated Linux VM with DM_PLATFORM_ISOLATED=1')
    if not Path('/run/systemd/system').is_dir() or Path('/proc/1/comm').read_text().strip() != 'systemd':
        parser.error('A real running systemd guest is required')
    args.assets, args.source, args.report = args.assets.resolve(), args.source.resolve(), args.report.absolute()
    if args.report.is_symlink():
        parser.error('Refusing a symlink report')
    args.report.parent.mkdir(parents=True, exist_ok=True)
    args.phase = 'migration'
    os.umask(0o077)
    harness = load_harness(args.source)
    try:
        migration_class(harness)(args).run()
    except Exception as error:
        print('FAIL ' + str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
