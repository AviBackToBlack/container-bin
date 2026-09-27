# WSL2 frontend boundary

ContainerBin's selected WSL model is a native Linux `cb` binary and native
Linux shims inside one WSL2 distribution, using Docker Desktop's supported WSL
integration. A Windows `cb.exe` launched through WSL interoperability is not the
WSL frontend, and standalone Linux remains a separate, demand-gated product.

The implemented foundation establishes the runtime boundary, fixed native-WSL
layout contract and an explicit filesystem-preparation command. It does not
publish a Linux artifact or enable WSL execution yet. Until the remaining
frontend wiring, Docker and qualification slices land, ordinary non-bootstrap
commands fail closed on every host except native Windows.

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
- `cb wsl prepare --check` and `cb wsl prepare --apply` are the only native-WSL
  management exception. They classify the live host themselves and touch only
  the fixed layout described below; all normal tool and management execution
  remains gated.

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

`cb wsl prepare --check` validates this contract without changing the
filesystem and reports every missing required directory. Explicit
`cb wsl prepare --apply` creates only those missing fixed layout directories,
then revalidates the complete layout. Neither mode installs a binary, creates
management or tool shims, writes config, contacts Docker, or enables the WSL
frontend. The account home and numeric UID come from the native Linux account
database rather than redirectable environment variables.

Config, state and managed-binary directories must be
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

## Project storage boundary

The unexposed `internal/wslproject` classifier accepts an already-selected
project root only when it is a canonical, existing Linux directory and no path
component resolves through a symlink. It never accepts Windows drive/UNC
spelling and never translates a Windows path into a WSL path. Exact Linux case
and spelling remain the project identity.

A WSL-filesystem project must be on the same filesystem device as the
distribution root. A Windows-filesystem project must be below a proven default
`/mnt/<lowercase-drive>` mount reported as 9p DrvFs or WSL's `drvfs*` virtiofs
share. The drive root itself, a lookalike `/mnt` directory, custom DrvFs
automount roots, mounts masking a Windows drive and separate unqualified native
filesystems fail closed. Same-device distribution bind mounts remain separate
projects under their exact canonical spelling. This deliberately supports the
standard WSL boundary first instead of guessing how a custom mount maps back to
Windows.

## Native WSL volume identity

The implemented pure volume contract names every object
`cb-<namespace>-<group-length>-<group>-<logical-length>-<logical>`; project
volumes add a 12-hex project hash. Length delimiters keep hyphenated group and
logical names injective rather than allowing two owners to collide. Every
volume also carries `cb.wsl_namespace=<namespace>` in addition to the existing
`cb.managed`, `cb.kind`, `cb.owner` and project identity labels. Project hashing
uses a versioned WSL domain and the exact canonical absolute Linux path: it is
case-sensitive and performs no Unicode normalization. Namespace prefix/label
filters are discovery-only; adoption or mutation requires an exact constructed
name and complete label-set match. Docker creation and lifecycle commands are
not wired to this contract yet.

## Required before WSL execution can be enabled

Later reviewable slices must still implement and qualify all of the following:

1. integrate the prepared layout into the native installer/config lifecycle,
   revalidate each mutation boundary and extend validation to registry-derived
   tool shims;
2. wire the implemented distribution/machine/user volume identity contract
   into shared/project creation and every lifecycle command;
3. wire the implemented project storage boundary, then complete native Linux
   argument mapping, stdin/TTY and signal semantics;
4. Docker Desktop WSL-integration detection without accepting a separate local
   Docker Engine by accident;
5. Windows-filesystem and WSL-filesystem project tests plus mixed-invocation
   rejection; and
6. real WSL2 + Docker Desktop end-to-end qualification before any support claim.

Docker's setup contract is documented in its
[WSL2 backend guide](https://docs.docker.com/desktop/features/wsl/): WSL2
integration must be enabled for the selected distribution, and Docker recommends
keeping bind-mounted project files in the Linux filesystem where practical.
