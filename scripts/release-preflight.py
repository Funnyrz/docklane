#!/usr/bin/env python3
"""Read-only release readiness check; 0=ready, 1=failed, 2=blocked/unverified.

No repository creation, pushes, dispatches, registry login or publication occurs.
Use --online only to inspect the configured GitHub target with existing gh auth.
"""
import argparse
import datetime
import hashlib
import json
import pathlib
import re
import shutil
import subprocess
import sys
import urllib.parse


def run(args, root, timeout=30):
    try:
        return subprocess.run(args, cwd=root, text=True, capture_output=True, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired):
        return None


def github_repository(remote):
    match = re.fullmatch(r"(?:https://(?:[^/@]+@)?github\.com/|git@github\.com:)([\w.-]+/[\w.-]+?)(?:\.git)?/?", remote)
    return match.group(1) if match else None


def checksum_manifest(root, manifest, required):
    """Reject traversal/duplicates and check every listed payload, streaming large archives."""
    manifest = root / manifest
    if not manifest.is_file():
        return False, "checksum manifest missing"
    seen = set()
    for line in manifest.read_text().splitlines():
        match = re.fullmatch(r"([a-fA-F0-9]{64}) [ *](.+)", line)
        if not match:
            return False, "malformed checksum line"
        name = match.group(2)
        if name in seen or pathlib.Path(name).name != name or name in (".", ".."):
            return False, "duplicate or non-local payload name"
        seen.add(name)
        payload = root / name
        if not payload.is_file() or payload.is_symlink():
            return False, "payload missing or symlink: " + name
        digest = hashlib.sha256()
        with payload.open("rb") as stream:
            for block in iter(lambda: stream.read(1 << 20), b""):
                digest.update(block)
        if digest.hexdigest() != match.group(1).lower():
            return False, "checksum mismatch: " + name
    if not set(required).issubset(seen):
        return False, "manifest omits required payloads"
    return True, "all listed payload hashes match; this does not authenticate a publisher"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=pathlib.Path, default=pathlib.Path(__file__).resolve().parents[1])
    parser.add_argument("--repo", help="Explicit GitHub OWNER/REPO, otherwise inspect origin")
    parser.add_argument("--tag", help="Exact release tag, for example v0.3.0")
    parser.add_argument("--registry", help="Exact GHCR repository without tag, for example ghcr.io/OWNER/docker-manager")
    parser.add_argument("--online", action="store_true", help="Read GitHub account, repo settings, CI and tag using gh; never write")
    parser.add_argument("--actionlint", help="Optional existing actionlint executable")
    parser.add_argument("--report", type=pathlib.Path, help="JSON output path; stdout always receives the report")
    args = parser.parse_args()
    root = args.root.resolve()
    checks = []

    def record(name, status, detail, gate="ci"):
        checks.append(dict(name=name, status=status, detail=detail, gate=gate))

    def git(*arguments):
        result = run(["git", *arguments], root)
        return result.stdout.strip() if result and result.returncode == 0 else None

    head = git("rev-parse", "--verify", "HEAD")
    record("committed_source", "passed" if head else "blocked", head or "No local commit; remote CI has no immutable source commit")
    dirty = git("status", "--porcelain")
    record("committed_worktree", "passed" if head and dirty == "" else "blocked", "Clean committed worktree" if head and dirty == "" else "Commit the reviewed source and evidence before selecting a release SHA")
    remote = git("remote", "get-url", "origin")
    repo = args.repo or github_repository(remote or "")
    valid_repo = bool(repo and re.fullmatch(r"[\w.-]+/[\w.-]+", repo))
    record("github_target", "passed" if valid_repo else "blocked", repo if valid_repo else "Set the intended GitHub OWNER/REPO and origin; no repository is inferred from the Go module")
    tag = args.tag
    expected = "v" + json.loads((root / "web/package.json").read_text())["version"]
    record("release_tag", "passed" if tag == expected else "blocked", "Requested " + tag if tag == expected else "Select existing tag " + expected + "; current build scripts use this fixed version", "publication")
    registry_valid = bool(args.registry and re.fullmatch(r"ghcr\.io/[a-z0-9][a-z0-9_.-]*/[a-z0-9][a-z0-9_./-]*", args.registry))
    record("registry_target", "passed" if registry_valid else "blocked", args.registry if registry_valid else "Choose the exact lowercase ghcr.io/OWNER/IMAGE destination and package visibility", "publication")
    for manifest, names in [("SHA256SUMS", ["docklane-linux-amd64", "docklane-linux-arm64"]), ("IMAGE-SHA256SUMS", ["docker-manager-images.tar.gz"])]:
        ok, detail = checksum_manifest(root / "bin/release", manifest, names)
        record(manifest, "passed" if ok else "failed", detail, "local_artifacts")
    for name, machine in [("amd64", 62), ("arm64", 183)]:
        payload = root / "bin/release" / ("docklane-linux-" + name)
        header = b""
        if payload.is_file():
            with payload.open("rb") as stream:
                header = stream.read(20)
        ok = len(header) == 20 and header[:6] == b"\x7fELF\x02\x01" and int.from_bytes(header[18:20], "little") == machine
        record("ELF_" + name, "passed" if ok else "failed", "Linux ELF64 machine verified" if ok else "Missing or wrong architecture ELF payload", "local_artifacts")
    if args.actionlint:
        result = run([args.actionlint, "-shellcheck=", "-pyflakes="], root)
        ok = result is not None and result.returncode == 0
        record("workflow_syntax", "passed" if ok else "failed", "actionlint accepted both workflows" if ok else "actionlint failed; run it directly for source diagnostics", "local_artifacts")
    else:
        record("workflow_syntax", "unverified", "Pass --actionlint with an installed validator to verify workflow expressions and syntax", "local_artifacts")

    gh = shutil.which("gh")
    account = None
    settings = {}

    def api(path):
        result = run([gh, "api", path], root) if gh else None
        if result is None or result.returncode:
            return None
        try:
            return json.loads(result.stdout)
        except ValueError:
            return None

    if args.online and gh:
        user = api("user")
        account = user.get("login") if isinstance(user, dict) else None
        record("github_auth", "passed" if account else "blocked", "Authenticated API account: " + account if account else "gh API authentication unavailable; no token or raw auth output is retained")
        if valid_repo:
            info = api("repos/" + repo)
            if isinstance(info, dict):
                settings = {key: info.get(key) for key in ("full_name", "default_branch", "private", "archived", "permissions")}
                can_push = bool(info.get("permissions", {}).get("push")) and not info.get("archived")
                record("repository_access", "passed" if can_push else "blocked", "Repository writable" if can_push else "Read access alone cannot commit workflows or push a tag")
                actions = api("repos/" + repo + "/actions/permissions")
                record("actions_enabled", "passed" if isinstance(actions, dict) and actions.get("enabled") else "blocked", "Actions enabled" if isinstance(actions, dict) and actions.get("enabled") else "Actions disabled or admin settings could not be inspected")
                if head:
                    data = api("repos/" + repo + "/actions/workflows/ci.yml/runs?head_sha=" + head + "&status=success&per_page=100")
                    runs = data.get("workflow_runs", []) if isinstance(data, dict) else []
                    good = [item for item in runs if item.get("head_sha") == head and item.get("status") == "completed" and item.get("conclusion") == "success" and item.get("event") in ("push", "workflow_dispatch")]
                    record("exact_commit_ci", "passed" if good else "blocked", good[0]["html_url"] if good else "No successful completed push/manual ci.yml run for this local SHA")
                if tag == expected:
                    ref = api("repos/" + repo + "/git/ref/tags/" + urllib.parse.quote(tag, safe=""))
                    obj = ref.get("object", {}) if isinstance(ref, dict) else {}
                    for _ in range(4):
                        if obj.get("type") != "tag":
                            break
                        annotated = api("repos/" + repo + "/git/tags/" + obj.get("sha", ""))
                        obj = annotated.get("object", {}) if isinstance(annotated, dict) else {}
                    ok = bool(head and obj.get("type") == "commit" and obj.get("sha") == head)
                    record("remote_tag_matches", "passed" if ok else "blocked", "Existing remote tag resolves to local HEAD" if ok else "Remote tag missing, inaccessible, or not equal to the reviewed local commit", "publication")
            else:
                record("repository_access", "blocked", "Target repository unavailable to the authenticated account")
    else:
        record("github_auth", "unverified", "gh installed; use --online for read-only API checks" if gh else "Install/authenticate GitHub CLI for target inspection")
    if not valid_repo or not args.online or not settings:
        record("remote_configuration", "blocked", "Repository/default branch, Actions policy, exact-commit run, tag, runner availability and package access cannot be verified without an accessible target")
    record("registry_write_and_visibility", "unverified", "Read-only preflight does not authenticate to or push a registry; verify package Actions write access and intended visibility before publication", "publication")
    record("formal_publication", "blocked", "Only artifact preparation is automated. An authorized maintainer must select the target, review candidate hashes/security notes and explicitly publish the GHCR manifest and GitHub Release", "publication")
    gates = {}
    for gate in sorted({item["gate"] for item in checks}):
        states = {item["status"] for item in checks if item["gate"] == gate}
        gates[gate] = "failed" if "failed" in states else "blocked" if states & {"blocked", "unverified"} else "passed"
    report = dict(schema_version=1, recorded_at=datetime.datetime.now(datetime.timezone.utc).isoformat(), mode="read-only", remote_ci_executed=False, published=False, source_sha=head, repository=repo if valid_repo else None, release_tag=tag, registry=args.registry if registry_valid else None, authenticated_account=account, repository_settings=settings, gate_status=gates, checks=checks)
    output = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    if args.report:
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(output)
    sys.stdout.write(output)
    return 1 if "failed" in gates.values() else 2 if "blocked" in gates.values() else 0


if __name__ == "__main__":
    sys.exit(main())
