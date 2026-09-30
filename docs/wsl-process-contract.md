# Native WSL process contract

This document defines the process semantics selected for ContainerBin's future
native-Linux frontend inside WSL2. It pins behavior that is portable and
testable before that frontend is enabled. It does **not** enable ordinary WSL
commands, publish a Linux binary, or claim real WSL2 + Docker Desktop
qualification. The host gate in [the WSL boundary](wsl.md) remains closed.

The corresponding Windows behavior is documented separately in
[the Windows shell/process contract](shell-contract.md).

## Argv and paths

On native Linux, each argument received in `os.Args` is forwarded as the same
distinct string. ContainerBin does not perform the Windows-only PowerShell
repair that joins a registry-declared `path_equals` option ending in `=` with
the next argument. For example, the two arguments `-chdir=` and `project` stay
two arguments under WSL; only Windows can repair that shape to
`-chdir=project`.

The non-Windows path mapper is an exact argv pass-through and produces no
additional bind mounts. Linux absolute and relative paths, symlinks, case and
filesystem identity must be handled by the native WSL project/layout wiring,
not guessed through Windows path translation. That broader filesystem wiring
and its real Docker tests remain prerequisites for enabling the frontend.

## Environment names

Profiles remain allowlist-only: only names selected by `env_names` or prefixes
selected by `env_prefixes` are added to `docker run`. Matching follows the host
operating system:

- Windows matching is case-insensitive, preserving existing behavior.
- Native Linux/WSL matching is case-sensitive. `PATH`, `Path` and `path` are
  distinct names, and a profile declaring one does not silently select another.

ContainerBin passes the selected name to Docker without copying its value into
the command line. The Docker child inherits the process environment and Docker
resolves the selected value by exact name.

## Streams, TTY and exit status

The Docker CLI receives stdin, stdout and stderr directly. ContainerBin adds no
buffering, encoding conversion or line editing. The child also receives the
current process environment unchanged.

The unexposed native Engine API path has a separate strict decoder for Docker's
non-TTY raw-stream framing. It accepts complete stdout and stderr frames with
zeroed reserved header bytes, routes their payloads without frame-sized
allocation, and treats a clean EOF as valid only between frames. Stdin or
unknown stream identifiers, truncated frames, malformed reserved bytes, and
unsafe daemon-error payloads fail closed. This primitive does not enable the
frontend or change the existing Docker CLI path.

`docker run` always receives `-i`. It additionally receives `-t` only when both
stdin and stdout report character-device mode; either redirection, either stat
failure, or a non-character stream keeps the invocation non-TTY. Stderr does
not participate in that decision.

A normal child completion returns exit status 0. If the Docker child exits with
a non-zero status, ContainerBin returns that exact child status and no wrapper
error. A failure to start Docker is a ContainerBin infrastructure error; the
internal runner returns status 1 plus the start error, and the top-level command
maps infrastructure failures through ContainerBin's documented exit policy.

A signal-terminated Unix child is a separate, unresolved case:
`exec.ExitError.ExitCode()` returns `-1`, and the current pre-frontend path would
pass that value to `os.Exit`, surfacing as status 255 rather than a conventional
`128 + signal` status. This is not accepted as the final native-WSL contract.
The real WSL qualification slice must select and test an explicit mapping before
the frontend is enabled.

These stream, TTY and exit rules are covered by portable tests using a real
child process; they do not require Docker.

## Signals

The unexposed native Engine API path has a proof-bound container-signal
operation. It accepts one exact full container ID and one explicit numeric Linux
signal in the `1..64` domain, always supplies Docker's `signal` query parameter
and accepts only HTTP 204. It never relies on the endpoint's default `SIGKILL`.

This primitive does not choose which host signals to intercept, install signal
handlers or define cleanup ordering. The enabled frontend must make those
policies explicit and qualify them end to end before invoking the operation.

The tool-run path installs no signal handler and creates no new process group.
On native Linux/WSL, the `cb` process and its `docker` child therefore retain
the operating system's default process-group relationship. ContainerBin does
not synthesize, translate or explicitly forward signals.

That code-level statement is intentionally narrower than a runtime support
claim. Terminal-generated signal delivery through WSL, Docker Desktop, the
Docker CLI and the container process—and resulting cleanup behavior—must still
be exercised in the real WSL2 + Docker Desktop qualification suite before the
frontend can be enabled.
