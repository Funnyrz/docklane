#!/usr/bin/env python3
"""Native Linux resources release acceptance; only random labelled fixtures are mutated.

Uses integration.py's authenticated HTTP client and isolated temporary server.
DM_TEST_DOCKER, DM_TEST_DOCKER_ENDPOINT, DM_TEST_RESTIC and DM_TEST_IMAGE
select real tools. --binary is mandatory; --report writes sanitized JSON evidence.
"""
import argparse
import base64
import datetime
import hashlib
import http.client
import http.server
import json
import os
import pathlib
import re
import secrets
import shutil
import signal
import socket
import sqlite3
import struct
import threading
import sys
import tempfile
import traceback
import time
import urllib.error
import urllib.parse
import urllib.request

from integration import Client, Server, SECRETS, check, clean_message, command, digest, executable

LABEL = "io.docker-manager.resources-acceptance"


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("localhost", timeout=15)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def engine_get(endpoint, path):
    if endpoint.startswith("unix://"):
        connection = UnixHTTP(endpoint.removeprefix("unix://"))
    else:
        address = urllib.parse.urlsplit(endpoint)
        connection = http.client.HTTPConnection(address.hostname, address.port or 2375, timeout=15)
    try:
        connection.request("GET", path)
        response = connection.getresponse()
        check(response.status == 200, "independent Engine HTTP read failed")
        return json.loads(response.read())
    finally:
        connection.close()


def sse_events(response):
    event = {}
    data = []
    while True:
        line = response.readline()
        if not line:
            break
        text = line.decode().rstrip("\r\n")
        if not text:
            if data:
                event["data"] = json.loads("\n".join(data))
                yield event
            event, data = {}, []
        elif text.startswith("data:"):
            data.append(text[5:].lstrip())
        elif ":" in text:
            key, value = text.split(":", 1)
            event[key] = value.lstrip()


def open_sse(client, path, headers=None):
    response = client.opener.open(urllib.request.Request(client.base + "/api/v1" + path, headers=headers or {}), timeout=10)
    check(response.status == 200 and response.headers.get_content_type() == "text/event-stream", "missing SSE content type")
    return response


class HandshakeRejected(RuntimeError):
    def __init__(self, status, body):
        super().__init__("WebSocket upgrade rejected with HTTP %d" % status)
        self.status, self.body = status, body


class TerminalClosed(RuntimeError):
    pass


