# WSL2 frontend boundary

ContainerBin's selected WSL model is a native Linux `cb` binary and native
Linux shims inside one WSL2 distribution, using Docker Desktop's supported WSL
integration. A Windows `cb.exe` launched through WSL interoperability is not the
WSL frontend, and standalone Linux remains a separate, demand-gated product.

The implemented foundation establishes the runtime boundary and the fixed
native-WSL layout contract. It does not publish a Linux artifact or enable WSL
execution yet. Until the remaining filesystem, Docker and qualification slices
land, non-bootstrap commands fail closed on every host except native Windows.

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

The home directory must be a canonical absolute Linux path in the distribution
filesystem. A home under `/mnt` is rejected rather than placing trust or state
files on a Windows filesystem. The machine policy location remains the separate
administrator-owned `/etc/container-bin/policy.toml` contract.

Later filesystem wiring must create config and state directories as private,
current-user-owned directories; create registry and lock files with mode `0600`;
install the managed binary with mode `0755`; and reject an existing shim
directory that is group- or world-writable. It must never repair permissions on
an unrelated shared directory by guessing ownership intent. Tool shims are
native Linux symlinks to the managed binary, and collisions with unrelated
files or links fail closed.

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

1. Linux ownership, permission and symlink enforcement for the accepted native
   layout;
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
