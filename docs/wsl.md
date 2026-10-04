# WSL2 frontend boundary

ContainerBin's selected WSL model is a native Linux `cb` binary and native
Linux shims inside one WSL2 distribution, using Docker Desktop's supported WSL
integration. A Windows `cb.exe` launched through WSL interoperability is not the
WSL frontend, and standalone Linux remains a separate, demand-gated product.

The implementation establishes the runtime boundary, fixed native-WSL layout,
explicit install/config lifecycle and the complete managed-tool composition
through Docker Desktop's Engine socket. Managed-tool dispatch remains
fail-closed until the integration corpus and real WSL2 + Docker Desktop
qualification land. The v2-required native state inventory and cleanup
lifecycle is wired and available independently of that gate.

## Runtime classification

- Native Windows is the currently qualified release frontend. The native WSL2
  runtime is wired here but remains activation-gated for v2.0.0.
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
- `cb wsl prepare --check|--apply`, `cb wsl install --check|--apply`,
  `cb wsl cleanup --check|--apply`, `cb state` and `cb gc` are the native-WSL
  management surface.
  Managed tool shims are installed, but their runtime dispatch remains gated;
  other `cb` management commands remain explicitly unavailable rather than
  falling through to Windows-oriented Docker CLI, path or state behavior.

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
The installer revalidates the layout under the mutation lock, shim writes use
descriptor-relative, no-follow traversal, and ordinary tool execution repeats
the fixed-layout, registry, shim and Docker identity checks before each run.

`cb wsl prepare --check` validates this contract without changing the
filesystem and reports every missing required directory. Explicit
`cb wsl prepare --apply` creates only those missing fixed layout directories,
then revalidates the complete layout. Neither mode installs a binary, creates
management or tool shims, writes config, contacts Docker, or enables the WSL
frontend. The account home and numeric UID come from the native Linux account
database rather than redirectable environment variables.

`cb wsl install --check` is read-only. It validates the same layout before any
config read, loads machine policy only from `/etc/container-bin/policy.toml`,
loads the registry only from the fixed path above without backup-recovery
mutation, validates the running bootstrap executable, and reports whether the
registry, managed binary, management shim and registry-derived tool shims are
ready or require an explicit apply. For an unsigned registry that needs its
built-in defaults upgraded, the plan also preflights and reports the tool shims
that apply would add. A missing primary with a validated backup is reported as
`recover`, not `create`; check mode still leaves both paths unchanged. The
`cb config` output and help text report this fixed registry path whenever the
Microsoft WSL2 kernel is recognized, even if
`WSL_DISTRO_NAME` is missing or malformed. That bootstrap diagnostic derives
only the per-user config location; it does not certify distribution/state
identity, and install or execution still fail closed until that identity is
canonical and complete.

`cb wsl install --apply` first performs layout preparation, then acquires the
fixed registry mutation lock and revalidates the layout. An unmanaged registry
is recovered from a valid interrupted `.bak` when present, otherwise created,
and non-destructively upgraded at mode `0600`. A recovery candidate must itself
be a current-user-owned regular non-symlink file on the distribution-root
device with exact mode `0600`; parse-valid but permissive or foreign backups
are rejected before promotion. When policy requires a signed registry,
automatic creation and upgrade are disabled: the administrator must provision
an authenticated registry before apply can proceed. The source is the exact
running executable returned by the OS; it must be a canonical absolute,
bounded, current-user-owned regular non-symlink file that is owner-executable,
has no special bits and is not writable by group or other. Its bytes are copied
and hashed together through private same-directory staging; the copied digest
must still equal the preflight digest before an atomic publish at the fixed
managed-binary path with mode `0755`. An already byte-identical target is a
no-op. The management shim and every registry-derived tool shim are then
created only when missing and fully revalidated. Foreign files, owners, targets
or unsafe modes stop the transaction instead of being repaired or replaced.
The install command itself performs no Docker request. After a successful
apply and revalidation, its managed tool shims remain activation-gated by the
host boundary until integration coverage and real qualification land.
An interruption before the final binary rename can leave a current-user-owned
`.cb-install-<random>.tmp` regular file in the private binary directory.
ContainerBin does not sweep filename lookalikes without stronger provenance;
they are never adopted as the managed binary.

Config, state and managed-binary directories must be
private and current-user-owned; existing registry and lock files must be
regular non-symlink files with mode `0600`; and an existing managed binary must
be a regular non-symlink file with mode `0755`. The shim directory must be
current-user-owned, owner-accessible and not group- or world-writable. Existing
permissions and ownership are never repaired by guessing intent. The management
shim, when present, must be a current-user-owned symlink to the fixed managed
binary; unrelated files or links fail closed.

