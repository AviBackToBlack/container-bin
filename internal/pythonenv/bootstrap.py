import fcntl
import filecmp
import os
import pathlib
import shutil
import stat
import subprocess
import sys
import tempfile


def prepare(root, image_python):
    # Do not create/copy through a substituted venv directory or lock symlink.
    if root.is_symlink() or not root.is_dir():
        raise RuntimeError("Python state root must be a real directory")
    flags = os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW | os.O_CLOEXEC
    descriptor = os.open(root / ".cb-python.lock", flags, 0o600)
    with os.fdopen(descriptor, "a+b") as state_lock:
        if not stat.S_ISREG(os.fstat(state_lock.fileno()).st_mode):
            raise RuntimeError("Python state lock must be a regular file")
        fcntl.flock(state_lock, fcntl.LOCK_EX)
        binary_dir = root / "bin"
        if binary_dir.is_symlink() or (binary_dir.exists() and not binary_dir.is_dir()):
            raise RuntimeError("Python state bin must be a real directory")
        interpreter = binary_dir / "python"
        if not os.path.lexists(interpreter):
            # venv creation writes several paths, not just bin/python. Never
            # follow pre-existing aliases/configuration in incomplete state.
            if any(entry.name != ".cb-python.lock" for entry in root.iterdir()):
                raise RuntimeError(
                    "Python state has contents but no interpreter; inspect it before recreating the venv"
                )
            subprocess.run(
                [str(image_python), "-m", "venv", "--copies", str(root)],
                check=True,
            )
        image_python = image_python.resolve(strict=True)
        # Legacy venvs used image-interpreter symlinks. Repair only these known
        # aliases, and only if they resolve to this exact image interpreter.
        aliases = ("python", "python3", f"python{sys.version_info.major}.{sys.version_info.minor}")
        links = []
        for name in aliases:
            alias = binary_dir / name
            if not alias.is_symlink():
                continue
            target = alias.resolve(strict=True)
            # An interrupted migration can leave links to the first repaired
            # alias. Trust only a known internal alias with byte-exact content.
            repaired_copy = (
                target.parent == binary_dir
                and target.name in aliases
                and filecmp.cmp(target, image_python, shallow=False)
            )
            if target != image_python and not repaired_copy:
                raise RuntimeError(f"unsupported Python interpreter link: {alias}")
            links.append(alias)
        # Validate the whole chain before replacing its first link: normal
        # legacy aliases can resolve through bin/python to the image binary.
        for alias in links:
            descriptor, temporary = tempfile.mkstemp(prefix=".cb-python-", dir=binary_dir)
            os.close(descriptor)
            try:
                shutil.copy2(image_python, temporary)
                os.replace(temporary, alias)
            finally:
                if os.path.lexists(temporary):
                    os.unlink(temporary)
        if not interpreter.is_file() or not os.access(interpreter, os.X_OK):
            raise RuntimeError("Python state interpreter must be executable")


def main(argv):
    root = pathlib.Path("/venv")
    try:
        prepare(root, pathlib.Path(sys.executable))
    except subprocess.CalledProcessError as exc:
        return 128 - exc.returncode if exc.returncode < 0 else exc.returncode
    except (OSError, RuntimeError) as exc:
        print(f"container-bin: Python state: {exc}", file=sys.stderr)
        return 120
    interpreter = str(root / "bin" / "python")
    if argv and argv[0] == "__CB_PIP__":
        argv = ["-m", "pip", *argv[1:]]
    os.execv(interpreter, [interpreter, *argv])


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
