#!/usr/bin/env python3
"""Require customer-facing changelog updates for product feat/fix commits."""

from __future__ import annotations

import re
import subprocess
import sys
from pathlib import Path


PRODUCT_COMMIT = re.compile(r"^(feat|fix)(?:\(([^)]+)\))?!?:\s+", re.IGNORECASE)
NON_PRODUCT_SCOPES = {"build", "chore", "ci", "deps", "docs", "release", "test", "tests"}


def run_git(*args: str) -> str:
    result = subprocess.run(
        ["git", *args],
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
    )
    return result.stdout.strip()


def valid_commit(value: str) -> bool:
    if not value or set(value) == {"0"}:
        return False
    return subprocess.run(
        ["git", "cat-file", "-e", f"{value}^{{commit}}"],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    ).returncode == 0


def resolve_base(requested: str, head: str) -> str:
    if valid_commit(requested) and requested != head:
        return requested
    fallback = f"{head}^"
    if valid_commit(fallback):
        return run_git("rev-parse", fallback)
    return head


def requires_changelog(subjects: list[str]) -> bool:
    for subject in subjects:
        match = PRODUCT_COMMIT.match(subject.strip())
        if not match:
            continue
        scope = (match.group(2) or "").strip().lower()
        if scope not in NON_PRODUCT_SCOPES:
            return True
    return False


def changelog_changed(files: list[str]) -> bool:
    return "CHANGELOG.md" in {Path(item).as_posix() for item in files}


def main() -> int:
    requested_base = sys.argv[1].strip() if len(sys.argv) > 1 else ""
    head = sys.argv[2].strip() if len(sys.argv) > 2 else "HEAD"
    head = run_git("rev-parse", head)
    base = resolve_base(requested_base, head)
    if base == head:
        return 0

    subjects = run_git("log", "--format=%s", f"{base}..{head}").splitlines()
    if not requires_changelog(subjects):
        return 0

    files = run_git("diff", "--name-only", "--diff-filter=ACMR", base, head).splitlines()
    if changelog_changed(files):
        return 0

    print(
        "Product feat/fix commits must update CHANGELOG.md. "
        "Add a customer-facing bullet under '## Unreleased' before publishing.",
        file=sys.stderr,
    )
    print("Product commits in this range:", file=sys.stderr)
    for subject in subjects:
        if requires_changelog([subject]):
            print(f"- {subject}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
