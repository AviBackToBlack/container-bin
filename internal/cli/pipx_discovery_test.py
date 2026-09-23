import fcntl
import importlib.util
import io
import os
import pathlib
import subprocess
import sys
import tempfile
import time
import unittest


MODULE_PATH = pathlib.Path(__file__).with_name("pipx_discovery.py")
SPEC = importlib.util.spec_from_file_location("pipx_discovery", MODULE_PATH)
DISCOVERY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(DISCOVERY)


class PipxDiscoveryTests(unittest.TestCase):
    def setUp(self):
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.tempdir.name).resolve()
        self.bin_dir = self.root / "bin"
        self.bin_dir.mkdir()
        self.lock_path = self.root / ".cb-pipx.lock"
        self.lock_path.touch(mode=0o600)

    def tearDown(self):
        self.tempdir.cleanup()

    def make_executable(self, path):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("#!/bin/sh\n")
        path.chmod(0o755)
        return path

    def test_internal_target_is_emitted_as_name_and_resolved_path(self):
        target = self.make_executable(self.root / "home" / "demo")
        link = self.bin_dir / "demo"
        link.symlink_to(target)
        output = io.BytesIO()
        DISCOVERY.discover(self.bin_dir, self.root, self.lock_path, output)
        self.assertEqual(
            output.getvalue(),
            b"demo\0" + os.fsencode(target.resolve(strict=True)) + b"\0",
        )

    def test_external_target_fails_closed(self):
        external = self.make_executable(self.root.parent / "outside-pipx")
        (self.bin_dir / "evil").symlink_to(external)
        with self.assertRaisesRegex(RuntimeError, "resolves outside"):
            DISCOVERY.discover(self.bin_dir, self.root, self.lock_path, io.BytesIO())

    def test_discovery_waits_for_wrapper_exclusive_lock(self):
        self.make_executable(self.bin_dir / "demo")
        with self.lock_path.open("rb") as state_lock:
            fcntl.flock(state_lock, fcntl.LOCK_EX)
            process = subprocess.Popen(
                [
                    sys.executable,
                    str(MODULE_PATH),
                    "discover",
                    str(self.bin_dir),
                    str(self.root),
                    str(self.lock_path),
                ],
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            try:
                time.sleep(0.1)
                self.assertIsNone(process.poll(), "discovery did not wait for the lock")
            finally:
                fcntl.flock(state_lock, fcntl.LOCK_UN)
                stdout, stderr = process.communicate(timeout=5)
        self.assertEqual(process.returncode, 0, stderr.decode())
        self.assertEqual(stdout, b"demo\0" + os.fsencode(self.bin_dir / "demo") + b"\0")

    def test_lock_symlink_is_rejected(self):
        self.lock_path.unlink()
        self.lock_path.symlink_to(self.root / "other")
        with self.assertRaises(OSError):
            DISCOVERY.discover(self.bin_dir, self.root, self.lock_path, io.BytesIO())

    def test_missing_lock_reports_no_apps_without_scanning(self):
        self.lock_path.unlink()
        external = self.make_executable(self.root.parent / "outside-pipx")
        (self.bin_dir / "evil").symlink_to(external)
        output = io.BytesIO()
        DISCOVERY.discover(self.bin_dir, self.root, self.lock_path, output)
        self.assertEqual(output.getvalue(), b"")


if __name__ == "__main__":
    unittest.main()
