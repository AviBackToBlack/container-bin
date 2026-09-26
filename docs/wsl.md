# WSL2 frontend boundary

ContainerBin's selected WSL model is a native Linux `cb` binary and native
Linux shims inside one WSL2 distribution, using Docker Desktop's supported WSL
integration. A Windows `cb.exe` launched through WSL interoperability is not the
WSL frontend, and standalone Linux remains a separate, demand-gated product.

The implemented foundation establishes the runtime boundary, fixed native-WSL
layout contract and an unexposed filesystem-preparation step. It does not
publish a Linux artifact or enable WSL execution yet. Until the remaining
frontend wiring, Docker and qualification slices land, non-bootstrap commands
fail closed on every host except native Windows.

## Runtime classification

- Native Windows is the currently supported frontend.
- A Windows process with `WSL_INTEROP` or `WSL_DISTRO_NAME` is classified as
  Windows-through-WSL interoperability and rejected. The diagnostic names the
  inherited marker so a stray variable in an otherwise native Windows process
  can be found and removed.
- A native Linux process is classified as WSL2 only when
  `/proc/sys/kernel/osrelease` contains both the Microsoft and WSL2 markers.
  `WSL_DISTRO_NAME` is then required so later state identity cannot silently
  collapse multiple distributions together.
- A Microsoft kernel without the WSL2 marker is rejected. Classic Microsoft
  kernels are classified as WSL1; `microsoft-standard` kernels are reported as
  ambiguous because early WSL2 releases used that form before the WSL2 suffix
  became consistent. Other Linux kernels are standalone Linux and rejected.
- `cb version`, `cb help` and `cb config` remain bootstrap-safe for diagnosis;
  they perform no Docker or registry mutation and return before host enforcement.

Environment variables alone never promote an ordinary Linux kernel to WSL2.
Custom kernels that remove the Microsoft WSL2 identity markers fail closed;
Docker Desktop also documents custom WSL kernels as unsupported.

## Native layout and state identity

The WSL frontend uses fixed, distribution-local locations. `XDG_*`, `PATH` and
project configuration cannot redirect the binary, registry, lockfile or state
root:

| Purpose | Location |
|---|---|
| managed binary | `~/.local/lib/container-bin/cb` |
| management shim | `~/.local/bin/cb` |
| tool shim directory | `~/.local/bin` |
| user registry | `~/.config/container-bin/container-bin.toml` |
| lockfile | `~/.config/container-bin/container-bin.lock` |
| private state | `~/.local/state/container-bin` |

The home directory must be a canonical absolute Linux path on the same
filesystem device as the distribution root. A lexical `/mnt` check rejects the
default Windows-drive layout early; filesystem preparation then rejects a
symlinked home, a
[custom DrvFs automount root](https://learn.microsoft.com/windows/wsl/wsl-config#automount-settings),
a bind-mounted Windows home, and any other separate filesystem rather than
guessing its trust semantics. This is deliberately narrower than accepting
every possible Linux `/home` mount: support for a separate native filesystem
needs its own filesystem-type and ownership qualification. The machine policy
location remains the separate
administrator-owned `/etc/container-bin/policy.toml` contract.

The filesystem checks are a point-in-time preflight, not a durable path handle.
The later wiring slice must revalidate managed paths at each mutation boundary
or use descriptor-relative, no-follow traversal so a path swap after preflight
cannot redirect a registry, lockfile, binary, or shim operation.

The unexposed `internal/wslfs` preparation step creates only missing fixed
layout directories. Config, state and managed-binary directories must be
private and current-user-owned; existing registry and lock files must be
regular non-symlink files with mode `0600`; and an existing managed binary must
be a regular non-symlink file with mode `0755`. The shim directory must be
current-user-owned, owner-accessible and not group- or world-writable. Existing
permissions and ownership are never repaired by guessing intent. The management
shim, when present, must be a current-user-owned symlink to the fixed managed
binary; unrelated files or links fail closed. Tool-shim enumeration remains a
later registry/install wiring concern.

Every ContainerBin-managed Docker object in WSL is scoped to one exact tuple:

- the case-sensitive `WSL_DISTRO_NAME` value;
- the canonical lowercase `/etc/machine-id` value; and
- the numeric Linux user ID.

ContainerBin derives an opaque, versioned namespace from that tuple. It does
not lowercase, Unicode-normalize or expose the raw identity: two spellings
produce separate namespaces instead of being guessed equivalent. Volume
creation must prefix and label every managed object with the namespace, and
state listing, garbage collection, backup and restore must filter on the exact
namespace. Reinstalling a distribution, changing users or selecting another
distribution therefore cannot silently adopt existing state.

## Required before WSL execution can be enabled

Later reviewable slices must still implement and qualify all of the following:

1. wire the implemented Linux ownership, permission and symlink preflight into
   the native installer/config lifecycle and extend it to registry-derived tool
   shims;
2. wiring the accepted distribution/machine/user namespace into shared and
   project volume creation and lifecycle commands;
3. native Linux path, symlink, case, stdin/TTY and signal semantics;
4. Docker Desktop WSL-integration detection without accepting a separate local
   Docker Engine by accident;
5. Windows-filesystem and WSL-filesystem project tests plus mixed-invocation
   rejection; and
6. real WSL2 + Docker Desktop end-to-end qualification before any support claim.

Docker's setup contract is documented in its
[WSL2 backend guide](https://docs.docker.com/desktop/features/wsl/): WSL2
integration must be enabled for the selected distribution, and Docker recommends
keeping bind-mounted project files in the Linux filesystem where practical.
