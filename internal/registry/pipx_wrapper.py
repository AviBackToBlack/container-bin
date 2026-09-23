import fcntl
import os
import pathlib
import secrets
import shutil
import subprocess
import sys


def normalize_state(root, allowed_python):
    for link in root.rglob("*"):
        try:
            if not link.is_symlink():
                continue
            raw_target = os.readlink(link)
        except FileNotFoundError:
            continue
        except OSError as exc:
            raise RuntimeError(f"pipx produced unreadable symlink: {link}") from exc
        try:
            target = link.resolve(strict=True)
        except FileNotFoundError as exc:
            if not link.is_symlink():
                continue
            raise RuntimeError(
                f"pipx produced unsupported symlink: {link} -> {raw_target}"
            ) from exc
        except OSError as exc:
            raise RuntimeError(
                f"pipx produced unsupported symlink: {link} -> {raw_target}"
            ) from exc
        try:
            target.relative_to(root)
        except ValueError:
            parts = link.relative_to(root).parts
            interpreter_aliases = ("python", "python3", allowed_python.name)
            venv_python = (
                len(parts) == 5
                and parts[:2] == ("home", "venvs")
                and parts[-2] == "bin"
                and parts[-1] in interpreter_aliases
            )
            shared_python = (
                len(parts) == 4
                and parts[:2] == ("home", "shared")
                and parts[-2] == "bin"
                and parts[-1] in interpreter_aliases
            )
            launcher_python = (
                len(parts) == 5
                and parts[:2] == ("launcher-cache", "archive-v0")
                and parts[-2:] == ("bin", "python")
            )
            if target != allowed_python or not (
                venv_python or shared_python or launcher_python
            ):
                raise RuntimeError(
                    f"pipx produced unsupported symlink: {link} -> {raw_target}"
                )
            temporary = link.with_name(
                f".{link.name}.cb-{os.getpid()}-{secrets.token_hex(8)}"
            )
            try:
                shutil.copy2(target, temporary)
                os.replace(temporary, link)
            finally:
                if temporary.exists() or temporary.is_symlink():
                    temporary.unlink()
            continue
        relative = os.path.relpath(target, start=link.parent)
        temporary = link.with_name(
            f".{link.name}.cb-{os.getpid()}-{secrets.token_hex(8)}"
        )
        try:
            temporary.symlink_to(relative)
            os.replace(temporary, link)
        finally:
            if temporary.exists() or temporary.is_symlink():
                temporary.unlink()


def run(argv, root, allowed_python, runner=subprocess.run):
    root = root.resolve(strict=True)
    allowed_python = allowed_python.resolve(strict=True)
    lock_path = root / ".cb-pipx.lock"
    with lock_path.open("a+b") as state_lock:
        fcntl.flock(state_lock, fcntl.LOCK_EX)
        status = 1
        try:
            status = runner(
                ["/usr/local/bin/uvx", "--from", "pipx==1.17.4", "pipx", *argv],
                check=False,
            ).returncode
        except KeyboardInterrupt:
            status = 130
        finally:
            normalize_state(root, allowed_python)
    if status < 0:
        return 128 - status
    return status


if __name__ == "__main__":
    raise SystemExit(
        run(
            sys.argv[1:],
            pathlib.Path("/cb/pipx"),
            pathlib.Path("/usr/local/bin/python3.13"),
        )
    )
