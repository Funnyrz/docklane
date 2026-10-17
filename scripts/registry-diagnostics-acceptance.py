#!/usr/bin/env python3
"""Verify network, Registry API and actual pull diagnostics on isolated Linux.

Requires integration.py beside this script and a compiled Linux service. Uses
DM_TEST_DOCKER, DM_TEST_DOCKER_ENDPOINT and DM_TEST_RESTIC. The loopback-only
Registry fixture serves /v2/ but deliberately has no image manifests. No daemon
configuration, operational resource, default Docker context or preview is changed.
"""
import argparse
import datetime
import http.server
import json
import os
import pathlib
import platform
import secrets
import shutil
import signal
import socket
import sys
import tempfile
import threading
import urllib.parse

from integration import Client, Server, SECRETS, check, clean_message, command, digest, executable


class EmptyRegistry:
    def __init__(self):
        self.requests = []
        self.tls_client_hellos = 0
        self.require_authentication = False
        fixture = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                path = urllib.parse.urlsplit(self.path).path
                if path == "/v2/":
                    status = 401 if fixture.require_authentication else 200
                    payload = b'{}'
                else:
                    status = 404
                    payload = b'{"errors":[{"code":"MANIFEST_UNKNOWN","message":"owned diagnostic fixture intentionally contains no images"}]}'
                fixture.requests.append({"method": self.command, "path": path, "status": status})
                self.send_response(status)
                self.send_header("Docker-Distribution-API-Version", "registry/2.0")
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                if status == 401:
                    self.send_header("WWW-Authenticate", 'Basic realm="owned diagnostics fixture"')
                self.end_headers()
                if self.command != "HEAD":
                    self.wfile.write(payload)

            do_HEAD = do_GET

            def log_message(self, *_):
                pass

        class RegistryServer(http.server.ThreadingHTTPServer):
            def get_request(self):
                connection, address = super().get_request()
                connection.settimeout(2)
                try:
                    # Observe actual Engine contact without consuming bytes.
                    # Some Engine stores require TLS even on loopback HTTP.
                    initial = connection.recv(5, socket.MSG_PEEK)
                    if initial.startswith(b"\x16\x03"):
                        fixture.tls_client_hellos += 1
                except TimeoutError:
                    pass
                finally:
                    connection.settimeout(None)
                return connection, address

        self.server = RegistryServer(("127.0.0.1", 0), Handler)
        self.server.daemon_threads = True
        self.port = self.server.server_address[1]
        self.origin = "http://127.0.0.1:%d" % self.port
        self.image = "127.0.0.1:%d/dm-diagnostics-%s/image:missing" % (self.port, secrets.token_hex(8))
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
        check(not self.thread.is_alive(), "owned Registry fixture thread did not stop")