class WebSocket:
    """Small stdlib RFC6455 client: verifies handshake and masks all client frames."""
    def __init__(self, client, path, *, origin=None):
        address = urllib.parse.urlsplit(client.base)
        self.socket = socket.create_connection((address.hostname, address.port), timeout=10)
        self.buffer = b""
        key = base64.b64encode(secrets.token_bytes(16)).decode()
        cookie_request = urllib.request.Request(client.base + path)
        client.cookies.add_cookie_header(cookie_request)
        headers = {"Host": address.netloc, "Upgrade": "websocket", "Connection": "Upgrade", "Sec-WebSocket-Key": key,
                   "Sec-WebSocket-Version": "13", "Origin": origin or client.base, "Cookie": cookie_request.get_header("Cookie", "")}
        self.socket.sendall(("GET /api/v1" + path + " HTTP/1.1\r\n" + "\r\n".join(k + ": " + v for k, v in headers.items()) + "\r\n\r\n").encode())
        response = b""
        while b"\r\n\r\n" not in response:
            part = self.socket.recv(4096)
            check(bool(part), "WebSocket handshake disconnected")
            response += part
        head, self.buffer = response.split(b"\r\n\r\n", 1)
        status = int(head.split(b"\r\n", 1)[0].split()[1])
        if status != 101:
            fields = dict(line.split(":", 1) for line in head.decode().split("\r\n")[1:] if ":" in line)
            length = next((int(value.strip()) for key, value in fields.items() if key.lower() == "content-length"), len(self.buffer))
            body = self.exact(length)
            self.socket.close()
            raise HandshakeRejected(status, body)
        expected = base64.b64encode(hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()).decode()
        check(any(line.lower() == ("sec-websocket-accept: " + expected).lower() for line in head.decode().split("\r\n")), "invalid WebSocket accept key")

    def exact(self, count):
        while len(self.buffer) < count:
            data = self.socket.recv(max(4096, count - len(self.buffer)))
            check(bool(data), "WebSocket stream closed before expected output")
            self.buffer += data
        data, self.buffer = self.buffer[:count], self.buffer[count:]
        return data

    def send(self, data, opcode=1):
        if isinstance(data, str):
            data = data.encode()
        mask = secrets.token_bytes(4)
        length = len(data)
        header = bytes([0x80 | opcode])
        if length < 126:
            header += bytes([0x80 | length])
        elif length < 65536:
            header += bytes([0x80 | 126]) + struct.pack("!H", length)
        else:
            header += bytes([0x80 | 127]) + struct.pack("!Q", length)
        self.socket.sendall(header + mask + bytes(value ^ mask[i % 4] for i, value in enumerate(data)))

    def receive(self):
        first, second = self.exact(2)
        check(first & 0x80 and not second & 0x80, "unexpected fragmented/masked server frame")
        length = second & 127
        if length == 126:
            length = struct.unpack("!H", self.exact(2))[0]
        elif length == 127:
            length = struct.unpack("!Q", self.exact(8))[0]
        check(length < 2 * 1024 * 1024, "unexpectedly large terminal frame")
        data = self.exact(length)
        opcode = first & 15
        if opcode == 9:
            self.send(data, 10)
            return b""
        if opcode == 8:
            raise TerminalClosed(data[2:].decode(errors="replace"))
        return data if opcode in (1, 2) else b""

    def until(self, marker):
        result = b""
        deadline = time.monotonic() + 10
        while marker not in result and time.monotonic() < deadline:
            result += self.receive()
        check(marker in result, "terminal command did not produce expected non-echoed output")
        return result

    def close(self):
        try:
            self.send(struct.pack("!H", 1000), 8)
        except OSError:
            pass
        try:
            self.socket.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        self.socket.close()


class FixtureRegistry:
    """Loopback Distribution v2 authentication fixture; never changes daemon trust."""
    def __init__(self):
        self.username, self.password = "owned-" + secrets.token_hex(8), secrets.token_urlsafe(32)
        SECRETS.append(self.password)
        expected_auth = "Basic " + base64.b64encode((self.username + ":" + self.password).encode()).decode()
        SECRETS.append(expected_auth)
        self.requests = []
        self.deny = False
        registry = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args):
                pass

            def do_HEAD(self):
                self.handle_request(False)

            def do_GET(self):
                self.handle_request(True)

            def handle_request(self, write):
                path = urllib.parse.urlsplit(self.path).path
                if self.headers.get("Authorization") != expected_auth:
                    code, payload, media = 401, b'{"errors":[{"code":"UNAUTHORIZED","message":"test authentication required"}]}', "application/json"
                elif registry.deny:
                    code, payload, media = 403, json.dumps({"errors": [{"code": "DENIED", "message": "registry refused " + registry.password}]}).encode(), "application/json"
                elif path == "/v2/":
                    code, payload, media = 200, b"{}", "application/json"
                else:
                    code, payload, media = 404, b'{"errors":[{"code":"MANIFEST_UNKNOWN","message":"owned test tag absent"}]}', "application/json"
                registry.requests.append({"method": self.command, "path": path, "status": code})
                self.send_response(code)
                self.send_header("Docker-Distribution-API-Version", "registry/2.0")
                self.send_header("Content-Type", media)
                self.send_header("Content-Length", str(len(payload)))
                if code == 401:
                    self.send_header("WWW-Authenticate", 'Basic realm="owned acceptance registry"')
                self.end_headers()
                if write:
                    try:
                        self.wfile.write(payload)
                        self.wfile.flush()
                    except (BrokenPipeError, ConnectionResetError):
                        pass

        class RegistryServer(http.server.ThreadingHTTPServer):
            def handle_error(self, request, address):
                if isinstance(sys.exc_info()[1], ConnectionResetError):
                    return
                super().handle_error(request, address)

        self.server = RegistryServer(("127.0.0.1", 0), Handler)
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.address = "127.0.0.1:%d" % self.server.server_port

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)


