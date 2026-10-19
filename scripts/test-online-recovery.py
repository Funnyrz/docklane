#!/usr/bin/env python3
"""Destructive failure injection ONLY inside an explicitly isolated test guest.

Requires an existing candidate installation. Leaves paired recovery materials,
failed data and logs private in the guest, and a working restored service.
"""
import argparse
import datetime
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import sys
import tempfile


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def command(*args, check=True):
    return subprocess.run(args, check=check, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=240)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--report', type=Path, required=True)
    args = parser.parse_args()
    if os.geteuid() != 0 or os.getenv('DM_PLATFORM_ISOLATED') != '1' or Path('/proc/1/comm').read_text().strip() != 'systemd':
        parser.error('Requires root in an explicitly isolated real systemd guest')
    args.report.parent.mkdir(parents=True, exist_ok=True)
    state = Path(tempfile.mkdtemp(prefix='docklane-recovery-', dir='/var/lib'))
    spec = importlib.util.spec_from_file_location('install_support', args.source / 'scripts/install-support.py')
    support = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(support)
    config = json.loads(support.CONFIG.read_text())
    data = Path(config['data_dir'])
    support.health_check(config['listen'])
    key = digest(data / 'master.key')
    with sqlite3.connect(f'file:{data}/docker-manager.db?mode=ro', uri=True) as db:
        users = db.execute('SELECT id,username,password_hash FROM users ORDER BY id').fetchall()
    existing = set(support.BACKUPS.iterdir()) if support.BACKUPS.exists() else set()
    invalid = state / 'invalid-binary'
    invalid.write_bytes(b'This deliberately invalid binary must never run successfully.\n')
    invalid.chmod(0o755)
    result = command('sh', str(args.source / 'scripts/upgrade.sh'), str(invalid), check=False)
    (state / 'failure.log').write_text(result.stdout + result.stderr)
    assert result.returncode != 0, 'invalid binary unexpectedly upgraded successfully'
    assert command('systemctl', 'is-active', '--quiet', 'docker-manager', check=False).returncode != 0, 'failed new service still active'
    assert command('systemctl', 'show', 'docker-manager', '--property=MainPID', '--value').stdout.strip() == '0', 'failed service process remains'
    groups = set(support.BACKUPS.iterdir()) - existing
    assert len(groups) == 1, 'expected exactly one paired stopped backup'
    backup = groups.pop()
    restore = json.loads((backup / 'restore.json').read_text())
    hashes = json.loads((backup / 'data-sha256.json').read_text())
    assert support.tree_hashes(backup / 'data') == hashes, 'backup data checksum mismatch'
    assert digest(backup / 'data/master.key') == key, 'backup master key changed'
    assert restore['data_dir'] == str(data), 'backup refers to another effective data directory'
    for name in ('docklane', 'docker-manager.service', 'config/config.json', 'startup-config.json', 'effective-config.json'):
        assert (backup / name).is_file(), 'incomplete recovery group: ' + name
    command('systemctl', 'stop', 'docker-manager')
    data.rename(state / 'failed-data')
    support.copy_tree(backup / 'data', data)
    config_dir = support.CONFIG.parent
    config_dir.rename(state / 'failed-config')
    support.copy_tree(backup / 'config', config_dir)
    shutil.copy2(backup / 'docklane', support.BINARY)
    shutil.copy2(backup / 'docker-manager.service', support.UNIT)
    if (backup / 'release.json').exists():
        shutil.copy2(backup / 'release.json', support.STATE)
    assert support.tree_hashes(data) == hashes, 'restored data differs before startup'
    command('systemctl', 'daemon-reload')
    command('systemctl', 'start', 'docker-manager')
    support.health_check(config['listen'])
    assert digest(data / 'master.key') == key
    with sqlite3.connect(f'file:{data}/docker-manager.db?mode=ro', uri=True) as db:
        assert db.execute('SELECT id,username,password_hash FROM users ORDER BY id').fetchall() == users, 'administrator identity changed'
    report = {'status': 'passed', 'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
              'checks': ['invalid executable upgrade returns nonzero', 'failed new service has MainPID=0',
                         'stopped backup includes effective data, config, binary, unit and master.key',
                         'whole backup group restored and verified before startup', 'restored service active/enabled/healthy',
                         'administrator password hashes and master key retained'],
              'backup_group': str(backup), 'private_failure_materials': str(state),
              'environment': dict(line.split('=', 1) for line in Path('/etc/os-release').read_text().splitlines() if '=' in line),
              'limitations': ['Deliberately invalid executable injection; database migration failure is covered separately by existing acceptance.']}
    args.report.write_text(json.dumps(report, indent=2) + '\n')
    print('PASS failed upgrade and matching-group recovery')


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('FAIL recovery acceptance: ' + str(error), file=sys.stderr)
        sys.exit(1)