def main(binary, report_path):
    check(platform.system() == "Linux", "run this acceptance inside isolated Linux")
    docker = executable(os.getenv("DM_TEST_DOCKER", "docker"), "Docker CLI")
    restic = executable(os.getenv("DM_TEST_RESTIC", "restic"), "Restic")
    endpoint = os.getenv("DM_TEST_DOCKER_ENDPOINT", "unix:///var/run/docker.sock")
    check(endpoint.startswith("unix://"), "use an explicitly selected local test Engine socket")
    root = pathlib.Path(tempfile.mkdtemp(prefix="dm-registry-diagnostics-", dir="/tmp"))
    fixture = server = None
    checks = []
    report = {
        "started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "binary_sha256": digest(binary), "harness_sha256": digest(__file__),
        "integration_helper_sha256": digest(pathlib.Path(__file__).with_name("integration.py")),
        "platform": {"system": platform.system(), "architecture": platform.machine(), "kernel": platform.release()},
        "endpoint": endpoint, "checks": checks,
        "scope": "AC-IMG-05 and RELEASE-04; final compiled service against real Engine and owned empty loopback Registry v2",
        "limitations": ["Loopback HTTP fixture does not prove remote Registry TLS or mirror performance.",
                        "Actual pull uses current Engine routing; no unapplied mirror is claimed to have been used."],
        "reproduction": {"command": [sys.executable, str(pathlib.Path(__file__).resolve()), "--binary", binary, "--report", str(report_path)],
                         "environment": {"DM_TEST_DOCKER": docker, "DM_TEST_RESTIC": restic, "DM_TEST_DOCKER_ENDPOINT": endpoint}},
    }
    failed = True

    def record(name, evidence):
        checks.append({"name": name, "status": "passed", "evidence": evidence})
        print("PASS " + name, flush=True)

    try:
        report["engine"] = json.loads(command([docker, "--host", endpoint, "version", "--format", "{{json .Server}}"], timeout=30).stdout)
        fixture = EmptyRegistry()
        server = Server(binary, root, "state", endpoint, docker, restic, "")
        server.start()
        client = Client(server.base)
        client.call("GET", "/auth/status")
        password = secrets.token_urlsafe(36)
        SECRETS.append(password)
        client.call("POST", "/auth/setup", {"username": "diagnostics-" + secrets.token_hex(8), "password": password})
        SECRETS.extend(cookie.value for cookie in client.cookies)

        preview = client.call("POST", "/host/diagnostics", {"url": fixture.origin, "image": fixture.image})
        check(preview["network"]["available"] is True, "reachable loopback TCP was misreported")
        check(preview["registry_api"]["available"] is True and preview["registry_api"]["status_code"] == 200, "working Registry API was misreported")
        check(preview["image_pull"]["tested"] is False and "task_id" not in preview["image_pull"], "unconfirmed diagnostic unexpectedly submitted a pull")
        check(all(request["path"] == "/v2/" for request in fixture.requests), "unconfirmed diagnostic requested a manifest")
        record("reachable TCP and Registry API do not imply image availability or silently trigger a pull", preview)

        confirmed = client.call("POST", "/host/diagnostics", {"url": fixture.origin, "image": fixture.image, "confirm_pull": True})
        check(confirmed["network"]["available"] is True and confirmed["registry_api"]["available"] is True, "confirmed pull lost independent reachability results")
        task = client.task({"id": confirmed["image_pull"]["task_id"]}, timeout=60, expected_status="Failed")
        logs = client.call("GET", "/tasks/" + task["id"] + "/logs")
        check(task.get("error_code") and task.get("error_message"), "failed actual pull omitted a diagnosable error")
        manifest_missing = any("/manifests/missing" in request["path"] and request["status"] == 404 for request in fixture.requests)
        check(manifest_missing or fixture.tls_client_hellos > 0,
              "real Engine never contacted the fixture: " + clean_message(task["error_message"]))
        absent = command([docker, "--host", endpoint, "image", "inspect", fixture.image], allow_failure=True, timeout=30)
        check(absent.returncode != 0, "missing fixture image unexpectedly exists")
        record("real Engine pull fails with an inspectable task while TCP and Registry API remain available", {
            "diagnostic": confirmed, "task_id": task["id"], "task_status": task["status"],
            "error_code": task["error_code"], "error_message": clean_message(task["error_message"]),
            "task_logs": logs, "image_absent_after_failure": True,
            "engine_fixture_contact": {"manifest_404": manifest_missing, "tls_client_hellos": fixture.tls_client_hellos},
        })

        fixture.require_authentication = True
        auth = client.call("POST", "/host/diagnostics", {"url": fixture.origin})
        check(auth["network"]["available"] is True and auth["registry_api"]["available"] is True, "HTTP 401 was confused with network/API unavailability")
        check(auth["registry_api"]["status_code"] == 401 and auth["registry_api"]["authentication_required"] is True, "HTTP 401 omitted authentication requirement")
        check(auth["image_pull"]["tested"] is False and "task_id" not in auth["image_pull"], "authentication diagnostic unexpectedly submitted pull")
        record("Registry HTTP 401 is reachable but requires authentication and does not prove a pull", auth)

        report["registry_requests"] = fixture.requests
        fixture.close()
        closed_origin = fixture.origin
        fixture = None
        unavailable = client.call("POST", "/host/diagnostics", {"url": closed_origin})
        check(unavailable["network"]["available"] is False and unavailable["registry_api"]["available"] is False, "closed TCP endpoint was reported as available")
        check(unavailable["image_pull"]["tested"] is False, "unreachable unconfirmed endpoint was treated as tested pull")
        record("closed TCP endpoint reports network failure independently of untested image pull", unavailable)
        failed = False
    except BaseException as error:
        report["failure"] = clean_message(error) or type(error).__name__
        raise
    finally:
        cleanup_errors = []
        if server:
            try:
                server.stop()
            except Exception as error:
                cleanup_errors.append(clean_message(error))
        if fixture:
            try:
                fixture.close()
            except Exception as error:
                cleanup_errors.append(clean_message(error))
        try:
            shutil.rmtree(root)
        except Exception as error:
            cleanup_errors.append(clean_message(error))
        report["cleanup"] = {"server_stopped": server is None or server.process is None, "temporary_installation_removed": not root.exists(),
                             "registry_closed": fixture is None or not fixture.thread.is_alive(), "daemon_reconfigured": False,
                             "docker_resources_created": 0, "errors": cleanup_errors}
        report["result"] = "failed" if failed or cleanup_errors else "passed"
        report["finished_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        destination = pathlib.Path(report_path)
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        check(not cleanup_errors, "owned fixture cleanup failed: " + "; ".join(cleanup_errors))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--report", required=True)
    args = parser.parse_args()

    def terminate(*_):
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, terminate)
    try:
        main(str(pathlib.Path(args.binary).resolve()), args.report)
    except KeyboardInterrupt:
        print("ERROR: interrupted; owned fixture cleanup attempted", file=sys.stderr)
        raise SystemExit(130)
    except Exception as error:
        print("ERROR: " + clean_message(error), file=sys.stderr)
        raise SystemExit(1)
