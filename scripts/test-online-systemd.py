#!/usr/bin/env python3
"""Run ONLY as root inside a disposable systemd VM (DM_PLATFORM_ISOLATED=1).

The install phase uses verified local candidate downloads, with real dependency,
worker-image and systemd installation. It leaves the service installed. Reboot
the actual guest before resume. Credentials remain in a private sibling state
directory, never in the report. No cleanup or guest reboot is performed here.
"""
import argparse
import datetime
import hashlib
import http.cookiejar
import importlib.util
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import sys
import time
import urllib.request


DATA = Path('/var/lib/docker-manager')
BINARY = Path('/usr/local/bin/docklane')
UNIT = Path('/etc/systemd/system/docker-manager.service')
VERSION = 'v0.3.0'


def digest(path):
    value = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1 << 20), b''):
            value.update(block)
    return value.hexdigest()


def private_json(path, value):
    temporary = path.with_suffix('.tmp')
    descriptor = os.open(temporary, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(descriptor, 'w') as stream:
        json.dump(value, stream, indent=2)
        stream.write('\n')
    os.replace(temporary, path)


def boot_identity():
    fields = Path('/proc/1/stat').read_text().rsplit(')', 1)[1].split()
    return {'boot_id': Path('/proc/sys/kernel/random/boot_id').read_text().strip(),
            'pid1_start_ticks': fields[19], 'pid1_comm': Path('/proc/1/comm').read_text().strip()}


class Acceptance:
    def __init__(self, args):
        self.args = args
        spec = importlib.util.spec_from_file_location('online_integration_helper', args.source / 'scripts/integration.py')
        self.helper = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.helper)
        self.state = args.report.parent / (args.report.stem + '-state')
        self.check(not self.state.is_symlink(), 'refusing a symlink state directory')
        if self.state.exists():
            info = self.state.stat()
            self.check(info.st_uid == 0 and info.st_mode & 0o077 == 0,
                       'acceptance state must be root-owned and private')
        else:
            self.state.mkdir(mode=0o700)
        self.control_path = self.state / 'control.json'
        self.credentials_path = self.state / 'credentials.json'
        for path in (self.control_path, self.credentials_path):
            self.check(not path.is_symlink(), 'refusing symlink control files')
        self.control = json.loads(self.control_path.read_text()) if self.control_path.exists() else {}
        self.credentials = json.loads(self.credentials_path.read_text()) if self.credentials_path.exists() else {}
        self.helper.SECRETS.extend(self.credentials.values())
        self.report = json.loads(args.report.read_text()) if args.report.exists() else {'phases': []}
        self.phase = {'phase': args.phase, 'started_at': self.stamp(), 'checks': []}
        self.base = 'http://127.0.0.1:8080'
        self.env = {key: value for key, value in os.environ.items()
                    if not key.startswith('DM_') and key not in
                    ('DOCKER_HOST', 'DOCKER_CONTEXT', 'DOCKER_CERT_PATH', 'DOCKER_TLS_VERIFY', 'DOCKER_CONFIG')}

    @staticmethod
    def stamp():
        return datetime.datetime.now(datetime.timezone.utc).isoformat()

    @staticmethod
    def check(condition, message):
        if not condition:
            raise RuntimeError(message)

    def command(self, *args, **kwargs):
        return self.helper.command(args, env=self.env, **kwargs)

    def record(self, name, **evidence):
        serialized = json.dumps(evidence)
        self.check(all(value not in serialized for value in self.credentials.values()),
                   'credential found in public evidence')
        self.phase['checks'].append({'name': name, 'status': 'passed', 'evidence': evidence})
        print('PASS ' + name, flush=True)

    def save(self):
        private_json(self.control_path, self.control)
        if self.credentials:
            private_json(self.credentials_path, self.credentials)

    def unit(self):
        text = self.command('systemctl', 'show', 'docker-manager',
                            '--property=LoadState,ActiveState,UnitFileState,MainPID,User,Group,ProtectHome').stdout.decode()
        return dict(line.split('=', 1) for line in text.splitlines() if '=' in line)

    def healthy(self):
        until = time.monotonic() + 90
        while time.monotonic() < until:
            try:
                with urllib.request.urlopen(self.base + '/healthz', timeout=2) as response:
                    if response.status == 200:
                        break
            except Exception:
                pass
            time.sleep(0.5)
        else:
            raise RuntimeError('systemd service did not become healthy in 90 seconds')
        unit = self.unit()
        self.check(unit.get('ActiveState') == 'active' and unit.get('UnitFileState') == 'enabled',
                   'service must be active and enabled')
        self.check(unit.get('User') == 'docker-manager' and unit.get('Group') == 'docker-manager',
                   'service must use its dedicated account')
        self.check(unit.get('ProtectHome') == 'yes', 'ProtectHome must remain enabled')
        return unit

    def docker(self):
        until = time.monotonic() + 90
        while time.monotonic() < until:
            result = self.command('docker', '--host', 'unix:///var/run/docker.sock', 'version',
                                  '--format', '{{.Server.Version}}', allow_failure=True)
            if not result.returncode:
                break
            time.sleep(0.5)
        else:
            raise RuntimeError('an existing local Docker Engine must be running for this acceptance test')
        pid = self.command('systemctl', 'show', 'docker', '--property=MainPID', '--value').stdout.decode().strip()
        self.check(pid.isdigit() and int(pid) > 0, 'cannot identify the existing Docker daemon')
        process = Path('/proc') / pid
        fields = (process / 'stat').read_text().rsplit(')', 1)[1].split()
        return {'version': result.stdout.decode().strip(), 'pid': pid, 'start_ticks': fields[19],
                'executable': os.readlink(process / 'exe')}

    def client(self, existing=False):
        api = self.helper.Client(self.base)
        cookies = http.cookiejar.LWPCookieJar(str(self.state / 'cookies.txt'))
        if existing:
            cookies.load(ignore_discard=True, ignore_expires=True)
        api.cookies = cookies
        api.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cookies))
        return api

    def save_cookies(self, api):
        api.cookies.save(ignore_discard=True, ignore_expires=True)
        Path(api.cookies.filename).chmod(0o600)

    def fresh_login(self):
        api = self.client()
        status = api.call('GET', '/auth/status')
        self.check(status['initialized'], 'administrator initialization did not persist')
        value = api.call('POST', '/auth/login', self.credentials)
        self.check(value['authenticated'] and value['user']['id'] == self.control['admin_id'],
                   'administrator login did not retain the same user')
        self.save_cookies(api)
        return value['user']['id']

    def assert_retained(self):
        self.check(digest(DATA / 'master.key') == self.control['master_key_sha256'], 'master.key changed')
        self.check(digest(BINARY) == self.control['binary_sha256'], 'installed binary bytes changed')
        self.check(BINARY.stat().st_ino == self.control['binary_inode'], 'same-version binary was replaced')
        status = self.client(existing=True).call('GET', '/auth/status')
        self.check(status['initialized'] and status['authenticated'] and
                   status['user']['id'] == self.control['admin_id'], 'persistent administrator session was lost')
        return self.fresh_login()

    def install_wrapper(self):
        # Override download transport/selection only. Verification, dependencies,
        # worker load, deploy helper, and service lifecycle are production code.
        wrapper = self.state / 'run-online.sh'
        wrapper.write_text('''#!/usr/bin/env bash
set -euo pipefail
source "$TEST_SOURCE/scripts/install-online.sh"
eval "$(declare -f fetch | sed '1s/^fetch /upstream_fetch /')"
fetch() {
  case "$1" in
    https://github.com/Funnyrz/docklane/releases/download/"$TEST_VERSION"/*)
      local name=${1##*/}
      [[ $name != */* && -f $TEST_ASSETS/$name && ! -L $TEST_ASSETS/$name ]] || die 'Missing local candidate asset.'
      cp -- "$TEST_ASSETS/$name" "$2"
      ;;
    *) upstream_fetch "$@" ;;
  esac
}
resolve_release() {
  version=${version:-$TEST_VERSION}
  [[ $version == "$TEST_VERSION" ]] || die 'Unexpected acceptance version.'
  local required=(RELEASE-SHA256SUMS LICENSE SHA256SUMS IMAGE-SHA256SUMS IMAGES.txt
    docker-manager-images.tar.gz docker-manager-installation.tar.gz
    "docklane-$version-linux-$arch.tar.gz" "docklane-$version-linux-$arch.tar.gz.sha256")
  printf '%s\\n' "$version" "${required[@]}" > "$work/release-selection"
  local name
  for name in "${required[@]}"; do
    fetch "https://github.com/Funnyrz/docklane/releases/download/$version/$name" "$work/$name"
  done
}
main "$@"
''')
        wrapper.chmod(0o700)
        return wrapper

    def invoke_online(self):
        wrapper = self.install_wrapper()
        result = self.helper.command(['bash', str(wrapper), '--version', VERSION],
                                     env={**self.env, 'TEST_SOURCE': str(self.args.source),
                                          'TEST_ASSETS': str(self.args.assets), 'TEST_VERSION': VERSION},
                                     timeout=2400, allow_failure=True)
        log = self.state / ('installer-' + secrets.token_hex(4) + '.log')
        with log.open('wb') as stream:
            stream.write(result.stdout + b'\n' + result.stderr)
        log.chmod(0o600)
        self.check(result.returncode == 0, 'online installer failed; inspect private log: ' + str(log) + '\n' +
                   self.helper.clean_message(result.stderr.decode(errors='replace')[-2500:]))
        return log.name

    def install(self):
        self.check(not self.control, 'install phase already has state; use resume after an actual guest reboot')
        for path in (DATA, Path('/etc/docker-manager'), BINARY, UNIT, Path('/usr/local/bin/docker-manager')):
            self.check(not path.exists() and not path.is_symlink(), 'refusing existing installation path: ' + str(path))
        self.check(self.unit().get('LoadState') == 'not-found', 'refusing an existing docker-manager service')
        with socket.socket() as probe:
            probe.bind(('127.0.0.1', 8080))
        self.check((self.args.assets / 'install-online.sh').read_bytes() ==
                   (self.args.source / 'scripts/install-online.sh').read_bytes(),
                   'candidate online entrypoint differs from the supplied source')
        if shutil.which('docker', path=self.env.get('PATH')):
            before = self.docker()
        else:
            self.check(self.args.allow_missing_docker,
                       'an existing Docker Engine is required unless --allow-missing-docker is explicit')
            before = None
        self.control = {'source': str(self.args.source), 'assets': str(self.args.assets),
                        'version': VERSION, 'boot_before': boot_identity(), 'docker_before': before}
        self.save()
        log = self.invoke_online()
        unit = self.healthy()
        after = self.docker()
        if before:
            self.check(after == before, 'installer changed the existing Docker daemon identity or version')
        else:
            self.check(after['version'].startswith('29.'), 'missing Docker was not installed as supported stable 29.x')
            self.command('systemctl', 'is-enabled', '--quiet', 'docker')
        self.control['docker_after_initial'] = after
        self.save()
        self.record('local candidate online install starts and enables hardened systemd service',
                    unit=unit, docker_before=before, docker_after=after,
                    docker_installed=before is None, private_log=log)
        api = self.client()
        self.check(not api.call('GET', '/auth/status')['initialized'], 'fresh installation was already initialized')
        self.credentials = {'username': 'online-' + secrets.token_hex(6), 'password': secrets.token_urlsafe(36)}
        self.helper.SECRETS.extend(self.credentials.values())
        self.save()
        value = api.call('POST', '/auth/setup', self.credentials)
        self.control.update(admin_id=value['user']['id'], master_key_sha256=digest(DATA / 'master.key'),
                            binary_sha256=digest(BINARY), binary_inode=BINARY.stat().st_ino)
        self.save_cookies(api)
        self.save()
        self.fresh_login()
        self.record('first administrator setup and fresh login succeed', admin_id=self.control['admin_id'])
        repeated_log = self.invoke_online()
        unit = self.healthy()
        self.assert_retained()
        self.check(self.docker() == after, 'repeated installer changed the Docker daemon')
        self.control['installed'] = True
        self.save()
        self.record('same-version repeat preserves binary inode, master key, administrator and daemon',
                    binary_inode=self.control['binary_inode'], binary_sha256=self.control['binary_sha256'],
                    unit=unit, private_log=repeated_log)

    def resume(self):
        self.check(self.control.get('installed'), 'install phase must pass before resume')
        current_boot = boot_identity()
        self.check(current_boot['boot_id'] != self.control['boot_before']['boot_id'],
                   'resume requires a real guest reboot with a changed kernel boot ID')
        unit = self.healthy()
        self.assert_retained()
        current_docker = self.docker()
        installed_docker = self.control.get('docker_after_initial') or self.control['docker_before']
        self.check(current_docker['version'] == installed_docker['version'],
                   'Docker version changed across the guest reboot')
        self.control['resumed'] = True
        self.save()
        self.record('guest reboot retains automatic startup, health, key, administrator and session',
                    before=self.control['boot_before'], after=current_boot,
                    unit=unit, docker=current_docker, binary_inode=self.control['binary_inode'])

    def run(self):
        try:
            getattr(self, self.args.phase)()
            self.phase['status'] = 'passed'
        except Exception as error:
            self.phase['status'] = 'failed'
            self.phase['error'] = self.helper.clean_message(error)
            raise
        finally:
            self.phase['completed_at'] = self.stamp()
            self.report['phases'].append(self.phase)
            self.report['limitations'] = [
                'Candidate Release selection/downloads are redirected to local files; public HTTP is a separate smoke test.',
                'Existing Docker is preserved; explicit --allow-missing-docker additionally verifies stable 29.x installation and Docker boot startup.',
                'Worker loading uses production code; real volume backup/restore is verified separately.',
                'Installation intentionally remains in the disposable guest for reboot/resume verification.',
            ]
            private_json(self.args.report, self.report)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--assets', type=Path, required=True)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--report', type=Path, required=True)
    parser.add_argument('--phase', choices=('install', 'resume'), required=True)
    parser.add_argument('--allow-missing-docker', action='store_true',
                        help='In an initially clean guest, verify automatic stable Docker 29.x installation')
    args = parser.parse_args()
    if sys.platform != 'linux' or os.geteuid() != 0 or os.getenv('DM_PLATFORM_ISOLATED') != '1':
        parser.error('Run only as root inside an isolated Linux VM with DM_PLATFORM_ISOLATED=1')
    if not Path('/run/systemd/system').is_dir() or Path('/proc/1/comm').read_text().strip() != 'systemd':
        parser.error('A real running systemd guest is required')
    args.assets, args.source, args.report = args.assets.resolve(), args.source.resolve(), args.report.absolute()
    if args.report.is_symlink():
        parser.error('Refusing a symlink report')
    args.report.parent.mkdir(parents=True, exist_ok=True)
    os.umask(0o077)
    try:
        Acceptance(args).run()
    except Exception as error:
        print('FAIL ' + str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
