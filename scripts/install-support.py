#!/usr/bin/env python3
"""Local deployment lifecycle for verified Docklane release materials (Python 3)."""

import argparse
import contextlib
import copy
import datetime
import fcntl
import grp
import hashlib
import json
import os
from pathlib import Path
import pwd
import re
import select
import shlex
import shutil
import signal
import socket
import sqlite3
import subprocess
import sys
import time
import urllib.request


# Checked by the online bootstrap before Docker, tool or service changes.
LAZY_WORKER_DOWNLOAD = True

DATA = Path('/var/lib/docklane')
CONFIG = Path('/etc/docklane/config.json')
BINARY = Path('/usr/local/bin/docklane')
UNIT = Path('/etc/systemd/system/docklane.service')
BACKUPS = Path('/var/backups/docklane')
STATE = Path('/var/lib/docklane-installer/release.json')
LICENSE = Path('/usr/share/licenses/docklane/LICENSE')
SERVICE = 'docklane'
ACCOUNT = 'docklane'
LEGACY_PATHS = (
    Path('/etc/docker-manager'), Path('/var/lib/docker-manager'), Path('/var/backups/docker-manager'),
    Path('/usr/local/bin/docker-manager'),
    Path('/etc/systemd/system/multi-user.target.wants/docker-manager.service'),
    *(Path(root) / name for root in ('/etc/systemd/system', '/run/systemd/system', '/usr/lib/systemd/system', '/lib/systemd/system')
      for name in ('docker-manager.service', 'docker-manager.service.d')),
)
TOOL_PATH = '/usr/local/lib/docklane/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
STOP_TIMEOUT = 180
HEALTH_TIMEOUT = 60
ENV_FIELDS = {
    'DM_LISTEN': 'listen', 'DM_DATA_DIR': 'data_dir',
    'DM_DOCKER_ENDPOINT': 'docker_endpoint', 'DM_MODE': 'mode',
    'DM_RESTIC_PATH': 'restic_path', 'DM_COMPOSE_PATH': 'compose_path',
    'DM_BACKUP_WORKER_IMAGE': 'backup_worker_image', 'DM_LOG_LEVEL': 'log_level',
    'DM_BACKUP_TEMP_DIR': 'backup_temp_dir',
}
GO_DEFAULTS = {
    'version': 1, 'log_level': 'info', 'listen': ':9999', 'data_dir': './data',
    'docker_endpoint': 'unix:///var/run/docker.sock', 'mode': 'binary',
    'daemon_config_path': '/etc/docker/daemon.json', 'compose_path': 'docker',
    'restic_path': 'restic', 'backup_worker_image': 'docklane:v0.3.2-beta.2',
    'allowed_paths': [], 'path_mappings': [], 'enable_daemon_write': False,
    'session_secure': False, 'backup_temp_dir': '',
}


class InstallError(Exception):
    pass


def refuse_legacy_installation():
    """Inspect legacy names without creating directories or changing services."""
    for path in LEGACY_PATHS:
        if path.exists() or path.is_symlink():
            raise InstallError(f'Legacy docker-manager installation found at {path}. This release requires a new Docklane installation; preserve and resolve the old installation manually.')
    result = run(['systemctl', 'show', 'docker-manager.service', '--property=LoadState,FragmentPath,DropInPaths'], check=False)
    props = dict(line.split('=', 1) for line in result.stdout.splitlines() if '=' in line)
    if len([line for line in result.stdout.splitlines() if line.startswith('LoadState=')]) != 1 or not props.get('LoadState'):
        raise InstallError('Cannot verify the legacy docker-manager.service state; no installation changes were made.')
    if props['LoadState'] != 'not-found' or props.get('FragmentPath') or props.get('DropInPaths'):
        raise InstallError('Legacy docker-manager.service is still known to systemd. This release does not rename or migrate it; resolve it manually before installation.')


def material_version(source_dir):
    version_file = Path(source_dir) / 'VERSION'
    package = Path(source_dir) / 'web/package.json'
    value = version_file.read_text().strip() if version_file.is_file() else (read_json(package).get('version') if package.is_file() else '0.3.2-beta.2')
    version_key(value)
    return 'v' + value.removeprefix('v')


def version_key(value):
    match = re.fullmatch(r'v?(\d+)\.(\d+)\.(\d+)(?:-beta\.(\d+))?', value or '')
    if not match:
        raise InstallError('Cannot safely compare installed/selected release version.')
    return tuple(map(int, match.group(1, 2, 3))) + (1 if match[4] is None else 0, int(match[4] or 0))


def prevent_implicit_downgrade(selected, installed, explicit=False):
    if installed and not explicit and version_key(selected) < version_key(installed):
        raise InstallError('Implicit downgrade refused; specify --version explicitly and retain a matching recovery backup.')


