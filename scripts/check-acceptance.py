#!/usr/bin/env python3
"""Check PRD/matrix coverage and local documentation links, not runtime behavior."""
import argparse
import datetime
import hashlib
import json
import pathlib
import re
import urllib.parse


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report", type=pathlib.Path)
    args = parser.parse_args()
    root = pathlib.Path(__file__).resolve().parents[1]
    prd = (root / "docker-manager-prd-v0.3.md").read_text()
    matrix = (root / "docs/acceptance-matrix.md").read_text()
    ac_pattern = r"AC-[A-Z]+-\d{2}"
    expected = set(re.findall(ac_pattern, prd))
    rows = re.findall(r"^\| (AC-[A-Z]+-\d{2}) \|", matrix, re.M)
    scenarios = re.findall(r"^\| (RELEASE-\d{2}) \|", matrix, re.M)
    release_section = prd.split("## 八、发布验收场景", 1)[1].split("\n## ", 1)[0]
    expected_scenarios = re.findall(r"^\d+\. ", release_section, re.M)
    definitions = set(re.findall(r"\[([A-Z])：[^\]]+\]\(", matrix))
    references = {name for group in re.findall(r"\[([A-Z](?:/[A-Z])*)\]", matrix)
                  for name in group.split("/")}
    missing_links, checked = [], 0
    documents = sorted((root / "docs").glob("*.md"))
    for document in documents:
        for target in re.findall(r"\[[^\]]*\]\(([^)]+)\)", document.read_text()):
            target = target.strip("<>")
            parsed = urllib.parse.urlsplit(target)
            if parsed.scheme or not parsed.path:
                continue
            checked += 1
            destination = document.parent / urllib.parse.unquote(parsed.path)
            if not destination.exists():
                missing_links.append(dict(document=str(document.relative_to(root)), target=target))
    errors = []
    if set(rows) != expected or len(rows) != len(set(rows)):
        errors.append("AC rows must cover each PRD AC exactly once")
    expected_release_ids = {"RELEASE-%02d" % index for index in range(1, len(expected_scenarios) + 1)}
    if set(scenarios) != expected_release_ids or len(scenarios) != len(set(scenarios)):
        errors.append("release scenario rows must match the PRD exactly once")
    if references - definitions:
        errors.append("undefined evidence indexes: " + ", ".join(sorted(references - definitions)))
    if missing_links:
        errors.append("local documentation links are missing")
    report = dict(
        verified_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),
        validator_sha256=hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest(),
        result="failed" if errors else "passed",
        ac_count=len(expected), applicable_ac_count=len(expected - {"AC-ARCH-07"}),
        deferred=["AC-ARCH-07"], release_scenarios=len(scenarios),
        documentation_link_scope=[str(path.relative_to(root)) for path in documents],
        documentation_link_algorithm="Inline Markdown local file links; fragments omitted; external URLs excluded",
        documentation_links_checked=checked, missing_links=missing_links,
        evidence_indexes=sorted(definitions), undefined_evidence_indexes=sorted(references - definitions),
        errors=errors,
        scope="Structural contract only. Runtime evidence and remaining release gates are documented separately.",
    )
    if args.report:
        args.report.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return int(bool(errors))


if __name__ == "__main__":
    raise SystemExit(main())
