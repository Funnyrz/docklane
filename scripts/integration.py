#!/usr/bin/env python3
"""Exercise the compiled service against real Linux Docker, Compose and Restic.

DM_TEST_BINARY is normally passed by integration.sh. Optional configuration:
DM_TEST_DOCKER, DM_TEST_DOCKER_ENDPOINT, DM_TEST_RESTIC, DM_TEST_IMAGE,
DM_TEST_WORKER_IMAGE, DM_TEST_TMPDIR, DM_TEST_REPORT, DM_TEST_KEEP_ARTIFACTS.
No system-wide prune, daemon reconfiguration or operational resource mutation.
"""
import argparse
import datetime
import hashlib
import http.cookiejar
import http.client
import json
import os
import pathlib
import secrets
import shutil
import signal
import socket
import sqlite3
import stat
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request
import urllib.parse

LABEL = "io.docker-manager.integration"
SECRETS = []
RESULTS = []
TASK_IDS = set()


def clean_message(value):
    text = str(value)
    for secret in SECRETS:
        text = text.replace(secret, "[REDACTED]")
    return text


def check(condition, message):
    if not condition:
        raise RuntimeError(message)


def record(name, detail=""):
    RESULTS.append({"name": name, "status": "passed", "detail": detail})
    print("PASS " + name + (": " + detail if detail else ""), flush=True)


def command(args, *, env=None, cwd=None, data=None, timeout=180, allow_failure=False):
    result = subprocess.run(
        [str(arg) for arg in args], cwd=cwd, env=env, input=data,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout,
    )
    if result.returncode and not allow_failure:
        # Do not print argv: they can include generated credentials or env values.
        raise RuntimeError("external tool failed (%s): %s" % (
            pathlib.Path(str(args[0])).name, clean_message(result.stderr.decode(errors="replace")[-3000:])))
    return result


def executable(value, name):
    resolved = shutil.which(value)
    check(resolved is not None, "%s is required (configured path: %s)" % (name, value))
    return str(pathlib.Path(resolved).resolve())


class Client:
    def __init__(self, base):
        self.base = base
        self.cookies = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.cookies))

    def token(self):
        return next((cookie.value for cookie in self.cookies if cookie.name == "csrf_token"), "")

    def call(self, method, path, body=None, *, expected=200, key=None, csrf=True, raw=None):
        headers = {}
        data = raw
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        if raw is not None:
            headers["Content-Type"] = "application/x-tar"
        if method not in ("GET", "HEAD") and csrf:
            headers["X-CSRF-Token"] = self.token()
        if key:
            headers["Idempotency-Key"] = key
        request = urllib.request.Request(self.base + "/api/v1" + path, data=data, headers=headers, method=method)
        try:
            response = self.opener.open(request, timeout=240)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            payload = response.read()
            code = response.code
        allowed = (expected,) if isinstance(expected, int) else expected
        if code not in allowed:
            error_text = payload.decode(errors="replace")[:2000]
            raise RuntimeError("%s %s: expected HTTP %s, got %s: %s" % (
                method, path, allowed, code, clean_message(error_text)))
        if not payload:
            return None
        try:
            return json.loads(payload)
        except ValueError:
            raise RuntimeError("%s %s returned non-JSON response" % (method, path))

    def task(self, accepted, timeout=300, expected_status="Succeeded"):
        check(isinstance(accepted, dict) and accepted.get("id"), "operation did not return a task ID")
        task_id = accepted["id"]
        TASK_IDS.add(task_id)
        until = time.monotonic() + timeout
        while time.monotonic() < until:
            value = self.call("GET", "/tasks/" + task_id)
            if value["status"] in ("Succeeded", "Failed", "Canceled"):
                if value["status"] != expected_status:
                    try:
                        logs = self.call("GET", "/tasks/" + task_id + "/logs")
                        diagnostic = "\n".join(entry["message"] for entry in logs[-10:])[-6000:]
                    except Exception:
                        diagnostic = "task logs unavailable"
                    raise RuntimeError("task %s returned unexpected status: %s; sanitized task logs: %s" % (
                        task_id, clean_message(value.get("error_message", "")), clean_message(diagnostic)))
                return value
            time.sleep(0.15)
        raise RuntimeError("task %s timed out" % task_id)

    def download(self, path, destination):
        request = urllib.request.Request(self.base + "/api/v1" + path)
        with self.opener.open(request, timeout=240) as response, open(destination, "wb") as output:
            check(response.code == 200, "image export did not return HTTP 200")
            shutil.copyfileobj(response, output, 1024 * 1024)


class Server:
    def __init__(self, binary, root, state_name, docker_endpoint, docker, restic, worker):
        self.binary = binary
        self.root = root
        self.state = root / state_name
        self.state.mkdir(mode=0o700)
        self.config_path = root / (state_name + "-config.json")
        self.log_path = root / (state_name + "-server.log")
        self.process = None
        self.output = None
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            self.port = sock.getsockname()[1]
        self.base = "http://127.0.0.1:%d" % self.port
        config = {
            "listen": "127.0.0.1:%d" % self.port,
            "data_dir": str(self.state), "mode": "binary",
            "docker_endpoint": docker_endpoint, "allowed_paths": [str(root)],
            "path_mappings": [], "compose_path": docker, "restic_path": restic,
            "daemon_config_path": str(root / "daemon-never-change.json"),
            "enable_daemon_write": False, "session_secure": False,
            "backup_worker_image": worker or "docker-manager:integration-worker-not-supplied",
        }
        self.config_path.write_text(json.dumps(config))
        self.config_path.chmod(0o600)

    def start(self):
        check(self.process is None, "server already running")
        self.output = open(self.log_path, "ab", buffering=0)
        # Explicit test config is authoritative; prevent inherited production DM_* overrides.
        env = {key: value for key, value in os.environ.items() if not key.startswith("DM_")}
        env["DM_CONFIG"] = str(self.config_path)
        self.process = subprocess.Popen([self.binary], stdout=self.output, stderr=self.output, env=env)
        until = time.monotonic() + 45
        while time.monotonic() < until:
            if self.process.poll() is not None:
                raise RuntimeError("server exited during startup: " + self.logs())
            try:
                with urllib.request.urlopen(self.base + "/healthz", timeout=1) as response:
                    if response.status == 200:
                        return
            except (OSError, urllib.error.URLError):
                time.sleep(0.15)
        raise RuntimeError("server did not become healthy: " + self.logs())

    def crash(self):
        process = self.process
        check(process is not None and process.poll() is None, "cannot crash an inactive owned test server")
        process.kill()  # Popen owns this exact child; no process-name or guessed PID kill.
        process.wait(timeout=15)
        self.process = None
        if self.output:
            self.output.close()
            self.output = None
        check(process.returncode == -signal.SIGKILL, "owned panel did not exit from SIGKILL")

    def logs(self):
        return clean_message(self.log_path.read_text(errors="replace")[-5000:])

    def stop(self):
        process = self.process
        if process is None:
            return
        self.process = None
        if process.poll() is None:
            process.send_signal(signal.SIGTERM)
            try:
                process.wait(timeout=40)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
                raise RuntimeError("integration server did not stop gracefully")
        if self.output:
            self.output.close()
            self.output = None
        check(process.returncode == 0, "server shutdown failed: " + self.logs())


