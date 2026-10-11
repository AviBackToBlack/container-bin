import importlib.util
import io
import os
import pathlib
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location(
    "python_bootstrap", pathlib.Path(__file__).with_name("bootstrap.py")
)
BOOTSTRAP = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BOOTSTRAP)


class PythonBootstrapTests(unittest.TestCase):
    def setUp(self):
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.tempdir.name).resolve()
        self.image_python = pathlib.Path(sys.executable).resolve(strict=True)

    def tearDown(self):
        self.tempdir.cleanup()

    def test_new_environment_copies_interpreters_and_preserves_packages(self):
        BOOTSTRAP.prepare(self.root, self.image_python)
        interpreter = self.root / "bin" / "python"
        self.assertFalse(interpreter.is_symlink())
        result = subprocess.run(
            [str(interpreter), "-c", "import sys;print(sys.prefix)"],
            capture_output=True, text=True, check=True,
        )
        self.assertEqual(result.stdout.strip(), str(self.root))
        marker = self.root / "package-preserved"
        marker.write_text("installed package fixture")
        BOOTSTRAP.prepare(self.root, self.image_python)
        self.assertEqual(marker.read_text(), "installed package fixture")
        archive = self.root / "state.tar"
        with tarfile.open(archive, "w", dereference=False) as writer:
            writer.add(self.root / "bin", arcname="./bin")
        with tarfile.open(archive) as reader:
            for member in reader:
                if member.issym() or member.islnk():
                    self.assertFalse(pathlib.PurePosixPath(member.linkname).is_absolute())

    def test_legacy_alias_chain_is_copied_without_touching_other_files(self):
        binary_dir = self.root / "bin"
        binary_dir.mkdir()
        (binary_dir / "python").symlink_to(self.image_python)
        aliases = ("python3", f"python{sys.version_info.major}.{sys.version_info.minor}")
        for name in aliases:
            (binary_dir / name).symlink_to("python")
        unrelated = self.root / "unrelated"
        unrelated.symlink_to("/outside")
        marker = self.root / "installed-package"
        marker.write_text("unchanged")
        BOOTSTRAP.prepare(self.root, self.image_python)
        for name in ("python", *aliases):
            alias = binary_dir / name
            self.assertFalse(alias.is_symlink())
            self.assertEqual(alias.read_bytes(), self.image_python.read_bytes())
        self.assertEqual(marker.read_text(), "unchanged")
        self.assertEqual(os.readlink(unrelated), "/outside")
        self.assertEqual(list(binary_dir.glob(".cb-python-*")), [])

    def test_unknown_interpreter_target_fails_before_repairing_any_alias(self):
        binary_dir = self.root / "bin"
        binary_dir.mkdir()
        (binary_dir / "python").symlink_to(self.image_python)
        other = self.root / "other"
        other.write_text("not the image interpreter")
        (binary_dir / "python3").symlink_to(other)
        with self.assertRaisesRegex(RuntimeError, "unsupported Python interpreter link"):
            BOOTSTRAP.prepare(self.root, self.image_python)
        self.assertTrue((binary_dir / "python").is_symlink())
        self.assertEqual(other.read_text(), "not the image interpreter")

    def test_bin_symlink_is_rejected_without_writing_its_target(self):
        outside = self.root / "outside"
        outside.mkdir()
        (self.root / "bin").symlink_to(outside, target_is_directory=True)
        with self.assertRaisesRegex(RuntimeError, "bin must be a real directory"):
            BOOTSTRAP.prepare(self.root, self.image_python)
        self.assertEqual(list(outside.iterdir()), [])

    def test_interrupted_alias_copy_resumes_and_removes_temporary_files(self):
        binary_dir = self.root / "bin"
        binary_dir.mkdir()
        (binary_dir / "python").symlink_to(self.image_python)
        (binary_dir / "python3").symlink_to("python")
        copy = BOOTSTRAP.shutil.copy2
        count = 0

        def interrupt_second_copy(*args):
            nonlocal count
            count += 1
            if count == 2:
                raise KeyboardInterrupt
            return copy(*args)

        with mock.patch.object(BOOTSTRAP.shutil, "copy2", side_effect=interrupt_second_copy):
            with self.assertRaises(KeyboardInterrupt):
                BOOTSTRAP.prepare(self.root, self.image_python)
        self.assertFalse((binary_dir / "python").is_symlink())
        self.assertTrue((binary_dir / "python3").is_symlink())
        self.assertEqual(list(binary_dir.glob(".cb-python-*")), [])
        BOOTSTRAP.prepare(self.root, self.image_python)
        self.assertFalse((binary_dir / "python3").is_symlink())

    def test_modified_internal_interpreter_copy_is_not_trusted(self):
        binary_dir = self.root / "bin"
        binary_dir.mkdir()
        (binary_dir / "python").write_text("not the exact image interpreter")
        (binary_dir / "python3").symlink_to("python")
        with self.assertRaisesRegex(RuntimeError, "unsupported Python interpreter link"):
            BOOTSTRAP.prepare(self.root, self.image_python)

    def test_lock_symlink_is_rejected_without_writing_its_target(self):
        outside = self.root / "outside"
        outside.write_text("unchanged")
        (self.root / ".cb-python.lock").symlink_to(outside)
        with self.assertRaises(OSError):
            BOOTSTRAP.prepare(self.root, self.image_python)
        self.assertEqual(outside.read_text(), "unchanged")

    def test_root_symlink_is_rejected(self):
        root_link = self.root / "link"
        root_link.symlink_to(self.root, target_is_directory=True)
        with self.assertRaisesRegex(RuntimeError, "root must be a real directory"):
            BOOTSTRAP.prepare(root_link, self.image_python)

    def test_incomplete_state_never_runs_venv_creation_through_existing_paths(self):
        for name in ("alias", "configuration"):
            with self.subTest(name=name):
                root = self.root / name
                root.mkdir()
                outside = self.root / f"outside-{name}"
                outside.write_text("unchanged")
                if name == "alias":
                    (root / "bin").mkdir()
                    (root / "bin" / "python3").symlink_to(outside)
                else:
                    (root / "pyvenv.cfg").symlink_to(outside)
                with mock.patch.object(BOOTSTRAP.subprocess, "run") as create:
                    with self.assertRaisesRegex(RuntimeError, "contents but no interpreter"):
                        BOOTSTRAP.prepare(root, self.image_python)
                    create.assert_not_called()
                self.assertEqual(outside.read_text(), "unchanged")

    def test_unknown_dangling_interpreter_is_rejected(self):
        binary_dir = self.root / "bin"
        binary_dir.mkdir()
        (binary_dir / "python").symlink_to("/missing-interpreter")
        with self.assertRaises(OSError):
            BOOTSTRAP.prepare(self.root, self.image_python)

    def test_exec_preserves_python_and_pip_argv(self):
        for argv, expected in (
            (["-c", "print('quoted')", "a b", ""], ["-c", "print('quoted')", "a b", ""]),
            (["__CB_PIP__", "install", "a b"], ["-m", "pip", "install", "a b"]),
            ([], []),
        ):
            with self.subTest(argv=argv), mock.patch.object(BOOTSTRAP, "prepare"), mock.patch.object(BOOTSTRAP.os, "execv") as execute:
                BOOTSTRAP.main(argv)
                execute.assert_called_once_with("/venv/bin/python", ["/venv/bin/python", *expected])

    def test_prepare_failure_never_execs_user_command(self):
        with mock.patch.object(BOOTSTRAP, "prepare", side_effect=RuntimeError("unsupported link")), mock.patch.object(BOOTSTRAP.os, "execv") as execute, mock.patch("sys.stderr", new_callable=io.StringIO):
            self.assertEqual(BOOTSTRAP.main(["-c", "raise Exception()"]), 120)
            execute.assert_not_called()


if __name__ == "__main__":
    unittest.main()
