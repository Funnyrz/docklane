#!/usr/bin/env python3
"""Real stable-to-beta acceptance, only in an explicitly isolated systemd VM.

Run stable, beta, then resume after an actual guest reboot. Credentials and logs
stay in the root-owned state directory; the report contains no credentials.
The stable phase is a historical scenario requiring the verified archived
v0.3.0 baseline (or its retained test state). v0.3.0 is no longer a public Release;
its absence must not be mistaken for a successful baseline installation. Any
public baseline request explicitly selects stable and v0.3.0.
The beta phase uses the literal public mirror curl command, without mocks.
"""
import argparse
import datetime
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import secrets
import sqlite3
import subprocess
import sys
import urllib.request


URL = 'https://gh-proxy.com/https://raw.githubusercontent.com/Funnyrz/docklane/main/scripts/install-online.sh'
DATA = Path('/var/lib/docker-manager')
BINARY = Path('/usr/local/bin/docklane')
RECEIPT = Path('/var/lib/docklane-installer/release.json')
DISCLAIMER = '本程序尚未经过充分、严格的测试，仅供学习与测试使用，请勿用于生产环境。使用前请自行评估风险并备份重要数据。'


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def command(*args):
    return subprocess.check_output(args, text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--state', type=Path, required=True)
    parser.add_argument('--phase', choices=('stable', 'beta', 'resume'), required=True)
    args = parser.parse_args()
    if (sys.platform != 'linux' or os.geteuid() != 0 or
            os.getenv('DM_PLATFORM_ISOLATED') != '1' or
            Path('/proc/1/comm').read_text().strip() != 'systemd'):
        parser.error('Requires root in an explicitly isolated systemd VM')
    os.umask(0o077)
    source = args.source.resolve(strict=True)
    state = args.state.absolute()
    if state.exists() and (state.is_symlink() or state.stat().st_uid != 0 or state.stat().st_mode & 0o077):
        parser.error('State directory must be private, root-owned and not a symlink')
    state.mkdir(mode=0o700, parents=True, exist_ok=True)
    control_file, report_file = state / 'control.json', state / 'report.json'
    control = json.loads(control_file.read_text()) if control_file.exists() else {}
    report = json.loads(report_file.read_text()) if report_file.exists() else {'phases': []}
    phase = {'phase': args.phase, 'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'checks': []}
    report['phases'].append(phase)
    spec = importlib.util.spec_from_file_location('beta_integration', source / 'scripts/integration.py')
    integration = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(integration)
    expected = json.loads((source / 'web/package.json').read_text())['version']
    env = {k: v for k, v in os.environ.items() if not k.startswith('DM_') and k not in
           ('DOCKER_HOST', 'DOCKER_CONTEXT', 'DOCKER_CONFIG', 'DOCKER_CERT_PATH', 'DOCKER_TLS_VERIFY')}

    def save():
        control_file.write_text(json.dumps(control, indent=2) + '\n')
        report_file.write_text(json.dumps(report, indent=2) + '\n')

    def record(name):
        phase['checks'].append({'name': name, 'status': 'passed'})
        print('PASS ' + name, flush=True)
        save()

    def client(setup=False):
        api = integration.Client('http://127.0.0.1:8080')
        credentials = control['credentials']
        integration.SECRETS.extend(credentials.values())
        api.call('GET', '/auth/status')
        value = api.call('POST', '/auth/setup' if setup else '/auth/login', credentials)
        assert value['user']['id'] == control.setdefault('admin_id', value['user']['id'])
        return api

    def healthy(version):
        assert command('systemctl', 'is-active', 'docker-manager') == 'active'
        assert command('systemctl', 'is-enabled', 'docker-manager') == 'enabled'
        assert command('systemctl', 'show', 'docker-manager', '--property=ProtectHome', '--value') == 'yes'
        with urllib.request.urlopen('http://127.0.0.1:8080/healthz', timeout=10) as response:
            assert response.status == 200
        settings = client().call('GET', '/settings')
        assert settings['version'] == version, settings['version']
        assert digest(DATA / 'master.key') == control['key']
        with sqlite3.connect(f'file:{DATA}/docker-manager.db?mode=ro', uri=True) as db:
            users = db.execute('SELECT id,username,password_hash FROM users ORDER BY id').fetchall()
        assert hashlib.sha256(json.dumps(users).encode()).hexdigest() == control['users']
        assert settings['capability_settings']['saved'] == control['saved_settings']
        return settings

    def invoke(arguments=(), public=False, success=True):
        if public:
            import shlex
            cmd = ['bash', '-c', 'set -o pipefail; curl -fsSL ' + URL + ' | sudo bash -s -- ' + shlex.join(arguments)]
        else:
            cmd = ['bash', str(source / 'scripts/install-online.sh'), *arguments]
        result = subprocess.run(cmd, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=2400)
        log = state / ('installer-' + secrets.token_hex(5) + '.log')
        log.write_bytes(result.stdout + b'\n' + result.stderr)
        assert (result.returncode == 0) == success, 'Unexpected installer status; inspect private log ' + str(log)
        assert DISCLAIMER in result.stdout.decode(errors='replace'), 'Missing pre-install disclaimer'
        return result

    try:
        if args.phase == 'stable':
            if not control:
                assert not DATA.exists() and not BINARY.exists(), 'Refusing an existing installation'
                invoke(('--channel', 'stable', '--version', 'v0.3.0'))
                control['credentials'] = {'username': 'beta-test-' + secrets.token_hex(5), 'password': secrets.token_urlsafe(36)}
                save()
            else:
                assert 'credentials' in control and 'users' not in control, 'Stable setup is already complete'
                assert not integration.Client('http://127.0.0.1:8080').call('GET', '/auth/status')['initialized']
            receipt = json.loads(RECEIPT.read_text())
            assert receipt['version'] == 'v0.3.0', receipt['version']
            api = client(setup=True)
            control['key'] = digest(DATA / 'master.key')
            with sqlite3.connect(f'file:{DATA}/docker-manager.db?mode=ro', uri=True) as db:
                users = db.execute('SELECT id,username,password_hash FROM users ORDER BY id').fetchall()
            control['users'] = hashlib.sha256(json.dumps(users).encode()).hexdigest()
            control['saved_settings'] = api.call('GET', '/settings')['capability_settings']['saved']
            control['docker_pid'] = command('systemctl', 'show', 'docker', '--property=MainPID', '--value')
            healthy('0.3.0')
            record('Explicit historical stable baseline installs legacy v0.3.0 and permits administrator setup')
        elif args.phase == 'beta':
            assert control and json.loads(RECEIPT.read_text())['version'] == 'v0.3.0'
            with urllib.request.urlopen(URL, timeout=45) as response:
                assert hashlib.sha256(response.read()).hexdigest() == digest(source / 'scripts/install-online.sh')
            old_backups = set(Path('/var/backups/docker-manager').glob('*'))
            invoke(('--channel', 'beta'), public=True)
            healthy(expected)
            assert json.loads(RECEIPT.read_text())['version'] == 'v' + expected
            assert command('systemctl', 'show', 'docker', '--property=MainPID', '--value') == control['docker_pid']
            assert set(Path('/var/backups/docker-manager').glob('*')) - old_backups, 'Upgrade did not create a backup'
            record('Public beta curl upgrades stable, retains administrator, key, settings and Docker, and creates backup')
            inode, binary_hash = BINARY.stat().st_ino, digest(BINARY)
            pid = command('systemctl', 'show', 'docker-manager', '--property=MainPID', '--value')
            invoke(('--channel', 'beta'), public=True)
            healthy(expected)
            assert BINARY.stat().st_ino == inode and digest(BINARY) == binary_hash
            assert command('systemctl', 'show', 'docker-manager', '--property=MainPID', '--value') == pid
            record('Repeated public beta installation preserves binary inode and running process')
            result = invoke(('--channel', 'stable'), public=True, success=False)
            rejection = (result.stdout + result.stderr).decode().lower()
            assert 'downgrade' in rejection or 'no stable release could be verified' in rejection
            control['stable_rejection'] = 'downgrade blocked' if 'downgrade' in rejection else 'no verified stable release'
            healthy(expected)
            assert digest(BINARY) == binary_hash and command('systemctl', 'show', 'docker-manager', '--property=MainPID', '--value') == pid
            record('Explicit stable request is safely refused without stopping beta: ' + control['stable_rejection'])
            control.update(binary_hash=binary_hash, boot=Path('/proc/sys/kernel/random/boot_id').read_text())
        else:
            assert control.get('boot') and Path('/proc/sys/kernel/random/boot_id').read_text() != control['boot']
            healthy(expected)
            assert digest(BINARY) == control['binary_hash']
            record('Real guest reboot retains enabled service, beta version, administrator, key and settings')
        phase['result'] = 'passed'
    except Exception as error:
        phase.update(result='failed', error=integration.clean_message(error))
        raise
    finally:
        phase['completed_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        report['environment'] = {'system': os.uname().sysname, 'machine': os.uname().machine, 'pid1': 'systemd'}
        report['limitations'] = ['One isolated native arm64 Ubuntu 24.04 VM; no independent mainland ISP measurement',
                                 'Historical stable phase requires archived v0.3.0 baseline availability; v0.3.0 is no longer a public Release. Beta phase has no transport or selector mocks']
        save()


if __name__ == '__main__':
    main()
