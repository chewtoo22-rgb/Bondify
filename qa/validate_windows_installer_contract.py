#!/usr/bin/env python3
"""Validate the Windows installer keeps relay input admission fail-closed."""
from __future__ import annotations

import re
import sys
from pathlib import Path

REQUIRED_MARKERS = (
    "function Assert-RelayInputs",
    "Assert-RelayInputs -Address $RelayAddr -PublicKey $RelayPubKey",
    "RelayAddr must be host:port or [IPv6]:port.",
    "RelayPubKey must be a canonical base64-encoded 32-byte public key.",
    "FromBase64String($PublicKey)",
    "Start-Process -FilePath \"powershell.exe\" -ArgumentList $argList -Verb RunAs -Wait",
)


def validate(path: Path) -> None:
    text = path.read_text(encoding="utf-8")
    missing = [marker for marker in REQUIRED_MARKERS if marker not in text]
    if missing:
        raise AssertionError(f"missing installer safety markers: {missing}")
    if text.find("Assert-RelayInputs -Address $RelayAddr -PublicKey $RelayPubKey") > text.find("$isAdmin ="):
        raise AssertionError("relay input validation must happen before elevation")
    if re.search(r"Invoke-WebRequest.*RelayAddr|Invoke-WebRequest.*RelayPubKey", text, re.IGNORECASE):
        raise AssertionError("relay inputs must not be used as download URLs")


if __name__ == "__main__":
    try:
        validate(Path("desktop/windows/install.ps1"))
    except (OSError, AssertionError) as exc:
        print(f"Windows installer contract failed: {exc}", file=sys.stderr)
        raise SystemExit(2)
    print("Windows installer contract passed")