The `internal/wslshim` lifecycle extends that identity contract to
registry-derived tool names. It plans only sorted direct children of the fixed
shim directory, requires every existing tool shim to be a current-user-owned
symlink to the fixed managed binary, and reports missing shims separately.
Reconciliation runs only after `internal/wslfs` validates the same layout's
canonical home, intermediate components and distribution-root device boundary.
It then reopens every shim-directory component without following symlinks, pins
the validated directory, and publishes only missing symlinks with an atomic
no-clobber operation. Concurrent correct creation is accepted; a regular file,
foreign link, wrong target or path redirection fails closed and is never
replaced. The installer also uses a prerequisite-independent name preflight so
collisions are rejected before publishing the managed binary, then uses the
same pinned mutation boundary for the management shim and tool shims. Removal
remains separate work, and unrelated directory entries are never adopted or
enumerated as managed shims.
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

The `internal/wslproject` selector applies the profile's shared
project-marker defaults and `nearest`/`outermost` policy, or an exact trusted
overlay root. It rejects malformed marker names and symlink or special-file
markers instead of following them. With no marker, the exact working directory
remains the project-root fallback. It then accepts the selected root only when
it is a canonical, existing Linux directory and proves that the starting
working directory remains under the same project mount. It never accepts
Windows drive/UNC spelling and never translates a Windows path into a WSL path.
Exact Linux case and spelling remain the project identity.

Its descendant classifier revalidates that project identity for each candidate
path, rejects lexical escape, symlinks and nested mount crossings, and returns
the exact case-sensitive project-relative path. A not-yet-created output is
accepted only through its nearest existing non-symlink directory ancestor on
the same exact mount. The unexposed argument mapper consumes that proof for
absolute Linux paths, explicit relative paths and registry-forced path
positions. It maps only project descendants into the container workspace,
preserves relative package patterns such as `./...`, maps absolute package
patterns, and rejects external, symlinked or cross-mount paths instead of
creating implicit mounts or translating Windows spellings. Ambiguous bare
arguments remain unchanged so a project entry cannot replace a tool subcommand.
The wired native runtime consumes this exact proof for every project-mode tool;
production dispatch remains gated.

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
failure blindly. A separate proof-bound attach transport now permits only a
live-stream POST for an exact full container ID. It repeats the complete
socket/peer proof, fixes the attach query and upgrade headers, bounds the
pre-upgrade/error phase, and returns a context-bound duplex stream while
reporting whether Docker multiplexed framing applies. Canceling the parent
context closes the upgraded connection and unblocks I/O; callers can half-close
stdin to deliver EOF while continuing to read output. A separate wait operation
accepts only an exact full container ID and fixes the request to
`condition=not-running`. It repeats the socket/peer proof, uses the caller's
context as the long-poll lifetime, bounds the response to 64 KiB, rejects an
unsafe Engine error, and accepts only process exit codes from 0 through 255.
A separate inspect operation accepts only an exact full container ID, bounds
the response to 1 MiB, requires the returned ID to match exactly and rejects
missing lifecycle/terminal fields. It exposes only the immutable ID, running,
TTY, stdin, auto-remove and defensively copied label state needed by later
lifecycle checks.
A separate start operation accepts only an exact full container ID, repeats
the socket/peer proof, fixes the request to `POST /containers/{id}/start`, and
accepts only HTTP 204. Docker's HTTP 304 "already started" response fails
closed instead of being treated as an idempotent success.
A separate proof-bound signal operation accepts only an exact full container ID
and an explicit numeric Linux signal in the `1..64` domain, always supplies the
Engine `signal` query and accepts only HTTP 204.
The container-creation operation accepts an already-authorized image, command,
environment and working directory, at most one canonical project bind, and
only exact volume identities carrying their complete expected ownership labels.
The bind root must exist, resolve without symlinks and cannot be the fixed
Docker socket, its `/run` alias or an ancestor containing either socket path.
Every named volume is freshly inspected before creation and must match its exact
namespace-prefixed name, complete labels, local driver and local scope, so Docker
cannot implicitly create or adopt foreign state. Duplicate mount targets,
malformed environment entries and implicit privilege/endpoint controls are not
representable. Creation fixes all three attach streams, open/one-shot stdin and
an explicit retention mode, generates a 128-bit run identity, and labels the
container with the exact namespace, run and tool ownership. A successful Engine
response must contain one full lowercase container ID and no warnings. A fresh
inspect must then prove the same ID and labels, stopped state, requested TTY
mode, all attach flags, open/one-shot stdin and retention configuration. The
default primitive retains daemon auto-remove behavior. The runtime instead
retains its owned container until the exact wait response captures the exit
status, avoiding an auto-remove race for very short-lived tools, and then uses
the proof-bound non-force cleanup operation. A post-create validation failure
uses a fresh bounded cleanup context, re-proves
the returned ID's complete ownership and performs non-force deletion only when
that proof succeeds; an invalid/missing ID fails closed because no safe cleanup
target exists.