def run(args, check=True, **kwargs):
    try:
        result = subprocess.run([str(x) for x in args], text=True, capture_output=True, timeout=200, **kwargs)
    except subprocess.TimeoutExpired as exc:
        raise InstallError(f'{args[0]} timed out; inspect the system before retrying.') from exc
    if check and result.returncode:
        raise InstallError(f'{args[0]} failed: {result.stderr.strip() or result.stdout.strip()}')
    return result


def digest(path):
    h = hashlib.sha256()
    with open(path, 'rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def absolute(path, cwd):
    candidate = Path(path)
    return Path(os.path.abspath(candidate if candidate.is_absolute() else Path(cwd) / candidate))


def read_json(path):
    value = json.loads(Path(path).read_text())
    if not isinstance(value, dict):
        raise InstallError(f'Expected a JSON object: {path}')
    return value


def process_context(pid):
    proc = Path('/proc') / str(pid)
    env = {}
    for entry in (proc / 'environ').read_bytes().split(b'\0'):
        key, sep, value = entry.partition(b'=')
        if sep and key.startswith(b'DM_'):
            env[key.decode()] = value.decode()
    return env, Path(os.readlink(proc / 'cwd'))


def stop_process(lock):
    """A pidfd prevents PID reuse between verification and SIGTERM."""
    if not lock.owner:
        return
    try:
        descriptor = os.pidfd_open(lock.owner)
    except (AttributeError, OSError) as exc:
        raise InstallError('Safe migration requires Linux pidfd support; source process was not signaled.') from exc
    try:
        process_identity(lock.owner, os.fstat(lock.stream.fileno()))
        signal.pidfd_send_signal(descriptor, signal.SIGTERM)
        poller = select.poll()
        poller.register(descriptor, select.POLLIN)
        if not poller.poll(STOP_TIMEOUT * 1000):
            raise InstallError('Docklane did not exit within 180 seconds; no process was killed.')
        lock.acquire_after_stop()
    finally:
        os.close(descriptor)


def process_identity(pid, lock_stat):
    """Do not trust the PID text without a matching kernel FLOCK record."""
    proc = Path('/proc') / str(pid)
    executable = proc / 'exe'
    actual_name = Path(os.readlink(executable).removesuffix(' (deleted)')).name
    if actual_name not in ('docklane', 'docker-manager', 'docklane-linux-amd64', 'docklane-linux-arm64', 'docker-manager-linux-amd64', 'docker-manager-linux-arm64'):
        raise InstallError('The data-directory lock owner is not a recognized Docklane executable.')
    with executable.open('rb') as stream:
        if b'github.com/docker-manager/docker-manager' not in stream.read():
            raise InstallError('The lock owner does not contain the Docklane build identity.')
    args = (proc / 'cmdline').read_bytes().split(b'\0')
    if len(args) > 1 and args[1] in (b'backup-worker', b'restore-worker'):
        raise InstallError('The lock owner is a worker, not the Docklane server.')
    identity = (os.major(lock_stat.st_dev), os.minor(lock_stat.st_dev), lock_stat.st_ino)
    for line in Path('/proc/locks').read_text().splitlines():
        parts = line.split()
        if len(parts) < 8 or parts[1:5] != ['FLOCK', 'ADVISORY', 'WRITE', str(pid)]:
            continue
        major, minor, inode = parts[5].split(':')
        if (int(major, 16), int(minor, 16), int(inode)) == identity:
            return
    raise InstallError('The PID in server.lock does not own its kernel file lock; refusing to signal it.')


class DataLock:
    def __init__(self, directory):
        self.path = Path(directory) / 'server.lock'
        self.stream = None
        self.owner = None

    def inspect(self):
        if self.path.is_symlink():
            raise InstallError('server.lock must not be a symlink.')
        self.stream = self.path.open('a+')
        try:
            fcntl.flock(self.stream.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
            return None
        except BlockingIOError:
            self.stream.seek(0)
            try:
                self.owner = int(self.stream.read().strip())
            except ValueError as exc:
                raise InstallError('A live data lock has no valid PID.') from exc
            if self.owner <= 1:
                raise InstallError('Invalid server lock PID.')
            process_identity(self.owner, os.fstat(self.stream.fileno()))
            return self.owner

    def acquire_after_stop(self):
        deadline = time.monotonic() + STOP_TIMEOUT
        while True:
            try:
                fcntl.flock(self.stream.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
                actual, held = os.stat(self.path), os.fstat(self.stream.fileno())
                if (actual.st_dev, actual.st_ino) != (held.st_dev, held.st_ino):
                    raise InstallError('server.lock changed while stopping the old process.')
                return
            except BlockingIOError:
                if time.monotonic() >= deadline:
                    raise InstallError('Docklane did not release its lock within 180 seconds; no process was killed.')
                time.sleep(0.2)

    def close(self):
        if self.stream:
            self.stream.close()
            self.stream = None


def service_context():
    result = run(['systemctl', 'show', SERVICE, '--property=LoadState,FragmentPath,ExecStart,User,Group,WorkingDirectory,Environment,DropInPaths,MainPID,Restart'], check=False)
    props = dict(line.split('=', 1) for line in result.stdout.splitlines() if '=' in line)
    if props.get('LoadState', 'not-found') == 'not-found':
        if UNIT.exists() or UNIT.is_symlink():
            raise InstallError('An unrecognized service unit already exists; inspect it before installing.')
        return None
    if props.get('FragmentPath') != str(UNIT) or props.get('User') != ACCOUNT or props.get('Group') != ACCOUNT:
        raise InstallError('Existing docklane service is not a standard managed installation.')
    if props.get('DropInPaths'):
        raise InstallError('Service has local drop-ins; reconcile them before automatic deployment.')
    if props.get('Restart') != 'on-failure':
        raise InstallError('Managed service requires Restart=on-failure for a safe graceful stop.')
    start = props.get('ExecStart', '')
    if f'path={BINARY} ' not in start:
        raise InstallError('Existing service executes an unknown binary.')
    env = dict(item.split('=', 1) for item in shlex.split(props.get('Environment', '')) if '=' in item)
    if 'DM_CONFIG' not in env:
        raise InstallError('Managed service has no DM_CONFIG; provide a standard service first.')
    cwd = Path(props.get('WorkingDirectory') or str(DATA))
    pid = int(props.get('MainPID', '0') or '0')
    if pid:
        live_env, cwd = process_context(pid)
        env.update(live_env)
    return env, cwd, pid


def effective_config(source_dir, env, cwd, explicit=None, migration=False, live_context=False):
    # An existing process began from Go defaults, not the fresh-install example.
    config = copy.deepcopy(GO_DEFAULTS) if migration else read_json(source_dir / 'deployments/config.example.json')
    if migration:
        config['backup_worker_image'] = 'docklane:' + material_version(source_dir)
    selected = explicit or env.get('DM_CONFIG')
    startup = {}
    if selected:
        startup = read_json(absolute(selected, cwd))
        config.update(startup)
    elif migration and not live_context:
        raise InstallError('Cannot recover the old startup configuration. Supply --config with the old effective JSON configuration.')
    if migration and 'listen' not in startup and not env.get('DM_LISTEN'):
        raise InstallError('Cannot recover the old listen address after the default port change. Supply --config containing the old effective listen value.')
    for key, field in ENV_FIELDS.items():
        if env.get(key):
            config[field] = env[key]
    if env.get('DM_ALLOWED_PATHS'):
        config['allowed_paths'] = env['DM_ALLOWED_PATHS'].split(',')
    if env.get('DM_PATH_MAPPINGS'):
        config['path_mappings'] = json.loads(env['DM_PATH_MAPPINGS'])
    for key, field in [('DM_SESSION_SECURE', 'session_secure'), ('DM_ENABLE_DAEMON_WRITE', 'enable_daemon_write')]:
        if env.get(key) == 'true':
            config[field] = True
    for field in ('data_dir', 'backup_temp_dir', 'daemon_config_path'):
        if config.get(field):
            config[field] = str(absolute(config[field], cwd))
    if not config.get('backup_temp_dir'):
        config['backup_temp_dir'] = str(Path(config['data_dir']) / 'tmp')
    for field in ('compose_path', 'restic_path'):
        if '/' in config.get(field, ''):
            config[field] = str(absolute(config[field], cwd))
    if config.get('mode') != 'binary':
        raise InstallError('Only binary-mode installations can be managed by this installer.')
    if Path(config['data_dir']).resolve() == Path('/'):
        raise InstallError('Refusing a filesystem-root data directory.')
    return config, absolute(selected, cwd) if selected else None


def database_paths(directory, config):
    """Read saved grants with the same precedence as ApplySavedCapabilitySettings."""
    paths = set()
    db_path = Path(directory) / 'docker-manager.db'
    if not db_path.exists():
        return paths
    if not (Path(directory) / 'master.key').is_file() or (Path(directory) / 'master.key').stat().st_size != 32:
        raise InstallError('A database requires its original 32-byte master.key; source data was not changed.')
    with contextlib.closing(sqlite3.connect(db_path.as_uri() + '?mode=ro', uri=True)) as db:
        tables = {row[0] for row in db.execute("SELECT name FROM sqlite_master WHERE type='table'")}
        if 'settings' in tables:
            saved = db.execute("SELECT value FROM settings WHERE key='capability_settings_v1'").fetchone()
            if saved:
                value = json.loads(saved[0])
                if set(value) != {'allowed_paths', 'path_mappings', 'enable_daemon_write'} or not isinstance(value['allowed_paths'], list) or not isinstance(value['path_mappings'], list) or not isinstance(value['enable_daemon_write'], bool):
                    raise InstallError('Saved capability settings are incomplete or use an unsupported schema; repair them before migration.')
                config.update({key: value[key] for key in ('allowed_paths', 'path_mappings', 'enable_daemon_write')})
            fingerprint = db.execute("SELECT value FROM settings WHERE key='master_key_fingerprint'").fetchone()
            if fingerprint:
                key = Path(directory) / 'master.key'
                if not key.is_file() or digest(key) != fingerprint[0]:
                    raise InstallError('Database and master.key do not match; restore a matching pair before migration.')
        if 'compose_projects' in tables:
            for workdir, files, envfiles in db.execute('SELECT workdir,config_paths,env_files FROM compose_projects'):
                paths.add((str(workdir), 'write'))
                for name in json.loads(files) + json.loads(envfiles):
                    paths.add((str(absolute(name, workdir)), 'write'))
        if 'backup_repositories' in tables:
            paths.update((row[0], 'write') for row in db.execute('SELECT location FROM backup_repositories'))
        if 'backup_policies' in tables:
            for (definition,) in db.execute('SELECT definition FROM backup_policies'):
                for target in json.loads(definition).get('targets', []):
                    if target.get('path'):
                        paths.add((target['path'], 'read'))
    return paths


def check_external_paths(config, database_items):
    identity = service_identity()
    items = set(database_items)
    items.update((p, 'traverse') for p in config.get('allowed_paths', []))
    for mapping in config.get('path_mappings', []):
        items.update((mapping[key], 'traverse') for key in ('host', 'local'))
    for name, role in sorted(items):
        path = Path(name)
        if not path.is_absolute() or path.resolve() == Path('/'):
            raise InstallError(f'An external path is not an explicit absolute path: {name}')
        if path.resolve() == DATA.resolve() or DATA.resolve() in path.resolve().parents:
            continue
        if any(path.resolve() == root.resolve() or root.resolve() in path.resolve().parents for root in (Path('/home'), Path('/root'), Path('/run/user'))):
            raise InstallError(f'ProtectHome blocks {name}. Move the resource to an accessible authorized directory before migration; permissions will not be relaxed.')
        probe = path
        while not probe.exists() and probe != probe.parent:
            probe = probe.parent
        flags = ['-x'] if role in ('traverse', 'execute') else ['-r']
        if role == 'write':
            flags.append('-w')
        if probe.is_dir() and '-x' not in flags:
            flags.append('-x')
        for flag in flags:
            if run(identity + ['test', flag, str(probe)], check=False).returncode:
                raise InstallError(f'{ACCOUNT} cannot access {name} ({flag}); grant the required access explicitly before retrying.')


def tool_paths(config, include_temp=True):
    items = set()
    for field in ('restic_path', 'compose_path'):
        if Path(config.get(field, '')).is_absolute():
            items.add((config[field], 'execute'))
    if include_temp and config.get('backup_temp_dir'):
        items.add((config['backup_temp_dir'], 'write'))
    endpoint = config.get('docker_endpoint', '')
    if endpoint.startswith('unix://'):
        # The systemd unit adds the docker supplementary group for the socket.
        socket_path = Path(endpoint.removeprefix('unix://'))
        for root in (Path('/home'), Path('/root'), Path('/run/user')):
            if socket_path.resolve() == root.resolve() or root.resolve() in socket_path.resolve().parents:
                raise InstallError('The configured Docker socket is blocked by ProtectHome.')
    return items


def account():
    try:
        group = grp.getgrnam(ACCOUNT)
    except KeyError:
        run(['groupadd', '--system', ACCOUNT])
        group = grp.getgrnam(ACCOUNT)
    try:
        user = pwd.getpwnam(ACCOUNT)
        if user.pw_gid != group.gr_gid or user.pw_uid == 0:
            raise InstallError('Existing service account has unexpected UID or primary group.')
    except KeyError:
        run(['useradd', '--system', '--gid', ACCOUNT, '--home-dir', str(DATA), '--shell', '/usr/sbin/nologin', ACCOUNT])
        user = pwd.getpwnam(ACCOUNT)
    return user.pw_uid, group.gr_gid


def require_docker_group():
    try:
        grp.getgrnam('docker')
    except KeyError as exc:
        raise InstallError('Docker is running but the docker group is missing. Configure the local Engine socket group explicitly before installation; this installer will not modify the existing socket.') from exc


def check_docker_socket():
    identity = service_identity()
    for flag in ('-r', '-w'):
        result = run(identity + ['test', flag, '/var/run/docker.sock'], check=False)
        if result.returncode:
            raise InstallError('The dedicated account with the docker supplementary group cannot access /var/run/docker.sock. Repair socket permissions explicitly; the installer will not modify them.')


def service_groups():
    primary = grp.getgrnam(ACCOUNT).gr_gid
    groups = set(os.getgrouplist(ACCOUNT, primary)) | {grp.getgrnam('docker').gr_gid}
    return sorted(grp.getgrgid(gid).gr_name for gid in groups)


def service_identity():
    command = ['runuser', '-u', ACCOUNT, '-g', ACCOUNT]
    for group in service_groups():
        command.extend(['-G', group])
    return command + ['--']


def owned_directory(path, uid, gid, mode):
    path.mkdir(parents=True, exist_ok=True)
    if path.is_symlink():
        raise InstallError(f'Refusing a symlink at installation path: {path}')
    os.chown(path, uid, gid)
    path.chmod(mode)


def install_file(source, target, mode, uid=0, gid=0):
    if target.is_symlink():
        raise InstallError(f'Refusing to replace symlink: {target}')
    target.parent.mkdir(parents=True, exist_ok=True)
    temp = target.with_name(target.name + '.install-tmp')
    if temp.exists() or temp.is_symlink():
        raise InstallError(f'Temporary install path already exists: {temp}')
    try:
        shutil.copyfile(source, temp)
        temp.chmod(mode)
        os.chown(temp, uid, gid)
        os.replace(temp, target)
    finally:
        temp.unlink(missing_ok=True)


def write_json(path, value, mode=0o600, uid=0, gid=0):
    if path.is_symlink():
        raise InstallError(f'Refusing symlink: {path}')
    path.parent.mkdir(parents=True, exist_ok=True)
    # Parent is a private directory controlled by the installer.
    import tempfile
    fd, name = tempfile.mkstemp(prefix='.install-', dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as output:
            json.dump(value, output, indent=2)
            output.write('\n')
        os.chmod(name, mode)
        os.chown(name, uid, gid)
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def copy_tree(source, target):
    """Keep ownership as well as copy2 metadata for matching-group recovery."""
    shutil.copytree(source, target, symlinks=True)
    for item in [Path(source), *Path(source).rglob('*')]:
        metadata = item.lstat()
        os.lchown(Path(target) / item.relative_to(source), metadata.st_uid, metadata.st_gid)


def backup(directory, config, original_config, effective=None):
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%S.%fZ')
    destination = BACKUPS / stamp
    owned_directory(BACKUPS, 0, 0, 0o700)
    destination.mkdir(mode=0o700)
    copy_tree(directory, destination / 'data')
    expected = tree_hashes(directory)
    if tree_hashes(destination / 'data') != expected:
        raise InstallError(f'Backup verification failed: {destination}. Source data was retained.')
    write_json(destination / 'startup-config.json', config)
    write_json(destination / 'effective-config.json', effective if effective is not None else config)
    if original_config:
        shutil.copy2(original_config, destination / 'original-config.json')
    if CONFIG.parent.exists():
        copy_tree(CONFIG.parent, destination / 'config')
    for path, name in ((BINARY, 'docklane'), (UNIT, 'docklane.service'), (STATE, 'release.json')):
        if path.is_file():
            shutil.copy2(path, destination / name)
    write_json(destination / 'restore.json', {'data_dir': str(directory), 'installed_data_dir': str(DATA), 'binary': str(BINARY), 'config': str(CONFIG), 'unit': str(UNIT)})
    write_json(destination / 'data-sha256.json', expected)
    print(f'Consistent stopped backup: {destination}', flush=True)
    return destination


def validate_data(directory):
    for base, directories, files in os.walk(directory, followlinks=False):
        for name in directories + files:
            item = Path(base) / name
            if item.is_symlink() or not (item.is_file() or item.is_dir()):
                raise InstallError(f'Application data contains a symlink or special file: {item}; resolve it before migration.')


def tree_hashes(directory):
    return {str(path.relative_to(directory)): digest(path) for path in sorted(Path(directory).rglob('*')) if path.is_file()}


def checked_worker_image(configured, version):
    """Advance release defaults without inspecting or downloading Docker images.

    The application verifies and prepares its native worker when a task needs
    one. Custom references remain under the administrator's control.
    """
    if re.fullmatch(r'docklane:v\d+\.\d+\.\d+(?:-beta\.\d+)?(?:-(?:amd64|arm64))?', configured or ''):
        return 'docklane:' + version
    return configured


def check_engine_endpoint(config):
    if config.get('docker_endpoint', GO_DEFAULTS['docker_endpoint']) != GO_DEFAULTS['docker_endpoint']:
        raise InstallError('This installer prepares only unix:///var/run/docker.sock. A different docker_endpoint is unsupported; source configuration and process were not changed.')


def listen_address(value):
    if value.startswith(':'):
        host, port = '127.0.0.1', value[1:]
    elif value.startswith('['):
        host, separator, port = value[1:].partition(']:')
        if not separator:
            raise InstallError('Invalid listen address.')
    else:
        host, separator, port = value.rpartition(':')
        if not separator:
            raise InstallError('Listen address must include a TCP port.')
    try:
        if not 1 <= int(port) <= 65535:
            raise ValueError()
    except ValueError as exc:
        raise InstallError('Invalid listen port.') from exc
    if host in ('', '0.0.0.0'):
        host = '127.0.0.1'
    if host == '::':
        host = '::1'
    return host, int(port)


def check_port(listen):
    host, port = listen_address(listen)
    for family, kind, proto, _, address in socket.getaddrinfo(host, port, type=socket.SOCK_STREAM):
        try:
            with socket.socket(family, kind, proto) as probe:
                # Go's listener also reuses stopped sockets in TIME_WAIT.
                # An actively listening process still prevents this bind.
                probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                probe.bind(address)
            return
        except OSError:
            continue
    raise InstallError(f'Listen address {listen} is already occupied or unavailable; no new service was started.')


def health_check(listen):
    host, port = listen_address(listen)
    host = f'[{host}]' if ':' in host else host
    endpoint = f'http://{host}:{port}/healthz'
    # Local service checks must never go through an administrator's HTTP proxy.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    deadline = time.monotonic() + HEALTH_TIMEOUT
    while time.monotonic() < deadline:
        active = run(['systemctl', 'is-active', '--quiet', SERVICE], check=False).returncode == 0
        if active:
            try:
                with opener.open(endpoint, timeout=2) as response:
                    if response.status == 200:
                        run(['systemctl', 'is-enabled', '--quiet', SERVICE])
                        return
            except (OSError, ValueError):
                pass
        time.sleep(1)
    raise InstallError('The service did not become active and healthy within 60 seconds.')


def deploy(args):
    if sys.platform != 'linux' or os.geteuid() != 0 or not Path('/run/systemd/system').is_dir():
        raise InstallError('Deployment requires root on Linux with systemd running.')
    refuse_legacy_installation()
    source_dir = Path(args.source_dir).resolve(strict=True)
    if material_version(source_dir) != args.version:
        raise InstallError('Installation material version does not match the selected release.')
    candidate = Path(args.binary).resolve(strict=True)
    if digest(candidate) != args.sha256.lower():
        raise InstallError('Binary SHA-256 does not match; installation refused.')
    for required in ('deployments/docklane.service', 'deployments/config.example.json', 'LICENSE'):
        if not (source_dir / required).is_file():
            raise InstallError(f'Release installation material missing: {required}')
    run(['docker', 'info'])
    require_docker_group()
    if not run(['docker', 'compose', 'version', '--short']).stdout.strip().lstrip('v').startswith('2.'):
        raise InstallError('Docker Compose v2 is required.')
    if not run(['restic', 'version'], env={**os.environ, 'PATH': TOOL_PATH}).stdout.startswith('restic 0.19.1 '):
        raise InstallError('Restic 0.19.1 is required on the service PATH.')
    lock_path = Path('/run/lock/docklane-install.lock')
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    with lock_path.open('a') as installer_lock:
        try:
            fcntl.flock(installer_lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise InstallError('Another Docklane installer is already running.') from exc
        _deploy_locked(args, source_dir, candidate)


def _deploy_locked(args, source_dir, candidate):
    refuse_legacy_installation()
    managed = service_context()
    managed_unit = bool(managed)
    installed_state = read_json(STATE) if STATE.is_file() else {}
    prevent_implicit_downgrade(args.version, installed_state.get('version'), getattr(args, 'explicit_version', False))
    # Uninstall deliberately retains this private receipt and the data/config.
    if not managed and STATE.is_file() and CONFIG.is_file() and not BINARY.exists() and not UNIT.exists():
        if STATE.is_symlink() or STATE.stat().st_uid != os.geteuid() or STATE.stat().st_mode & 0o077:
            raise InstallError('Installer receipt is not a private root-owned regular file.')
        retained = read_json(STATE)
        retained_config = read_json(CONFIG)
        if not retained.get('data_dir') or Path(retained['data_dir']).resolve() != Path(retained_config.get('data_dir', '')).resolve():
            raise InstallError('Retained installer receipt and configuration disagree on data_dir.')
        managed = ({'DM_CONFIG': str(CONFIG)}, DATA, 0)
    if args.migrate_from and managed:
        raise InstallError('--migrate-from cannot replace an existing managed installation.')
    env, cwd = {}, source_dir
    lock = None
    backup_path = None
    service_touched = False
    migration = bool(args.migrate_from)
    existing = bool(managed)
    source_data = None
    old_pid = None
    try:
        if managed:
            env, cwd, old_pid = managed
        elif migration:
            source_data = Path(args.migrate_from).resolve(strict=True)
            if source_data == Path('/') or not source_data.is_dir():
                raise InstallError('Migration source must be an existing non-root data directory.')
            if DATA.exists() and any(DATA.iterdir()):
                raise InstallError('Migration target is nonempty; refusing to overwrite existing data.')
            if source_data == DATA or DATA in source_data.parents or source_data in DATA.parents:
                raise InstallError('Migration source and target must not overlap.')
            lock = DataLock(source_data)
            old_pid = lock.inspect()
            if old_pid:
                env, cwd = process_context(old_pid)
            elif not args.config:
                raise InstallError('Stopped migration sources require --config with an effective JSON configuration and absolute paths.')
        elif BINARY.exists() or BINARY.is_symlink():
            raise InstallError('An unmanaged Docklane binary exists; use explicit migration or resolve it first.')
        elif DATA.exists() and any(DATA.iterdir()):
            raise InstallError('The data directory is nonempty without a recognized service; use explicit migration.')

        config, original_config = effective_config(source_dir, env, cwd, args.config, migration or existing, live_context=bool(old_pid))
        startup_config = copy.deepcopy(config)
        check_engine_endpoint(config)
        target_data = Path(config['data_dir'])
        if not migration and not existing and target_data.exists() and any(target_data.iterdir()):
            raise InstallError('Configured data directory is nonempty; use explicit migration rather than a fresh install.')
        if not migration and any(target_data.resolve() == root.resolve() or root.resolve() in target_data.resolve().parents for root in (Path('/home'), Path('/root'), Path('/run/user'))):
            raise InstallError('Configured data_dir is blocked by ProtectHome; use an accessible service data directory.')
        if not existing and not migration and CONFIG.exists() and not args.config:
            raise InstallError('An unmanaged configuration exists; provide --config explicitly to preserve it.')
        if migration and Path(config['data_dir']).resolve() != source_data:
            raise InstallError('Recovered data_dir differs from --migrate-from; provide the actual old configuration.')
        if existing:
            source_data = Path(config['data_dir']).resolve(strict=True)
            lock = DataLock(source_data)
            owner = lock.inspect()
            if owner and owner != old_pid:
                raise InstallError('The effective data directory is locked by a process other than the managed MainPID.')
            if old_pid and not owner:
                raise InstallError('Managed MainPID does not hold the effective data-directory lock; refusing to stop it.')
        if source_data:
            validate_data(source_data)
        uid, gid = account()
        check_docker_socket()
        preflight_config = copy.deepcopy(config)
        if source_data:
            database_items = database_paths(source_data, preflight_config) | tool_paths(preflight_config, include_temp=not migration)
            check_external_paths(preflight_config, database_items)
        elif args.config:
            check_external_paths(config, tool_paths(config, include_temp=False))
        old_worker = config.get('backup_worker_image', GO_DEFAULTS['backup_worker_image'])
        config['backup_worker_image'] = checked_worker_image(old_worker, args.version)
        worker_changed = config['backup_worker_image'] != old_worker
        if args.listen:
            config['listen'] = args.listen
        listen_address(config['listen'])
        same_binary = BINARY.is_file() and digest(BINARY) == args.sha256.lower()
        old_state = read_json(STATE) if STATE.is_file() else {}
        same = existing and old_pid and same_binary and old_state.get('version') == args.version and not args.config and not args.listen and not worker_changed and not getattr(args, 'explicit_version', False)
        if same:
            lock.close()
            run(['systemctl', 'enable', '--now', SERVICE])
            # A status-only retry must never stop a previously running service.
            # Its JSON may contain a new listen address awaiting a restart.
            health_check(config['listen'])
            print(f'Docklane {args.version} already installed; binary unchanged. Service is healthy and enabled.')
            return
        if existing:
            # Exit gracefully first; old units may otherwise escalate stop to SIGKILL.
            stop_process(lock)
            if managed_unit:
                run(['systemctl', 'stop', SERVICE])
                service_touched = True
            lock.acquire_after_stop()
        elif migration and old_pid:
            stop_process(lock)
        if source_data:
            validate_data(source_data)
            backup_path = backup(source_data, startup_config, original_config, effective=preflight_config)
            # Settings may have changed between the live preflight and SIGTERM.
            # Recheck the final stopped database before copying or changing files.
            preflight_config = copy.deepcopy(startup_config)
            database_items = database_paths(source_data, preflight_config) | tool_paths(preflight_config, include_temp=not migration)
            write_json(backup_path / 'effective-config.json', preflight_config)
            check_external_paths(preflight_config, database_items)
        check_port(config['listen'])
        owned_directory(CONFIG.parent, 0, gid, 0o750)
        if migration:
            config['data_dir'] = str(DATA)
            config['backup_temp_dir'] = str(DATA / 'tmp')
            if DATA.exists():
                DATA.rmdir()
            shutil.copytree(source_data, DATA, symlinks=True)
            for base, directories, files in os.walk(DATA, followlinks=False):
                os.chown(base, uid, gid)
                for name in directories + files:
                    os.lchown(Path(base) / name, uid, gid)
        effective_data = Path(config['data_dir'])
        owned_directory(effective_data, uid, gid, 0o700)
        if effective_data.resolve() != DATA.resolve():
            # The shipped unit keeps a stable WorkingDirectory even with a custom data_dir.
            owned_directory(DATA, uid, gid, 0o700)
        # Preserve all resolved settings, including saved capability grants.
        write_json(CONFIG, config, mode=0o640, uid=0, gid=gid)
        if not same_binary:
            install_file(candidate, BINARY, 0o755)
        install_file(source_dir / 'deployments/docklane.service', UNIT, 0o644)
        install_file(source_dir / 'LICENSE', LICENSE, 0o644)
        owned_directory(STATE.parent, 0, 0, 0o700)
        if lock:
            lock.close()
        run(['systemctl', 'daemon-reload'])
        service_touched = True
        run(['systemctl', 'enable', '--now', SERVICE])
        health_check(config['listen'])
        write_json(STATE, {'version': args.version, 'sha256': args.sha256.lower(), 'data_dir': str(effective_data)})
        print(f'Docklane {args.version} is healthy and enabled at {config["listen"]}.')
        print(f'Config: {CONFIG}; logs: journalctl -u {SERVICE} -f')
        print('Backup worker images are downloaded only when a backup or restore task first needs one. Offline hosts must import a matching native image beforehand.')
    except Exception:
        stop_message = 'No new service was started.'
        if service_touched:
            try:
                stopped = run(['systemctl', 'stop', SERVICE], check=False)
                remaining = run(['systemctl', 'show', SERVICE, '--property=MainPID', '--value'], check=False)
                if stopped.returncode == 0 and remaining.stdout.strip() == '0':
                    stop_message = 'The new service is stopped.'
                else:
                    stop_message = 'A service stop was requested but not confirmed. Inspect systemctl status and MainPID before recovery; no process was forcibly killed.'
            except (InstallError, OSError):
                stop_message = 'The service stop timed out or failed. Inspect systemctl status and MainPID before recovery; no process was forcibly killed.'
        if backup_path:
            print(f'Installation failed. {stop_message} Source data and consistent backup retained at {backup_path}. Restore the binary, unit, config, database and master.key from this SAME backup group using docs/operations.md. No automatic rollback was attempted.', file=sys.stderr)
        elif service_touched:
            print(stop_message, file=sys.stderr)
        raise
    finally:
        if lock:
            lock.close()


def uninstall():
    if sys.platform != 'linux' or os.geteuid() != 0 or not Path('/run/systemd/system').is_dir():
        raise InstallError('Uninstall requires root on Linux with systemd running.')
    refuse_legacy_installation()
    lock_path = Path('/run/lock/docklane-install.lock')
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    with lock_path.open('a') as installer_lock:
        try:
            fcntl.flock(installer_lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise InstallError('Another Docklane installer is already running.') from exc
        _uninstall_locked()


def _uninstall_locked():
    refuse_legacy_installation()
    # A receipt and the matching managed unit protect unrelated executables.
    if not service_context():
        raise InstallError('No recognized managed docklane.service was found; no files were removed.')
    for path in (UNIT, STATE, BINARY):
        if path.is_symlink() or not path.is_file() or path.stat().st_uid != 0 or path.stat().st_mode & 0o022:
            raise InstallError(f'Refusing to remove an unrecognized or insecure installation file: {path}')
    if STATE.stat().st_mode & 0o077:
        raise InstallError('Installer receipt is not private; no files were removed.')
    state = read_json(STATE)
    version_key(state.get('version'))
    if state.get('sha256') != digest(BINARY):
        raise InstallError('Installed binary does not match the Docklane receipt; no files were removed.')
    run(['systemctl', 'disable', '--now', SERVICE])
    remaining = run(['systemctl', 'show', SERVICE, '--property=MainPID', '--value'])
    if remaining.stdout.strip() != '0':
        raise InstallError('The Docklane service did not confirm a graceful stop; no files were removed.')
    UNIT.unlink()
    BINARY.unlink()
    run(['systemctl', 'daemon-reload'])
    print('Docklane service removed. Configuration, application data, account, installer receipt and backup repositories were preserved.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    command = commands.add_parser('deploy')
    command.add_argument('--binary', required=True)
    command.add_argument('--source-dir', required=True)
    command.add_argument('--version', required=True)
    command.add_argument('--sha256', required=True)
    command.add_argument('--listen')
    command.add_argument('--migrate-from')
    command.add_argument('--config')
    command.add_argument('--explicit-version', action='store_true')
    inspect_version = commands.add_parser('version')
    inspect_version.add_argument('--source-dir', required=True)
    commands.add_parser('uninstall')
    commands.add_parser('check-legacy')
    args = parser.parse_args()
    try:
        if args.command == 'version':
            print(material_version(args.source_dir))
            return 0
        if args.command == 'check-legacy':
            refuse_legacy_installation()
            return 0
        if os.environ.get('DOCKLANE_NOTICE_SHOWN') != '1':
            print('本程序尚未经过充分、严格的测试，仅供学习与测试使用，请勿用于生产环境。使用前请自行评估风险并备份重要数据。', flush=True)
            print('This program has not undergone sufficient, rigorous testing. It is for learning and testing only; do not use it in production. Assess the risks yourself and back up important data before use.', flush=True)
        if args.command == 'uninstall':
            uninstall()
        else:
            deploy(args)
    except (InstallError, OSError, ValueError, sqlite3.Error) as exc:
        print(f'Installation refused: {exc}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
