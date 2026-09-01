#!/usr/bin/env python3
import argparse
import pathlib
import re
import tarfile

ARCHIVE_RE = re.compile(r"^(bondify(?:-relay)?)-(linux|windows)-(amd64|arm64)\.tar\.gz$")


def expected_members(archive_name: str):
    match = ARCHIVE_RE.fullmatch(archive_name)
    if not match:
        raise ValueError(f"unsupported release archive name: {archive_name}")
    product, os_name, arch = match.groups()
    root = archive_name.removesuffix(".tar.gz")
    if product == "bondify-relay":
        if os_name != "linux":
            raise ValueError("relay archives are supported only on linux")
        files = {f"{root}/bondify-relay": 0o755, f"{root}/install.sh": 0o755}
    else:
        if os_name == "windows":
            files = {f"{root}/bondify.exe": 0o755, f"{root}/install.ps1": 0o644}
        else:
            files = {f"{root}/bondify": 0o755}
    return root, files


def validate_member_name(name: str) -> None:
    path = pathlib.PurePosixPath(name)
    if not name or name.startswith("/") or "\\" in name:
        raise ValueError(f"unsafe archive path: {name!r}")
    if any(part in ("", ".", "..") for part in path.parts):
        raise ValueError(f"unsafe archive path: {name!r}")


def validate_archive(path: pathlib.Path) -> None:
    if not path.is_file() or path.is_symlink():
        raise ValueError(f"archive must be a regular non-symlink file: {path}")
    root, expected_files = expected_members(path.name)
    expected_names = {root, *expected_files.keys()}

    with tarfile.open(path, mode="r:gz") as archive:
        members = archive.getmembers()
        names = [member.name.rstrip("/") for member in members]
        if len(names) != len(set(names)):
            raise ValueError("archive contains duplicate member names")
        if set(names) != expected_names:
            missing = sorted(expected_names - set(names))
            extra = sorted(set(names) - expected_names)
            raise ValueError(f"archive member mismatch: missing={missing} extra={extra}")

        for member in members:
            name = member.name.rstrip("/")
            validate_member_name(name)
            if member.uid != 0 or member.gid != 0:
                raise ValueError(f"non-root archive ownership metadata: {name}")
            if member.mtime != 0:
                raise ValueError(f"non-deterministic archive mtime: {name}={member.mtime}")
            if member.pax_headers:
                raise ValueError(f"unexpected pax headers: {name}")
            if name == root:
                if not member.isdir():
                    raise ValueError("archive root must be a directory")
                continue
            if not member.isfile():
                raise ValueError(f"release payload member must be a regular file: {name}")
            expected_mode = expected_files[name]
            actual_mode = member.mode & 0o777
            if actual_mode != expected_mode:
                raise ValueError(
                    f"unexpected mode for {name}: expected {expected_mode:04o}, got {actual_mode:04o}"
                )


def main() -> int:
    parser = argparse.ArgumentParser(description="Validate Bondify release archive structure")
    parser.add_argument("archives", nargs="+", type=pathlib.Path)
    args = parser.parse_args()
    try:
        for archive in args.archives:
            validate_archive(archive)
    except (OSError, tarfile.TarError, ValueError) as exc:
        parser.exit(1, f"release archive contract: FAIL: {exc}\n")
    print(f"release archive contract: PASS ({len(args.archives)} archive(s))")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
