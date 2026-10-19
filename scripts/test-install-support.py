#!/usr/bin/env python3
"""Run with python3 scripts/test-install-support.py; no host changes are made."""
import argparse
import contextlib
import fcntl
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('install_support', Path(__file__).with_name('install-support.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class LifecycleTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.material = self.root / 'material'
        (self.material / 'deployments').mkdir(parents=True)
        (self.material / 'VERSION').write_text('0.3.0\n')
        self.example = {'version': 1, 'data_dir': str(self.root / 'managed-data'), 'listen': ':8080', 'mode': 'binary', 'allowed_paths': [], 'path_mappings': [], 'backup_temp_dir': '', 'restic_path': 'restic', 'compose_path': 'docker'}
        (self.material / 'deployments/config.example.json').write_text(json.dumps(self.example))
        (self.material / 'deployments/docker-manager.service').write_text('test unit')
        (self.material / 'LICENSE').write_text('MIT')
        self.binary = self.root / 'candidate'
        self.binary.write_bytes(b'candidate')
        for name, path in {'DATA': self.root / 'managed-data', 'CONFIG': self.root / 'config/config.json', 'BINARY': self.root / 'bin/docklane', 'UNIT': self.root / 'systemd/docker-manager.service', 'BACKUPS': self.root / 'backups', 'STATE': self.root / 'state/release.json', 'LICENSE': self.root / 'license/LICENSE'}.items():
            p = patch.object(m, name, path)
            p.start()
            self.addCleanup(p.stop)
        self.run = patch.object(m, 'run', return_value=subprocess.CompletedProcess([], 0, '', '')).start()
        self.addCleanup(patch.stopall)
        patch.object(m.os, 'chown').start()
        patch.object(m.os, 'lchown').start()
        patch.object(m, 'account', return_value=(os.getuid(), os.getgid())).start()
        patch.object(m, 'service_context', return_value=None).start()
        patch.object(m, 'check_port').start()
        patch.object(m, 'health_check').start()
        patch.object(m, 'check_external_paths').start()
        patch.object(m, 'checked_worker_image', side_effect=lambda configured, version: 'docker-manager:' + version).start()
        patch.object(m, 'service_groups', return_value=['docker']).start()
        self.args = argparse.Namespace(binary=str(self.binary), source_dir=str(self.material), version='v0.3.0', sha256=m.digest(self.binary), listen=None, migrate_from=None, config=None)

    def db(self, data):
        data.mkdir(parents=True, exist_ok=True)
        key = b'k' * 32
        (data / 'master.key').write_bytes(key)
        db = sqlite3.connect(data / 'docker-manager.db')
        db.execute('CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT)')
        db.execute('CREATE TABLE users(username TEXT,password_hash TEXT)')
        db.execute('INSERT INTO users VALUES(?,?)', ('admin', 'saved-password-hash'))
        db.execute('INSERT INTO settings VALUES(?,?)', ('master_key_fingerprint', hashlib.sha256(key).hexdigest()))
        db.commit()
        return db

    def deploy(self):
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            m._deploy_locked(self.args, self.material, self.binary)

    def test_material_version_uses_release_file_then_package_fallback(self):
        self.assertEqual(m.material_version(self.material), 'v0.3.0')
        (self.material / 'VERSION').unlink()
        (self.material / 'web').mkdir()
        (self.material / 'web/package.json').write_text('{"version":"0.3.1-beta.10"}')
        self.assertEqual(m.material_version(self.material), 'v0.3.1-beta.10')
        (self.material / 'web/package.json').unlink()
        self.assertEqual(m.material_version(self.material), 'v0.3.1-beta.1')

    def test_numeric_version_order_places_stable_after_same_core_beta(self):
        self.assertGreater(m.version_key('v0.3.1-beta.10'), m.version_key('v0.3.1-beta.2'))
        self.assertGreater(m.version_key('v0.3.1'), m.version_key('v0.3.1-beta.100'))
        self.assertGreater(m.version_key('v0.4.0-beta.1'), m.version_key('v0.3.99'))

    def test_implicit_downgrade_retains_schema_credentials_and_configuration(self):
        self.deploy()
        self.db(m.DATA).close()
        prior = json.loads(m.STATE.read_text())
        m.STATE.write_text(json.dumps({**prior, 'version': 'v0.3.1-beta.10'}))
        patch.object(m, 'service_context', return_value=({'DM_CONFIG': str(m.CONFIG)}, m.DATA, 0)).start()
        before = {str(path): path.read_bytes() for path in (m.CONFIG, m.DATA / 'master.key', m.DATA / 'docker-manager.db', m.STATE)}
        stop = patch.object(m, 'stop_process').start()
        with self.assertRaisesRegex(m.InstallError, 'Implicit downgrade'):
            self.deploy()
        stop.assert_not_called()
        for name, content in before.items():
            self.assertEqual(Path(name).read_bytes(), content)
        self.args.explicit_version = True
        self.deploy()
        snapshot = next(m.BACKUPS.iterdir())
        self.assertEqual((snapshot / 'data/master.key').read_bytes(), before[str(m.DATA / 'master.key')])
        with contextlib.closing(sqlite3.connect(m.DATA / 'docker-manager.db')) as db:
            self.assertEqual(db.execute('SELECT username,password_hash FROM users').fetchone(), ('admin', 'saved-password-hash'))
        self.assertEqual(json.loads(m.STATE.read_text())['version'], 'v0.3.0')

    def test_beta_worker_default_with_architecture_tracks_selected_release(self):
        self.run.return_value = subprocess.CompletedProcess([], 0, 'sha256:' + 'a' * 64, '')
        self.assertEqual(REAL_CHECK_WORKER('docker-manager:v0.3.1-beta.2-arm64', 'v0.3.1-beta.10'), 'docker-manager:v0.3.1-beta.10')

    def test_fresh_install_enables_health_and_tool_path_contract(self):
        self.deploy()
        self.assertEqual(m.BINARY.read_bytes(), b'candidate')
        self.assertEqual(json.loads(m.STATE.read_text())['version'], 'v0.3.0')
        self.assertIn(unittest.mock.call(['systemctl', 'enable', '--now', 'docker-manager']), self.run.call_args_list)
        m.health_check.assert_called_once_with(':8080')
        self.assertEqual(m.LICENSE.read_text(), 'MIT')

    def test_environment_and_relative_paths(self):
        config = self.root / 'old.json'
        config.write_text(json.dumps({'data_dir': 'data', 'backup_temp_dir': 'cache', 'restic_path': './tools/restic', 'listen': ':9000'}))
        result, _ = m.effective_config(self.material, {'DM_CONFIG': str(config), 'DM_LISTEN': ':9999', 'DM_ALLOWED_PATHS': '/srv/one,/srv/two', 'DM_SESSION_SECURE': 'true'}, self.root, migration=True)
        self.assertEqual(result['data_dir'], str(self.root / 'data'))
        self.assertEqual(result['backup_temp_dir'], str(self.root / 'cache'))
        self.assertEqual(result['restic_path'], str(self.root / 'tools/restic'))
        self.assertEqual(result['listen'], ':9999')
        self.assertTrue(result['session_secure'])

    def test_missing_old_configuration_refused(self):
        with self.assertRaisesRegex(m.InstallError, 'old startup configuration'):
            m.effective_config(self.material, {}, self.root, migration=True)

    def test_live_source_without_config_uses_go_defaults_and_proc_environment(self):
        result, original = m.effective_config(self.material, {'DM_LISTEN': ':9999', 'DM_DATA_DIR': './state', 'DM_BACKUP_TEMP_DIR': './cache'}, self.root, migration=True, live_context=True)
        self.assertIsNone(original)
        self.assertEqual(result['data_dir'], str(self.root / 'state'))
        self.assertEqual(result['backup_temp_dir'], str(self.root / 'cache'))
        self.assertEqual(result['listen'], ':9999')
        self.assertEqual(result['allowed_paths'], [])
        self.assertEqual(result['backup_worker_image'], 'docker-manager:v0.3.0')
        self.assertFalse(result['enable_daemon_write'])

    def test_live_default_data_is_resolved_against_verified_process_cwd(self):
        result, _ = m.effective_config(self.material, {}, self.root, migration=True, live_context=True)
        self.assertEqual(result['data_dir'], str(self.root / 'data'))
        self.assertEqual(result['backup_temp_dir'], str(self.root / 'data/tmp'))

    def test_live_migration_needs_only_migrate_from(self):
        old = self.root / 'data'
        self.db(old).close()
        self.args.migrate_from = str(old)
        patch.object(m.DataLock, 'inspect', return_value=1234).start()
        patch.object(m, 'process_context', return_value=({'DM_LISTEN': ':9123'}, self.root)).start()
        stop = patch.object(m, 'stop_process').start()
        self.deploy()
        installed = json.loads(m.CONFIG.read_text())
        self.assertEqual(installed['listen'], ':9123')
        self.assertEqual(installed['data_dir'], str(m.DATA))
        self.assertEqual(installed['allowed_paths'], [])
        stop.assert_called_once()
        self.assertTrue((old / 'master.key').is_file())

    def test_saved_settings_override_environment_and_matching_key_required(self):
        data = self.root / 'old'
        db = self.db(data)
        saved = {'allowed_paths': ['/srv/saved'], 'path_mappings': [], 'enable_daemon_write': False}
        db.execute('INSERT INTO settings VALUES(?,?)', ('capability_settings_v1', json.dumps(saved)))
        db.commit()
        db.close()
        config = {'allowed_paths': ['/srv/env']}
        m.database_paths(data, config)
        self.assertEqual(config['allowed_paths'], ['/srv/saved'])
        (data / 'master.key').write_bytes(b'w' * 32)
        with self.assertRaisesRegex(m.InstallError, 'do not match'):
            m.database_paths(data, config)

    def test_migration_keeps_admin_key_source_and_complete_backup(self):
        old = self.root / 'old'
        self.db(old).close()
        (old / 'docker-manager.db-wal').write_bytes(b'wal evidence')
        config = self.root / 'old.json'
        config.write_text(json.dumps({**self.example, 'data_dir': str(old)}))
        self.args.migrate_from = str(old)
        self.args.config = str(config)
        self.deploy()
        self.assertEqual((m.DATA / 'master.key').read_bytes(), (old / 'master.key').read_bytes())
        with contextlib.closing(sqlite3.connect(m.DATA / 'docker-manager.db')) as db:
            self.assertEqual(db.execute('SELECT username,password_hash FROM users').fetchone(), ('admin', 'saved-password-hash'))
        snapshot = next(m.BACKUPS.iterdir())
        self.assertEqual((snapshot / 'data/docker-manager.db-wal').read_bytes(), b'wal evidence')
        self.assertTrue((snapshot / 'effective-config.json').is_file())
        self.assertTrue((old / 'master.key').is_file())
        self.assertEqual(json.loads(m.CONFIG.read_text())['data_dir'], str(m.DATA))

    def test_nonempty_migration_target_refused(self):
        m.DATA.mkdir()
        (m.DATA / 'important').write_text('keep')
        old = self.root / 'old'
        old.mkdir()
        self.args.migrate_from = str(old)
        with self.assertRaisesRegex(m.InstallError, 'target is nonempty'):
            self.deploy()
        self.assertEqual((m.DATA / 'important').read_text(), 'keep')

    def test_unmanaged_nonempty_custom_data_refused(self):
        old = self.root / 'custom'
        self.db(old).close()
        config = self.root / 'custom.json'
        config.write_text(json.dumps({**self.example, 'data_dir': str(old)}))
        self.args.config = str(config)
        with self.assertRaisesRegex(m.InstallError, 'Configured data directory is nonempty'):
            self.deploy()

    def test_same_release_skips_binary_replacement(self):
        self.deploy()
        config = json.loads(m.CONFIG.read_text())
        patch.object(m, 'service_context', return_value=({'DM_CONFIG': str(m.CONFIG)}, m.DATA, 2)).start()
        patch.object(m.DataLock, 'inspect', return_value=2).start()
        with patch.object(m, 'install_file') as install:
            self.deploy()
            install.assert_not_called()
        self.assertEqual(json.loads(m.CONFIG.read_text()), config)

    def test_status_only_health_failure_does_not_stop_old_service(self):
        self.deploy()
        patch.object(m, 'service_context', return_value=({'DM_CONFIG': str(m.CONFIG)}, m.DATA, 2)).start()
        patch.object(m.DataLock, 'inspect', return_value=2).start()
        m.health_check.side_effect = m.InstallError('pending listen configuration differs')
        self.run.reset_mock()
        with self.assertRaisesRegex(m.InstallError, 'pending listen'):
            self.deploy()
        self.assertFalse(any(call.args[0][:2] == ['systemctl', 'stop'] for call in self.run.call_args_list))
        self.assertFalse(m.BACKUPS.exists())

    def test_final_stopped_settings_are_rechecked_before_replacing_files(self):
        self.deploy()
        self.db(m.DATA).close()
        patch.object(m, 'service_context', return_value=({'DM_CONFIG': str(m.CONFIG)}, m.DATA, 0)).start()
        db_calls = []

        def settings(directory, config):
            db_calls.append(directory)
            config['allowed_paths'] = ['/srv/initial'] if len(db_calls) == 1 else ['/root/changed-while-stopping']
            return set()

        def permissions(config, _items):
            if config['allowed_paths'] == ['/root/changed-while-stopping']:
                raise m.InstallError('ProtectHome blocks the final saved path')

        patch.object(m, 'database_paths', side_effect=settings).start()
        m.check_external_paths.side_effect = permissions
        with patch.object(m, 'install_file') as install:
            with self.assertRaisesRegex(m.InstallError, 'final saved path'):
                self.deploy()
            install.assert_not_called()
        self.assertEqual(len(db_calls), 2)
        snapshot = next(m.BACKUPS.iterdir())
        self.assertEqual(json.loads((snapshot / 'effective-config.json').read_text())['allowed_paths'], ['/root/changed-while-stopping'])
        self.assertTrue((snapshot / 'data/master.key').is_file())

    def test_db_grants_are_checked_without_overwriting_startup_config(self):
        old = self.root / 'old'
        db = self.db(old)
        db.execute('INSERT INTO settings VALUES(?,?)', ('capability_settings_v1', json.dumps({'allowed_paths': ['/srv/saved'], 'path_mappings': [], 'enable_daemon_write': True})))
        db.commit()
        db.close()
        config = self.root / 'old.json'
        config.write_text(json.dumps({**self.example, 'data_dir': str(old), 'allowed_paths': ['/srv/config'], 'enable_daemon_write': False, 'listen': ':9191'}))
        self.args.config, self.args.migrate_from = str(config), str(old)
        self.deploy()
        checked = m.check_external_paths.call_args.args[0]
        self.assertEqual(checked['allowed_paths'], ['/srv/saved'])
        self.assertTrue(checked['enable_daemon_write'])
        installed = json.loads(m.CONFIG.read_text())
        self.assertEqual(installed['allowed_paths'], ['/srv/config'])
        self.assertFalse(installed['enable_daemon_write'])
        self.assertEqual(installed['listen'], ':9191')
        with contextlib.closing(sqlite3.connect(m.DATA / 'docker-manager.db')) as db:
            saved = json.loads(db.execute("SELECT value FROM settings WHERE key='capability_settings_v1'").fetchone()[0])
            self.assertEqual(saved['allowed_paths'], ['/srv/saved'])
        snapshot = next(m.BACKUPS.iterdir())
        self.assertEqual(json.loads((snapshot / 'startup-config.json').read_text())['allowed_paths'], ['/srv/config'])
        self.assertEqual(json.loads((snapshot / 'effective-config.json').read_text())['allowed_paths'], ['/srv/saved'])

    def test_listen_is_preserved_unless_explicitly_overridden(self):
        self.example['listen'] = ':9191'
        (self.material / 'deployments/config.example.json').write_text(json.dumps(self.example))
        self.deploy()
        self.assertEqual(json.loads(m.CONFIG.read_text())['listen'], ':9191')
        self.assertIsNone(self.args.listen)
        patch.object(m, 'service_context', return_value=({'DM_CONFIG': str(m.CONFIG)}, m.DATA, 0)).start()
        self.args.listen = '127.0.0.1:9292'
        self.deploy()
        self.assertEqual(json.loads(m.CONFIG.read_text())['listen'], '127.0.0.1:9292')
        snapshot = next(m.BACKUPS.iterdir())
        self.assertEqual(json.loads((snapshot / 'startup-config.json').read_text())['listen'], ':9191')

    def test_standard_worker_is_updated_to_selected_release(self):
        self.run.return_value = subprocess.CompletedProcess([], 0, 'sha256:' + 'a' * 64 + '\n', '')
        self.assertEqual(REAL_CHECK_WORKER('docker-manager:v0.2.0', 'v0.3.0'), 'docker-manager:v0.3.0')

    def test_custom_worker_requires_same_image_identity(self):
        self.run.side_effect = [subprocess.CompletedProcess([], 0, 'sha256:' + 'a' * 64, ''), subprocess.CompletedProcess([], 0, 'sha256:' + 'b' * 64, '')]
        with self.assertRaisesRegex(m.InstallError, 'does not match'):
            REAL_CHECK_WORKER('private/custom-worker:latest', 'v0.3.0')
        self.run.side_effect = [subprocess.CompletedProcess([], 0, 'sha256:' + 'a' * 64, ''), subprocess.CompletedProcess([], 0, 'sha256:' + 'a' * 64, '')]
        self.assertEqual(REAL_CHECK_WORKER('private/custom-worker:matching', 'v0.3.0'), 'private/custom-worker:matching')

    def test_missing_selected_worker_refused_before_deploy(self):
        self.run.return_value = subprocess.CompletedProcess([], 1, '', 'image missing')
        with self.assertRaisesRegex(m.InstallError, 'unavailable'):
            REAL_CHECK_WORKER('docker-manager:v0.2.0', 'v0.3.0')

    def test_nonlocal_engine_refused_before_source_stop_or_config_write(self):
        old = self.root / 'data'
        self.db(old).close()
        self.args.migrate_from = str(old)
        patch.object(m.DataLock, 'inspect', return_value=1234).start()
        patch.object(m, 'process_context', return_value=({'DM_DOCKER_ENDPOINT': 'tcp://other-host:2375'}, self.root)).start()
        stop = patch.object(m, 'stop_process').start()
        with self.assertRaisesRegex(m.InstallError, 'different docker_endpoint is unsupported'):
            self.deploy()
        stop.assert_not_called()
        m.checked_worker_image.assert_not_called()
        self.assertFalse(m.CONFIG.exists())
        self.assertFalse(m.DATA.exists())

    def test_canonical_local_endpoint_supported(self):
        m.check_engine_endpoint({'docker_endpoint': 'unix:///var/run/docker.sock'})
        for endpoint in ('tcp://localhost:2375', 'unix:///run/custom.sock', 'ssh://host'):
            with self.subTest(endpoint=endpoint):
                with self.assertRaises(m.InstallError):
                    m.check_engine_endpoint({'docker_endpoint': endpoint})

    def test_reinstall_after_uninstall_preserves_known_data(self):
        self.deploy()
        self.db(m.DATA).close()
        m.BINARY.unlink()
        m.UNIT.unlink()
        self.deploy()
        self.assertTrue(m.BINARY.is_file())
        self.assertEqual((m.DATA / 'master.key').read_bytes(), b'k' * 32)
        self.assertTrue(next(m.BACKUPS.iterdir()).joinpath('data/master.key').is_file())

    def test_upgrade_backs_up_effective_custom_directory(self):
        data = self.root / 'custom-data'
        self.db(data).close()
        m.CONFIG.parent.mkdir()
        m.CONFIG.write_text(json.dumps({**self.example, 'data_dir': str(data)}))
        m.BINARY.parent.mkdir()
        m.BINARY.write_bytes(b'old executable')
        m.UNIT.parent.mkdir()
        m.UNIT.write_text('old unit')
        patch.object(m, 'service_context', return_value=({'DM_CONFIG': str(m.CONFIG)}, self.root, 0)).start()
        self.deploy()
        snapshot = next(m.BACKUPS.iterdir())
        self.assertEqual((snapshot / 'docklane').read_bytes(), b'old executable')
        self.assertEqual(json.loads((snapshot / 'restore.json').read_text())['data_dir'], str(data.resolve()))
        self.assertTrue((snapshot / 'data/master.key').is_file())

    def test_failed_health_stops_new_service_and_retains_backup(self):
        old = self.root / 'old'
        self.db(old).close()
        config = self.root / 'old.json'
        config.write_text(json.dumps({**self.example, 'data_dir': str(old)}))
        self.args.config, self.args.migrate_from = str(config), str(old)
        m.health_check.side_effect = m.InstallError('unhealthy')
        with self.assertRaisesRegex(m.InstallError, 'unhealthy'):
            self.deploy()
        self.assertIn(unittest.mock.call(['systemctl', 'stop', 'docker-manager'], check=False), self.run.call_args_list)
        self.assertTrue(next(m.BACKUPS.iterdir()).joinpath('data/master.key').is_file())
        self.assertTrue((old / 'master.key').is_file())

    def test_symlink_application_data_refused(self):
        data = self.root / 'old'
        data.mkdir()
        (data / 'key').symlink_to('/etc/passwd')
        with self.assertRaisesRegex(m.InstallError, 'symlink'):
            m.validate_data(data)

    def test_real_flock_blocks_even_when_pid_is_stale(self):
        data = self.root / 'locked'
        data.mkdir()
        script = 'import os,fcntl,sys,time; f=open(sys.argv[1],"w"); fcntl.flock(f,fcntl.LOCK_EX); f.write(str(os.getpid())); f.flush(); print("ready",flush=True); time.sleep(30)'
        proc = subprocess.Popen([sys.executable, '-c', script, str(data / 'server.lock')], stdout=subprocess.PIPE, text=True)
        self.addCleanup(lambda: proc.poll() is None and proc.kill())
        self.assertEqual(proc.stdout.readline().strip(), 'ready')
        lock = m.DataLock(data)
        self.addCleanup(lock.close)
        with patch.object(m, 'process_identity', side_effect=m.InstallError('PID inode mismatch')) as identity:
            with self.assertRaisesRegex(m.InstallError, 'inode mismatch'):
                lock.inspect()
            self.assertEqual(identity.call_args.args[0], proc.pid)
        proc.terminate()
        proc.wait(timeout=5)
        lock.acquire_after_stop()
        proc.stdout.close()

    def test_real_unlocked_flock_ignores_stale_pid(self):
        data = self.root / 'unlocked'
        data.mkdir()
        (data / 'server.lock').write_text('99999999')
        lock = m.DataLock(data)
        self.addCleanup(lock.close)
        self.assertIsNone(lock.inspect())

    def test_signal_uses_pidfd_after_lock_identity_verification(self):
        lock = m.DataLock(self.root)
        lock.stream = (self.root / 'server.lock').open('a+')
        lock.owner = 1234
        self.addCleanup(lock.close)
        poller = unittest.mock.Mock()
        poller.poll.return_value = [(123, 1)]
        with patch.object(m.os, 'pidfd_open', return_value=123, create=True), patch.object(m.signal, 'pidfd_send_signal', create=True) as send, patch.object(m, 'process_identity') as identity, patch.object(m.select, 'poll', return_value=poller, create=True), patch.object(m.os, 'close') as close, patch.object(lock, 'acquire_after_stop') as acquired:
            m.stop_process(lock)
            identity.assert_called_once()
            send.assert_called_once_with(123, m.signal.SIGTERM)
            poller.poll.assert_called_once_with(180000)
            acquired.assert_called_once()
            close.assert_called_once_with(123)

    def test_signal_timeout_does_not_escalate(self):
        lock = m.DataLock(self.root)
        lock.stream = (self.root / 'server.lock').open('a+')
        lock.owner = 1234
        self.addCleanup(lock.close)
        poller = unittest.mock.Mock()
        poller.poll.return_value = []
        with patch.object(m.os, 'pidfd_open', return_value=123, create=True), patch.object(m.signal, 'pidfd_send_signal', create=True) as send, patch.object(m, 'process_identity'), patch.object(m.select, 'poll', return_value=poller, create=True), patch.object(m.os, 'close'), patch.object(m.os, 'kill') as kill:
            with self.assertRaisesRegex(m.InstallError, 'did not exit'):
                m.stop_process(lock)
            self.assertEqual(send.call_count, 1)
            kill.assert_not_called()

    def test_process_identity_matches_kernel_lock_inode_and_pid(self):
        proc = self.root / 'proc'
        process = proc / '1234'
        process.mkdir(parents=True)
        executable = self.root / 'docklane'
        executable.write_bytes(b'github.com/docker-manager/docker-manager')
        (process / 'exe').symlink_to(executable)
        (process / 'cmdline').write_bytes(b'docklane\0')
        lockfile = self.root / 'lock'
        lockfile.touch()
        info = lockfile.stat()
        lock_record = f'1: FLOCK ADVISORY WRITE 1234 {os.major(info.st_dev):02x}:{os.minor(info.st_dev):02x}:{info.st_ino} 0 EOF\n'
        (proc / 'locks').write_text(lock_record)
        real_path = Path

        def fake_path(value):
            if str(value) == '/proc':
                return proc
            if str(value) == '/proc/locks':
                return proc / 'locks'
            return real_path(value)

        with patch.object(m, 'Path', side_effect=fake_path):
            m.process_identity(1234, info)
            (proc / 'locks').write_text(lock_record.replace('WRITE 1234', 'WRITE 9876'))
            with self.assertRaisesRegex(m.InstallError, 'does not own'):
                m.process_identity(1234, info)

    def test_flock_timeout_never_kills(self):
        lock = m.DataLock(self.root)
        lock.stream = (self.root / 'server.lock').open('a+')
        self.addCleanup(lock.close)
        with patch.object(m.fcntl, 'flock', side_effect=BlockingIOError), patch.object(m.time, 'monotonic', side_effect=[0, 181]), patch.object(m.os, 'kill') as kill:
            with self.assertRaisesRegex(m.InstallError, '180 seconds'):
                lock.acquire_after_stop()
            kill.assert_not_called()

    def test_protect_home_not_relaxed(self):
        # Call the original function instead of setUp's deployment boundary mock.
        with patch.object(m, 'check_external_paths', new=REAL_CHECK_PATHS):
            with self.assertRaisesRegex(m.InstallError, 'ProtectHome'):
                m.check_external_paths({'allowed_paths': ['/home/ubuntu/compose']}, set())

    def test_readonly_backup_source_and_allowlist_do_not_require_write(self):
        source = self.root / 'read-only-source'
        source.mkdir(mode=0o555)
        self.run.reset_mock()
        REAL_CHECK_PATHS({'allowed_paths': [str(source)]}, {(str(source), 'read')})
        commands = [call.args[0] for call in self.run.call_args_list]
        self.assertTrue(commands)
        self.assertTrue(all('-w' not in command for command in commands))
        self.assertTrue(any('-r' in command for command in commands))
        self.assertTrue(any('-x' in command for command in commands))
        self.assertTrue(all('-G' in command and 'docker' in command for command in commands))

    def test_allowlist_only_requires_traversal(self):
        source = self.root / 'grant'
        source.mkdir()
        self.run.reset_mock()
        REAL_CHECK_PATHS({'allowed_paths': [str(source)]}, set())
        commands = [call.args[0] for call in self.run.call_args_list]
        self.assertEqual(len(commands), 1)
        self.assertIn('-x', commands[0])
        self.assertNotIn('-r', commands[0])
        self.assertNotIn('-w', commands[0])

    def test_backup_repository_and_compose_require_write(self):
        for role in ('write',):
            path = self.root / 'repository'
            path.mkdir(exist_ok=True)
            self.run.return_value = subprocess.CompletedProcess([], 0, '', '')
            self.run.reset_mock()
            REAL_CHECK_PATHS({}, {(str(path), role)})
            self.assertTrue(any('-w' in call.args[0] for call in self.run.call_args_list))
            self.run.side_effect = lambda command, **kwargs: subprocess.CompletedProcess([], 1 if '-w' in command else 0, '', '')
            with self.assertRaisesRegex(m.InstallError, 'cannot access'):
                REAL_CHECK_PATHS({}, {(str(path), role)})

    def test_database_paths_carry_the_required_role(self):
        data = self.root / 'role-data'
        db = self.db(data)
        db.execute('CREATE TABLE compose_projects(workdir TEXT,config_paths TEXT,env_files TEXT)')
        db.execute('CREATE TABLE backup_repositories(location TEXT)')
        db.execute('CREATE TABLE backup_policies(definition TEXT)')
        db.execute('INSERT INTO compose_projects VALUES(?,?,?)', ('/srv/project', '["compose.yaml"]', '[".env"]'))
        db.execute('INSERT INTO backup_repositories VALUES(?)', ('/srv/repository',))
        db.execute('INSERT INTO backup_policies VALUES(?)', (json.dumps({'targets': [{'type': 'directory', 'path': '/srv/source'}]}),))
        db.commit()
        db.close()
        paths = m.database_paths(data, {})
        self.assertIn(('/srv/source', 'read'), paths)
        self.assertIn(('/srv/repository', 'write'), paths)
        self.assertIn(('/srv/project', 'write'), paths)
        self.assertIn(('/srv/project/compose.yaml', 'write'), paths)

    def test_missing_docker_group_is_explicit_and_does_not_create_it(self):
        with patch.object(m.grp, 'getgrnam', side_effect=KeyError):
            self.run.reset_mock()
            with self.assertRaisesRegex(m.InstallError, 'docker group is missing'):
                m.require_docker_group()
            self.run.assert_not_called()

    def test_unwritable_docker_socket_is_not_modified(self):
        self.run.side_effect = lambda command, **kwargs: subprocess.CompletedProcess([], 1 if '-w' in command else 0, '', '')
        with self.assertRaisesRegex(m.InstallError, 'Repair socket permissions explicitly'):
            m.check_docker_socket()
        self.assertTrue(all(call.args[0][0] == 'runuser' for call in self.run.call_args_list))

    def test_service_groups_preserve_existing_memberships_and_add_docker(self):
        groups = {100: unittest.mock.Mock(gr_name='docker-manager'), 101: unittest.mock.Mock(gr_name='backup-read'),
                  102: unittest.mock.Mock(gr_name='docker')}
        with patch.object(m.grp, 'getgrnam', side_effect=lambda name: unittest.mock.Mock(gr_gid=100 if name == 'docker-manager' else 102)), patch.object(m.grp, 'getgrgid', side_effect=groups.__getitem__), patch.object(m.os, 'getgrouplist', return_value=[100, 101]):
            self.assertEqual(REAL_SERVICE_GROUPS(), ['backup-read', 'docker', 'docker-manager'])

    def test_real_subprocess_runuser_receives_repeated_group_options(self):
        commands = self.root / 'commands'
        commands.mkdir()
        trace = self.root / 'runuser-arguments.jsonl'
        fake = commands / 'runuser'
        fake.write_text('#!' + sys.executable + '\n'
                        'import json,os,sys\n'
                        'args=sys.argv[1:]\n'
                        'groups=[args[i+1] for i,arg in enumerate(args) if arg=="-G"]\n'
                        'assert groups==["backup-read","docker","docker-manager"], groups\n'
                        'assert not any("," in group for group in groups), groups\n'
                        'assert args[:4]==["-u","docker-manager","-g","docker-manager"]\n'
                        'assert args[-4:][0:2]==["--","test"]\n'
                        'with open(os.environ["DM_GROUP_TEST_TRACE"],"a") as f: f.write(json.dumps(args)+"\\n")\n')
        fake.chmod(0o755)
        with patch.object(m, 'run', new=REAL_RUN), patch.object(m, 'service_groups', return_value=['backup-read', 'docker', 'docker-manager']), patch.dict(os.environ, {'PATH': str(commands) + os.pathsep + os.environ['PATH'], 'DM_GROUP_TEST_TRACE': str(trace)}):
            m.check_docker_socket()
        lines = [json.loads(line) for line in trace.read_text().splitlines()]
        self.assertEqual(len(lines), 2)
        self.assertEqual(lines[0][-2:], ['-r', '/var/run/docker.sock'])
        self.assertEqual(lines[1][-2:], ['-w', '/var/run/docker.sock'])

    def test_busy_port_refused(self):
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            listener.listen()
            with self.assertRaisesRegex(m.InstallError, 'occupied'):
                REAL_CHECK_PORT('127.0.0.1:' + str(listener.getsockname()[1]))

    @unittest.skipUnless(sys.platform == 'linux', 'Linux TCP TIME_WAIT lifecycle regression')
    def test_stopped_listener_time_wait_can_be_reused_like_go(self):
        listener = socket.socket()
        listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        listener.bind(('127.0.0.1', 0))
        address = listener.getsockname()
        listener.listen()
        client = socket.create_connection(address, timeout=3)
        accepted, _ = listener.accept()
        accepted.shutdown(socket.SHUT_WR)
        accepted.close()
        self.assertEqual(client.recv(1), b'')
        client.close()
        listener.close()
        with socket.socket() as ordinary:
            with self.assertRaises(OSError):
                ordinary.bind(address)
        REAL_CHECK_PORT('127.0.0.1:' + str(address[1]))

    def test_unknown_service_refused(self):
        self.run.return_value = subprocess.CompletedProcess([], 0, 'LoadState=loaded\nUser=root\nFragmentPath=/elsewhere\n', '')
        with self.assertRaisesRegex(m.InstallError, 'standard managed'):
            REAL_SERVICE_CONTEXT()

    def test_health_timeout_is_bounded(self):
        self.run.return_value = subprocess.CompletedProcess([], 1, '', '')
        with patch.object(m.time, 'monotonic', side_effect=[0, 0, 61]), patch.object(m.time, 'sleep'):
            with self.assertRaisesRegex(m.InstallError, '60 seconds'):
                REAL_HEALTH_CHECK(':8080')


REAL_CHECK_PATHS = m.check_external_paths
REAL_CHECK_PORT = m.check_port
REAL_SERVICE_CONTEXT = m.service_context
REAL_HEALTH_CHECK = m.health_check
REAL_CHECK_WORKER = m.checked_worker_image
REAL_SERVICE_GROUPS = m.service_groups
REAL_RUN = m.run
if __name__ == '__main__':
    unittest.main()
