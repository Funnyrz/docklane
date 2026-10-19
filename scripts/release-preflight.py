#!/usr/bin/env python3
"""Read-only release readiness check; 0=ready, 1=failed, 2=blocked/unverified.

No repository creation, pushes, dispatches, registry login or publication occurs.
Use --online only to inspect the configured GitHub target with existing gh auth.
"""
import argparse
import base64
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
    if not manifest.is_file() or manifest.is_symlink():
        return False, "checksum manifest missing"
    seen = set()
    for line in manifest.read_text().splitlines():
        match = re.fullmatch(r"([a-fA-F0-9]{64}) [ *](.+)", line)
        if not match:
            return False, "malformed checksum line"
        name = match.group(2)
        if name in seen or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", name):
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


def release_assets(tag):
    names = {"SHA256SUMS", "IMAGE-SHA256SUMS", "docker-manager-images.tar.gz",
             "docker-manager-installation.tar.gz", "IMAGES.txt", "TOOL-VERSIONS.txt",
             "SECURITY-NOTES.md", "LICENSE", "release-ci-evidence.json", "install-online.sh"}
    for arch in ("amd64", "arm64"):
        names.update({"docklane-linux-" + arch, f"docklane-{tag}-linux-{arch}.tar.gz",
                      f"docklane-{tag}-linux-{arch}.tar.gz.sha256"})
    return names


