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
binary; unrelated files or links fail closed.

The unexposed `internal/wslshim` lifecycle extends that identity contract to
registry-derived tool names. It plans only sorted direct children of the fixed
shim directory, requires every existing tool shim to be a current-user-owned
symlink to the fixed managed binary, and reports missing shims separately.
Reconciliation runs only after `internal/wslfs` validates the same layout's
canonical home, intermediate components and distribution-root device boundary.
It then reopens every shim-directory component without following symlinks, pins
the validated directory, and publishes only missing symlinks with an atomic
no-clobber operation. Concurrent correct creation is accepted; a regular file,
foreign link, wrong target or path redirection fails closed and is never
replaced. Installer/config wiring and removal remain later work, and unrelated
directory entries are never adopted or enumerated as managed shims.
An interrupted publish can leave a current-user-owned `.cb-<random>.tmp`
symlink to the managed binary. ContainerBin does not delete such entries based
on a filename pattern alone because that would not prove provenance.

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

Its descendant classifier revalidates that project identity for each candidate
path, rejects lexical escape, symlinks and nested mount crossings, and returns
the exact case-sensitive project-relative path. A not-yet-created output is
accepted only through its nearest existing non-symlink directory ancestor on
the same exact mount. This classifier is not yet wired into argument mapping.

A WSL-filesystem project root must be on the same filesystem device as the
distribution root. A Windows-filesystem project root must be below a proven default
`/mnt/<lowercase-drive>` mount reported as 9p DrvFs or WSL's exact
`drvfs<uppercase-drive><privilege-bit>` virtiofs share tag. The drive root
itself, a lookalike `/mnt` directory, custom DrvFs
automount roots, mounts masking a Windows drive and separate unqualified native
filesystems fail closed. Same-device distribution bind mounts remain separate
projects under their exact canonical spelling. This deliberately supports the
standard WSL boundary first instead of guessing how a custom mount maps back to
Windows. This classification proves the storage backing the root dentry only;
consumers must resolve and classify each descendant path before mapping or
executing it because a nested mount may cross the project-root boundary.

## Docker Desktop integration proof

The implemented detector issues a direct, bounded Engine API request only over
`unix:///var/run/docker.sock`. It does not execute a `docker` binary, search
`PATH`, or read Docker CLI config/context state. `DOCKER_HOST`, `DOCKER_CONTEXT`,
`DOCKER_CONFIG`, Docker TLS/certificate variables and `DOCKER_API_VERSION` must
still be unset so later execution cannot silently target or downgrade a
different daemon. The socket must resolve to a root-owned Unix socket that is
not world-writable; the connected peer must also be root, and the socket device
and inode must remain unchanged across the identity request.

The bounded engine query then requires all of the independent signals Docker
Desktop exposes: Linux `OSType`, operating system `Docker Desktop`, engine name
`docker-desktop`, an explicit Microsoft WSL2 kernel and exactly one supported
`com.docker.desktop.address` label. A reachable in-distribution Docker Engine,
remote context, TCP endpoint, Windows-container engine or ambiguous response
fails closed. A successful request is not a durable authorization.

The unexposed control-operation primitive repeats that complete proof for each
request, then requires the fixed socket to retain the proven device/inode before
and after the operation and independently requires a root peer on the operation
connection. Method, canonical path, query, JSON body, accepted success statuses,
duration and response size are all bounded explicitly: operation requests have
a fixed 30-second ceiling, response headers are capped at 16 KiB and response
bodies at 1 MiB. It never reads ambient Docker endpoint configuration. A socket
replacement detected after a mutating request causes failure but cannot undo an
operation the proven peer already accepted. Likewise, a client-side timeout
does not prove the engine abandoned the request. Callers must keep any
engine-side grace period within the fixed ceiling and must not retry either
failure blindly. Streaming, attach and hijacked connections require a separate
process/IO contract and are deliberately not supported by this primitive. It is
not wired into tool execution yet; real WSL2 + Docker Desktop qualification
remains mandatory before support.

## Native WSL volume identity and control lifecycle

The implemented volume contract names every object
`cb-<namespace>-<group-length>-<group>-<logical-length>-<logical>`; project
volumes add a 12-hex project hash. Length delimiters keep hyphenated group and
logical names injective rather than allowing two owners to collide. Every
volume also carries `cb.wsl_namespace=<namespace>` in addition to the existing
`cb.managed`, `cb.kind`, `cb.owner` and project identity labels. Project hashing
uses a versioned WSL domain and the exact canonical absolute Linux path: it is
case-sensitive and performs no Unicode normalization. Namespace prefix/label
filters are discovery-only; adoption or mutation requires an exact constructed
name and complete label-set match.

The same package now binds inspect, create, namespace discovery and non-forced
remove operations to the proof-bound Docker Desktop control transport. It does
not invoke `docker` or accept an ambient context. Existing volumes must also use
the local driver and scope. Create validates the Engine response and then
re-inspects the exact volume, so Docker's idempotent create behavior cannot
silently adopt a same-name foreign object. Remove first proves the complete
identity, never requests force, and verifies that the name is absent afterward.
Discovery retains complete untrusted labels for later exact matching and fails
on partial-result warnings, duplicates or results outside both namespace
filters. Tool execution plus `cb state`/`cb gc` are not wired to these
primitives yet.

## Required before WSL execution can be enabled

Later reviewable slices must still implement and qualify all of the following:

1. integrate the prepared layout and registry-derived tool-shim reconciliation
   into the native installer/config lifecycle; the unexposed mutation primitive
   is race-safe and revalidates the fixed layout at its mutation boundary;
2. wire the proof-bound volume primitives into tool-time shared/project
   creation plus `cb state`, `cb gc`, backup and restore; each consumer must
   construct and match the complete distribution/machine/user identity;
3. wire the implemented project and descendant storage boundary into native
   Linux argument mapping, then complete stdin/TTY and signal semantics;
4. wire the implemented bounded Docker Desktop control-operation primitive into
   volume/container lifecycle calls, and add a separately reviewed streaming
   execution path without accepting ambient endpoint overrides;
5. Windows-filesystem and WSL-filesystem project tests plus mixed-invocation
   rejection; and
6. real WSL2 + Docker Desktop end-to-end qualification before any support claim.

Docker's setup contract is documented in its
[WSL2 backend guide](https://docs.docker.com/desktop/features/wsl/): WSL2
integration must be enabled for the selected distribution, and Docker recommends
keeping bind-mounted project files in the Linux filesystem where practical.
