#!/usr/bin/env python3
"""Static, fail-closed checks for Bondify's Android release configuration."""
from __future__ import annotations

import re
import sys
from pathlib import Path

REQUIRED = {
    'namespace': 'dev.bondify.app',
    'applicationId': 'dev.bondify.app',
    'compileSdk': 34,
    'minSdk': 24,
    'targetSdk': 34,
}


def validate(text: str) -> list[str]:
    errors: list[str] = []
    for key, expected in REQUIRED.items():
        if key in {'namespace', 'applicationId'}:
            pattern = rf'{key}\s*=\s*"([^"]+)"'
            match = re.search(pattern, text)
            if not match or match.group(1) != expected:
                errors.append(f'{key} must be {expected!r}')
        else:
            pattern = rf'{key}\s*=\s*(\d+)'
            match = re.search(pattern, text)
            if not match or int(match.group(1)) != expected:
                errors.append(f'{key} must be {expected}')

    if 'implementation(files("libs/bondifymobile.aar"))' not in text:
        errors.append('release config must consume bondifymobile.aar')
    if 'dependsOn(generateMobileAar)' not in text:
        errors.append('preBuild must depend on generateMobileAar')
    if 'ANDROID_NDK_HOME' not in text:
        errors.append('AAR generation must fail closed when ANDROID_NDK_HOME is absent')
    if 'isMinifyEnabled = false' in text:
        errors.append('release build must not explicitly disable minification')
    return errors


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print('usage: validate_android_release_contract.py <build.gradle.kts>', file=sys.stderr)
        return 2
    path = Path(argv[1])
    if not path.is_file() or path.is_symlink():
        print('build file must be a regular non-symlink file', file=sys.stderr)
        return 1
    errors = validate(path.read_text(encoding='utf-8'))
    if errors:
        for error in errors:
            print(f'ERROR: {error}', file=sys.stderr)
        return 1
    print('Android release contract: PASS')
    return 0


if __name__ == '__main__':
    raise SystemExit(main(sys.argv))
