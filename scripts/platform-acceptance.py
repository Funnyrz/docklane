#!/usr/bin/env python3
"""Isolated Linux release acceptance, run as root INSIDE a disposable guest.

DM_PLATFORM_ISOLATED=1 is mandatory. DM_PLATFORM_ENVIRONMENT records vm,
lxd-system-container, or emulated-vm. prepare leaves an enabled installation;
the operator reboots the actual guest before resume. resume tests upgrade,
failure rollback, and uninstall. docker tests image replacement persistence.
cleanup removes only the recorded installation and labelled test fixtures.
Credentials and cookie jars remain root-only in --state and never in reports.
"""
import argparse
import datetime
import hashlib
import http.cookiejar
import importlib
import json
import os
import pathlib
import pwd
import re
import secrets
import shutil
import socket
import sqlite3
import subprocess
import sys
import time
import urllib.request

LABEL = "io.docker-manager.platform-acceptance"
DATA = pathlib.Path("/var/lib/docklane")
CONFIG = pathlib.Path("/etc/docklane")
INSTALLED = pathlib.Path("/usr/local/bin/docklane")
LEGACY_INSTALLED = pathlib.Path("/usr/local/bin/docker-manager")
UNIT = pathlib.Path("/etc/systemd/system/docklane.service")


def stamp():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def sha(path):
    with open(path, "rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def private_json(path, value):
    temporary = path.with_suffix(path.suffix + ".tmp")
    with open(temporary, "w", opener=lambda name, flags: os.open(name, flags, 0o600)) as stream:
        json.dump(value, stream, ensure_ascii=False, indent=2)
        stream.write("\n")
    temporary.chmod(0o600)
    temporary.replace(path)


class Acceptance:
    def __init__(self, args, helper):
        self.args, self.helper = args, helper
        self.state = args.state
        self.control_path = self.state / "control.json"
        self.secrets_path = self.state / "credentials.json"
        self.control = json.loads(self.control_path.read_text()) if self.control_path.exists() else {}
        self.credentials = json.loads(self.secrets_path.read_text()) if self.secrets_path.exists() else {}
        helper.SECRETS.extend(self.credentials.values())
        self.report = json.loads(args.report.read_text()) if args.report.exists() else {"phases": []}
        self.phase = {"phase": args.phase, "started_at": stamp(), "checks": []}

    def check(self, condition, message):
        self.helper.check(condition, message)

    def command(self, *args, allow_failure=False, **kwargs):
        return self.helper.command(list(args), allow_failure=allow_failure, **kwargs)

    def dc(self, *args, **kwargs):
        return self.command("docker", "--host", "unix:///var/run/docker.sock", *args, **kwargs)

    def inspect(self, cid):
        return json.loads(self.dc("inspect", "--type", "container", cid).stdout)[0]

    def record(self, name, **evidence):
        serialized = json.dumps(evidence)
        self.check(all(secret not in serialized for secret in self.credentials.values()), "report contains a credential")
        self.phase["checks"].append(dict(name=name, status="passed", evidence=evidence))
        print("PASS " + name, flush=True)

    def save(self):
        private_json(self.control_path, self.control)
        if self.credentials:
            private_json(self.secrets_path, self.credentials)

    def secret(self, key):
        if key not in self.credentials:
            self.credentials[key] = secrets.token_urlsafe(36)
            self.helper.SECRETS.append(self.credentials[key])
            self.save()
        return self.credentials[key]

    def cookie_path(self, scope):
        return self.state / (scope + "-cookies.txt")

    def client(self, base, scope, existing=False):
        api = self.helper.Client(base)
        cookies = http.cookiejar.LWPCookieJar(str(self.cookie_path(scope)))
        if existing:
            self.check(self.cookie_path(scope).is_file(), "persistent session cookie file missing")
            cookies.load(ignore_discard=True, ignore_expires=True)
        api.cookies = cookies
        api.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cookies))
        return api

    def save_cookies(self, api):
        api.cookies.save(ignore_discard=True, ignore_expires=True)
        pathlib.Path(api.cookies.filename).chmod(0o600)

    def wait_api(self, base, timeout=120):
        until = time.monotonic() + timeout
        while time.monotonic() < until:
            try:
                with urllib.request.urlopen(base + "/healthz", timeout=2) as response:
                    if response.status == 200:
                        return
            except Exception:
                pass
            time.sleep(0.3)
        raise RuntimeError("service did not become healthy within bounded wait")

    def process_identity(self):
        fields = pathlib.Path("/proc/1/stat").read_text().rsplit(")", 1)[1].split()
        return {"boot_id": pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip(),
                "pid1_start_ticks": fields[19], "pid1_comm": pathlib.Path("/proc/1/comm").read_text().strip(),
                "pid1_command": pathlib.Path("/proc/1/cmdline").read_bytes().replace(b"\0", b" ").decode(errors="replace").strip()}

    def environment(self):
        release = {}
        for line in pathlib.Path("/etc/os-release").read_text().splitlines():
            if "=" in line:
                key, value = line.split("=", 1)
                release[key] = value.strip('"')
        def optional_tool(*args):
            if not shutil.which(args[0]):
                return "unavailable on host (image-bundled tools checked separately)"
            result = self.command(*args, allow_failure=True)
            return result.stdout.decode().strip() if result.returncode == 0 else "unavailable on host"

        until = time.monotonic() + 90
        while self.dc("info", allow_failure=True).returncode:
            self.check(time.monotonic() < until, "guest Engine did not become ready after boot")
            time.sleep(0.5)
        return dict(os=release, machine=os.uname().machine, kernel=os.uname().release,
                    environment=os.getenv("DM_PLATFORM_ENVIRONMENT", "unspecified-isolated-guest"),
                    process=self.process_identity(), engine=json.loads(self.dc("version", "--format", "{{json .Server}}").stdout),
                    docker_cli=self.command("docker", "--version").stdout.decode().strip(),
                    compose=optional_tool("docker", "compose", "version", "--short"),
                    restic=optional_tool("restic", "version"),
                    supplied_binary_sha256=sha(self.args.binary))

    def own_container(self, cid):
        value = self.inspect(cid)
        self.check(value["Config"]["Labels"].get(LABEL) == self.control["token"], "refusing unowned container operation")
        return value

    def image(self, name):
        if self.dc("image", "inspect", name, allow_failure=True).returncode:
            self.dc("pull", name, timeout=600)
        return json.loads(self.dc("image", "inspect", name).stdout)[0]

    def new_control(self):
        if self.control:
            self.check(self.control.get("state") == str(self.state), "state identity mismatch")
            return
        self.check(not any(self.state.iterdir()), "state contains unowned files")
        token = secrets.token_hex(8)
        self.control = dict(state=str(self.state), token=token, namespace="dm-platform-" + token,
                            installation_owned=False, containers=[], volumes=[], backups=[])
        self.save()

    def fixture(self, scope, directory):
        directory.mkdir(parents=True, exist_ok=True)
        source = directory / "source"
        source.mkdir(mode=0o750)
        payload = source / "payload.bin"
        payload.write_bytes(secrets.token_bytes(128 * 1024))
        payload.chmod(0o640)
        image = self.image(self.args.fixture_image)
        cid = self.dc("run", "--detach", "--name", self.control["namespace"] + "-" + scope + "-writer",
                      "--label", LABEL + "=" + self.control["token"], "--restart", "unless-stopped",
                      "--mount", "type=bind,src=" + str(source) + ",dst=/data", "--entrypoint", "/bin/sh",
                      image["Id"], "-c", "trap 'exit 0' TERM INT; while :; do sleep 1; done").stdout.decode().strip()
        self.control["containers"].append(cid)
        fixture = dict(directory=str(directory), source=str(source), writer=cid, payload_sha256=sha(payload), image_id=image["Id"])
        self.control[scope + "_fixture"] = fixture
        self.save()
        return fixture

    def seed_api(self, api, scope, fixture):
        self.check(not api.call("GET", "/auth/status")["initialized"], "test installation already initialized")
        setup = api.call("POST", "/auth/setup", dict(username=self.control["namespace"] + "-" + scope,
                        password=self.secret(scope + "_admin")))
        directory = pathlib.Path(fixture["directory"])
        repo = api.call("POST", "/backup/repositories", dict(name=scope + "-repository", location=str(directory / "repository"),
                        password=self.secret(scope + "_repository")), expected=201)
        policy = api.call("POST", "/backup/policies", dict(name=scope + "-daily", repository_id=repo["id"],
                         targets=[dict(type="directory", path=fixture["source"])], consistency_mode="stop", schedule="0 3 * * *",
                         timezone="Etc/UTC", keep_last=2, excludes=[], retries=0, retry_seconds=0, enabled=True), expected=201)
        pre = api.call("POST", "/backup/policies/" + policy["id"] + "/preview", {})
        self.check(pre["stop_containers"] == [fixture["writer"]], "backup writer scope differs from sole owned fixture")
        task = api.task(api.call("POST", "/backup/policies/" + policy["id"] + "/run", dict(confirm=True, revision=pre["revision"]), expected=202))
        snapshots = api.call("GET", "/backup/repositories/" + repo["id"] + "/snapshots")
        snapshot = next(value for value in snapshots if "dm-policy:" + policy["id"] in value.get("tags", []))
        target = directory / "restored"
        request = dict(repository_id=repo["id"], snapshot=snapshot["id"], target=str(target), overwrite=False, recreate=False)
        preview = api.call("POST", "/backup/restore/preview", request)
        restored = api.task(api.call("POST", "/backup/restore", dict(request, revision=preview["revision"], confirm=True), expected=202))
        source = next(value for value in preview["manifest"]["sources"] if value.get("host_path") == fixture["source"])
        restored_payload = target / source["snapshot_path"].lstrip("/") / "payload.bin"
        self.check(sha(restored_payload) == fixture["payload_sha256"], "actual restored content mismatch")
        self.check(self.own_container(fixture["writer"])["State"]["Running"], "backup did not resume original fixture")
        self.save_cookies(api)
        expected = dict(user_id=setup["user"]["id"], repository_id=repo["id"], policy_id=policy["id"],
                        task_ids=[task["id"], restored["id"]], snapshot_id=snapshot["id"], restored_payload=str(restored_payload))
        self.control[scope + "_expected"] = expected
        self.save()
        return expected

    def verify_api(self, api, scope):
        expected, fixture = self.control[scope + "_expected"], self.control[scope + "_fixture"]
        status = api.call("GET", "/auth/status")
        self.check(status["initialized"] and status["authenticated"] and status["user"]["id"] == expected["user_id"], "persistent user/session changed")
        self.check(any(row["id"] == expected["repository_id"] for row in api.call("GET", "/backup/repositories")), "repository lost")
        policy = next(row for row in api.call("GET", "/backup/policies") if row["id"] == expected["policy_id"])
        self.check(policy["schedule"] == "0 3 * * *" and policy["enabled"], "scheduled policy lost")
        for task_id in expected["task_ids"]:
            self.check(api.call("GET", "/tasks/" + task_id)["status"] == "Succeeded", "task history changed")
        snapshots = api.call("GET", "/backup/repositories/" + expected["repository_id"] + "/snapshots")
        self.check(any(row["id"] == expected["snapshot_id"] for row in snapshots), "encrypted snapshot lost")
        self.check(sha(expected["restored_payload"]) == fixture["payload_sha256"], "restored file persistence differs")
        original = self.own_container(fixture["writer"])
        self.check(original["State"]["Running"] and original["HostConfig"]["RestartPolicy"]["Name"] == "unless-stopped", "original restart-policy container changed")
        return dict(expected, original_container_id=fixture["writer"], restored_sha256=fixture["payload_sha256"])

    def unit_state(self):
        output = self.command("systemctl", "show", "docklane", "--property=ActiveState,SubState,UnitFileState,MainPID,User,Group,FragmentPath").stdout.decode()
        return dict(line.split("=", 1) for line in output.splitlines() if "=" in line)

    def prepare(self):
        self.check(not self.control, "prepare already has state; use resume or cleanup")
        for path in (DATA, CONFIG, INSTALLED, LEGACY_INSTALLED, UNIT):
            self.check(not path.exists(), "refusing existing installation path: " + str(path))
        self.check(self.command("systemctl", "cat", "docklane", allow_failure=True).returncode != 0, "refusing an existing service")
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 8080))
        self.new_control()
        self.control["account_created"] = self.command("getent", "passwd", "docklane", allow_failure=True).returncode != 0
        fixture = self.fixture("binary", self.state / "fixture")
        self.control["installation_owned"] = True
        self.control["prepare_process"] = self.process_identity()
        self.save()
        self.command("sh", str(self.args.source / "scripts/install.sh"), str(self.args.binary), timeout=180)
        self.command("systemctl", "stop", "docklane", timeout=200)
        account = pwd.getpwnam("docklane")
        os.chown(self.state, 0, account.pw_gid)
        self.state.chmod(0o750)
        for path in [pathlib.Path(fixture["directory"]), *pathlib.Path(fixture["directory"]).rglob("*")]:
            os.chown(path, account.pw_uid, account.pw_gid)
        config = json.loads((CONFIG / "config.json").read_text())
        config.update(listen="127.0.0.1:8080", allowed_paths=[fixture["directory"]], backup_worker_image=self.args.image,
                      compose_path=shutil.which("docker"), restic_path=shutil.which("restic"),
                      enable_daemon_write=False, session_secure=False)
        (CONFIG / "config.json").write_text(json.dumps(config, indent=2) + "\n")
        self.command("systemctl", "start", "docklane", timeout=60)
        base = "http://127.0.0.1:8080"
        self.wait_api(base)
        api = self.client(base, "binary")
        expected = self.seed_api(api, "binary", fixture)
        self.control.update(binary_base=base, master_key_sha256=sha(DATA / "master.key"),
                            installed_binary_sha256=sha(INSTALLED), prepared=True)
        self.save()
        unit = self.unit_state()
        self.check(unit["ActiveState"] == "active" and unit["UnitFileState"] == "enabled", "actual installer did not enable and start service")
        self.record("actual install preserves pre-existing fixture and seeds encrypted backup/restore", expected=expected,
                    original_container_id=fixture["writer"], unit=unit, master_key_sha256=self.control["master_key_sha256"])

    def fingerprints(self):
        return {"database": sha(DATA / "docker-manager.db"), "master_key": sha(DATA / "master.key"),
                "config": sha(CONFIG / "config.json"), "binary": sha(INSTALLED), "unit": sha(UNIT)}

    def run_upgrade(self, binary, expect_failure=False):
        self.command("systemctl", "stop", "docklane", timeout=200)
        fingerprints = self.fingerprints()
        backup_root = pathlib.Path("/var/backups/docklane")
        before = set(backup_root.iterdir()) if backup_root.exists() else set()
        result = self.command("sh", str(self.args.source / "scripts/upgrade.sh"), str(binary), timeout=200, allow_failure=expect_failure)
        self.check((result.returncode != 0) == expect_failure, "upgrade exit result differs from expected real outcome")
        after = set(backup_root.iterdir())
        new = after - before
        self.check(len(new) == 1, "upgrade did not create exactly one new consistent backup")
        backup = next(iter(new))
        self.control["backups"].append(str(backup))
        self.save()
        matching = {"database": sha(backup / "data/docker-manager.db"), "master_key": sha(backup / "data/master.key"),
                    "config": sha(backup / "config/config.json"), "binary": sha(backup / "docklane"),
                    "unit": sha(backup / "docklane.service")}
        self.check(matching == fingerprints, "upgrade backup is not the matching stopped database/key/config/binary group")
        self.check(backup.stat().st_mode & 0o077 == 0, "upgrade backup exposes credentials")
        if expect_failure:
            self.check(self.command("systemctl", "is-active", "--quiet", "docklane", allow_failure=True).returncode != 0,
                       "failed upgrade left service active")
            self.check(self.unit_state()["MainPID"] == "0", "failed upgrade did not stop service")
        else:
            self.wait_api(self.control["binary_base"])
        return dict(path=str(backup), fingerprints=matching, exit_code=result.returncode)

    def resume(self):
        self.check(self.control.get("prepared"), "prepare must complete before resume")
        self.check(not self.control.get("resumed_and_uninstalled"), "resume already completed; use cleanup")
        current = self.process_identity()
        original = self.control["prepare_process"]
        self.check(current["boot_id"] != original["boot_id"] or current["pid1_start_ticks"] != original["pid1_start_ticks"],
                   "resume requires an actual guest/PID1 restart; service-only restart is insufficient")
        self.check(self.command("systemctl", "is-enabled", "--quiet", "docklane", allow_failure=True).returncode == 0,
                   "service is not enabled after guest restart")
        self.wait_api(self.control["binary_base"])
        self.check(self.command("systemctl", "is-active", "--quiet", "docklane", allow_failure=True).returncode == 0,
                   "systemd failed to start service automatically")
        api = self.client(self.control["binary_base"], "binary", True)
        retained = self.verify_api(api, "binary")
        self.check(sha(DATA / "master.key") == self.control["master_key_sha256"], "master key changed across actual guest restart")
        self.record("actual guest restart preserves enabled service/container identity and all management data", before=original,
                    after=current, retained=retained, unit=self.unit_state())
        upgraded = self.run_upgrade(self.args.binary)
        retained = self.verify_api(api, "binary")
        self.check(sha(DATA / "master.key") == self.control["master_key_sha256"], "same-version upgrade changed key")
        self.record("actual same-version upgrade creates matching backup and preserves management data", backup=upgraded, retained=retained,
                    limitation="Same-version lifecycle check; no historical business release or migration result is fabricated.")
        invalid = self.state / "invalid-new-binary"
        invalid.write_bytes(b"Docklane acceptance: deliberately invalid executable\n")
        invalid.chmod(0o700)
        failed = self.run_upgrade(invalid, True)
        self.record("actual invalid-binary upgrade fails and leaves service stopped", backup=failed, unit=self.unit_state())
        backup = pathlib.Path(failed["path"])
        for path, saved in ((DATA, "failed-upgrade-data"), (CONFIG, "failed-upgrade-config")):
            shutil.move(str(path), str(self.state / saved))
        self.command("cp", "-a", str(backup / "data"), str(DATA))
        self.command("cp", "-a", str(backup / "config"), str(CONFIG))
        self.command("cp", "-a", str(backup / "docklane"), str(INSTALLED))
        self.command("cp", "-a", str(backup / "docklane.service"), str(UNIT))
        self.command("systemctl", "daemon-reload")
        self.command("systemctl", "reset-failed", "docklane")
        self.command("systemctl", "start", "docklane", timeout=60)
        self.wait_api(self.control["binary_base"])
        retained = self.verify_api(api, "binary")
        self.check(sha(DATA / "master.key") == self.control["master_key_sha256"], "matching-group rollback changed key")
        self.record("documented matching binary/config/database/key rollback recovers service and data", retained=retained)
        self.command("sh", str(self.args.source / "scripts/uninstall.sh"), timeout=200)
        self.check(not INSTALLED.exists() and not LEGACY_INSTALLED.exists() and not UNIT.exists(), "uninstall retained executable or unit")
        self.check((DATA / "docker-manager.db").is_file() and (CONFIG / "config.json").is_file(), "uninstall deleted persistent data/config")
        self.check(sha(DATA / "master.key") == self.control["master_key_sha256"], "uninstall deleted or changed key")
        self.check(pathlib.Path(self.control["binary_fixture"]["directory"], "repository/config").is_file(), "uninstall deleted encrypted repository")
        self.check(self.own_container(self.control["binary_fixture"]["writer"])["State"]["Running"], "uninstall changed original fixture")
        self.control["resumed_and_uninstalled"] = True
        self.save()
        self.record("actual uninstall preserves database/key/config/account/repository and external container", master_key_sha256=self.control["master_key_sha256"])

    def launch_docker(self, image_id, volume, directory):
        mappings = [dict(host=str(directory / name), local=local) for name, local in
                    (("source", "/source"), ("repository", "/repository"), ("restored", "/restored"))]
        for name in ("repository", "restored"):
            (directory / name).mkdir(exist_ok=True)
        host_network = os.getenv("DM_PLATFORM_DOCKER_NETWORK") == "host"
        network_args = ["--network", "host"] if host_network else ["--publish", "127.0.0.1::8080"]
        listen = "127.0.0.1:18085" if host_network else ":8080"
        cid = self.dc("run", "--detach", "--name", self.control["namespace"] + "-manager",
                      "--label", LABEL + "=" + self.control["token"], "--restart", "unless-stopped",
                      *network_args, "--security-opt", "no-new-privileges:true",
                      "--mount", "type=volume,src=" + volume + ",dst=/data",
                      "--mount", "type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock",
                      "--mount", "type=bind,src=" + str(directory / "source") + ",dst=/source,readonly",
                      "--mount", "type=bind,src=" + str(directory / "repository") + ",dst=/repository",
                      "--mount", "type=bind,src=" + str(directory / "restored") + ",dst=/restored",
                      "--env", "DM_MODE=docker", "--env", "DM_DATA_DIR=/data", "--env", "DM_LISTEN=" + listen,
                      "--env", "DM_ALLOWED_PATHS=/source,/repository,/restored", "--env", "DM_PATH_MAPPINGS=" + json.dumps(mappings),
                      "--env", "DM_BACKUP_WORKER_IMAGE=" + image_id, image_id).stdout.decode().strip()
        self.control["containers"].append(cid)
        self.save()
        inspected = self.inspect(cid)
        if host_network:
            base = "http://127.0.0.1:18085"
        else:
            ports = inspected["NetworkSettings"]["Ports"]["8080/tcp"]
            base = "http://127.0.0.1:" + ports[0]["HostPort"]
        self.wait_api(base)
        self.check(not any(m["Type"] == "bind" and m["Source"] == "/" for m in inspected["Mounts"]), "container unexpectedly mounted host root")
        return cid, base

    def migration(self):
        self.check(self.control.get("resumed_and_uninstalled"), "migration injection follows completed boot/upgrade acceptance")
        self.check(not self.control.get("migration_verified"), "migration injection already completed")
        self.command("sh", str(self.args.source / "scripts/install.sh"), str(self.args.binary), timeout=180)
        self.wait_api(self.control["binary_base"])
        api = self.client(self.control["binary_base"], "binary", True)
        self.verify_api(api, "binary")
        self.command("systemctl", "stop", "docklane", timeout=200)
        # An actual SQLite BEFORE INSERT abort hits Open's migration transaction,
        # even for INSERT OR IGNORE of the already applied version. This tests a
        # real SQL failure without inventing a historical business release.
        injection = "DM_ACCEPTANCE_MIGRATION_ABORT"
        with sqlite3.connect(DATA / "docker-manager.db") as database:
            database.execute("CREATE TRIGGER dm_acceptance_abort BEFORE INSERT ON schema_migrations "
                             "BEGIN SELECT RAISE(ABORT,'" + injection + "'); END")
        database.close()
        failed = self.run_upgrade(self.args.binary, True)
        journal = self.command("journalctl", "-u", "docklane", "--no-pager", "-n", "50").stdout.decode(errors="replace")
        self.check(injection in journal, "new executable did not reach the injected migration SQL failure")
        with sqlite3.connect(DATA / "docker-manager.db") as database:
            versions = database.execute("SELECT version FROM schema_migrations ORDER BY version").fetchall()
        database.close()
        self.check(versions == [(1,)], "failed migration changed applied schema versions")
        self.record("actual migration SQL abort stops upgrade and preserves matching recovery material", backup=failed,
                    injection=injection, applied_versions=[row[0] for row in versions], unit=self.unit_state(),
                    limitation="Explicit SQL fault injection on schema v1; not a cross-business-version migration claim.")
        backup = pathlib.Path(failed["path"])
        for path, saved in ((DATA, "migration-failed-data"), (CONFIG, "migration-failed-config")):
            shutil.move(str(path), str(self.state / saved))
        self.command("cp", "-a", str(backup / "data"), str(DATA))
        self.command("cp", "-a", str(backup / "config"), str(CONFIG))
        self.command("cp", "-a", str(backup / "docklane"), str(INSTALLED))
        with sqlite3.connect(DATA / "docker-manager.db") as database:
            database.execute("DROP TRIGGER dm_acceptance_abort")
        database.close()
        self.command("cp", "-a", str(backup / "docklane.service"), str(UNIT))
        self.command("systemctl", "daemon-reload")
        self.command("systemctl", "reset-failed", "docklane")
        self.command("systemctl", "start", "docklane", timeout=60)
        self.wait_api(self.control["binary_base"])
        retained = self.verify_api(api, "binary")
        self.check(sha(DATA / "master.key") == self.control["master_key_sha256"], "migration recovery changed encryption key")
        self.command("sh", str(self.args.source / "scripts/uninstall.sh"), timeout=200)
        self.control["migration_verified"] = True
        self.save()
        self.record("matching-group recovery after removing injected SQL fault preserves all management data", retained=retained)

    def docker(self):
        self.new_control()
        self.check("docker_expected" not in self.control, "Docker phase already completed; cleanup before rerun")
        image = self.image(self.args.image)
        fixture = self.fixture("docker", self.state / "docker-fixture")
        volume = self.control["namespace"] + "-manager-data"
        self.dc("volume", "create", "--label", LABEL + "=" + self.control["token"], volume)
        self.control["volumes"].append(volume)
        self.save()
        first, base = self.launch_docker(image["Id"], volume, pathlib.Path(fixture["directory"]))
        api = self.client(base, "docker")
        expected = self.seed_api(api, "docker", fixture)
        volume_path = pathlib.Path(json.loads(self.dc("volume", "inspect", volume).stdout)[0]["Mountpoint"])
        key_hash = sha(volume_path / "master.key")
        tools = {"compose": self.dc("exec", first, "docker", "compose", "version", "--short").stdout.decode().strip(),
                 "restic": self.dc("exec", first, "restic", "version").stdout.decode().strip()}
        self.dc("stop", "--time", "180", first, timeout=200)
        self.dc("rm", first)
        second, second_base = self.launch_docker(image["Id"], volume, pathlib.Path(fixture["directory"]))
        self.check(first != second, "Docker replacement reused the original container")
        api = self.client(second_base, "docker", True)
        retained = self.verify_api(api, "docker")
        self.check(sha(volume_path / "master.key") == key_hash, "Docker recreation changed master key")
        self.check(not api.call("GET", "/host/daemon")["available"], "Docker mode unexpectedly enabled host mutation")
        self.record("real image remove/recreate preserves named-volume users/session/policy/repository/tasks/key", first_container_id=first,
                    replacement_container_id=second, volume=volume, image_id=image["Id"], image_architecture=image["Architecture"],
                    explicit_mappings=[dict(host=str(pathlib.Path(fixture["directory"]) / name), local=local,
                                           read_only=name == "source") for name, local in
                                       (("source", "/source"), ("repository", "/repository"), ("restored", "/restored"))], master_key_sha256=key_hash,
                    tools=tools, retained=retained, host_root_mounted=False)

    def cleanup(self):
        self.check(self.control and self.control.get("state") == str(self.state), "cleanup requires recorded own state")
        if self.control.get("installation_owned"):
            if UNIT.exists() or INSTALLED.exists():
                self.command("sh", str(self.args.source / "scripts/uninstall.sh"), timeout=200)
            for path in (DATA, CONFIG):
                if path.exists():
                    shutil.rmtree(path)
            for saved in self.control.get("backups", []):
                path = pathlib.Path(saved)
                self.check(path.parent == pathlib.Path("/var/backups/docklane") and re.fullmatch(r"\d{8}T\d{6}Z", path.name), "unowned backup cleanup path")
                if path.exists():
                    shutil.rmtree(path)
        ids = self.dc("ps", "--all", "--quiet", "--no-trunc", "--filter", "label=" + LABEL + "=" + self.control["token"]).stdout.decode().split()
        for cid in ids:
            self.own_container(cid)
            self.dc("stop", "--time", "180", cid, timeout=200, allow_failure=True)
            self.dc("rm", "--force", cid)
        for volume in self.control.get("volumes", []):
            result = self.dc("volume", "inspect", volume, allow_failure=True)
            if result.returncode == 0:
                self.check(json.loads(result.stdout)[0]["Labels"].get(LABEL) == self.control["token"], "unowned volume cleanup")
                self.dc("volume", "rm", volume)
        for name in ("fixture", "docker-fixture", "failed-upgrade-data", "failed-upgrade-config", "migration-failed-data", "migration-failed-config"):
            path = self.state / name
            if path.exists():
                shutil.rmtree(path)
        if self.control.get("account_created"):
            self.command("userdel", "docklane", allow_failure=True)
            self.command("groupdel", "docklane", allow_failure=True)
        for path in [self.secrets_path, *self.state.glob("*-cookies.txt"), self.state / "invalid-new-binary"]:
            path.unlink(missing_ok=True)
        self.control["cleaned"] = True
        private_json(self.control_path, self.control)
        self.record("owned installation/resources/secret fixtures cleaned; report retained", remaining_owned_containers=self.dc("ps", "--all", "--quiet", "--filter", "label=" + LABEL + "=" + self.control["token"]).stdout.decode().split())

    def execute(self):
        try:
            self.phase["environment"] = self.environment()
            getattr(self, self.args.phase)()
            self.phase["status"] = "passed"
        except BaseException as error:
            self.phase["status"] = "failed"
            self.phase["error"] = self.helper.clean_message(error)
            if self.control.get("installation_owned"):
                self.phase["journal"] = self.helper.clean_message(self.command("journalctl", "-u", "docklane", "--no-pager", "-n", "30", allow_failure=True).stdout.decode(errors="replace"))
            raise
        finally:
            self.phase["finished_at"] = stamp()
            self.report["phases"].append(self.phase)
            self.report["state"] = str(self.state)
            self.report["namespace"] = self.control.get("namespace")
            self.args.report.parent.mkdir(parents=True, exist_ok=True)
            serialized = self.helper.clean_message(json.dumps(self.report, ensure_ascii=False, indent=2))
            self.args.report.write_text(serialized + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--phase", choices=("prepare", "resume", "migration", "docker", "cleanup"), required=True)
    parser.add_argument("--state", type=pathlib.Path, required=True)
    parser.add_argument("--source", type=pathlib.Path, required=True)
    parser.add_argument("--binary", type=pathlib.Path, required=True)
    parser.add_argument("--image", default="docklane:v0.3.2-beta.3")
    parser.add_argument("--fixture-image", default="busybox:1.37.0")
    parser.add_argument("--report", type=pathlib.Path, required=True)
    args = parser.parse_args()
    if os.uname().sysname != "Linux" or os.geteuid() != 0 or os.getenv("DM_PLATFORM_ISOLATED") != "1":
        parser.error("requires root INSIDE explicitly isolated Linux guest: DM_PLATFORM_ISOLATED=1")
    if args.phase in ("prepare", "resume", "migration") and pathlib.Path("/proc/1/comm").read_text().strip() != "systemd":
        parser.error("prepare/resume require real systemd as PID1")
    if not re.fullmatch(r"/var/lib/dm-platform-[a-zA-Z0-9-]+", str(args.state)) or args.state.is_symlink():
        parser.error("--state must be a dedicated nonsymlink /var/lib/dm-platform-<id> directory")
    args.source = args.source.resolve(strict=True)
    args.binary = args.binary.resolve(strict=True)
    args.report = args.report.resolve()
    if not (args.source / "scripts/integration.py").is_file() or not args.binary.is_file():
        parser.error("trusted source copy and compiled native binary are required")
    args.state.mkdir(mode=0o700, exist_ok=True)
    os.umask(0o077)
    sys.path.insert(0, str(args.source / "scripts"))
    helper = importlib.import_module("integration")
    Acceptance(args, helper).execute()


if __name__ == "__main__":
    main()
