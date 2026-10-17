#!/usr/bin/env python3
"""Additional real-Linux workflow release checks; no daemon/global mutations.

Run as the unprivileged application account, with passwordless sudo available
only for the owned quota fixture. Uses the adjacent integration.py helpers.
DM_TEST_REPORT chooses the evidence file; all operational fixtures use /tmp.
"""
import datetime
import json
import os
import pathlib
import secrets
import shutil
import sqlite3
import sys
import tempfile
import time

import integration as support


def main(binary):
    check, command = support.check, support.command
    check(os.uname().sysname == "Linux" and os.getuid() != 0, "requires unprivileged native Linux")
    token = secrets.token_hex(7)
    prefix = "dm-wf-" + token
    root = pathlib.Path(tempfile.mkdtemp(prefix=prefix + "-", dir="/tmp")).resolve()
    docker = os.environ["DM_TEST_DOCKER"]
    restic = os.environ["DM_TEST_RESTIC"]
    env = dict(os.environ, DOCKER_HOST="unix:///var/run/docker.sock")
    label = "io.docker-manager.workflows-acceptance"
    containers, mounts, servers = [], [], []
    report = {"namespace": prefix, "started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "binary_sha256": support.digest(binary), "platform": dict(system=os.uname().sysname,
              architecture=os.uname().machine, uid=os.getuid()), "checks": [], "cleanup": {}}
    failed = True

    def dc(*args, **kw):
        return command([docker, *args], env=env, **kw)

    def inspect(cid):
        return json.loads(dc("inspect", cid).stdout)[0]

    def record(name, ac, **evidence):
        serialized = json.dumps(evidence)
        check(all(secret not in serialized for secret in support.SECRETS), "task evidence exposes a credential")
        report["checks"].append(dict(name=name, status="passed", acceptance=ac, evidence=evidence))
        print("PASS " + name, flush=True)

    def task_failed(api, accepted):
        task = api.task(accepted, expected_status="Failed")
        check(task.get("error_code"), "failure has no actionable code")
        result = {key: task.get(key) for key in ("id", "status", "stage", "error_code", "error_message")}
        result["logs"] = api.call("GET", "/tasks/" + task["id"] + "/logs")[-6:]
        return result

    try:
        image = json.loads(dc("image", "inspect", os.getenv("DM_TEST_IMAGE", "busybox:1.37.0")).stdout)[0]["Id"]
        worker = json.loads(dc("image", "inspect", os.environ["DM_TEST_WORKER_IMAGE"]).stdout)[0]["Id"]
        report.update(engine=json.loads(dc("version", "--format", "{{json .Server}}").stdout),
                      compose=dc("compose", "version", "--short").stdout.decode().strip(),
                      restic=command([restic, "version"]).stdout.decode().strip(), worker_image_id=worker)
        fault_file = root / "fault.json"
        fault_file.write_text("{}")
        wrapper = root / "restic-fault-boundary"
        # Real kernel/FS failures, injected only after application preflight and
        # writer stop. Never fabricate a Restic result or a task status.
        wrapper.write_text("#!/usr/bin/python3\nimport json,os,resource,sys\n"
                           + "fault=json.load(open(" + repr(str(fault_file)) + "))\n"
                           + "if sys.argv[1] == fault.get('operation'):\n"
                           + " if fault.get('deny'): os.chmod(fault['deny'],0)\n"
                           + " if fault.get('file_limit'): resource.setrlimit(resource.RLIMIT_FSIZE,(1024,1024))\n"
                           + " if fault.get('fill'):\n"
                           + "  with open(fault['fill'],'wb') as f:\n"
                           + "   try:\n"
                           + "    while True: f.write(bytes(1024*1024)); f.flush()\n"
                           + "   except OSError: pass\n"
                           + "os.execv(" + repr(restic) + ", [" + repr(restic) + "]+sys.argv[1:])\n")
        wrapper.chmod(0o700)
        server = support.Server(binary, root, "state", env["DOCKER_HOST"], docker, str(wrapper), worker)
        servers.append(server)
        server.start()
        api = support.Client(server.base)
        api.call("GET", "/auth/status")
        password = secrets.token_urlsafe(32)
        repo_password = secrets.token_urlsafe(32)
        support.SECRETS.extend([password, repo_password])
        api.call("POST", "/auth/setup", {"username": prefix, "password": password})

        def repository(path, name, imported=False):
            return api.call("POST", "/backup/repositories", dict(name=name, location=str(path), password=repo_password, **{"import": imported}), expected=201)

        def policy(repo, targets, name, **extra):
            return api.call("POST", "/backup/policies", dict(name=name, repository_id=repo["id"], targets=targets,
                    consistency_mode="stop", schedule="", timezone="Etc/UTC", keep_last=1, excludes=[], retries=0,
                    retry_seconds=0, enabled=True, **extra), expected=201)

        def preview(p):
            result = api.call("POST", "/backup/policies/" + p["id"] + "/preview", {})
            for cid in result["stop_containers"]:
                check(inspect(cid)["Config"]["Labels"].get(label) == token, "unowned writer in scope")
            return result

        def run(p, expected="Succeeded"):
            pre = preview(p)
            accepted = api.call("POST", "/backup/policies/" + p["id"] + "/run", dict(confirm=True, revision=pre["revision"]), expected=202)
            return api.task(accepted) if expected == "Succeeded" else task_failed(api, accepted)

        def snapshots(repo):
            return api.call("GET", "/backup/repositories/" + repo["id"] + "/snapshots")

        def refs(repo):
            return sorted(item["id"] for item in snapshots(repo))

        def verified(repo):
            with sqlite3.connect(server.state / "docker-manager.db") as database:
                return database.execute("SELECT count(*) FROM backup_snapshots WHERE repository_id=? AND status='verified'", (repo["id"],)).fetchone()[0]

        compose = root / "compose"
        external = root / "external"
        compose.mkdir()
        external.mkdir()
        (compose / "data").mkdir()
        payload = secrets.token_bytes(128 * 1024)
        (compose / "data" / "payload").write_bytes(payload)
        base, override = compose / "compose.json", external / "override.json"
        context, service_env = external / "context.env", external / "service.env"
        context.write_text("CONTEXT=preserved-context\n")
        service_env.write_text("SERVICE_VALUE=preserved-service-env\n")
        labels = {label: token}
        base.write_text(json.dumps({"services": {
            "web": {"image": image, "command": ["sleep", "3600"], "labels": labels,
                    "healthcheck": {"test": ["CMD", "test", "-s", "/data/payload"], "interval": "1s", "timeout": "1s", "retries": 3},
                    "environment": {"CONTEXT": "${CONTEXT:?}"}, "env_file": [str(service_env)],
                    "volumes": ["./data:/data"]},
            "never_started": {"image": image, "command": ["sleep", "3600"], "labels": labels,
                              "volumes": [str(compose / "data") + ":/original-live-data"]}}}))
        override.write_text(json.dumps({"services": {"web": {"environment": {"OVERRIDE": "ordered-last"}}}}))
        dc("compose", "--project-name", prefix, "--file", str(base), "--file", str(override),
           "--env-file", str(context), "up", "--detach", "--wait", "--pull", "never", "web", timeout=180)
        original = dc("ps", "--quiet", "--filter", "label=com.docker.compose.project=" + prefix).stdout.decode().strip()
        containers.append(original)
        project = api.call("POST", "/compose/import", dict(name=prefix, workdir=str(compose),
                    config_paths=[str(base), str(override)], env_files=[str(context)], profiles=[], environment={}, confirm=True), expected=201)
        repo = repository(root / "repository", prefix)
        nested_source = root / "repository" / "forbidden-source"
        nested_source.mkdir()
        for forbidden in (root, nested_source):
            api.call("POST", "/backup/policies", dict(name="recursive", repository_id=repo["id"],
                     targets=[dict(type="directory", path=str(forbidden))], consistency_mode="file", schedule="",
                     timezone="Etc/UTC", keep_last=1, excludes=[], retries=0, retry_seconds=0, enabled=True), expected=400)
        nested_source.rmdir()
        record("API rejects both source-containing-repository and repository-containing-source", ["AC-BKP-05"], rejected_directions=2)
        p = policy(repo, [{"type": "compose", "name": project["id"]}], "compose")
        completed = run(p)
        snap = snapshots(repo)[0]
        meta = api.call("GET", "/backup/repositories/" + repo["id"] + "/snapshots/" + snap["id"] + "/metadata")
        check(any(value.get("host_path") == str(override) for value in meta["sources"]), "external Compose file absent from manifest")
        target, new_project = root / "recovered-compose", prefix + "-restored"
        request = dict(repository_id=repo["id"], snapshot=snap["id"], target=str(target), overwrite=False,
                       recreate=True, container_ids=[], project_ids=[project["id"]], project_names={project["id"]: new_project})
        pre = api.call("POST", "/backup/restore/preview", request)
        restored_task = api.task(api.call("POST", "/backup/restore", dict(request, revision=pre["revision"], confirm=True), expected=202))
        restored_ids = dc("ps", "--all", "--quiet", "--filter", "label=com.docker.compose.project=" + new_project).stdout.decode().split()
        containers.extend(restored_ids)
        check(len(restored_ids) == 1 and restored_ids[0] != original, "selective Compose recreation launched uncaptured service or reused original")
        restored = inspect(restored_ids[0])
        check(restored["State"].get("Health", {}).get("Status") == "healthy", "recreated service not healthy")
        check(dc("exec", restored_ids[0], "cat", "/data/payload").stdout == payload, "recreated Compose content differs")
        check(all(m["Source"].startswith(str(target) + "/") for m in restored["Mounts"]), "recreated service mounts original data")
        check("SERVICE_VALUE=preserved-service-env" in restored["Config"]["Env"], "external env file lost")
        check(inspect(original)["State"]["Running"] and (compose / "data" / "payload").read_bytes() == payload, "Compose recovery changed original")
        record("compose backup to selective new-project healthy recreation", ["AC-CMP-02", "AC-BKP-06", "AC-TASK-01"],
               backup_task=completed["id"], restore_task=restored_task["id"], original_id=original,
               restored_ids=restored_ids, external_config_and_env_preserved=True, uncaptured_service_absent=True)

        source = root / "shared-data"
        source.mkdir()
        (source / "payload").write_bytes(secrets.token_bytes(128 * 1024))
        writers = []
        for suffix in ("writer-a", "writer-b", "already-stopped"):
            cid = dc("create", "--name", prefix + "-" + suffix, "--label", label + "=" + token,
                     "--mount", "type=bind,src=" + str(source) + ",dst=/data", image, "sleep", "3600").stdout.decode().strip()
            containers.append(cid)
            if suffix != "already-stopped":
                dc("start", cid)
                writers.append(cid)
            else:
                stopped = cid
        shared = policy(repo, [{"type": "directory", "path": str(source)}], "shared")
        run(shared)
        valid_shared_snapshot = next(value for value in snapshots(repo) if "dm-policy:" + shared["id"] in value.get("tags", []))
        before = refs(repo)
        verified_before = verified(repo)
        fault_file.write_text(json.dumps(dict(operation="backup", deny=str(source))))
        failure = run(shared, "Failed")
        source.chmod(0o755)
        fault_file.write_text("{}")
        check(all(inspect(cid)["State"]["Running"] for cid in writers), "shared writers not compensated after permission failure")
        check(not inspect(stopped)["State"]["Running"], "originally stopped writer started")
        check(set(before).issubset(refs(repo)), "failure pruned baseline snapshots")
        check(verified(repo) == verified_before, "failed backup added a verified snapshot")
        record("real source EACCES compensates every originally running shared writer", ["AC-BKP-03", "AC-TASK-03"],
               task=failure, running_writers=writers, stopped_writer=stopped, baseline_snapshots_retained=True)

        # Use an actual encrypted repository, not a missing-path validation failure.
        wrong_path = root / "wrong-password-real"
        shutil.copytree(root / "repository", wrong_path)
        wrong = api.call("POST", "/backup/repositories", dict(name="bad-password-real", location=str(wrong_path), password="incorrect-passphrase", **{"import": True}), expected=400)
        check(not any(value["location"] == str(wrong_path) for value in api.call("GET", "/backup/repositories")), "wrong password persisted a usable repository")
        record("wrong encrypted-repository password rejected", ["AC-BKP-03"], error=wrong["error"]["code"], repository_not_imported=True)

        damaged_path = root / "damaged-repository"
        shutil.copytree(root / "repository", damaged_path)
        damaged = repository(damaged_path, "damaged", True)
        damaged_policy = policy(damaged, [{"type": "directory", "path": str(source)}], "damaged")
        packs = sorted((damaged_path / "data").glob("*/*"))
        check(packs, "encrypted repository contains no packs")
        packs[0].chmod(0o600)  # Restic packs are intentionally read-only.
        with open(packs[0], "r+b") as pack:
            byte = pack.read(1)
            pack.seek(0)
            pack.write(bytes([byte[0] ^ 0x80]))
        damaged_before = sorted(p.name for p in (damaged_path / "snapshots").iterdir())
        failed_corruption = run(damaged_policy, "Failed")
        check(set(damaged_before).issubset(p.name for p in (damaged_path / "snapshots").iterdir()), "corruption failure pruned baseline snapshots")
        check(verified(damaged) == 0, "corrupted backup added a verified snapshot")
        record("real encrypted pack corruption cannot pass full data check or prune baseline", ["AC-BKP-03", "AC-TASK-03"], task=failed_corruption, corrupted_pack=packs[0].name)

        quota = root / "quota-repository"
        quota.mkdir()
        command(["sudo", "mount", "-t", "tmpfs", "-o", "size=128m,uid=%d,gid=%d,mode=0700" % (os.getuid(), os.getgid()), "tmpfs", str(quota)])
        mounts.append(quota)
        quota_repo = repository(quota, "quota")
        quota_policy = policy(quota_repo, [{"type": "directory", "path": str(source)}], "quota")
        run(quota_policy)
        quota_before = refs(quota_repo)
        quota_verified = verified(quota_repo)
        filler = quota / ".acceptance-filler"
        fault_file.write_text(json.dumps(dict(operation="backup", fill=str(filler))))
        no_space = run(quota_policy, "Failed")
        filler.unlink()
        fault_file.write_text("{}")
        check(set(quota_before).issubset(refs(quota_repo)), "ENOSPC pruned baseline snapshots")
        check(verified(quota_repo) == quota_verified, "ENOSPC backup added a verified snapshot")
        check(all(inspect(cid)["State"]["Running"] for cid in writers), "ENOSPC did not recover shared writers")
        record("actual private tmpfs ENOSPC after writer stop fails and preserves retention", ["AC-BKP-03", "AC-TASK-03"], task=no_space, private_quota_bytes=128 * 1024 * 1024, original_writers_recovered=True)

        valid_snap = valid_shared_snapshot
        metadata = api.call("GET", "/backup/repositories/" + repo["id"] + "/snapshots/" + valid_snap["id"] + "/metadata")
        selected = next(value for value in metadata["sources"] if value.get("host_path") == str(source))
        req = dict(repository_id=repo["id"], snapshot=valid_snap["id"], target=str(source), source=selected["snapshot_path"], overwrite=True, recreate=False)
        unconfirmed_preview = api.call("POST", "/backup/restore/preview", req)
        api.call("POST", "/backup/restore", dict(req, revision=unconfirmed_preview["revision"], confirm=True), expected=400)
        check(all(inspect(cid)["State"]["Running"] for cid in writers), "missing second confirmation stopped writers")
        req["confirm_overwrite"] = str(source)
        pre = api.call("POST", "/backup/restore/preview", req)
        (source / "payload").write_bytes(secrets.token_bytes(128 * 1024))
        pre = api.call("POST", "/backup/restore/preview", req)
        fault_file.write_text(json.dumps(dict(operation="restore", file_limit=True)))
        partial = task_failed(api, api.call("POST", "/backup/restore", dict(req, revision=pre["revision"], confirm=True), expected=202))
        fault_file.write_text("{}")
        check(partial["error_code"] == "RESTORE_VALIDATION_REQUIRED", "partial overwrite lacks explicit validation error")
        check(all(not inspect(cid)["State"]["Running"] for cid in writers), "partial overwrite restarted writers")
        # The operator can still inspect and deliberately start the service.
        api.task(api.call("POST", "/hosts/local/containers/" + writers[0] + "/start", {}, expected=202))
        check(inspect(writers[0])["State"]["Running"], "safe partial-restore failure blocked explicit manual recovery")
        record("original overwrite requires second confirmation and real EFBIG failure holds writers", ["AC-BKP-06", "AC-TASK-03"], task=partial, kernel_file_limit_bytes=1024, all_writers_held=True, explicit_manual_start_available=True)

        # Actual offline elapsed time crosses a UTC minute; no system clock or
        # persisted scheduler timestamps are edited in this release scenario.
        scheduled = dict(shared, schedule="* * * * *", timezone="America/New_York")
        api.call("PUT", "/backup/policies/" + shared["id"], scheduled)
        server.stop()
        offline_start = datetime.datetime.now(datetime.timezone.utc)
        wait = 63 - time.time() % 60
        print("Waiting %.1fs across real offline cron boundary" % wait, flush=True)
        time.sleep(wait)
        server.start()
        missed = [row for row in api.call("GET", "/backup/schedule-instances") if row["policy_id"] == shared["id"] and row.get("error", "").startswith("MISSED_OFFLINE")]
        check(missed and all(not row.get("task_id") for row in missed), "offline schedule was omitted or automatically replayed")
        check(any(datetime.datetime.fromisoformat(row["scheduled_at_utc"].replace("Z", "+00:00")) > offline_start for row in missed),
              "no missed occurrence lies inside the actual offline interval")
        report["offline_interval"] = dict(start=offline_start.isoformat(), end=datetime.datetime.now(datetime.timezone.utc).isoformat())
        record("actual offline UTC cron occurrence recorded without catch-up execution", ["AC-BKP-02", "AC-TASK-02"], instances=missed)
        failed = False
    except BaseException as error:
        report["error"] = support.clean_message(error)
        print("FAIL " + report["error"], flush=True)
        raise
    finally:
        for server in reversed(servers):
            try:
                if failed:
                    report.setdefault("server_logs", []).append(server.logs())
                server.stop()
            except BaseException as error:
                report["cleanup"].setdefault("errors", []).append(support.clean_message(error))
        for cid in reversed(containers):
            result = dc("inspect", cid, allow_failure=True)
            if not result.returncode:
                value = json.loads(result.stdout)[0]
                check(value["Config"]["Labels"].get(label) == token, "refusing unowned cleanup")
                dc("rm", "--force", cid)
        # Recreated services can exist before a failed response; label inventory
        # catches them while keeping unrelated fixtures out of the cleanup scope.
        remaining = dc("ps", "--all", "--quiet", "--filter", "label=" + label + "=" + token).stdout.decode().split()
        if remaining:
            dc("rm", "--force", *remaining)
        for project_name in (prefix, prefix + "-restored"):
            networks = dc("network", "ls", "--quiet", "--filter", "label=com.docker.compose.project=" + project_name).stdout.decode().split()
            for network in networks:
                dc("network", "rm", network)
        for path in reversed(mounts):
            command(["sudo", "umount", str(path)])
        if (root / "shared-data").exists():
            (root / "shared-data").chmod(0o755)
        report["cleanup"]["remaining_owned_containers"] = dc("ps", "--all", "--quiet", "--filter", "label=" + label + "=" + token).stdout.decode().split()
        report["status"] = "failed" if failed or report["cleanup"].get("errors") else "passed"
        report["finished_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        destination = pathlib.Path(os.environ["DM_TEST_REPORT"])
        destination.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        shutil.rmtree(root)


if __name__ == "__main__":
    main(str(pathlib.Path(sys.argv[1]).resolve()))
