import fcntl
import os
import pathlib
import stat
import sys


def open_lock(path, flags, mode):
    flags |= getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(path, flags, 0o600)
    try:
        if not stat.S_ISREG(os.fstat(descriptor).st_mode):
            raise RuntimeError(f"pipx state lock is not a regular file: {path}")
        return os.fdopen(descriptor, mode)
    except BaseException:
        os.close(descriptor)
        raise


def discover(directory, root, lock_path, output=None):
    directory = directory.resolve(strict=False)
    root = root.resolve(strict=True)
    if output is None:
        output = sys.stdout.buffer
    try:
        state_lock = open_lock(lock_path, os.O_RDONLY, "rb")
    except FileNotFoundError:
        # The wrapper creates the lock before its first state mutation. No lock
        # therefore means there is no legitimate installed state to expose;
        # returning empty also closes the first-install race without writing.
        return
    with state_lock:
        fcntl.flock(state_lock, fcntl.LOCK_SH)
        if not directory.is_dir():
            return
        for entry in directory.iterdir():
            try:
                if not entry.is_file() or not os.access(entry, os.X_OK):
                    continue
                resolved = entry.resolve(strict=True)
                resolved.relative_to(root)
            except ValueError as exc:
                raise RuntimeError(
                    f"pipx global binary {entry.name!r} resolves outside {root}: {resolved}"
                ) from exc
            except OSError as exc:
                raise RuntimeError(
                    f"cannot resolve pipx global binary {entry.name!r}"
                ) from exc
            output.write(os.fsencode(entry.name))
            output.write(b"\0")
            output.write(os.fsencode(resolved))
            output.write(b"\0")


def main(argv):
    try:
        if len(argv) == 4 and argv[0] == "discover":
            discover(*(pathlib.Path(value) for value in argv[1:]))
            return 0
    except (OSError, RuntimeError) as exc:
        print(exc, file=sys.stderr)
        return 1
    print("invalid pipx discovery invocation", file=sys.stderr)
    return 2


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