The paired cleanup operation takes only the immutable identity returned by
creation. An already auto-removed container succeeds. Otherwise it re-inspects
the exact ID, requires every ownership label and the original retention configuration,
refuses a running container, sends `DELETE` with both force and anonymous-volume
removal disabled, and verifies absence afterward. If daemon-side auto-remove
wins the race between inspection and deletion, a DELETE 404 succeeds only after
a fresh proof-bound inspection confirms absence.
The runtime attaches before start, streams stdin with an explicit half-close,
decodes non-TTY stdout/stderr framing, selects TTY only when both stdin and
stdout accept real termios queries, uses raw terminal mode, and applies a
positive initial size from stdin when measurable. Zero-sized or unreadable
dimensions skip that resize; an unmeasurable `SIGWINCH` is dropped rather than
terminating the tool. A source-side stdin read failure remains fatal, while a
closed attach sink after the tool stops defers to the authoritative Engine wait
status. The runtime forwards HUP, INT, QUIT, USR1, USR2, TERM, CONT, TSTP and
PIPE numerically to the exact owned container. Completion-race 404/409 responses
from resize/signal are likewise deferred to the Engine wait. It waits for the
Engine exit status, drains output, restores the terminal and performs proof-bound
cleanup; the tool's `0..255` exit code passes through.
Infrastructure or stream failure cancels the live wait, sends SIGKILL through
the same proof-bound transport, waits for stop and then cleans up. Real WSL2 +
Docker Desktop qualification remains mandatory before release support.

Every ordinary run first acquires the private state-directory namespace lock,
applies orphan reconciliation, creates the retained container while still holding that
coordinator, publishes a private process-held lease keyed by the generated run
ID, and only then releases the coordinator. This closes the create-before-lease
race. Reconciliation treats a locked lease as active; an absent or unlockable
lease is orphaned. It re-proves every discovered container's full ID, labels,
retention mode and stream configuration before the first mutation. Running
orphans are SIGKILLed and waited; stopped orphans go directly through the same
proof-bound non-force removal. Lease paths are removed only after exact
container absence is established. A later coordinator-held pass reports and
reaps unlocked lease evidence only when the complete namespace discovery has
no matching retained container; locked lease-only records are preserved.

`cb wsl cleanup --check` exposes the classification without changing Docker or
lease state. `cb wsl cleanup --apply` performs explicit recovery. Both require
the complete fixed layout and fail closed on unsafe or replaced `0600` lease
files, ambiguous Docker candidates or any ownership/configuration mismatch.

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
filters. Because the Engine applies those label and name filters together,
discovery deliberately does not report a same-name foreign volume that omits
the namespace label; exact-name inspect or ensure still finds and rejects that
collision. The wired tool path plans the complete state set before mutation,
ensures each exact volume, and passes its complete labels into container
creation. Stateful project/shared profiles and the Python provider's
per-project venv, namespace-shared compatibility venv and pip cache are wired.
`cb state` validates the fixed installation and authenticated registry, derives
the exact distribution/machine/user namespace, constructs the current project
and configured shared identities, and proves every discovered Docker volume
before writing its report. It classifies exact current, shared and Python
compatibility state separately from stale managed state. A project volume is
`ORPHAN` only when its exact recorded canonical Linux path is absent; an
existing symlink or non-directory object is `UNSAFE` and never deletion input.

`cb gc [TOOL|STATE_GROUP] [--orphans] [--apply]` uses the same complete proof
set. The default is a dry run. Current-project mode selects only identities
constructed from the current registry and freshly selected project. Orphan
mode selects only exactly proven project volumes whose recorded path is missing.
Shared volumes are never candidates. Apply mode removes without force through
the proof-bound lifecycle and verifies exact absence after deletion. All
discovery, ownership proof, path classification and planning finish before the
first mutation. Native backup/restore remains separate future management work;
it is not required for v2 runtime activation.

## Remaining before the v2 WSL support claim

The ordinary managed tool path is implemented behind the host gate. Activation
and release qualification still require all of the following:

1. Windows-filesystem and WSL-filesystem project tests plus mixed-invocation
   rejection; and
2. real WSL2 + Docker Desktop end-to-end qualification before any support claim.

The WSL runtime deliberately rejects `host_mounts`: that registry field uses a
Windows drive-path grammar and silently reinterpreting it as Linux would violate
the no-equivalence contract. Project descendants and managed volumes are the
supported native mount inputs. The runtime likewise uses only the fixed WSL
registry and lockfile and never falls back to executable-relative Windows state.

Docker's setup contract is documented in its
[WSL2 backend guide](https://docs.docker.com/desktop/features/wsl/): WSL2
integration must be enabled for the selected distribution, and Docker recommends
keeping bind-mounted project files in the Linux filesystem where practical.
