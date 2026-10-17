#!/usr/bin/env python3
"""Native Linux acceptance of an authenticated, uncached Registry v2 image pull.

Only a unique labelled image and a private temporary app installation are created.
The stdlib-only fixture binds 127.0.0.1:5000, requires Basic auth, and throttles a
new random 20 MiB layer so actual Engine download byte progress is observable.
No daemon configuration, existing images, containers, or registry trust changes.
"""
import argparse
import base64
import datetime
import gzip
import hashlib
import hmac
import http.server
import io
import json
import os
import pathlib
import platform
import re
import secrets
import shutil
import signal
import sys
import tarfile
import tempfile
import threading
import time
import urllib.parse

from integration import Client, Server, SECRETS, check, clean_message, command, digest, executable

LABEL = "io.docker-manager.private-pull-acceptance"
MEDIA_MANIFEST = "application/vnd.docker.distribution.manifest.v2+json"
MEDIA_CONFIG = "application/vnd.docker.container.image.v1+json"
MEDIA_LAYER = "application/vnd.docker.image.rootfs.diff.tar.gzip"


def sha(data):
    return "sha256:" + hashlib.sha256(data).hexdigest()


def compact(value):
    return json.dumps(value, separators=(",", ":")).encode()


class PrivateRegistry:
    def __init__(self, namespace):
        self.username = "owned-" + secrets.token_hex(10)
        self.password = secrets.token_urlsafe(36)
        self.auth = "Basic " + base64.b64encode((self.username + ":" + self.password).encode()).decode()
        SECRETS.extend([self.password, self.auth, self.auth[6:]])
        self.repo, self.tag = namespace + "/image", "new-" + secrets.token_hex(8)
        self.reference = "127.0.0.1:5000/" + self.repo + ":" + self.tag
        self.requests = []
        self.served_layer_bytes = 0
        self.deny = False
        self.failed = None
        payload = secrets.token_bytes(20 * 1024 * 1024)
        self.payload_sha256 = hashlib.sha256(payload).hexdigest()
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w") as archive:
            entry = tarfile.TarInfo("owned-payload.bin")
            entry.size, entry.mode, entry.uid, entry.gid = len(payload), 0o600, 1001, 1001
            archive.addfile(entry, io.BytesIO(payload))
        uncompressed = buffer.getvalue()
        self.layer = gzip.compress(uncompressed, compresslevel=1, mtime=0)
        self.diff_id, self.layer_digest = sha(uncompressed), sha(self.layer)
        self.arch = {"aarch64": "arm64", "x86_64": "amd64"}.get(platform.machine())
        check(self.arch is not None, "unsupported native test architecture")
        self.config = compact({
            "created": datetime.datetime.now(datetime.timezone.utc).isoformat(),
            "architecture": self.arch, "os": "linux",
            "config": {"Cmd": ["/owned-fixture-never-executed"], "Labels": {LABEL: namespace}},
            "rootfs": {"type": "layers", "diff_ids": [self.diff_id]},
            "history": [{"created_by": "Docklane owned acceptance fixture"}],
        })
        self.config_digest = sha(self.config)
        self.manifest = compact({"schemaVersion": 2, "mediaType": MEDIA_MANIFEST,
            "config": {"mediaType": MEDIA_CONFIG, "size": len(self.config), "digest": self.config_digest},
            "layers": [{"mediaType": MEDIA_LAYER, "size": len(self.layer), "digest": self.layer_digest}]})
        self.manifest_digest = sha(self.manifest)
        registry = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args):
                pass

            def do_HEAD(self):
                self.serve(False)

            def do_GET(self):
                self.serve(True)

            def serve(self, send_body):
                path = urllib.parse.urlsplit(self.path).path
                authorized = hmac.compare_digest(self.headers.get("Authorization", ""), registry.auth)
                media, content_digest, throttle = "application/json", None, False
                if not authorized:
                    status, body = 401, compact({"errors": [{"code": "UNAUTHORIZED", "message": "authentication required"}]})
                elif registry.deny:
                    # An untrusted remote can echo even a valid secret without a password= prefix.
                    status, body = 403, compact({"errors": [{"code": "DENIED", "message": "remote echoed " + registry.password}]})
                elif path.rstrip("/") == "/v2":
                    status, body = 200, b"{}"
                elif path in ("/v2/" + registry.repo + "/manifests/" + registry.tag,
                               "/v2/" + registry.repo + "/manifests/" + registry.manifest_digest):
                    status, body, media, content_digest = 200, registry.manifest, MEDIA_MANIFEST, registry.manifest_digest
                elif path == "/v2/" + registry.repo + "/blobs/" + registry.config_digest:
                    status, body, media, content_digest = 200, registry.config, MEDIA_CONFIG, registry.config_digest
                elif path == "/v2/" + registry.repo + "/blobs/" + registry.layer_digest:
                    status, body, media, content_digest, throttle = 200, registry.layer, MEDIA_LAYER, registry.layer_digest, True
                else:
                    status, body = 404, compact({"errors": [{"code": "MANIFEST_UNKNOWN", "message": "unique test tag absent"}]})
                record = {"method": self.command, "path": path, "status": status,
                          "authenticated": authorized, "response_bytes": len(body), "sent_bytes": 0}
                registry.requests.append(record)
                self.send_response(status)
                self.send_header("Docker-Distribution-API-Version", "registry/2.0")
                self.send_header("Content-Type", media)
                self.send_header("Content-Length", str(len(body)))
                if content_digest:
                    self.send_header("Docker-Content-Digest", content_digest)
                if status == 401:
                    self.send_header("WWW-Authenticate", 'Basic realm="owned private pull acceptance"')
                self.end_headers()
                if not send_body:
                    return
                try:
                    if throttle:
                        for offset in range(0, len(body), 64 * 1024):
                            chunk = body[offset:offset + 64 * 1024]
                            self.wfile.write(chunk)
                            self.wfile.flush()
                            record["sent_bytes"] += len(chunk)
                            registry.served_layer_bytes += len(chunk)
                            time.sleep(0.025)
                    else:
                        self.wfile.write(body)
                        self.wfile.flush()
                        record["sent_bytes"] = len(body)
                except (BrokenPipeError, ConnectionResetError):
                    pass

        class HTTPServer(http.server.ThreadingHTTPServer):
            daemon_threads = True

            def handle_error(self, request, address):
                registry.failed = "unexpected fixture HTTP handler error"

        self.http = HTTPServer(("127.0.0.1", 5000), Handler)
        self.thread = threading.Thread(target=self.http.serve_forever, daemon=True)

    def start(self):
        self.thread.start()

    def close(self):
        self.http.shutdown()
        self.http.server_close()
        self.thread.join(timeout=5)


