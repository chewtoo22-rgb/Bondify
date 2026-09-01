#!/usr/bin/env python3
import io
import pathlib
import tarfile
import tempfile
import unittest
from unittest import mock

import release_archive_contract as contract
from release_archive_contract import validate_archive


def build_archive(path: pathlib.Path, members):
    with tarfile.open(path, "w:gz", format=tarfile.GNU_FORMAT) as archive:
        for name, kind, mode, payload in members:
            info = tarfile.TarInfo(name)
            info.uid = 0
            info.gid = 0
            info.mtime = 0
            info.mode = mode
            if kind == "dir":
                info.type = tarfile.DIRTYPE
                archive.addfile(info)
            elif kind == "symlink":
                info.type = tarfile.SYMTYPE
                info.linkname = payload.decode()
                archive.addfile(info)
            else:
                info.size = len(payload)
                archive.addfile(info, io.BytesIO(payload))


class ReleaseArchiveContractTest(unittest.TestCase):
    def run_case(self, name, members, should_pass):
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / name
            build_archive(path, members)
            if should_pass:
                validate_archive(path)
            else:
                with self.assertRaises(ValueError):
                    validate_archive(path)

    def test_linux_desktop_valid(self):
        root = "bondify-linux-amd64"
        self.run_case(f"{root}.tar.gz", [(root, "dir", 0o755, b""), (f"{root}/bondify", "file", 0o755, b"bin")], True)

    def test_windows_valid(self):
        root = "bondify-windows-amd64"
        self.run_case(f"{root}.tar.gz", [(root, "dir", 0o755, b""), (f"{root}/bondify.exe", "file", 0o755, b"exe"), (f"{root}/install.ps1", "file", 0o644, b"ps")], True)

    def test_relay_valid(self):
        root = "bondify-relay-linux-arm64"
        self.run_case(f"{root}.tar.gz", [(root, "dir", 0o755, b""), (f"{root}/bondify-relay", "file", 0o755, b"relay"), (f"{root}/install.sh", "file", 0o755, b"sh")], True)

    def test_extra_member_rejected(self):
        root = "bondify-linux-amd64"
        self.run_case(f"{root}.tar.gz", [(root, "dir", 0o755, b""), (f"{root}/bondify", "file", 0o755, b"bin"), (f"{root}/surprise", "file", 0o644, b"x")], False)

    def test_symlink_payload_rejected(self):
        root = "bondify-linux-amd64"
        self.run_case(f"{root}.tar.gz", [(root, "dir", 0o755, b""), (f"{root}/bondify", "symlink", 0o755, b"/tmp/evil")], False)

    def test_wrong_mode_rejected(self):
        root = "bondify-relay-linux-amd64"
        self.run_case(f"{root}.tar.gz", [(root, "dir", 0o755, b""), (f"{root}/bondify-relay", "file", 0o644, b"relay"), (f"{root}/install.sh", "file", 0o755, b"sh")], False)

    def test_wrong_mtime_rejected(self):
        root = "bondify-linux-amd64"
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / f"{root}.tar.gz"
            with tarfile.open(path, "w:gz", format=tarfile.GNU_FORMAT) as archive:
                directory = tarfile.TarInfo(root)
                directory.type = tarfile.DIRTYPE
                directory.mode = 0o755
                directory.mtime = 1
                archive.addfile(directory)
                binary = tarfile.TarInfo(f"{root}/bondify")
                binary.mode = 0o755
                binary.mtime = 0
                binary.size = 1
                archive.addfile(binary, io.BytesIO(b"x"))
            with self.assertRaises(ValueError):
                validate_archive(path)

    def test_unsupported_name_rejected(self):
        self.run_case("bondify-linux-riscv64.tar.gz", [("bondify-linux-riscv64", "dir", 0o755, b"")], False)

    def test_compressed_archive_size_bound_rejected(self):
        root = "bondify-linux-amd64"
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / f"{root}.tar.gz"
            build_archive(path, [(root, "dir", 0o755, b""), (f"{root}/bondify", "file", 0o755, b"bin")])
            with mock.patch.object(contract, "MAX_ARCHIVE_BYTES", path.stat().st_size - 1):
                with self.assertRaises(ValueError):
                    validate_archive(path)

    def test_member_size_bound_rejected(self):
        root = "bondify-linux-amd64"
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / f"{root}.tar.gz"
            build_archive(path, [(root, "dir", 0o755, b""), (f"{root}/bondify", "file", 0o755, b"bin")])
            with mock.patch.object(contract, "MAX_MEMBER_BYTES", 2):
                with self.assertRaises(ValueError):
                    validate_archive(path)

    def test_total_payload_size_bound_rejected(self):
        root = "bondify-windows-amd64"
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / f"{root}.tar.gz"
            build_archive(
                path,
                [
                    (root, "dir", 0o755, b""),
                    (f"{root}/bondify.exe", "file", 0o755, b"exe"),
                    (f"{root}/install.ps1", "file", 0o644, b"ps"),
                ],
            )
            with mock.patch.object(contract, "MAX_TOTAL_PAYLOAD_BYTES", 4):
                with self.assertRaises(ValueError):
                    validate_archive(path)


if __name__ == "__main__":
    unittest.main()
