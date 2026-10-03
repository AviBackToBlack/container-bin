# Native WSL process contract

This document defines the process semantics implemented by ContainerBin's
native-Linux frontend inside WSL2. The runtime is composed and covered in this
tree, but production dispatch remains activation-gated until retained-container
orphan reconciliation and real WSL2 + Docker Desktop qualification land.
The corresponding Windows behavior is documented separately in
[the Windows shell/process contract](shell-contract.md).

## Argv, working directory and paths

Each argument received in `os.Args` remains one distinct string. ContainerBin
does not perform the Windows-only PowerShell repair that joins a declared
`path_equals` option ending in `=` with the next argument.

Project-mode tools apply the registry's nearest/outermost marker policy (or an
exact trusted project root), prove the current directory and project storage
boundary, and bind that root at `/workspace/project`. Linux absolute paths,
explicit relative paths and registry-forced path positions are mapped only
after the exact descendant is re-proven under that root. External paths,
symlinks, nested mounts, Windows spelling and guessed `/mnt/<drive>` equivalence
fail closed. Ambiguous bare arguments and package patterns remain tool syntax.

Isolated tools use `/root`, create no project bind and preserve argv literally.
They can use only shared state volumes; registry validation already forbids
project state and project-marker policy in isolated mode.

`host_mounts` is rejected by the WSL runtime. Its registry grammar deliberately
describes Windows drive paths, and interpreting those values as native Linux
paths would violate the no-equivalence rule.

## Environment

Profiles remain allowlist-only. Names selected by `env_names` or prefixes
selected by `env_prefixes` are matched case-sensitively against the native Linux
environment. Selected `NAME=value` assignments are copied into the Engine create
request; the Docker CLI and its ambient environment are not involved. Literal
`env_set` assignments override a selected host value of the same exact name,
matching the existing provider precedence.

Python additionally fixes `VIRTUAL_ENV=/venv` and the provider-owned `PATH`.
Those provider values override a conflicting host or profile value because the
bootstrap depends on the exact `/venv/bin/python` identity.

## Container and state lifecycle

The runtime resolves the image against the fixed WSL lockfile and machine
policy, plans the complete mount set, validates the create request, and only
then ensures each exact namespaced volume. Stateful profiles retain their
project/shared declarations. The Python provider uses a case-sensitive
project-scoped venv when a marker is found, a namespace-shared compatibility
venv otherwise, and one namespace-shared pip cache.

The owned container is created stopped and retained until cleanup. This avoids
daemon auto-remove racing an ultra-short-lived process before its status can be
collected. ContainerBin attaches before start, starts exactly once, begins an
Engine wait, captures the `0..255` status, drains output, and removes the stopped
container through the proof-bound non-force cleanup path. Every control or
stream connection repeats the Docker Desktop WSL socket and peer proof.

## Streams and TTY

ContainerBin always attaches stdin, stdout and stderr. Non-TTY stdin is copied
byte-for-byte and half-closed at EOF while output remains open. Docker's strict
raw-stream framing is decoded into the caller's separate stdout and stderr; a
truncated or malformed frame is an infrastructure failure.

If stdin copying has already completed with an error when the tool exits, that
error wins over the tool status so truncated piped input is not reported as
success. ContainerBin does not wait indefinitely for a terminal or pipe reader
that remains blocked after the tool and output stream have both completed.

TTY mode is selected only when both stdin and stdout are character devices.
The native terminal enters raw mode, the initial size is applied after start,
and each `SIGWINCH` triggers a fresh positive row/column resize. Docker's TTY
stream is unframed and is written to stdout; terminal state is restored on every
return path.

The runtime waits up to five seconds for the attach stream to drain after the
Engine reports exit. Failure to drain is an infrastructure error rather than a
silent loss of trailing output.

## Signals, failures and exit status

The host intercepts HUP, INT, QUIT, USR1, USR2, TERM, CONT and TSTP and forwards
their exact numeric Linux value to the owned container. `SIGWINCH` is consumed
as a resize event and is not forwarded. KILL and STOP cannot be intercepted.

The tool's Engine status passes through unchanged, including conventional
signal-derived statuses such as 130 when the container process returns them.
ContainerBin does not invent a second mapping from host signals.

If attach, output, resize, signal forwarding, wait or the caller context fails
while the container is running, ContainerBin cancels the live operations, sends
SIGKILL through the proof-bound signal endpoint, waits for the retained
container to stop, and then performs proof-bound cleanup. Any cleanup failure is
joined to the original diagnostic. The top level maps infrastructure failures
to ContainerBin's documented exit code 120.

A failed start response is treated as transport-ambiguous: the Engine may have
accepted the request before the connection failed. Cleanup therefore attempts
the same bounded stop/wait sequence and then a proof-bound non-force removal;
the removal can safely clean an already-stopped container but refuses one that
is still running.

These semantics have in-process integration coverage. The release gate still
requires real WSL2 + Docker Desktop exercises for piped stdin, split output,
interactive resize, Ctrl-C/termination, fast exit, cleanup and exact exit-code
propagation on both distribution and default Windows-drive projects.