def publication_workflow(source):
    """Check the reviewed release contract; actionlint remains the syntax validator."""
    checks = [
        r"(?m)^permissions:\n  contents: read\n  actions: read\s*$",
        r"(?m)^      publish:\n(?:        [^\n]*\n)*?        default: false\n        type: boolean\s*$",
        r"(?m)^  publish:\n    if: \$\{\{ inputs\.publish \}\}\n    needs: prepare\s*$",
        r"(?m)^    permissions:\n      contents: write\n      actions: read\s*$",
    ]
    return all(re.search(pattern, source) for pattern in checks)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=pathlib.Path, default=pathlib.Path(__file__).resolve().parents[1])
    parser.add_argument("--repo", help="Explicit GitHub OWNER/REPO, otherwise inspect origin")
    parser.add_argument("--tag", help="Exact release tag, for example v0.3.0")
    parser.add_argument("--registry", help="Optional future GHCR destination; not required or published by the archive-based release")
    parser.add_argument("--artifacts", type=pathlib.Path, help="Prepared candidate directory; defaults to ROOT/bin/release")
    parser.add_argument("--online", action="store_true", help="Read GitHub account, repo settings, CI and tag using gh; never write")
    parser.add_argument("--actionlint", help="Optional existing actionlint executable")
    parser.add_argument("--report", type=pathlib.Path, help="JSON output path; stdout always receives the report")
    args = parser.parse_args()
    root = args.root.resolve()
    artifacts = args.artifacts.resolve() if args.artifacts else root / "bin/release"
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
    record("distribution", "passed", "Public GitHub Release with dual-architecture offline worker archive; no registry publication is required", "publication")
    record("registry_target", "failed" if args.registry and not registry_valid else "passed",
           "Optional future destination: " + args.registry if registry_valid else
           "Invalid optional GHCR destination" if args.registry else "Not requested; the installer loads the Release worker archive", "publication")
    manifests = [("SHA256SUMS", ["docklane-linux-amd64", "docklane-linux-arm64", "LICENSE"]),
                 ("IMAGE-SHA256SUMS", ["docker-manager-images.tar.gz"]),
                 ("RELEASE-SHA256SUMS", release_assets(expected))]
    for arch in ("amd64", "arm64"):
        archive = f"docklane-{expected}-linux-{arch}.tar.gz"
        manifests.append((archive + ".sha256", [archive]))
    for manifest, names in manifests:
        ok, detail = checksum_manifest(artifacts, manifest, names)
        record(manifest, "passed" if ok else "failed", detail, "local_artifacts")
    inventory = artifacts / "RELEASE-SHA256SUMS"
    listed = set()
    if inventory.is_file() and not inventory.is_symlink():
        for line in inventory.read_text().splitlines():
            match = re.fullmatch(r"[a-fA-F0-9]{64} [ *]([A-Za-z0-9][A-Za-z0-9._-]*)", line)
            if match:
                listed.add(match[1])
    actual = {path.name for path in artifacts.iterdir()} if artifacts.is_dir() else set()
    ok = bool(listed) and actual == listed | {"RELEASE-SHA256SUMS"}
    record("release_asset_inventory", "passed" if ok else "failed",
           "Every candidate asset is listed in RELEASE-SHA256SUMS" if ok else "Candidate contains unlisted files or has no complete checksum inventory", "local_artifacts")
    evidence = {}
    try:
        evidence = json.loads((artifacts / "release-ci-evidence.json").read_text())
    except (OSError, ValueError):
        pass
    if not isinstance(evidence, dict):
        evidence = {}
    candidate_matches = bool(head and evidence.get("head_sha") == head)
    record("candidate_source", "passed" if candidate_matches else "failed",
           "Candidate CI evidence identifies local HEAD" if candidate_matches else "Candidate CI evidence is missing or identifies another commit", "local_artifacts")
    for name, machine in [("amd64", 62), ("arm64", 183)]:
        payload = artifacts / ("docklane-linux-" + name)
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

    def api(path, paginate=False):
        arguments = [gh, "api", *(('--paginate', '--slurp') if paginate else ()), path] if gh else []
        result = run(arguments, root) if gh else None
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
                public = info.get("private") is False
                record("public_downloads", "passed" if public else "blocked", "Repository allows unauthenticated installation downloads" if public else "The curl installer requires a public, accessible repository", "publication")
                actions = api("repos/" + repo + "/actions/permissions")
                record("actions_enabled", "passed" if isinstance(actions, dict) and actions.get("enabled") else "blocked", "Actions enabled" if isinstance(actions, dict) and actions.get("enabled") else "Actions disabled or admin settings could not be inspected")
                workflow = api("repos/" + repo + "/actions/workflows/release-prepare.yml")
                content = api("repos/" + repo + "/contents/.github/workflows/release-prepare.yml?ref=" + (head or "")) if head else None
                source = ""
                try:
                    if isinstance(content, dict) and content.get("encoding") == "base64":
                        source = base64.b64decode(content["content"]).decode()
                except (KeyError, ValueError, UnicodeDecodeError):
                    pass
                workflow_ready = isinstance(workflow, dict) and workflow.get("state") == "active" and publication_workflow(source)
                record("formal_publication", "passed" if workflow_ready else "blocked",
                       "Active workflow supports opt-in publication with a separate write-permission job at this commit" if workflow_ready else
                       "Commit and enable the reviewed archive-based release workflow before publication", "publication")
                if head:
                    data = api("repos/" + repo + "/actions/workflows/ci.yml/runs?head_sha=" + head + "&status=success&per_page=100")
                    runs = data.get("workflow_runs", []) if isinstance(data, dict) else []
                    good = [item for item in runs if item.get("head_sha") == head and item.get("status") == "completed" and item.get("conclusion") == "success" and item.get("event") in ("push", "workflow_dispatch")]
                    record("exact_commit_ci", "passed" if good else "blocked", good[0]["html_url"] if good else "No successful completed push/manual ci.yml run for this local SHA")
                    evidence_matches = any(item.get("id") == evidence.get("ci_run_id") for item in good)
                    record("candidate_ci_evidence", "passed" if evidence_matches else "blocked",
                           "Candidate names a successful exact-commit CI run" if evidence_matches else "Candidate evidence does not name an approved exact-commit CI run", "publication")
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
                    pages = api("repos/" + repo + "/releases?per_page=100", paginate=True)
                    available = isinstance(pages, list) and all(isinstance(page, list) for page in pages)
                    existing = [item for page in pages for item in page if item.get("tag_name") == tag] if available else []
                    record("release_not_overwritten", "passed" if available and not existing else "blocked",
                           "No existing draft or published Release uses this tag" if available and not existing else
                           "Release already exists; publication refuses overwrite" if existing else "Could not inspect existing Releases", "publication")
            else:
                record("repository_access", "blocked", "Target repository unavailable to the authenticated account")
    else:
        record("github_auth", "unverified", "gh installed; use --online for read-only API checks" if gh else "Install/authenticate GitHub CLI for target inspection")
    if not valid_repo or not args.online or not settings:
        record("remote_configuration", "blocked", "Repository/default branch, Actions policy, exact-commit run, tag, runner availability and package access cannot be verified without an accessible target")
    gates = {}
    for gate in sorted({item["gate"] for item in checks}):
        states = {item["status"] for item in checks if item["gate"] == gate}
        gates[gate] = "failed" if "failed" in states else "blocked" if states & {"blocked", "unverified"} else "passed"
    report = dict(schema_version=1, recorded_at=datetime.datetime.now(datetime.timezone.utc).isoformat(), mode="read-only", remote_ci_executed=False, published=False, distribution="github-release-archive", source_sha=head, repository=repo if valid_repo else None, release_tag=tag, registry=args.registry if registry_valid else None, authenticated_account=account, repository_settings=settings, artifact_directory=str(artifacts), gate_status=gates, checks=checks)
    output = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    if args.report:
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(output)
    sys.stdout.write(output)
    return 1 if "failed" in gates.values() else 2 if "blocked" in gates.values() else 0


if __name__ == "__main__":
    sys.exit(main())