def byte_value(number, unit):
    powers = {"B": 0, "kB": 1, "KB": 1, "MB": 2, "GB": 3, "TB": 4}
    return int(float(number) * 1000 ** powers[unit])


def run(args):
    check(sys.platform.startswith("linux"), "acceptance must execute in a native Linux guest")
    binary = executable(args.binary, "compiled Docklane")
    docker = executable(os.environ.get("DM_TEST_DOCKER", "docker"), "Docker CLI")
    restic = executable(os.environ.get("DM_TEST_RESTIC", "restic"), "Restic")
    endpoint = os.environ.get("DM_TEST_DOCKER_ENDPOINT", "unix:///var/run/docker.sock")
    namespace = "dm-private-pull-" + secrets.token_hex(10)
    root = pathlib.Path(tempfile.mkdtemp(prefix=namespace + "-"))
    report = {"namespace": namespace, "started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "binary_sha256": digest(binary), "harness_sha256": digest(__file__),
              "integration_helper_sha256": digest(sys.modules["integration"].__file__),
              "platform": {"system": platform.system(), "architecture": platform.machine(), "uid": os.getuid()},
              "daemon_configuration_changed": False, "checks": [], "cleanup_errors": []}
    daemon_config = pathlib.Path("/etc/docker/daemon.json")
    daemon_config_before = digest(daemon_config) if daemon_config.exists() else None
    report["reproduction"] = {"command": [sys.executable, __file__, "--binary", binary, "--report", args.report],
        "environment": {"DM_TEST_DOCKER": docker, "DM_TEST_RESTIC": restic, "DM_TEST_DOCKER_ENDPOINT": endpoint,
                        "PYTHONPATH": str(pathlib.Path(sys.modules["integration"].__file__).parent)}}
    report["limitations"] = ["Private registry uses HTTP Basic only on guest loopback; this run does not claim TLS registry verification.",
        "Native Debian 12 arm64 / the recorded Engine version only; broader platform evidence is maintained separately."]
    registry = server = None
    owned_reference = owned_image_id = None
    failures = []

    def cli(*argv, **kwargs):
        return command([docker, "--host", endpoint, *argv], **kwargs)

    def passed(name, evidence):
        report["checks"].append({"name": name, "status": "passed", "evidence": evidence})
        print("PASS " + name, flush=True)

    try:
        report["engine"] = json.loads(cli("version", "--format", "{{json .Server}}").stdout)
        report["engine_driver"] = cli("info", "--format", "{{.Driver}}").stdout.decode().strip()
        report["distribution"] = pathlib.Path("/etc/os-release").read_text()
        registry = PrivateRegistry(namespace)
        owned_reference, owned_image_id = registry.reference, registry.config_digest
        absent = cli("image", "inspect", owned_reference, allow_failure=True)
        check(absent.returncode != 0, "unique image already exists; refusing cached test")
        absent = cli("image", "inspect", owned_image_id, allow_failure=True)
        check(absent.returncode != 0, "unique config already exists; refusing cached test")
        report["fixture"] = {"reference": owned_reference, "config_digest": registry.config_digest,
            "manifest_digest": registry.manifest_digest, "layer_digest": registry.layer_digest,
            "diff_id": registry.diff_id, "layer_compressed_bytes": len(registry.layer),
            "payload_bytes": 20 * 1024 * 1024, "payload_sha256": registry.payload_sha256,
            "throttle_chunk_bytes": 64 * 1024, "throttle_interval_seconds": 0.025,
            "image_absent_before": True, "cached_layers": False}
        registry.start()
        server = Server(binary, root, "state", endpoint, docker, restic, None)
        server.start()
        client = Client(server.base)
        client.call("GET", "/auth/status")
        password = secrets.token_urlsafe(36)
        SECRETS.append(password)
        client.call("POST", "/auth/setup", {"username": namespace, "password": password})
        credential = client.call("POST", "/registry-credentials", {"name": namespace,
            "server": "http://127.0.0.1:5000", "username": registry.username, "password": registry.password}, expected=201)
        login = client.call("POST", "/registry-credentials/" + credential["id"] + "/test", {})
        check(login["authenticated"] is True, "real Engine RegistryLogin did not authenticate")
        passed("encrypted saved credential authenticates against real private Registry v2", {
            "credential_id": credential["id"], "authenticated": login["authenticated"],
            "challenge_seen": any(r["status"] == 401 for r in registry.requests),
            "authorized_v2_seen": any(r["path"] == "/v2/" and r["status"] == 200 and r["authenticated"] for r in registry.requests)})
        pull = client.task(client.call("POST", "/hosts/local/images/pull", {"image": owned_reference,
            "credential_id": credential["id"], "platform": "linux/" + registry.arch}, expected=202))
        logs = client.call("GET", "/tasks/" + pull["id"] + "/logs")
        messages = [entry["message"] for entry in logs]
        samples = []
        expression = re.compile(r"Downloading\s+\[[^\]]*\]\s*([\d.]+)\s*([kKMGT]?B)\s*/\s*([\d.]+)\s*([kKMGT]?B)")
        for message in messages:
            match = expression.search(message)
            if match:
                current = byte_value(match[1], match[2])
                total = byte_value(match[3], match[4])
                if not samples or current != samples[-1]["current_bytes_approx"]:
                    samples.append({"current_bytes_approx": current, "total_bytes_approx": total, "message": message})
        check(len(samples) >= 3, "actual new layer did not emit multiple byte-progress updates")
        check(any(0 < s["current_bytes_approx"] < s["total_bytes_approx"] for s in samples), "no in-flight byte progress was visible")
        check(any(s["current_bytes_approx"] >= s["total_bytes_approx"] * 0.99 for s in samples), "no complete download byte progress was visible")
        check(registry.served_layer_bytes >= len(registry.layer), "Engine did not receive the complete new compressed layer")
        inspected = json.loads(cli("image", "inspect", owned_reference).stdout)[0]
        check(inspected["Id"] == registry.config_digest, "independent Engine config digest mismatch")
        check(inspected["RootFS"]["Layers"] == [registry.diff_id], "independent Engine layer diff-id mismatch")
        expected_repo_digest = "127.0.0.1:5000/" + registry.repo + "@" + registry.manifest_digest
        check(expected_repo_digest in inspected["RepoDigests"], "independent Engine manifest digest mismatch")
        check(any(registry.manifest_digest in message and "Digest:" in message for message in messages), "task did not report actual manifest Digest")
        check(inspected["Config"]["Labels"].get(LABEL) == namespace, "image ownership label is absent")
        check(registry.failed is None, "fixture had an unexpected request failure")
        passed("authenticated uncached 20 MiB image pull emits new-layer bytes and verified Digest", {
            "task_id": pull["id"], "status": pull["status"], "image_id": inspected["Id"],
            "repo_digest": expected_repo_digest, "layer_bytes_transferred": registry.served_layer_bytes,
            "download_progress_count": len(samples), "download_progress": samples,
            "extract_progress_observed": any("Extracting" in message for message in messages),
            "cached_layers": False, "digest_verified_independently": True})
        missing = client.task(client.call("POST", "/hosts/local/images/pull", {
            "image": "127.0.0.1:5000/" + registry.repo + ":absent-" + secrets.token_hex(8),
            "credential_id": credential["id"]}, expected=202), expected_status="Failed")
        wrong_password = secrets.token_urlsafe(36)
        SECRETS.append(wrong_password)
        wrong = client.call("POST", "/registry-credentials", {"name": namespace + "-wrong",
            "server": "http://127.0.0.1:5000", "username": registry.username, "password": wrong_password}, expected=201)
        wrong_pull = client.task(client.call("POST", "/hosts/local/images/pull", {
            "image": owned_reference, "credential_id": wrong["id"]}, expected=202), expected_status="Failed")
        registry.deny = True
        echoed = client.task(client.call("POST", "/hosts/local/images/pull", {
            "image": owned_reference, "credential_id": credential["id"]}, expected=202), expected_status="Failed")
        failed_logs = []
        for task in (missing, wrong_pull, echoed):
            check(task["error_code"] == "TASK_FAILED" and task.get("error_message"), "failed pull has no actionable error")
            failed_logs.extend(client.call("GET", "/tasks/" + task["id"] + "/logs"))
        # Check raw bytes, not a sanitized printer, in public responses and live SQLite/WAL files.
        response_bytes = json.dumps([credential, wrong, login, logs, failed_logs, missing, wrong_pull, echoed]).encode()
        snapshots = [response_bytes, server.log_path.read_bytes()]
        for filename in ("docker-manager.db", "docker-manager.db-wal"):
            file = server.state / filename
            if file.exists():
                snapshots.append(file.read_bytes())
        sensitive = [registry.password, wrong_password, registry.auth, registry.auth[6:]]
        for secret in sensitive:
            check(all(secret.encode() not in data for data in snapshots), "registry secret appeared in an API response, server log, SQLite or WAL")
        check(any("[REDACTED]" in str(value) for value in (echoed, failed_logs)), "remote reflected-secret denial was not visibly redacted")
        passed("missing tag, wrong credentials and remote secret echo fail safely", {
            "missing_tag_task": missing["id"], "wrong_credential_task": wrong_pull["id"],
            "echoed_secret_task": echoed["id"], "statuses": [missing["status"], wrong_pull["status"], echoed["status"]],
            "error_codes": [missing["error_code"], wrong_pull["error_code"], echoed["error_code"]],
            "secret_plaintext_absent_in_responses_logs_sqlite_wal": True, "echo_redacted": True})
        report["registry_requests"] = registry.requests
        report["acceptance"] = ["AC-IMG-01", "AC-SYS-02", "AC-TASK-03"]
    except BaseException as error:
        failures.append(clean_message(error))
        report["error"] = failures[-1]
    finally:
        if server:
            try:
                server.stop()
            except BaseException as error:
                report["cleanup_errors"].append(clean_message(error))
        if registry:
            registry.close()
        if owned_image_id:
            try:
                existing = cli("image", "inspect", owned_image_id, allow_failure=True)
                if existing.returncode == 0:
                    image = json.loads(existing.stdout)[0]
                    check(image["Config"]["Labels"].get(LABEL) == namespace, "cleanup image ownership mismatch")
                    check(all(tag == owned_reference for tag in image.get("RepoTags", [])), "cleanup image has an unexpected additional tag")
                    cli("image", "rm", owned_image_id)
                absent = cli("image", "inspect", owned_image_id, allow_failure=True)
                check(absent.returncode != 0, "owned image remains after cleanup")
                report["owned_image_removed"] = True
            except BaseException as error:
                report["cleanup_errors"].append(clean_message(error))
        shutil.rmtree(root)
        report["temporary_installation_removed"] = not root.exists()
        daemon_config_after = digest(daemon_config) if daemon_config.exists() else None
        report["daemon_config_before_sha256"] = daemon_config_before
        report["daemon_config_after_sha256"] = daemon_config_after
        if daemon_config_before != daemon_config_after:
            report["cleanup_errors"].append("daemon configuration changed during acceptance")
            report["daemon_configuration_changed"] = True
        report["finished_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        report["result"] = "passed" if not failures and not report["cleanup_errors"] else "failed"
        output = pathlib.Path(args.report)
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps(report, indent=2) + "\n")
        print("RESULT " + report["result"], flush=True)
        if failures:
            print("ERROR " + failures[-1], flush=True)
    return 0 if report["result"] == "passed" else 1


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--report", required=True)
    arguments = parser.parse_args()
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    sys.exit(run(arguments))