def digest(path):
    with open(path, "rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def assert_restored(source_metadata, restored):
    for rel, metadata in source_metadata.items():
        path = restored / rel
        info = path.lstat()
        check(stat.S_IMODE(info.st_mode) == metadata["mode"], "restored mode mismatch: " + rel)
        check(info.st_uid == metadata["uid"] and info.st_gid == metadata["gid"], "restored ownership mismatch: " + rel)
        if "hash" in metadata:
            check(digest(path) == metadata["hash"], "restored file content mismatch: " + rel)
        if "link" in metadata:
            check(os.readlink(path) == metadata["link"], "restored symlink mismatch: " + rel)


def metadata_tree(source):
    result = {}
    for path in [source] + sorted(source.rglob("*")):
        info = path.lstat()
        value = {"mode": stat.S_IMODE(info.st_mode), "uid": info.st_uid, "gid": info.st_gid}
        if path.is_symlink():
            value["link"] = os.readlink(path)
        elif path.is_file():
            value["hash"] = digest(path)
        result[str(path.relative_to(source))] = value
    return result


def process_identity(pid):
    """Read Linux identity without signalling a process or confusing a reused PID."""
    proc = pathlib.Path("/proc") / str(pid)
    try:
        fields = (proc / "stat").read_text().rsplit(")", 1)[1].split()
        return {"pid": int(pid), "state": fields[0], "parent": int(fields[1]), "started": fields[19]}
    except (OSError, ValueError, IndexError):
        return None


def restic_backup_child(panel_pid, restic):
    for proc in pathlib.Path("/proc").iterdir():
        if not proc.name.isdigit():
            continue
        identity = process_identity(proc.name)
        if not identity or identity["parent"] != panel_pid or identity["state"] == "Z":
            continue
        try:
            executable_path = (proc / "exe").resolve(strict=True)
            args = (proc / "cmdline").read_bytes().split(b"\0")
        except OSError:
            continue
        if executable_path == pathlib.Path(restic) and b"backup" in args:
            return identity
    return None


def main(binary):
    check(sys.version_info >= (3, 11), "Python 3.11 or newer is required")
    docker = executable(os.getenv("DM_TEST_DOCKER", "docker"), "Docker CLI")
    restic = executable(os.getenv("DM_TEST_RESTIC", "restic"), "Restic")
    docker_env = os.environ.copy()
    endpoint = os.getenv("DM_TEST_DOCKER_ENDPOINT") or os.getenv("DOCKER_HOST")
    if not endpoint:
        endpoint = command([docker, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}"], env=docker_env).stdout.decode().strip()
    check(endpoint.startswith(("unix://", "tcp://")), "test Docker endpoint must be unix:// or tcp://")
    docker_env["DOCKER_HOST"] = endpoint
    info = json.loads(command([docker, "info", "--format", "{{json .}}"], env=docker_env).stdout)
    check(info.get("OSType") == "linux", "integration requires a Linux Docker Engine")
    compose_version = command([docker, "compose", "version", "--short"], env=docker_env).stdout.decode().strip()
    check(compose_version.lstrip("v").startswith("2."), "Docker Compose v2 is required")
    restic_version = command([restic, "version"]).stdout.decode().strip().splitlines()[0]
    print("Real tools: Docker %s; Compose %s; %s" % (info.get("ServerVersion"), compose_version, restic_version), flush=True)
    token = secrets.token_hex(8)
    prefix = "dm-e2e-" + token
    temp_parent = os.getenv("DM_TEST_TMPDIR")
    root = pathlib.Path(tempfile.mkdtemp(prefix=prefix + "-", dir=temp_parent)).resolve()
    root.chmod(0o700)
    print("Owned test namespace: " + prefix, flush=True)
    servers = []
    containers = []
    volumes = []
    compose_args = None
    tagged_image = None
    offline_state = None
    privileged_cleanup_dirs = []
    failed = True
    report = {"namespace": prefix, "engine": info.get("ServerVersion"), "compose": compose_version,
              "restic": restic_version, "binary_sha256": digest(binary), "worker_image_id": None, "host": {"system": os.uname().sysname, "architecture": os.uname().machine, "uid": os.getuid()},
              "engine_os": info.get("OperatingSystem"), "started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "tests": RESULTS, "limitations": ["systemd/distro and dual-architecture runtime matrix are separate release checks",
              "daemon configuration mutation/restart is a separate isolated release check"]}
    try:
        def dc(*args, data=None, timeout=180, allow_failure=False, env=None):
            return command([docker] + list(args), env=env or docker_env, data=data, timeout=timeout, allow_failure=allow_failure)

        def engine_get(path):
            # CLI image ls applies presentation filters; compare the dashboard with raw Engine summaries.
            if endpoint.startswith("unix://"):
                class UnixConnection(http.client.HTTPConnection):
                    def connect(self):
                        self.sock = socket.socket(socket.AF_UNIX)
                        self.sock.settimeout(15)
                        self.sock.connect(endpoint.removeprefix("unix://"))
                connection = UnixConnection("localhost", timeout=15)
            else:
                address = urllib.parse.urlsplit(endpoint)
                connection = http.client.HTTPConnection(address.hostname, address.port or 2375, timeout=15)
            try:
                connection.request("GET", path)
                response = connection.getresponse()
                check(response.status == 200, "independent Engine HTTP inventory failed")
                return json.loads(response.read())
            finally:
                connection.close()

        def inspect_container(container_id):
            return json.loads(dc("inspect", "--type", "container", container_id).stdout)[0]

        def owned(container_id):
            return inspect_container(container_id).get("Config", {}).get("Labels", {}).get(LABEL) == token

        image = os.getenv("DM_TEST_IMAGE", "busybox:1.37.0")
        if dc("image", "inspect", image, allow_failure=True).returncode:
            dc("pull", image, timeout=300)
        image_id = json.loads(dc("image", "inspect", image).stdout)[0]["Id"]
        tagged_image = prefix + ":fixture"
        dc("tag", image_id, tagged_image)
        worker = os.getenv("DM_TEST_WORKER_IMAGE", "")
        if worker:
            worker = json.loads(dc("image", "inspect", worker).stdout)[0]["Id"]
        report["worker_image_id"] = worker or None
        source = root / "source"
        source.mkdir(mode=0o750)
        (source / "nested").mkdir(mode=0o700)
        (source / "nested" / "payload.bin").write_bytes(secrets.token_bytes(65536))
        (source / "nested" / "payload.bin").chmod(0o640)
        (source / "unicode.txt").write_text("真实 Docklane 恢复验证\n", encoding="utf-8")
        (source / "unicode.txt").chmod(0o600)
        (source / "link").symlink_to("unicode.txt")
        metadata = metadata_tree(source)
        daemon_file = root / "daemon-never-change.json"
        daemon_bytes = b'{"log-driver":"json-file","integration-sentinel":true}\n'
        daemon_file.write_bytes(daemon_bytes)
        container_name = prefix + "-external"
        fixture_id = dc("run", "--detach", "--name", container_name, "--label", LABEL + "=" + token,
                        "--mount", "type=bind,src=%s,dst=/owned-data" % source,
                        image_id, "sh", "-c", "trap 'exit 0' TERM INT; while :; do sleep 1; done").stdout.decode().strip()
        containers.append(fixture_id)
        before = inspect_container(fixture_id)
        check(before["State"]["Running"], "CLI fixture did not start")
        server = Server(binary, root, "state", endpoint, docker, restic, worker)
        servers.append(server)
        server.start()
        client = Client(server.base)
        status = client.call("GET", "/auth/status")
        check(not status["initialized"] and not status["authenticated"], "fresh temporary state was already initialized")
        client.call("GET", "/hosts/local/containers", expected=401)
        username = prefix
        password = secrets.token_urlsafe(36)
        repository_password = secrets.token_urlsafe(36)
        SECRETS.extend([password, repository_password])
        client.call("POST", "/auth/setup", {"username": username, "password": password}, csrf=False, expected=403)
        setup = client.call("POST", "/auth/setup", {"username": username, "password": password})
        user_id = setup["user"]["id"]
        client.call("POST", "/auth/setup", {"username": username, "password": password}, expected=409)
        check(inspect_container(fixture_id)["Id"] == before["Id"] and inspect_container(fixture_id)["State"]["Running"],
              "installation changed the existing CLI container")
        record("installation preserves pre-existing container ID and running state")
        found = client.call("GET", "/hosts/local/containers?q=" + fixture_id)
        check(any(value["Id"] == fixture_id for value in found["items"]), "CLI-created container missing from discovery")
        client.call("POST", "/hosts/local/containers/" + fixture_id + "/stop", {}, csrf=False, expected=403)
        check(inspect_container(fixture_id)["State"]["Running"], "CSRF-rejected request changed a container")
        client.call("GET", "/hosts/not-local/containers", expected=404)
        client.call("GET", "/this-api-does-not-exist", expected=404)
        daemon = client.call("GET", "/host/daemon")
        check(not daemon["available"], "unauthorized daemon capability is enabled")
        denied = client.call("POST", "/host/daemon/apply", {"mirrors": ["https://registry-1.docker.io"],
                             "revision": hashlib.sha256(daemon_bytes).hexdigest(), "confirm": True, "confirm_restart": True}, expected=403)
        check(denied["error"]["code"] == "CAPABILITY_UNAVAILABLE", "daemon write failed for validation instead of capability enforcement")
        check(daemon_file.read_bytes() == daemon_bytes, "forbidden daemon operation modified the sentinel file")
        record("authentication, single setup, CSRF, API404 and daemon capability enforcement")
        # Populate every state category and compare read-only refreshes with independent CLI inventory.
        stopped_fixture = dc("create", "--name", prefix + "-stopped", "--label", LABEL + "=" + token,
                             image_id, "true").stdout.decode().strip()
        containers.append(stopped_fixture)
        paused_fixture = dc("run", "--detach", "--name", prefix + "-paused", "--label", LABEL + "=" + token,
                            image_id, "sleep", "3600").stdout.decode().strip()
        containers.append(paused_fixture)
        dc("pause", paused_fixture)
        dashboard_volume = prefix + "-dashboard-data"
        dc("volume", "create", "--label", LABEL + "=" + token, dashboard_volume)
        volumes.append(dashboard_volume)

        def engine_snapshot():
            ids = dc("ps", "--all", "--quiet", "--no-trunc").stdout.decode().split()
            inspected = json.loads(dc("inspect", "--type", "container", *ids).stdout) if ids else []
            stable = {}
            counts = {"total": len(inspected), "running": 0, "stopped": 0, "abnormal": 0}
            for value in inspected:
                state = value["State"]
                category = "running" if state["Status"] == "running" else "stopped" if state["Status"] in ("exited", "created") else "abnormal"
                counts[category] += 1
                stable[value["Id"]] = {key: value.get(key) for key in ("Name", "Image", "Config", "HostConfig", "Mounts", "NetworkSettings", "RestartCount")}
                stable[value["Id"]]["State"] = {key: state.get(key) for key in ("Status", "Running", "Paused", "Restarting", "Dead", "Pid", "ExitCode", "StartedAt", "FinishedAt", "OOMKilled")}
            images = sorted(value["Id"] for value in engine_get("/images/json"))
            volume_names = sorted(dc("volume", "ls", "--quiet").stdout.decode().split())
            networks = sorted(dc("network", "ls", "--quiet", "--no-trunc").stdout.decode().split())
            return {"containers": stable, "images": images, "volumes": volume_names, "networks": networks}, counts

        inventory_before, expected_counts = engine_snapshot()
        check(all(expected_counts[key] > 0 for key in ("running", "stopped", "abnormal")), "dashboard fixtures did not populate every state category")
        for _ in range(3):
            dashboard = client.call("GET", "/hosts/local/dashboard")
            check(dashboard["docker"]["connected"] and not dashboard.get("resource_error"), "dashboard could not read real Engine resources")
            check(dashboard["containers"] == expected_counts, "dashboard container counts differ from independent Engine inventory")
            check(dashboard["images"] == len(inventory_before["images"]), "dashboard image count differs from Engine")
            check(dashboard["volumes"] == len(inventory_before["volumes"]), "dashboard volume count differs from Engine")
        inventory_after, after_counts = engine_snapshot()
        check(inventory_after == inventory_before and after_counts == expected_counts, "dashboard refresh mutated Engine resources or container running state")
        report["dashboard_counts"] = dict(expected_counts, images=len(inventory_before["images"]), volumes=len(inventory_before["volumes"]))
        record("dashboard counts match independent Engine inventory and repeated refresh preserves resources", "independent CLI/Engine HTTP; running/stopped/paused fixtures; three refreshes; IDs/config/mounts/network/state unchanged")
        dc("unpause", paused_fixture)
        stop_key = prefix + "-stop"
        first = client.call("POST", "/hosts/local/containers/" + fixture_id + "/stop", {}, key=stop_key, expected=202)
        second = client.call("POST", "/hosts/local/containers/" + fixture_id + "/stop", {}, key=stop_key, expected=202)
        check(first["id"] == second["id"], "lifecycle idempotency created duplicate tasks")
        stopped_task = client.task(first)
        check(not inspect_container(fixture_id)["State"]["Running"], "stop task succeeded without actual Engine stop")
        client.task(client.call("POST", "/hosts/local/containers/" + fixture_id + "/start", {}, expected=202))
        client.task(client.call("POST", "/hosts/local/containers/" + fixture_id + "/restart", {}, expected=202))
        check(inspect_container(fixture_id)["State"]["Running"], "start/restart task did not verify running state")
        record("CLI discovery and Engine-verified lifecycle with task idempotency")
        rebuild_volume = prefix + "-rebuild-data"
        dc("volume", "create", "--label", LABEL + "=" + token, rebuild_volume)
        volumes.append(rebuild_volume)
        rebuild_name = prefix + "-rebuild"
        rebuild_id = dc("run", "--detach", "--name", rebuild_name, "--label", LABEL + "=" + token,
                        "--health-cmd", "test -d /", "--health-interval", "1s", "--health-timeout", "1s", "--health-retries", "1",
                        "--mount", "type=volume,src=%s,dst=/data" % rebuild_volume, image_id, "sleep", "3600").stdout.decode().strip()
        containers.append(rebuild_id)
        dc("exec", rebuild_id, "sh", "-c", "printf retained-volume-data > /data/payload")
        health_deadline = time.monotonic() + 30
        while time.monotonic() < health_deadline:
            if inspect_container(rebuild_id)["State"].get("Health", {}).get("Status") == "healthy":
                break
            time.sleep(0.2)
        check(inspect_container(rebuild_id)["State"].get("Health", {}).get("Status") == "healthy", "rebuild fixture never became healthy")
        changes = {"cmd": ["sleep", "3500"]}
        change_preview = client.call("POST", "/hosts/local/containers/" + rebuild_id + "/config/preview", {"changes": changes})
        client.task(client.call("POST", "/hosts/local/containers/" + rebuild_id + "/config", {"changes": changes, "fingerprint": change_preview["fingerprint"], "confirmed": True}, expected=202))
        replacement = inspect_container(rebuild_name)
        replacement_id = replacement["Id"]
        containers.append(replacement_id)
        check(replacement_id != rebuild_id and replacement["State"].get("Health", {}).get("Status") == "healthy", "rebuild did not produce a new healthy container")
        check(dc("exec", replacement_id, "cat", "/data/payload").stdout == b"retained-volume-data", "rebuild discarded volume data")
        failing_changes = {"cmd": ["/definitely-missing-docker-manager-e2e-command"]}
        failure_preview = client.call("POST", "/hosts/local/containers/" + replacement_id + "/config/preview", {"changes": failing_changes})
        client.task(client.call("POST", "/hosts/local/containers/" + replacement_id + "/config", {"changes": failing_changes,
                         "fingerprint": failure_preview["fingerprint"], "confirmed": True}, expected=202), expected_status="Failed")
        after_failure = inspect_container(rebuild_name)
        check(after_failure["Id"] == replacement_id and after_failure["State"]["Running"], "failed rebuild did not restore the original container")
        check(dc("exec", replacement_id, "cat", "/data/payload").stdout == b"retained-volume-data", "failed rebuild modified retained data")
        record("healthy container rebuild creates new ID and failing replacement restores original ID/data")
        archive = root / "image.tar"
        client.download("/hosts/local/images/" + image_id + "/export", archive)
        with tarfile.open(archive) as saved:
            check(any(name.endswith("manifest.json") or name == "index.json" for name in saved.getnames()), "export is not a Docker/OCI image tar")
        # Valid tar zero padding exercises the import-only body cap above the ordinary 4 MiB API limit.
        minimum_tar_size = 5 * 1024 * 1024
        if archive.stat().st_size < minimum_tar_size:
            with open(archive, "ab") as padding:
                padding.write(bytes(minimum_tar_size - archive.stat().st_size))
        with open(archive, "rb") as source_tar:
            imported_task = client.task(client.call("POST", "/hosts/local/images/import", raw=iter(lambda: source_tar.read(1 << 20), b""), expected=202))
        check(json.loads(dc("image", "inspect", image_id).stdout)[0]["Id"] == image_id, "imported image unavailable")
        record("real streamed Docker image export and asynchronous tar import", "%d-byte tar exceeds ordinary API body cap" % archive.stat().st_size)
        compose_dir = root / "compose"
        compose_dir.mkdir()
        (compose_dir / "data").mkdir()
        base = compose_dir / "compose.json"
        override = compose_dir / "override.json"
        env_file = compose_dir / "context.env"
        service_env = compose_dir / "service.env"
        environment_value = "explicit-" + token
        env_file.write_text("E2E_VALUE=from-file\nDOT_CONTEXT=from-context\n")
        service_env.write_text("SERVICE_FILE_VALUE=from-service-env\n")
        (compose_dir / ".env").write_text("IGNORED_DOTFILE=preserved-for-revision\n")
        base.write_text(json.dumps({"services": {
            "main": {"image": image_id, "command": ["sleep", "3600"], "labels": {LABEL: token},
                     "env_file": ["service.env"], "environment": {"FROM_INTERPOLATION": "${E2E_VALUE:?}", "FROM_CONTEXT": "${DOT_CONTEXT:?}"},
                     "volumes": ["./data:/data"], "ports": ["127.0.0.1::18080"]},
            "profiled": {"image": image_id, "command": ["sleep", "3600"], "profiles": ["integration"], "labels": {LABEL: token}}}}))
        override.write_text(json.dumps({"services": {"main": {"labels": {"integration.override": "present"}},
                                                     "profiled": {"environment": {"PROFILE_ACTIVE": "yes"}}}}))
        compose_args = [docker, "compose", "--project-name", prefix, "--file", str(base), "--file", str(override),
                        "--env-file", str(env_file), "--profile", "integration"]
        compose_env = dict(docker_env, E2E_VALUE=environment_value)
        command(compose_args + ["up", "--detach", "--wait", "--pull", "never"], env=compose_env, cwd=compose_dir, timeout=180)
        compose_ids = dc("ps", "--all", "--quiet", "--filter", "label=com.docker.compose.project=" + prefix).stdout.decode().split()
        containers.extend(compose_ids)
        check(len(compose_ids) == 2, "Compose profile context did not create both services")
        compose_before = {cid: inspect_container(cid) for cid in compose_ids}
        discovered = client.call("GET", "/compose/discover")
        check(any(value["project"]["name"] == prefix for value in discovered), "Compose project not discovered")
        project = client.call("POST", "/compose/import", {"name": prefix, "workdir": str(compose_dir), "config_paths": [str(base), str(override)],
                            "env_files": [str(env_file)], "profiles": ["integration"], "environment": {"E2E_VALUE": environment_value}, "confirm": True}, expected=201)
        for cid, prior in compose_before.items():
            current = inspect_container(cid)
            check(current["Id"] == prior["Id"] and current["Mounts"] == prior["Mounts"] and current["HostConfig"]["PortBindings"] == prior["HostConfig"]["PortBindings"],
                  "Compose import rebuilt or changed an existing service")
        detail = client.call("GET", "/compose/projects/" + project["id"])["project"]
        check(detail["config_paths"] == [str(base), str(override)] and detail["env_files"] == [str(env_file)] and detail["profiles"] == ["integration"],
              "Compose file/env/profile order was not retained")
        preview = client.call("POST", "/compose/projects/" + project["id"] + "/preview", {})
        configuration = preview["configuration"]
        if isinstance(configuration, str):
            configuration = json.loads(configuration)
        main_service = configuration["services"]["main"]
        check(main_service["environment"]["FROM_INTERPOLATION"] == environment_value and main_service["environment"]["SERVICE_FILE_VALUE"] == "from-service-env",
              "Compose interpolation or service env_file context lost")
        check("profiled" in configuration["services"], "Compose profile context lost")
        record("real Compose multiconfig/env/profile import preserves IDs, ports and mounts")
        repository_path = root / "repository"
        repository = client.call("POST", "/backup/repositories", {"name": prefix, "location": str(repository_path), "password": repository_password}, expected=201)
        recovery = client.call("POST", "/backup/repositories/" + repository["id"] + "/export", {"confirm": True})
        check(recovery.get("password") == repository_password and recovery.get("location") == str(repository_path), "offline recovery material differs from repository")

        def create_policy(name, targets):
            return client.call("POST", "/backup/policies", {"name": name, "repository_id": repository["id"], "targets": targets,
                "consistency_mode": "stop", "schedule": "", "timezone": "Etc/UTC", "keep_last": 3,
                "excludes": [], "retries": 0, "retry_seconds": 0, "enabled": True}, expected=201)

        def backup(policy):
            value = client.call("POST", "/backup/policies/" + policy["id"] + "/preview", {})
            check(all(owned(cid) for cid in value["stop_containers"]), "refusing backup: writer scope includes an unowned container")
            completed = client.task(client.call("POST", "/backup/policies/" + policy["id"] + "/run", {"confirm": True, "revision": value["revision"]}, expected=202))
            snapshots = client.call("GET", "/backup/repositories/" + repository["id"] + "/snapshots")
            matching = [item for item in snapshots if "dm-policy:" + policy["id"] in item.get("tags", [])]
            check(matching, "successful backup has no committed Restic snapshot")
            return max(matching, key=lambda value: value["time"]), completed

        policy = create_policy(prefix + "-directory", [{"type": "directory", "path": str(source)}])
        backup_preview = client.call("POST", "/backup/policies/" + policy["id"] + "/preview", {})
        check(fixture_id in backup_preview["stop_containers"], "stop backup omitted the bind-mount writer")
        snapshot, backup_task = backup(policy)
        check(inspect_container(fixture_id)["State"]["Running"], "backup did not restore the original writer's running state")
        restic_env = dict(os.environ, RESTIC_REPOSITORY=str(repository_path), RESTIC_PASSWORD=repository_password)
        command([restic, "check", "--read-data"], env=restic_env, timeout=240)

        def restore(api, repository_id, snapshot_id, target):
            req = {"repository_id": repository_id, "snapshot": snapshot_id, "target": str(target), "overwrite": False, "recreate": False}
            value = api.call("POST", "/backup/restore/preview", req)
            accepted = api.call("POST", "/backup/restore", dict(req, revision=value["revision"], confirm=True), expected=202)
            completed = api.task(accepted)
            return value, completed

        restored_target = root / "restored"
        restore_preview, restore_task = restore(client, repository["id"], snapshot["id"], restored_target)
        restored_source = restored_target / str(source).lstrip(os.sep)
        assert_restored(metadata, restored_source)
        logs = client.call("GET", "/tasks/" + restore_task["id"] + "/logs")
        check(any("File contents verified" in entry["message"] for entry in logs), "restore omitted file verification evidence")
        record("stop-writer directory backup, full Restic data read and content/mode/ownership restore")
        check(os.uname().sysname == "Linux" and pathlib.Path("/proc").is_dir(), "SIGKILL supervision verification requires native Linux /proc")
        interruption_file = source / "interruption-data.bin"
        with open(interruption_file, "wb") as output:
            for _ in range(256):
                output.write(secrets.token_bytes(1024 * 1024))
            output.flush()
            os.fsync(output.fileno())
        interrupted_policy = create_policy(prefix + "-interrupted", [{"type": "directory", "path": str(source)}])
        interruption_preview = client.call("POST", "/backup/policies/" + interrupted_policy["id"] + "/preview", {})
        check(fixture_id in interruption_preview["stop_containers"] and all(owned(cid) for cid in interruption_preview["stop_containers"]), "SIGKILL backup scope is missing own writer or contains unowned writers")
        interrupted = client.call("POST", "/backup/policies/" + interrupted_policy["id"] + "/run",
                                  {"confirm": True, "revision": interruption_preview["revision"]}, expected=202)
        TASK_IDS.add(interrupted["id"])
        observed_child = None
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            observed_child = restic_backup_child(server.process.pid, restic)
            if observed_child:
                break
            time.sleep(0.01)
        check(observed_child is not None, "could not observe actual direct Restic backup child for SIGKILL test")
        check(not inspect_container(fixture_id)["State"]["Running"], "actual backup started before its owned writer stopped")
        server.crash()
        deadline = time.monotonic() + 0.25
        while time.monotonic() < deadline:
            current_child = process_identity(observed_child["pid"])
            if current_child is None or current_child["started"] != observed_child["started"] or current_child["state"] == "Z":
                break
            time.sleep(0.02)
        else:
            raise RuntimeError("actual Restic backup child remained active after its owned panel was SIGKILLed")
        server.start()
        recovered_interruption = client.task(interrupted, expected_status="Failed")
        check(recovered_interruption["error_code"] == "INTERRUPTED", "interrupted backup was not durably marked INTERRUPTED")
        check(inspect_container(fixture_id)["Id"] == fixture_id and inspect_container(fixture_id)["State"]["Running"], "restart did not recover the original stopped writer")
        interruption_file.unlink()
        report["sigkill_recovery"] = {"observed_restic_pid": observed_child["pid"], "restic_inactive_after_panel_kill": True,
                                       "task_id": interrupted["id"], "error_code": recovered_interruption["error_code"], "original_writer_recovered": True}
        record("SIGKILL during real stopped-writer Restic backup terminates child and recovers writer on restart", "256MiB random source; direct child executable+PPID+starttime verified through /proc; task Failed/INTERRUPTED")
        container_policy = create_policy(prefix + "-container", [{"type": "container", "name": fixture_id}])
        container_snapshot, _ = backup(container_policy)
        service_target = root / "restored-service"
        restored_name = prefix + "-restored-service"
        request = {"repository_id": repository["id"], "snapshot": container_snapshot["id"], "target": str(service_target),
                   "overwrite": False, "recreate": True, "container_ids": [fixture_id], "project_ids": [],
                   "container_names": {fixture_id: restored_name}}
        service_preview = client.call("POST", "/backup/restore/preview", request)
        client.task(client.call("POST", "/backup/restore", dict(request, revision=service_preview["revision"], confirm=True), expected=202))
        restored_service = inspect_container(restored_name)
        containers.append(restored_service["Id"])
        check(restored_service["Id"] != fixture_id and restored_service["State"]["Running"], "container recreation did not start a new service ID")
        check(inspect_container(fixture_id)["State"]["Running"], "service recreation changed original container running state")
        expected_bind = service_target / str(source).lstrip(os.sep)
        mounts = [mount for mount in restored_service["Mounts"] if mount["Destination"] == "/owned-data"]
        check(len(mounts) == 1 and mounts[0]["Type"] == "bind" and mounts[0]["Source"] == str(expected_bind), "recreated service mount does not point at verified recovered data")
        assert_restored(metadata, expected_bind)
        check(dc("exec", restored_service["Id"], "cat", "/owned-data/unicode.txt").stdout == (source / "unicode.txt").read_bytes(), "recreated service cannot read restored data")
        record("container snapshot recreates a running service against verified recovered bind data", "original ID remains running; new ID/name/bind path and in-container content independently verified")
        if worker:
            volume_name = prefix + "-volume"
            dc("volume", "create", "--label", LABEL + "=" + token, volume_name)
            volumes.append(volume_name)
            volume_payload = secrets.token_bytes(16384)
            dc("run", "--rm", "--interactive", "--label", LABEL + "=" + token,
               "--mount", "type=volume,src=%s,dst=/data" % volume_name, image_id, "sh", "-c", "cat > /data/payload.bin && chown %d:%d /data /data/payload.bin && chmod 700 /data && chmod 600 /data/payload.bin" % (os.getuid(), os.getgid()), data=volume_payload)
            writer = dc("run", "--detach", "--name", prefix + "-volume-writer", "--label", LABEL + "=" + token,
                        "--mount", "type=volume,src=%s,dst=/volume" % volume_name, image_id, "sleep", "3600").stdout.decode().strip()
            containers.append(writer)
            volume_policy = create_policy(prefix + "-volume", [{"type": "volume", "name": volume_name}])
            volume_snapshot, _ = backup(volume_policy)
            check(inspect_container(writer)["State"]["Running"], "Volume backup did not restore writer state")
            volume_target = root / "restored-volume"
            volume_preview, _ = restore(client, repository["id"], volume_snapshot["id"], volume_target)
            snapshot_source = volume_preview["manifest"]["sources"][0]["snapshot_path"]
            check((volume_target / snapshot_source.lstrip("/") / "payload.bin").read_bytes() == volume_payload,
                  "Volume worker snapshot restored different content")
            record("named Volume worker backup and verified restore", "locally pinned worker image ID")
            foreign_uid = 1001 if os.getuid() != 1001 else 1002
            dc("run", "--rm", "--label", LABEL + "=" + token, "--mount", "type=volume,src=%s,dst=/data" % volume_name,
               image_id, "sh", "-c", "chown %d:%d /data /data/payload.bin && chmod 700 /data && chmod 600 /data/payload.bin" % (foreign_uid, foreign_uid))
            foreign_snapshot, _ = backup(volume_policy)
            dumped = command([restic, "dump", foreign_snapshot["id"], snapshot_source + "/payload.bin"], env=restic_env, timeout=180).stdout
            check(dumped == volume_payload, "worker could not read the foreign UID/mode0600 Volume data")
            check(repository_path.stat().st_uid == os.getuid(), "worker changed repository ownership away from the non-root manager")
            for tree_root, tree_dirs, tree_files in os.walk(repository_path):
                for entry in tree_dirs + tree_files:
                    check((pathlib.Path(tree_root) / entry).lstat().st_uid == os.getuid(), "worker created foreign-owned repository entries")
            record("worker ambient capability reads foreign UID mode0600 and preserves repository ownership", "app UID %d, source UID %d" % (os.getuid(), foreign_uid))
            foreign_restore_target = root / "restored-foreign-owner-volume"
            privileged_cleanup_dirs.append(foreign_restore_target)
            restore(client, repository["id"], foreign_snapshot["id"], foreign_restore_target)
            restored_payload_path = "/restore" + snapshot_source + "/payload.bin"
            foreign_meta = dc("run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_READ_SEARCH", "--label", LABEL + "=" + token,
                              "--mount", "type=bind,src=%s,dst=/restore,readonly" % foreign_restore_target,
                              image_id, "stat", "-c", "%u:%g:%a", restored_payload_path).stdout.decode().strip()
            check(foreign_meta == "%d:%d:600" % (foreign_uid, foreign_uid), "restore worker did not preserve foreign UID/GID and mode0600")
            foreign_contents = dc("run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_READ_SEARCH", "--label", LABEL + "=" + token,
                                  "--mount", "type=bind,src=%s,dst=/restore,readonly" % foreign_restore_target,
                                  image_id, "cat", restored_payload_path).stdout
            check(foreign_contents == volume_payload, "foreign-owner restore produced different data")
            record("non-root manager restores foreign UID/GID mode0600 through scoped worker", "independent read-only Docker stat and content check")
        else:
            RESULTS.append({"name": "named Volume worker backup and verified restore", "status": "skipped", "detail": "DM_TEST_WORKER_IMAGE not supplied"})
            print("SKIP named Volume worker backup: set DM_TEST_WORKER_IMAGE to an installed pinned worker image", flush=True)
        scheduled_instance = None
        if worker:
            cron_policy = client.call("POST", "/backup/policies", {"name": prefix + "-cron", "repository_id": repository["id"],
                "targets": [{"type": "volume", "name": volume_name}], "consistency_mode": "file", "schedule": "* * * * *",
                "timezone": "Etc/UTC", "keep_last": 3, "excludes": [], "retries": 0, "retry_seconds": 0, "enabled": True}, expected=201)
            print("Waiting for real Cron UTC minute trigger (at most 75 seconds)", flush=True)
            deadline = time.monotonic() + 75
            while time.monotonic() < deadline:
                instances = [value for value in client.call("GET", "/backup/schedule-instances") if value["policy_id"] == cron_policy["id"]]
                check(not any(value.get("error") for value in instances), "real scheduled backup reported an instance error")
                ready = [value for value in instances if value.get("task_id")]
                if ready:
                    scheduled_instance = ready[0]
                    break
                time.sleep(0.5)
            check(scheduled_instance is not None, "real Cron did not create an instance within 75 seconds")
            client.task({"id": scheduled_instance["task_id"]})
            scheduled_snapshots = client.call("GET", "/backup/repositories/" + repository["id"] + "/snapshots")
            check(any("dm-policy:" + cron_policy["id"] in value.get("tags", []) for value in scheduled_snapshots), "scheduled task succeeded without a committed snapshot")
            report["scheduled_instance"] = scheduled_instance
        server.stop()
        server.start()
        persisted = client.call("GET", "/auth/status")
        check(persisted["authenticated"] and persisted["user"]["id"] == user_id, "restart lost the administrator/session")
        check(any(value["id"] == policy["id"] for value in client.call("GET", "/backup/policies")), "restart lost backup policies")
        check(client.call("GET", "/tasks/" + stopped_task["id"])["status"] == "Succeeded", "restart lost task history")
        check(client.call("GET", "/tasks/" + imported_task["id"])["status"] == "Succeeded", "restart lost image import history")
        if scheduled_instance:
            instances = [value for value in client.call("GET", "/backup/schedule-instances") if value["policy_id"] == cron_policy["id"]]
            utc_keys = [value["scheduled_at_utc"] for value in instances]
            check(len(utc_keys) == len(set(utc_keys)), "Cron restart duplicated a policy UTC instance")
            original_instances = [value for value in instances if value["scheduled_at_utc"] == scheduled_instance["scheduled_at_utc"]]
            check(len(original_instances) == 1 and original_instances[0]["task_id"] == scheduled_instance["task_id"], "restart requeued the completed Cron UTC instance")
            client.call("DELETE", "/backup/policies/" + cron_policy["id"], expected=204)
            record("real Cron Volume file backup commits snapshot and restart preserves unique UTC task instance", "file consistency only; no database consistency claim; no clock or database manipulation")
        record("restart persists users, sessions, policies and completed tasks")
        server.stop()
        # Remove the original database/key from its configured location and deny traversal.
        # Fresh recovery must work even while that installation is inaccessible to this UID.
        offline_state = root / "original-state-offline"
        server.state.rename(offline_state)
        offline_state.chmod(0)
        source.rename(root / "source-offline")
        fresh = Server(binary, root, "fresh-state", endpoint, docker, restic, worker)
        servers.append(fresh)
        fresh.start()
        fresh_client = Client(fresh.base)
        fresh_status = fresh_client.call("GET", "/auth/status")
        check(not fresh_status["initialized"], "offline recovery accidentally reused original database")
        fresh_password = secrets.token_urlsafe(36)
        SECRETS.append(fresh_password)
        fresh_client.call("POST", "/auth/setup", {"username": prefix + "-recovery", "password": fresh_password})
        check(fresh_client.call("GET", "/backup/repositories") == [], "fresh database already has repository records")
        wrong_password = secrets.token_urlsafe(36)
        SECRETS.append(wrong_password)
        fresh_client.call("POST", "/backup/repositories", {"name": "wrong-key", "location": str(repository_path), "password": wrong_password, "import": True}, expected=400)
        imported_repo = fresh_client.call("POST", "/backup/repositories", {"name": prefix + "-recovery", "location": recovery["location"], "password": recovery["password"], "import": True}, expected=201)
        found_snapshots = fresh_client.call("GET", "/backup/repositories/" + imported_repo["id"] + "/snapshots")
        check(any(value["id"] == snapshot["id"] for value in found_snapshots), "fresh DB could not discover original snapshots")
        disaster_target = root / "disaster-restored"
        restore(fresh_client, imported_repo["id"], snapshot["id"], disaster_target)
        assert_restored(metadata, disaster_target / str(source).lstrip(os.sep))
        record("fresh database imports encrypted repository and restores without original SQLite or source directory")
        fresh.stop()
        # Read only after process shutdown; no SQLite mutation is performed by this test.
        with sqlite3.connect("file:%s?mode=ro" % fresh.state.joinpath("docker-manager.db"), uri=True) as db:
            check(db.execute("SELECT count(*) FROM backup_snapshots").fetchone()[0] == 0, "fresh recovery used a copied local snapshot index")
        check(daemon_file.read_bytes() == daemon_bytes, "integration modified daemon configuration sentinel")
        failed = False
    finally:
        cleanup_errors = []
        if sys.exc_info()[1] is not None:
            report["failure"] = clean_message(sys.exc_info()[1])
        for server in reversed(servers):
            try:
                server.stop()
            except Exception as error:
                cleanup_errors.append(clean_message(error))
        if compose_args:
            try:
                command(compose_args + ["down", "--volumes", "--remove-orphans"], env=dict(docker_env, E2E_VALUE="cleanup"), timeout=120)
            except Exception as error:
                cleanup_errors.append(clean_message(error))
        # Discover remaining unique-labelled fixtures, including a replacement whose response was interrupted.
        discovery = command([docker, "ps", "--all", "--quiet", "--filter", "label=" + LABEL + "=" + token], env=docker_env, allow_failure=True)
        if discovery.returncode:
            cleanup_errors.append("could not enumerate remaining owned container fixtures")
        else:
            containers.extend(discovery.stdout.decode().split())
        # Backup workers carry task ownership instead of the fixture label; only our accepted task IDs qualify.
        workers = command([docker, "ps", "--all", "--quiet", "--filter", "label=docker-manager.worker"], env=docker_env, allow_failure=True)
        if workers.returncode == 0:
            for cid in workers.stdout.decode().split():
                value = command([docker, "inspect", "--type", "container", cid], env=docker_env, allow_failure=True)
                if value.returncode == 0 and json.loads(value.stdout)[0].get("Config", {}).get("Labels", {}).get("docker-manager.task") in TASK_IDS:
                    containers.append(cid)
        for cid in reversed(list(dict.fromkeys(containers))):
            try:
                value = command([docker, "inspect", "--type", "container", cid], env=docker_env, allow_failure=True)
                if value.returncode:
                    continue
                details = json.loads(value.stdout)[0]
                labels = details.get("Config", {}).get("Labels", {})
                check(labels.get(LABEL) == token or (labels.get("docker-manager.worker") in ("backup", "restore") and labels.get("docker-manager.task") in TASK_IDS), "refusing cleanup of a container without this run's ownership label/task")
                if details.get("State", {}).get("Paused"):
                    command([docker, "unpause", cid], env=docker_env)
                command([docker, "rm", "--force", cid], env=docker_env)
            except Exception as error:
                cleanup_errors.append(clean_message(error))
        for name in reversed(volumes):
            try:
                value = command([docker, "volume", "inspect", name], env=docker_env, allow_failure=True)
                if value.returncode:
                    continue
                check(json.loads(value.stdout)[0].get("Labels", {}).get(LABEL) == token, "refusing cleanup of an unowned volume")
                command([docker, "volume", "rm", name], env=docker_env)
            except Exception as error:
                cleanup_errors.append(clean_message(error))
        if tagged_image:
            result = command([docker, "image", "rm", tagged_image], env=docker_env, allow_failure=True)
            if result.returncode:
                cleanup_errors.append("could not remove the unique fixture image tag")
        # Restoring foreign ownership is intentional. Clean only these known fixture subtrees
        # through a labelled helper, because the non-root test UID cannot traverse mode0700 data.
        for target in privileged_cleanup_dirs:
            if target.exists():
                try:
                    check(target.parent == root and not target.is_symlink(), "unsafe privileged fixture cleanup target")
                    command([docker, "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--cap-add", "FOWNER", "--label", LABEL + "=" + token,
                             "--mount", "type=bind,src=%s,dst=/cleanup" % target, image_id,
                             "sh", "-c", "rm -rf /cleanup/* /cleanup/.[!.]* /cleanup/..?*"], env=docker_env)
                except Exception as error:
                    cleanup_errors.append(clean_message(error))
        report["result"] = "failed" if failed or cleanup_errors else "passed"
        report["cleanup_errors"] = cleanup_errors
        report["finished_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        report_path = os.getenv("DM_TEST_REPORT")
        if report_path:
            destination = pathlib.Path(report_path)
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        if failed:
            for server in servers:
                print("SERVER LOG (%s): %s" % (server.state.name, server.logs()), file=sys.stderr)
        keep = os.getenv("DM_TEST_KEEP_ARTIFACTS") == "1"
        if keep:
            print("Private test artifacts retained at " + str(root), file=sys.stderr)
        else:
            if offline_state is not None:
                offline_state.chmod(0o700)
            shutil.rmtree(root)
        if cleanup_errors:
            raise RuntimeError("owned fixture cleanup failed: " + "; ".join(cleanup_errors))
    print("Real integration checks passed; skipped scenarios remain explicitly marked in the report.", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    options = parser.parse_args()
    def terminate(signum, frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, terminate)
    try:
        main(str(pathlib.Path(options.binary).resolve()))
    except KeyboardInterrupt:
        print("ERROR: integration interrupted; owned fixture cleanup attempted", file=sys.stderr)
        sys.exit(130)
    except Exception as error:
        print("ERROR: " + clean_message(error), file=sys.stderr)
        sys.exit(1)
