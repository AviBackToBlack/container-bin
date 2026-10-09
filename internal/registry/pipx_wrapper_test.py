import importlib.util
import pathlib
import sys
import tempfile
import unittest
from unittest import mock


MODULE_PATH = pathlib.Path(__file__).with_name("pipx_wrapper.py")
SPEC = importlib.util.spec_from_file_location("pipx_wrapper", MODULE_PATH)
WRAPPER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(WRAPPER)


class Result:
    def __init__(self, returncode):
        self.returncode = returncode


class PipxWrapperTests(unittest.TestCase):
    def setUp(self):
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.tempdir.name).resolve()
        self.allowed_python = pathlib.Path(sys.executable).resolve(strict=True)

    def tearDown(self):
        self.tempdir.cleanup()

    def make_internal_link(self, name="demo"):
        target = self.root / "home" / "venvs" / name / "bin" / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text("fixture")
        link = self.root / "bin" / name
        link.parent.mkdir(parents=True, exist_ok=True)
        link.symlink_to(target)
        return link, target

    def test_failed_command_still_normalizes_links(self):
        link, target = self.make_internal_link()
        status = WRAPPER.run(
            ["list"], self.root, self.allowed_python, lambda *a, **k: Result(7)
        )
        self.assertEqual(status, 7)
        self.assertEqual(link.resolve(strict=True), target)
        self.assertFalse(pathlib.Path(link.readlink()).is_absolute())

    def test_interrupt_still_normalizes_links_and_returns_130(self):
        link, _ = self.make_internal_link()

        def interrupt(*args, **kwargs):
            raise KeyboardInterrupt

        status = WRAPPER.run([], self.root, self.allowed_python, interrupt)
        self.assertEqual(status, 130)
        self.assertFalse(pathlib.Path(link.readlink()).is_absolute())

    def test_signal_return_code_maps_to_shell_convention(self):
        status = WRAPPER.run(
            [], self.root, self.allowed_python, lambda *a, **k: Result(-2)
        )
        self.assertEqual(status, 130)

    def test_relative_escape_fails_closed(self):
        link = self.root / "bin" / "evil"
        link.parent.mkdir(parents=True)
        link.symlink_to("../../../etc/passwd")
        with self.assertRaisesRegex(RuntimeError, "unsupported symlink"):
            WRAPPER.run([], self.root, self.allowed_python, lambda *a, **k: Result(0))

    def test_known_python3_alias_becomes_regular_file(self):
        link = self.root / "home" / "venvs" / "demo" / "bin" / "python3"
        link.parent.mkdir(parents=True)
        link.symlink_to(self.allowed_python)
        status = WRAPPER.run([], self.root, self.allowed_python, lambda *a, **k: Result(0))
        self.assertEqual(status, 0)
        self.assertTrue(link.is_file())
        self.assertFalse(link.is_symlink())

    def test_launcher_interpreter_alias_chains_normalize_in_any_creation_order(self):
        aliases = tuple(dict.fromkeys(("python", "python3", self.allowed_python.name)))
        for reverse in (False, True):
            with self.subTest(reverse=reverse):
                directory = self.root / "launcher-cache" / "archive-v0" / str(reverse) / "bin"
                directory.mkdir(parents=True)
                ordered = tuple(reversed(aliases)) if reverse else aliases
                for alias in ordered:
                    link = directory / alias
                    link.symlink_to(self.allowed_python if alias == "python" else "python")
                # Pin visitation order: directory enumeration is not a contract.
                with mock.patch.object(
                    pathlib.Path, "rglob", return_value=iter(directory / alias for alias in ordered)
                ):
                    status = WRAPPER.run(
                        [], self.root, self.allowed_python, lambda *a, **k: Result(0)
                    )
                self.assertEqual(status, 0)
                for alias in aliases:
                    target = (directory / alias).resolve(strict=True)
                    target.relative_to(self.root)
                    self.assertFalse(target.is_symlink())
                    self.assertEqual(target.read_bytes(), self.allowed_python.read_bytes())
                    if (directory / alias).is_symlink():
                        self.assertFalse((directory / alias).readlink().is_absolute())

    def test_launcher_direct_interpreter_aliases_are_copied(self):
        for alias in dict.fromkeys(("python", "python3", self.allowed_python.name)):
            with self.subTest(alias=alias):
                link = self.root / "launcher-cache" / "archive-v0" / "direct" / "bin" / alias
                link.parent.mkdir(parents=True, exist_ok=True)
                link.symlink_to(self.allowed_python)
                self.assertEqual(
                    WRAPPER.run([], self.root, self.allowed_python, lambda *a, **k: Result(0)),
                    0,
                )
                self.assertFalse(link.is_symlink())
                self.assertEqual(link.read_bytes(), self.allowed_python.read_bytes())

    def test_launcher_alias_allowlist_does_not_accept_other_names_or_locations(self):
        for relative in (
            "launcher-cache/archive-v0/demo/bin/not-python",
            "launcher-cache/archive-v0/demo/nested/bin/python3",
            "launcher-cache/other/demo/bin/python3",
            "bin/python3",
        ):
            with self.subTest(relative=relative):
                link = self.root / relative
                link.parent.mkdir(parents=True, exist_ok=True)
                link.symlink_to(self.allowed_python)
                try:
                    with self.assertRaisesRegex(RuntimeError, "unsupported symlink"):
                        WRAPPER.run([], self.root, self.allowed_python, lambda *a, **k: Result(0))
                    self.assertTrue(link.is_symlink())
                finally:
                    link.unlink()

    def test_launcher_alias_does_not_accept_another_external_target(self):
        with tempfile.TemporaryDirectory() as external:
            target = pathlib.Path(external) / "python"
            target.write_text("not the image interpreter")
            link = self.root / "launcher-cache" / "archive-v0" / "demo" / "bin" / "python3"
            link.parent.mkdir(parents=True)
            link.symlink_to(target)
            with self.assertRaisesRegex(RuntimeError, "unsupported symlink"):
                WRAPPER.run([], self.root, self.allowed_python, lambda *a, **k: Result(0))
            self.assertTrue(link.is_symlink())

    def test_lock_symlink_is_rejected(self):
        lock_path = self.root / ".cb-pipx.lock"
        lock_path.symlink_to(self.root / "other")
        with self.assertRaises(OSError):
            WRAPPER.run([], self.root, self.allowed_python, lambda *a, **k: Result(0))


if __name__ == "__main__":
    unittest.main()
