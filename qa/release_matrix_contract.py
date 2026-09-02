#!/usr/bin/env python3
from __future__ import annotations
import re
import sys
from pathlib import Path

EXPECTED = {
    "bondify-linux-amd64": ("linux", "amd64", "./desktop/cmd/bondify", "bondify"),
    "bondify-linux-arm64": ("linux", "arm64", "./desktop/cmd/bondify", "bondify"),
    "bondify-windows-amd64": ("windows", "amd64", "./desktop/cmd/bondify", "bondify.exe"),
    "bondify-relay-linux-amd64": ("linux", "amd64", "./relay/cmd/bondify-relay", "bondify-relay"),
    "bondify-relay-linux-arm64": ("linux", "arm64", "./relay/cmd/bondify-relay", "bondify-relay"),
}

def fail(msg: str) -> None:
    raise SystemExit(f"release matrix contract failed: {msg}")

def main() -> int:
    text = Path(sys.argv[1] if len(sys.argv) > 1 else ".github/workflows/release.yml").read_text(encoding="utf-8")
    if "permissions:\n  contents: read" not in text: fail("workflow must default to contents: read")
    if "needs: verify" not in text: fail("build job must depend on verify")
    if "needs: [build, android-ci]" not in text: fail("publish job must depend on build and android-ci")
    if "if: startsWith(github.ref, 'refs/tags/v')" not in text: fail("publish job must be tag-gated")
    for name, (goos, goarch, package, output) in EXPECTED.items():
        m = re.search(rf"- name: {re.escape(name)}(?P<body>.*?)(?=\n\s*- name:|\n\s*steps:)", text, re.DOTALL)
        if not m: fail(f"missing matrix entry {name}")
        body = m.group("body")
        for label, value in (("goos", goos), ("goarch", goarch), ("package", package), ("output", output)):
            if re.search(rf"^\s*{label}: {re.escape(value)}\s*$", body, re.MULTILINE) is None:
                fail(f"{name} missing {label}={value}")
    if "if-no-files-found: error" not in text: fail("release uploads must fail when expected assets are absent")
    if "qa/release_archive_contract.py" not in text: fail("release archives must pass archive contract validation")
    print(f"release matrix contract passed: {len(EXPECTED)} platform targets and publish gates validated")
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
