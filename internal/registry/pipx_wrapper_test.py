import importlib.util
import pathlib
import sys
import tempfile
import unittest


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


if __name__ == "__main__":
    unittest.main()