def main(binary, report_path):
    check(os.uname().sysname == "Linux", "run resources acceptance inside native Linux")
    docker = executable(os.getenv("DM_TEST_DOCKER", "docker"), "Docker CLI")
    restic = executable(os.getenv("DM_TEST_RESTIC", "restic"), "Restic")
    endpoint = os.getenv("DM_TEST_DOCKER_ENDPOINT", "unix:///var/run/docker.sock")
    env = dict(os.environ, DOCKER_HOST=endpoint)
    token = secrets.token_hex(10)
    prefix = "dm-resource-" + token
    labels = {LABEL: token}
    root = pathlib.Path(tempfile.mkdtemp(prefix=prefix + "-", dir="/tmp"))
    outside = pathlib.Path(tempfile.mkdtemp(prefix=prefix + "-outside-", dir="/tmp"))
    servers, containers, volumes, networks = [], [], [], []
    registry = None
    checks = []
    report = {"namespace": prefix, "started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "binary_sha256": digest(binary), "checks": checks, "host": {"uid": os.getuid(), "system": os.uname().sysname, "architecture": os.uname().machine},
              "registry_protocol": "https://distribution.github.io/distribution/spec/api/"}
    failed = True

    def dc(*args, allow_failure=False):
        return command([docker, *args], env=env, allow_failure=allow_failure)

    def record(acs, name, evidence):
        checks.append({"ac": acs, "name": name, "status": "passed", "evidence": evidence})
        print("PASS " + name, flush=True)

    def inspect(cid):
        return json.loads(dc("inspect", "--type", "container", cid).stdout)[0]

    try:
        info = engine_get(endpoint, "/info")
        report.update(engine=info["ServerVersion"], engine_os=info["OperatingSystem"])
        image = os.getenv("DM_TEST_IMAGE", "busybox:1.37.0")
        check(dc("image", "inspect", image, allow_failure=True).returncode == 0, "install DM_TEST_IMAGE first; acceptance never removes shared base images")
        image_id = json.loads(dc("image", "inspect", image).stdout)[0]["Id"]
        bind = root / "bind"
        bind.mkdir()
        retained = secrets.token_bytes(8192)
        (bind / "retained.bin").write_bytes(retained)
        volume_name = prefix + "-data"
        dc("volume", "create", "--label", LABEL + "=" + token, volume_name)
        volumes.append(volume_name)
        cid = dc("run", "--detach", "--name", prefix + "-logs", "--label", LABEL + "=" + token,
                 "--mount", "type=bind,src=%s,dst=/bind" % bind, "--mount", "type=volume,src=%s,dst=/data" % volume_name,
                 image_id, "sh", "-c", "trap 'exit 0' TERM INT; for n in 1 2 3 4 5 6 7 8; do echo stdout-$n; echo stderr-$n >&2; done; while :; do sleep 1; done").stdout.decode().strip()
        containers.append(cid)
        dc("exec", cid, "sh", "-c", "printf retained-volume-content > /data/payload")
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline and b"stdout-8" not in dc("logs", cid).stdout:
            time.sleep(0.05)
        server = Server(binary, root, "state", endpoint, docker, restic, "")
        servers.append(server)
        server.start()
        client = Client(server.base)
        client.call("GET", "/auth/status")
        password = secrets.token_urlsafe(36)
        SECRETS.append(password)
        client.call("POST", "/auth/setup", {"username": prefix, "password": password})
        SECRETS.extend(cookie.value for cookie in client.cookies)
        route = "/hosts/local/containers/" + cid
        with open_sse(client, route + "/logs?tail=5&timestamps=true&follow=false") as response:
            tail = list(sse_events(response))
        check(len(tail) == 5 and all(value.get("event") == "log" for value in tail), "Tail did not return exactly five log events")
        check(all(re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d+Z", value.get("id", "")) and value["data"]["text"].startswith(value["id"] + " ") for value in tail), "Docker timestamps/SSE IDs were lost")
        with open_sse(client, route + "/logs?tail=100&timestamps=false&follow=false") as response:
            plain = list(sse_events(response))
        expected = {stream + "-" + str(n) for stream in ("stdout", "stderr") for n in range(1, 9)}
        check({value["data"]["text"] for value in plain} == expected and all(value.get("id") for value in plain), "stdout/stderr demultiplex, timestamp hiding or reconnect cursor failed")
        hidden_marker = "hidden-timestamp-" + secrets.token_hex(8)
        dc("exec", cid, "sh", "-c", "for n in 1 2 3 4 5 6 7 8; do echo " + hidden_marker + "-$n > /proc/1/fd/1; done")
        with open_sse(client, route + "/logs?tail=3&timestamps=false&follow=false", {"Last-Event-ID": plain[-1]["id"]}) as response:
            hidden_resumed = list(sse_events(response))
        check(all(any(value["data"]["text"] == hidden_marker + "-" + str(n) for value in hidden_resumed) for n in range(1, 9)), "hidden timestamp mode lost reconnect gap or exposed timestamps")
        marker = "follow-" + secrets.token_hex(8)
        with open_sse(client, route + "/logs?tail=0&timestamps=true&follow=true") as response:
            dc("exec", cid, "sh", "-c", "echo " + marker + " > /proc/1/fd/1")
            received = next(sse_events(response))
            check(marker in received["data"]["text"], "live log follow did not receive a new Engine line")
        marker2 = "reconnected-" + secrets.token_hex(8)
        dc("exec", cid, "sh", "-c", "for n in 1 2 3 4 5 6 7 8; do echo " + marker2 + "-$n > /proc/1/fd/2; done")
        with open_sse(client, route + "/logs?tail=3&timestamps=true&follow=false", {"Last-Event-ID": received["id"]}) as response:
            resumed = list(sse_events(response))
        check(all(any(marker2 + "-" + str(n) in value["data"]["text"] for value in resumed) for n in range(1, 9)) and all(value["id"] >= received["id"] for value in resumed), "Last-Event-ID reconnect truncated the gap to initial Tail or replayed older logs")
        with open_sse(client, route + "/logs?tail=100&timestamps=true&follow=false&since=" + urllib.parse.quote(received["id"])) as response:
            explicit_since = list(sse_events(response))
        check(any(marker2 in value["data"]["text"] for value in explicit_since), "explicit since reconnect lost new logs")
        client.call("GET", route + "/logs?tail=-1", expected=400)
        record(["AC-CTR-03"], "real Engine Tail, timestamps, stdout/stderr, live SSE and reconnect", {"tail_count": len(tail), "initial_stdout_stderr_count": len(plain), "follow_event_id": received["id"], "reconnect_count": len(resumed), "since_query_verified": True, "hidden_timestamps_reconnect_gap": 8})

        terminal_path = route + "/terminal?csrf_token=" + urllib.parse.quote(client.token())
        unauthenticated = Client(server.base)
        unauthenticated.call("GET", terminal_path, expected=401)
        for origin, path in [("http://untrusted.invalid", terminal_path), (server.base, route + "/terminal?csrf_token=wrong")]:
            request = urllib.request.Request(server.base + "/api/v1" + path, headers={"Origin": origin})
            try:
                client.opener.open(request, timeout=5)
                raise RuntimeError("terminal accepted invalid Origin/CSRF")
            except urllib.error.HTTPError as error:
                check(error.code == 403, "terminal boundary rejection returned wrong status")
        missing_output = b""
        try:
            missing_ws = WebSocket(client, terminal_path + "&shell=/bin/bash")
        except HandshakeRejected as error:
            missing_shell = json.loads(error.body)
            check(error.status == 422 and missing_shell["error"]["code"] == "SHELL_UNAVAILABLE", "missing shell had no actionable error")
            missing_shell_code = missing_shell["error"]["code"]
        else:
            try:
                while True:
                    missing_output += missing_ws.receive()
            except TerminalClosed as error:
                check("shell" in str(error).lower() or b"/bin/bash" in missing_output, "missing shell close did not explain unavailable Shell")
                missing_shell_code = "SHELL_UNAVAILABLE_AFTER_UPGRADE"
            finally:
                missing_ws.close()
        fd_before = len(list(pathlib.Path("/proc/%d/fd" % server.process.pid).iterdir()))
        terminal_execs = []
        for n in range(4):
            prior = set(inspect(cid).get("ExecIDs") or [])
            ws = WebSocket(client, terminal_path)
            active = set(inspect(cid).get("ExecIDs") or []) - prior
            check(len(active) == 1, "could not identify the owned interactive Docker exec")
            exec_id = active.pop()
            terminal_execs.append(exec_id)
            execution = engine_get(endpoint, "/exec/" + exec_id + "/json")
            check(execution["Running"] and execution["ProcessConfig"]["tty"], "Docker exec did not allocate a real TTY")
            output_marker = "executed-" + secrets.token_hex(8)
            encoded = "".join("\\x%02x" % value for value in output_marker.encode())
            ws.send("test -t 0 && test -t 1 && printf '" + encoded + "\\n'\n")
            ws.until(output_marker.encode())
            if n == 0:
                ws.send(json.dumps({"type": "resize", "rows": 37, "cols": 113}))
                ws.send("stty size\n")
                ws.until(b"37 113\r\n")
            ws.close()
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline and engine_get(endpoint, "/exec/" + exec_id + "/json")["Running"]:
                time.sleep(0.05)
            check(not engine_get(endpoint, "/exec/" + exec_id + "/json")["Running"], "disconnect left interactive shell exec running")
        time.sleep(0.2)
        fd_after = len(list(pathlib.Path("/proc/%d/fd" % server.process.pid).iterdir()))
        check(fd_after <= fd_before + 3, "repeated terminal closure leaked backend descriptors")
        record(["AC-CTR-04", "AC-SYS-01"], "real WebSocket TTY execution, resize, missing shell and backend release", {"sessions": len(terminal_execs), "all_execs_stopped": True, "process_fds_before": fd_before, "process_fds_after": fd_after, "missing_shell_code": missing_shell_code, "origin_csrf_auth_enforced": True})

        stats = client.call("GET", route + "/stats")
        check(stats["id"] == cid and stats["memory_stats"], "real container stats are missing")
        with open_sse(client, route + "/stats?stream=true") as response:
            streamed_stats = next(sse_events(response))
        check(streamed_stats["event"] == "stats" and streamed_stats["data"]["id"] == cid, "real stats SSE lost container identity")
        record([], "real Engine stats snapshot and SSE", {"container_id": cid})

        network = client.call("POST", "/hosts/local/networks", {"name": prefix + "-network", "labels": labels}, expected=201)
        network_id = network["Id"]
        networks.append(network_id)
        dc("network", "connect", network_id, cid)
        detail = client.call("GET", "/hosts/local/networks/" + network_id)
        check(cid in detail["Containers"], "network inspect omitted its associated container")
        client.call("DELETE", "/hosts/local/networks/" + network_id, {"confirmed": True}, expected=409)
        dc("network", "disconnect", network_id, cid)
        client.call("DELETE", "/hosts/local/networks/" + network_id, {"confirmed": True})
        check(dc("network", "inspect", network_id, allow_failure=True).returncode != 0, "network delete did not reach Engine")
        record(["AC-STO-01"], "owned bridge network create, association, reference guard and delete", {"network_id": network_id, "attached_delete_status": 409})
        client.task(client.call("POST", route + "/stop", {}, expected=202))
        check(not inspect(cid)["State"]["Running"], "reference fixture did not stop")
        volume_ref = client.call("GET", "/hosts/local/volumes/" + volume_name)
        image_ref = client.call("GET", "/hosts/local/images/" + image_id)
        check(any(value["Id"] == cid for value in volume_ref["containers"]) and any(value["Id"] == cid for value in image_ref["containers"]), "inspect did not expose stopped container references")
        refused_volume = client.call("DELETE", "/hosts/local/volumes/" + volume_name, {"confirmed": True}, expected=409)
        refused_image = client.call("DELETE", "/hosts/local/images/" + image_id, {"confirmed": True}, expected=409)
        check(refused_volume["error"]["code"] == "VOLUME_REFERENCED" and refused_image["error"]["code"] == "IMAGE_REFERENCED", "stopped container references were not enforced")
        mounts = client.call("GET", "/hosts/local/mounts?page_size=200")
        check(any(value["container_id"] == cid and value["mount"].get("Name") == volume_name for value in mounts["items"]), "mount navigation omitted stopped container Volume")
        client.task(client.call("DELETE", route, {"confirmed": True}, expected=202))
        check(dc("inspect", cid, allow_failure=True).returncode != 0, "container delete task did not remove the container")
        check((bind / "retained.bin").read_bytes() == retained, "container deletion modified bind data")
        volume_payload = dc("run", "--rm", "--network", "none", "--read-only", "--label", LABEL + "=" + token,
                            "--mount", "type=volume,src=%s,dst=/data,readonly" % volume_name, image_id, "cat", "/data/payload").stdout
        check(volume_payload == b"retained-volume-content", "container deletion removed or changed retained Volume data")
        record(["AC-CTR-07", "AC-IMG-02", "AC-STO-01", "AC-STO-02"], "stopped references prevent image/Volume deletion; container deletion preserves Volume and bind data", {"volume_reference_count": len(volume_ref["containers"]), "image_reference_count": len(image_ref["containers"]), "bind_sha256": hashlib.sha256(retained).hexdigest(), "volume_content_verified_after_container_delete": True})

        registry = FixtureRegistry()
        inline_secret = secrets.token_urlsafe(24)
        SECRETS.append(inline_secret)
        for invalid_server in ("http://user:" + inline_secret + "@" + registry.address, "http://" + registry.address + "?token=" + inline_secret):
            client.call("POST", "/registry-credentials", {"name": prefix + "-invalid", "server": invalid_server,
                        "username": registry.username, "password": registry.password}, expected=400)
        credential = client.call("POST", "/registry-credentials", {"name": prefix, "server": "http://" + registry.address,
                                 "username": registry.username, "password": registry.password}, expected=201)
        login = client.call("POST", "/registry-credentials/" + credential["id"] + "/test", {})
        check(login["authenticated"], "real Engine RegistryLogin failed")
        registry.deny = True
        denied_login = client.call("POST", "/registry-credentials/" + credential["id"] + "/test", {}, expected=502)
        check(denied_login["error"]["code"] == "REGISTRY_LOGIN_FAILED" and registry.password not in json.dumps(denied_login), "untrusted registry denial exposed its credential")
        registry.deny = False

        # Registry auth is exercised on a private loopback fixture. Pull uses an
        # already installed official image over trusted HTTPS, avoiding daemon CA
        # changes and avoiding deletion or retagging of another lane's images.
        client.call("POST", "/hosts/local/images/pull", {"image": image, "credential_id": credential["id"]}, expected=400)
        cached = json.loads(dc("image", "inspect", image).stdout)[0]
        cached_digests = [value.split("@", 1)[1] for value in cached.get("RepoDigests", [])]
        check(bool(cached_digests), "official cached test image must have a registry Digest")
        pull = client.task(client.call("POST", "/hosts/local/images/pull", {"image": image}, expected=202))
        progress = client.call("GET", "/tasks/" + pull["id"] + "/logs")
        messages = [value["message"] for value in progress]
        pulled = json.loads(dc("image", "inspect", image).stdout)[0]
        check(pulled["Id"] == cached["Id"], "official version tag moved; do not mutate cached shared fixture identity")
        check(any("Download" in text or "Pull complete" in text or "Already exists" in text or "Pulling from" in text for text in messages), "actual pull emitted no layer progress")
        check(any("Digest: " in text and expected in text for text in messages for expected in cached_digests), "task Digest differs from independently inspected cached registry identity")
        missing_reference = image.rsplit(":", 1)[0] + ":" + prefix + "-absent"
        missing = client.task(client.call("POST", "/hosts/local/images/pull", {"image": missing_reference}, expected=202), expected_status="Failed")
        check(any(word in missing["error_message"].lower() for word in ("manifest", "not found")) and missing["error_code"] == "TASK_FAILED", "real missing-tag pull did not expose failure")
        for value in [json.dumps(client.call("GET", "/registry-credentials")), server.log_path.read_text(errors="replace")] + [path.read_bytes().decode(errors="ignore") for path in server.state.glob("docker-manager.db*")]:
            check(registry.password not in value, "registry plaintext leaked into API, normal log or SQLite")
        record(["AC-IMG-01", "AC-SYS-02", "AC-TASK-03"], "real HTTPS Engine pull reports cached-layer progress/Digest and missing-tag error; private registry login", {"image_id": pulled["Id"], "manifest_digests": cached_digests, "cached_layers": True, "credential_registry_binding_enforced": True, "task_id": pull["id"], "progress_messages": messages, "missing_tag_error_code": missing["error_code"], "registry_requests": registry.requests, "credential_plaintext_absent": True, "sqlite_wal_checked": True, "private_registry_denial_injected": True, "inline_credential_urls_rejected": True})

        scan = root / "scan"
        scan.mkdir()
        for directory in range(100):
            sub = scan / str(directory)
            sub.mkdir()
            for n in range(500):
                (sub / str(n)).write_bytes(b"abc")
        (outside / "private").write_bytes(b"outside-authorization")
        (scan / "ignored-symlink").symlink_to(outside, target_is_directory=True)
        start = time.monotonic()
        first_scan = client.call("POST", "/hosts/local/storage/scan", {"path": str(scan)}, expected=202)
        acceptance_seconds = time.monotonic() - start
        check(acceptance_seconds < 1.5, "scan submission blocked on directory traversal")
        second_scan = client.call("POST", "/hosts/local/storage/scan", {"path": str(scan)}, expected=202)
        check(client.call("GET", "/tasks/" + second_scan["id"])["status"] == "Pending", "scans ran concurrently instead of serializing")
        first_result = client.task(first_scan)
        first_seconds = time.monotonic() - start
        second_result = client.task(second_scan)
        check(first_seconds >= 1.7, "50000-file scan bypassed the 128-entry/5ms rate limiter")
        check(second_result["started_at"] >= first_result["finished_at"], "scan task execution overlapped")
        with sqlite3.connect("file:%s?mode=ro" % (server.state / "docker-manager.db"), uri=True) as database:
            result = json.loads(database.execute("SELECT checkpoint FROM tasks WHERE id=?", (first_scan["id"],)).fetchone()[0])
        check(result["bytes"] == 150000 and result["files"] == 50000 and result["directories"] == 101, "scan size/counts wrong or followed symlink")
        for unauthorized in ("/", str(outside), str(scan / "ignored-symlink"), str(root / ".." / outside.name)):
            rejected = client.call("POST", "/hosts/local/storage/scan", {"path": unauthorized}, expected=403)
            check(rejected["error"]["code"] == "PATH_NOT_AUTHORIZED", "scan unauthorized boundary was not enforced")
        # Queue an authorized nested path behind another long scan, then swap its ancestor.
        parent = root / "replace"
        nested = parent / "nested"
        nested.mkdir(parents=True)
        outside_nested = outside / "nested"
        outside_nested.mkdir()
        (outside_nested / "not-authorized").write_bytes(b"must-not-be-counted")
        blocking = client.call("POST", "/hosts/local/storage/scan", {"path": str(scan)}, expected=202)
        queued = client.call("POST", "/hosts/local/storage/scan", {"path": str(nested)}, expected=202)
        pending_cancel = client.call("POST", "/hosts/local/storage/scan", {"path": str(scan)}, expected=202)
        client.call("POST", "/tasks/" + pending_cancel["id"] + "/cancel", {})
        client.task(pending_cancel, expected_status="Canceled")
        parent.rename(root / "replace-original")
        parent.symlink_to(outside, target_is_directory=True)
        client.task(blocking)
        stale = client.task(queued, expected_status="Failed")
        check("authoriz" in stale["error_message"].lower() or "changed" in stale["error_message"].lower(), "queued symlink swap did not fail with path error")
        active_cancel = client.call("POST", "/hosts/local/storage/scan", {"path": str(scan)}, expected=202)
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline and client.call("GET", "/tasks/" + active_cancel["id"])["stage"] != "scanning":
            time.sleep(0.01)
        check(client.call("GET", "/tasks/" + active_cancel["id"])["stage"] == "scanning", "could not observe an active scan for cancellation")
        client.call("POST", "/tasks/" + active_cancel["id"] + "/cancel", {})
        client.task(active_cancel, expected_status="Canceled")
        record(["AC-STO-03", "AC-ARCH-08"], "large scans submit asynchronously, serialize, limit traversal and reject unauthorized/symlink paths", {"files": result["files"], "bytes": result["bytes"], "directories": result["directories"], "submit_seconds": round(acceptance_seconds, 3), "first_scan_seconds": round(first_seconds, 3), "second_starts_after_first_finishes": True, "symlinks_not_followed": True, "queued_ancestor_swap_rejected": True, "outside_paths_denied": 4, "pending_and_running_cancel_verified": True})

        mapped = Server(binary, root, "docker-mode", endpoint, docker, restic, "")
        servers.append(mapped)
        configuration = json.loads(mapped.config_path.read_text())
        configuration["mode"] = "docker"
        mapped.config_path.write_text(json.dumps(configuration))
        mapped.start()
        mapped_client = Client(mapped.base)
        mapped_client.call("GET", "/auth/status")
        mapped_password = secrets.token_urlsafe(36)
        SECRETS.append(mapped_password)
        mapped_client.call("POST", "/auth/setup", {"username": prefix, "password": mapped_password})
        denied = mapped_client.call("POST", "/hosts/local/storage/scan", {"path": str(scan)}, expected=403)
        check("mapping" in denied["error"]["message"].lower(), "Docker mode accepted an unmapped host path")
        mapped.stop()
        mapped_host = str(root / "explicit-host-mapping")
        configuration["allowed_paths"] = [mapped_host]
        configuration["path_mappings"] = [{"host": mapped_host, "local": str(scan)}]
        mapped.config_path.write_text(json.dumps(configuration))
        mapped.start()
        mapped_scan = mapped_client.call("POST", "/hosts/local/storage/scan", {"path": mapped_host}, expected=202)
        mapped_client.task(mapped_scan)
        mapped_client.call("POST", "/hosts/local/storage/scan", {"path": str(scan)}, expected=403)
        record(["AC-ARCH-08"], "Docker mode requires explicit mapping and rejects the unmapped local path", {"unmapped_status": 403, "error_code": denied["error"]["code"], "mapped_task_succeeded": True, "local_path_bypass_denied": True})
        failed = False
    except BaseException as error:
        report["failure"] = clean_message(error) or type(error).__name__
        print(clean_message(traceback.format_exc()), file=sys.stderr)
        raise
    finally:
        cleanup_errors = []
        for server in reversed(servers):
            try:
                server.stop()
            except Exception as error:
                cleanup_errors.append(clean_message(error))
        if registry:
            registry.close()
        # Discover by this run's exact random label, including creations whose
        # HTTP/CLI response was interrupted before their ID was recorded.
        for args, collection, kind in [
            (["ps", "--all", "--quiet", "--filter", "label=" + LABEL + "=" + token], containers, "containers"),
            (["volume", "ls", "--quiet", "--filter", "label=" + LABEL + "=" + token], volumes, "volumes"),
            (["network", "ls", "--quiet", "--filter", "label=" + LABEL + "=" + token], networks, "networks"),
        ]:
            try:
                discovered = dc(*args)
                collection.extend(discovered.stdout.decode().split())
            except Exception as error:
                cleanup_errors.append("could not discover owned " + kind + ": " + clean_message(error))
        for cid in reversed(list(dict.fromkeys(containers))):
            try:
                value = dc("inspect", "--type", "container", cid, allow_failure=True)
                if value.returncode:
                    continue
                check(json.loads(value.stdout)[0]["Config"].get("Labels", {}).get(LABEL) == token, "refusing removal of unowned container")
                dc("rm", "--force", cid)
            except Exception as error:
                cleanup_errors.append(clean_message(error))
        for volume_name in list(dict.fromkeys(volumes)):
            try:
                value = dc("volume", "inspect", volume_name, allow_failure=True)
                if value.returncode == 0:
                    check(json.loads(value.stdout)[0].get("Labels", {}).get(LABEL) == token, "refusing removal of unowned Volume")
                    dc("volume", "rm", volume_name)
            except Exception as error:
                cleanup_errors.append(clean_message(error))
        for network_id in list(dict.fromkeys(networks)):
            try:
                value = dc("network", "inspect", network_id, allow_failure=True)
                if value.returncode == 0:
                    check(json.loads(value.stdout)[0].get("Labels", {}).get(LABEL) == token, "refusing removal of unowned network")
                    dc("network", "rm", network_id)
            except Exception as error:
                cleanup_errors.append(clean_message(error))
        shutil.rmtree(root)
        shutil.rmtree(outside)
        report["result"] = "failed" if failed or cleanup_errors else "passed"
        report["cleanup_errors"] = cleanup_errors
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
    def terminate(signum, frame):
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
