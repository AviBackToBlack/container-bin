# ContainerBin architecture

One Go binary, `cb.exe`, built from the standard library only. Everything else
is configuration (`container-bin.toml`), a generated lockfile
(`container-bin.lock`), Docker named volumes, and hardlinked shims.

## Dispatch pipeline

```
NAME.exe (hardlink to cb.exe)
  → argv[0] dispatch            main() inspects its own invocation name
  → host runtime boundary       reject unsupported frontends before config I/O
  → machine policy load         fixed admin path, ownership/version validated
  → registry profile lookup     container-bin.toml, schema-validated, fail-closed
  → project overlay trust       canonical root + exact digest, add-only merge
  → argv normalization          repair PowerShell-split "-opt=" "value" pairs
  → path mapping                conservative Windows→container translation
  → host_mounts resolution      explicit registry-declared bind mounts, provider-agnostic
  → provider assembly           stateless | python | stateful volume/env setup
  → image lock resolution       container-bin.lock digest, fail-closed
  → policy authorization        lock/local-origin/repository/image-trust constraints
  → docker run --rm ...         stdio passthrough, exit code preserved
```

The shell/process contract for these stages (argv, stdio, TTY, signals,
working directory, and environment) is documented in
[docs/shell-contract.md](shell-contract.md).

`cb.exe` invoked as `cb` (or `container-bin`) is the management CLI; invoked
under any other name it is a shim and dispatches to the tool of that name.
Hardlinks make every shim byte-identical to `cb.exe` at zero disk cost, and
`cb install` reconciles the shim set from the registry (with a copy fallback
when hardlinking fails).

## Registry and providers

The registry is a deliberately tiny TOML subset: `[tools.NAME]` and
`[defaults.FAMILY]` sections, quoted strings, arrays of quoted strings, and
`schema_version`. Concrete profiles opt into a default family with an explicit
family, version and alias triple. The selected version resolves every alias in
that family together; incomplete or ambiguous families are rejected. The
custom parser rejects everything else — unknown keys, duplicate sections,
malformed syntax, newer schema versions. Misreading configuration silently
would be worse than refusing to run; this is a recurring design choice.

`internal/projectconfig` independently parses a nearest-ancestor
`.container-bin.toml`, applies the restricted project capability set, and then
performs an add-only merge. It never changes global defaults or replaces a
global concrete tool/default alias. Activation requires a user trust record in
the OS configuration directory whose key is the canonical project root and
whose value includes the SHA-256 digest of the exact overlay bytes and sorted
tool names. The trust store is strict, versioned, atomically written and
recoverable from the same narrow `.bak` interruption window as other state.
Read-only inspection can validate and use that backup without renaming it;
mutation-time trust/untrust recovery runs under the global mutation lock.
Project review commands apply the same rule to `container-bin.toml`: a valid
registry backup may be read in place, but only the subsequent locked mutation
path may promote it to the primary file.
Each merged overlay tool carries runtime-only provenance binding its workspace
mount and project-volume identity to that exact trusted root; its marker policy
cannot select an ancestor or a neighboring overlay's state. Review,
invalidation and untrust do not execute Docker.

Three providers own lifecycle semantics:

- **stateless** — disposable container, no tool state. jq, yq, terraform,
  ffmpeg, rustc.
- **python** — legacy provider predating the generic one, kept for
  compatibility: per-project persistent `/venv` volume + shared pip cache +
  a bootstrap that creates the venv on first use. `pip` runs as
  `python -m pip` inside the same environment.
- **stateful** — generic declarative provider: `state_group` namespacing,
  `project_volumes` (scoped per project root), `shared_volumes`. Node/npm/npx,
  go/gofmt, cargo, uv/uvx, pipx, dotnet, ruby/gem/bundle and everything
  `cb expose` creates use this.

## Project roots and volume naming

A project root is found by walking upward from the working directory looking
for the tool's `project_markers` (`package.json`, `pyproject.toml`, `.git`, …).
No marker → the working directory itself is the root (Python then falls back
to a shared "compat/global" environment instead, for programs that invoke
`python` from arbitrary places).

Volume names are deterministic:

```
cb-python-313-<hash>            per-project venv
cb-python-313-global            compat environment
cb-<group>-<name>-<hash>        stateful project volume (e.g. cb-node24-node-modules-a1b2c3d4e5f6)
cb-<group>-<name>               stateful shared volume  (e.g. cb-node24-npm-cache)
```

`<hash>` is the first 12 hex chars of SHA-256 over the lowercased, cleaned
project path — case-insensitive because Windows paths are. Volumes get Docker
labels (`cb.managed`, `cb.kind`, `cb.owner`, `cb.project_path`,
`cb.project_hash`) so that `cb state` and `cb gc --orphans` can *prove*
ownership instead of guessing. Pre-label legacy volumes are displayed as OTHER
and never auto-deleted.

## Path mapping

Only three argument shapes are treated as host paths without tool-specific
declarations:

1. absolute Windows paths (`D:\x\y`),
2. explicit relatives (`.\x`, `..\x`, `./x`, `../x`),
3. bare relatives that **already exist** on the host (`data\foo.json`).

The authoritative classification of supported, rejected, and deliberately
unsupported Windows path forms is in [docs/windows-paths.md](windows-paths.md).

Paths inside the project root map into the workspace bind mount. Paths outside
it get dedicated narrow mounts under `/cb/mounts/N` — mounting a whole drive
because one argument referenced `D:\Video\a.mkv` would be wildly excessive.
For a nonexistent output path, the nearest existing ancestor directory is
mounted so the tool can create the file.

Tool-specific semantics are declarative in the registry: `path_next` (option
whose *next* argv is a path), `path_equals` (`-opt=PATH`), `path_last` /
`path_last_if_any` (final operand is a path, optionally gated).

### Why FFmpeg's `-i` is not forced to be a path

Valid FFmpeg inputs include URLs, `pipe:`, devices and `lavfi` expressions.
Forcing `-i`'s operand through path mapping would corrupt those. FFmpeg relies
on the generic shape detection above, which handles real files correctly and
leaves everything else alone. This is the "conservative argument rewriting"
principle: when in doubt, don't touch it.

### Why PowerShell argv repair exists

PowerShell hands native processes `terraform -chdir=.\tf validate` as three
argv entries: `-chdir=`, `.\tf`, `validate`. For options declared in
`path_equals`, ContainerBin rejoins `-chdir=` + `.\tf` before mapping. This is
only done for declared options — it is unambiguous there, and guessing
elsewhere would violate the conservative-rewriting principle.

### Why the workspace keeps the project basename (stateful provider)

`D:\TEMP\node-demo-3` mounts at `/workspace/node-demo-3`, not `/workspace`,
because npm derives package metadata from the working directory's basename.
Stateless tools use plain `/workspace`.

### Why the host shows an empty `node_modules` directory

The project volume mounts *over* the project's `node_modules` path inside the
container. Docker requires the mountpoint to exist, so an empty directory may
appear on the host; package contents live only in the named volume. This keeps
node_modules I/O on the Linux side (fast) and the host tree clean.

## Host mounts

`host_mounts` is a separate mechanism from the path mapping above: instead of
*inferring* a mount from an argument, a registry profile *declares* a fixed
host source, container target, and `ro`/`rw` mode outright. It is
provider-agnostic — resolved once per `RunTool` invocation, right after path
mapping's own external mounts and before the provider-specific volume/env
setup — and uses the same `pathmap.CanonicalPath` every other host path in
this codebase goes through, not a second normalization implementation.
Registry-load-time validation (reserved namespaces, duplicate/collision
detection) is entirely separate from the environment-dependent resolution
(variable expansion, existence, UNC rejection) that happens at run time; see
[README.md](../README.md#explicit-host-bind-mounts-host_mounts) for the field
syntax and [docs/security-model.md](security-model.md) for the trust-boundary
discussion — this section only covers where the mechanism sits in the
pipeline, not repeating either.

## Image locking

`cb lock` pulls each unique registry-backed image and records
`configured → repository@sha256:digest` entries in `container-bin.lock`.
When a trusted project overlay is active, its full lock refresh preserves
strictly parsed, policy-authorized entries outside the current effective
registry so locking one project cannot unlock another. A global full refresh
still rebuilds the file from global configuration and removes stale entries.
Images explicitly selected with `--local TOOL` are not pulled and are recorded
as `configured → sha256:image-id`. Selection is explicit because current
Docker engines can expose `RepoDigests` for both local and pulled images, so
metadata alone does not establish user intent. Section IDs are a hash of the
configured reference, validated on load; the file is rendered, re-parsed as a
self-check, then written atomically.

Lock schema 1 carries digest identity only. Schema 2 can additionally carry one
versioned `ImageTrustEvidence` record per repository entry: mechanism,
canonical repository, locked digest, signer/key identity, optional keyless
issuer, authenticated bundle SHA-256, canonical UTC verification time, cosign
identity/hash and the full policy-byte fingerprint. The parser binds that
evidence back to the configured and resolved repository plus digest and rejects
partial evidence, duplicates and evidence on local image IDs. Digest-only
refreshes retain schema 1 unless an existing schema-2 document is being
preserved. An online policy-covered repository is resolved first, verified at
that exact digest, and then promotes the document to schema 2. A parser capped
at schema 1 rejects schema 2. This provides a one-way, fail-closed migration
boundary instead of silently discarding evidence.

Runtime resolution is fail-closed: lockfile present + configured image missing
from it = refuse to run and say exactly which command fixes it. Tools sharing
an image share one entry, so `node`, `npm`, `npx` and every npm-exposed tool
update together — by design: they are the same runtime and diverging them
would create unrepresentable states. The Node 22 family (`node22`, `npm22`,
`npx22` and anything exposed from `npm22`) is a separate `node:22-slim` entry.
Updates preserve the entry's identity mode: repository locks pull and resolve
a fresh matching RepoDigest, while local locks only re-inspect the configured
tag and record its current image ID. A missing local tag is an error, not an
implicit switch to a registry image. `cb update --local TOOL` and
`cb update --registry TOOL` are the explicit mode-switch operations.
Repository entries are accepted only as an immutable, valid SHA-256 RepoDigest
whose repository matches the configured reference after Docker Hub alias
normalization; a mutable tag or foreign repository in a hand-edited lockfile is
invalid.

## Enterprise policy

`internal/policy` is deliberately independent of registry parsing. `main`
loads it from the fixed machine path before the user registry, then passes the
immutable result to request resolution and diagnostics. The zero value means
unmanaged operation. Managed policy authorizes the final configured image plus
its lock identity; repository-mode lock creation is authorized before pull.
This preserves the precedence boundary: user/project/CLI layers may choose a
request, but only the machine layer can authorize it. Full schema and ownership
rules are in [enterprise-policy.md](enterprise-policy.md).

When policy schema 2 requires registry authentication, `registry.Load` passes
the exact file bytes to the policy verifier before parsing. The verifier accepts
only a strict detached Ed25519 envelope beside the registry and a currently
active, non-revoked machine-policy key. Missing signed files do not trigger the
built-in default or `.bak` recovery. Registry-mutating commands are disabled in
this mode because ContainerBin never possesses the administrator's signing key;
lockfile-only operations remain separate.

Policy schema 3 adds a canonical repository-bound image-trust rule set plus
absolute SHA-256 pins for an external cosign verifier and any public-key files.
Rule lookup reuses Docker Hub normalization and selects the most-specific
repository boundary. The policy layer can authenticate the exact configured
cosign file as a bounded regular non-symlink file with the pinned digest, but
does not invoke external code. Authentication yields an immutable byte snapshot,
not a path that could be replaced between checking and execution.

`internal/imagetrust` owns the invocation boundary. It authenticates and stages
only immutable verifier/key snapshots in a protected current-user directory,
uses bounded two-minute child processes with a minimal environment, and asks
the staged verifier to download signature bundles for one exact canonical
`repository@sha256` value. Each bounded bundle is privately staged and passed
back to the same verifier for local verification against the exact digest,
`https://sigstore.dev/cosign/sign/v1` predicate, and configured identity/key;
only those authenticated bundle bytes can become evidence. Staged material is
re-hashed after every use. Only online rules are accepted in this slice.
`offline-bundle` fails before process execution until
policy can pin the complete trusted-root material needed to guarantee a truly
network-independent verification; inherited registry credentials are also not
passed to the verifier yet.

`cb lock` and `cb update` now invoke this boundary after exact digest resolution
for every policy-covered repository. Evidence production requires exactly one
authenticated transparency bundle; zero or multiple distinct bundles abort the
refresh because schema 2 cannot represent them without ambiguity. The completed
record promotes the lockfile to schema 2, while project-scoped refresh can
preserve unrelated evidence without treating it as runtime authorization.
Runtime authorization consumes that evidence only for the exact configured and
resolved repository/digest. It requires the current policy fingerprint,
mechanism, signer/key, issuer and pinned cosign hash to match the recorded
values; any mismatch fails with `policy.image_trust_unverified` and requires an
explicit lock/update refresh. No digest-only path can silently bypass a current
repository trust rule.

## Atomic writes

Registry and lock mutations (add, expose, unexpose, uninstall, lock, update,
restore) validate the complete resulting document *before* replacement, then
write via temp file + rename with a short-lived `.bak` window (Windows cannot
reliably rename over an open file). A crash mid-operation leaves either the
old file, the new file, or the old file under `.bak` — never a torn write.
The next `cb` load automatically recovers `container-bin.toml` or
`container-bin.lock` from its `.bak` if the live file is missing, after
validating the backup. If the backup is unreadable or otherwise unusable,
loading stops with a hard error rather than falling back to defaults or an
unlocked state. Required signed-registry mode is the intentional exception: a
missing live registry is never restored from an unauthenticated `.bak`; the
administrator must provision the registry/signature pair.

Atomic replacement protects file integrity, but it does not protect against
lost updates when two `cb` processes read, modify and write the same file.
Those mutations are therefore serialized through `container-bin.mutation.lock`
next to the registry: `install`, `add`, `setup`, `restore`, `expose`, `unexpose`,
`uninstall`, `lock` and `update` hold the lock across their whole
read-modify-write sequence, while the shim-dispatch path and all read-only
commands remain lock-free. `cb` loads the registry once before dispatch, outside the lock, only for
dispatch and read-only commands. Every mutating command re-reads the registry
after acquiring the lock, so the pre-dispatch snapshot is never used for a
write and cannot cause a lost update.
`cb backup` also holds this lock—not because it mutates configuration, but so a
registry/lock snapshot cannot straddle another command's atomic replacement.
A killed `cb` can leave the lock file behind; the next invocation refuses to
mutate the registry and tells the user to delete it.

## What runs where

| Piece | Runs on |
|---|---|
| `cb.exe`, shims, registry, lock | Windows host |
| Tool processes | Linux containers (ephemeral, `--rm`) |
| Tool state (venvs, node_modules, caches) | Docker named volumes |
| Nothing | permanently installed on the host |

## Package layout

The binary is built from `main.go` plus `internal/`. Dependencies point strictly
downward; there are no cycles. Packages roughly by depth (each one may skip
straight past its neighbor to something further down — the groupings below are
for orientation, not a claim that every package depends on every package in
the tier below it; see the exact edges further down for that):

```
main            argv[0] dispatch, host boundary, subcommand switch, version, usage,
                exit codes + fatalf/osExit, bootstrap self-update selection,
                withMutationLock's signal wrapper
  ↓
internal/cli    setup, install, add, expose, unexpose, uninstall, inspect,
                trace, env, backup, restore, lock, update
internal/projectconfig  overlay discovery, capability validation, trust store
  ↓
internal/statearchive  labeled-volume selection, manifest/checksum/tar
                       validation, Docker helper backup/restore
internal/diag   doctor, self-test, bugreport, verdict functions, redaction
  ↓
internal/dockerrun   docker run assembly, TTY decision, host-env selection,
                     mount specs
internal/state       cb state, cb gc
  ↓
internal/dockervol   docker volume primitives                        (leaf)
internal/lockfile    container-bin.lock, digest resolution
internal/policy      fixed machine policy, ownership and authorization
  ↓
internal/pathmap     Windows path classification and mapping, project roots,
                     volume naming
  ↓
internal/registry    Tool/Registry, TOML parser, defaults, registry file
                     lifecycle, shim install/remove
  ↓
internal/toml        the shared TOML subset lexer                    (leaf)
internal/atomicio    crash-safe write + .bak recovery                (leaf)
internal/mutationlock  the registry mutation lock primitive          (leaf)
internal/hostenv       host classification and gated WSL layout       (leaf)
internal/wslfs         native WSL filesystem ownership/mode preflight
internal/wslshim       native WSL registry-derived shim preflight/mutation
internal/wslinstall    native WSL install/config lifecycle orchestrator
internal/wslproject    native WSL project-root selection and storage boundary
internal/wslpathmap    native WSL project argument mapping
internal/wsldocker     native WSL Docker Desktop integration proof
internal/wslvolume     native WSL namespaced volume identity/lifecycle and
                       stateful-tool binding planning
internal/selfupdate    release selection, staging, verification and replacement
```

The exact import edges, from `go list -f '{{.ImportPath}} {{.Imports}}' ./...`,
project-internal imports only:

```
main         -> cli, diag, dockerrun, hostenv, mutationlock, policy, projectconfig, registry, selfupdate, state, wslfs, wslinstall
cli          -> atomicio, diag, dockerrun, dockervol, lockfile, pathmap, policy, registry, statearchive, toml
projectconfig -> atomicio, pathmap, policy, registry, toml
diag         -> dockerrun, dockervol, lockfile, pathmap, policy, registry
dockerrun    -> dockervol, lockfile, pathmap, policy, registry
state        -> dockervol, pathmap, registry
statearchive -> dockervol, pathmap
lockfile     -> atomicio, policy, registry, toml
pathmap      -> registry
registry     -> atomicio, toml
policy       -> toml
wslfs       -> hostenv
wslshim     -> hostenv, registry, wslfs
wslinstall  -> hostenv, policy, registry, wslfs, wslshim
wslproject  -> hostenv, registry
wslpathmap  -> registry, wslproject
wsldocker   -> hostenv
wslvolume   -> hostenv, registry, wsldocker, wslproject
selfupdate  -> mutationlock, registry
atomicio, dockervol, hostenv, mutationlock, toml -> (leaves)
```

Notably: `lockfile` and `pathmap` both depend on `registry` directly, not on
each other; `dockervol` is a true leaf with no internal dependencies at all
(not "beneath" `lockfile`/`pathmap` in any dependency sense — every one of
`diag`/`dockerrun`/`state` reaches it independently); and `mutationlock` is
reached from `main` and the self-update replacement transaction. The latter
also reaches `registry` to scope managed shim names without guessing.

Two boundaries are load-bearing rather than cosmetic:

- **`internal/mutationlock` knows nothing about signals or exit codes.** It
  exposes `Acquire`/`PathFor` only. The `os.Interrupt` handler and
  `osExit(exitInterrupted)` live in `main`'s `withMutationLock`, keeping
  exit-code policy in exactly one package.
- **`internal/dockervol` is a leaf.** `RunTool` must create labelled volumes
  while `cb state`/`cb gc`/`cb doctor` must list and remove them. Putting the
  volume primitives in any of those consumers would make them depend on each
  other; as a leaf, all of them reach it independently.

`version` stays in `package main` as `var version = "dev"`: both CI and the
release workflow inject it with `-ldflags "-X main.version=..."`, so that symbol
path is part of the release contract. Packages that need it take it as a
parameter.

`internal/wslfs` provides the narrowly exposed `cb wsl prepare --check|--apply`
preflight over the fixed layout derived by `internal/hostenv`. It accepts only
a real current-user-owned home on the distribution root filesystem device;
check mode is read-only, while apply mode creates missing ContainerBin layout
directories without repairing existing objects and then revalidates them. Both
modes validate strict modes for managed registry, lock, binary and
management-shim endpoints. The non-Linux build-tagged implementation always
rejects the operation. This exact management dispatch precedes the general host
gate but performs no policy, registry or Docker I/O.

`internal/wslshim` is the registry-derived tool-shim identity, preflight and
race-safe mutation boundary. It derives only direct children of the fixed native
shim directory, validates names through the registry rules, and requires the
fixed managed binary, shim directory and management symlink to retain their
expected owner/mode/target identity. Existing tool shims must be
current-user-owned symlinks whose canonical target is the fixed managed binary;
missing tool shims may be published without clobbering through a no-follow,
inode-pinned directory handle and are fully revalidated afterward. It must be
composed after `internal/wslfs` validates the same layout's home, intermediate
path and filesystem-device boundary. It never replaces or removes foreign
objects and never discovers unrelated directory entries.

`internal/wslinstall` composes those two boundaries into the explicitly gated
`cb wsl install --check|--apply` lifecycle. Check mode validates the layout
before read-only fixed-path policy/registry access and reports the exact
registry, binary and shim work without recovering backups. Apply mode prepares
the layout, revalidates it under `main`'s signal-aware mutation lock, recovers
or upgrades an unsigned registry at mode `0600` (or requires an authenticated
pre-provisioned signed registry), atomically publishes the validated running
binary at the fixed path, then reconciles the management and registry-derived
tool symlinks through `internal/wslshim`. It performs no Docker I/O and leaves
ordinary WSL dispatch gated.

`internal/wslproject` is an unexposed profile-aware selector and classifier for
native WSL project roots. It applies the registry's nearest/outermost marker
policy or an exact trusted overlay root, then proves both the selected root and
the starting working directory. Marker names must be single Linux path elements,
and symlink or special-file markers fail closed. With no marker it preserves the
existing working-directory fallback. Every selected root must be a canonical
existing directory with no symlink components. Distribution projects must stay
on the distribution root device;
Windows-filesystem projects must be below a proven default `/mnt/<drive>` 9p
DrvFs or WSL virtiofs mount. Custom DrvFs roots, entire-drive roots, ambiguous
`/mnt` paths and Windows spellings fail closed. The package preserves the exact
Linux spelling and does not translate between Windows and WSL path identities.

`internal/wsldocker` is an unexposed native-WSL detector for Docker Desktop's
supported distribution integration. It rejects Docker endpoint/TLS/API
environment overrides and uses a direct Engine API request on the root-owned,
non-world-writable `/var/run/docker.sock`, without loading an ambient Docker CLI
or context. The connected peer must be root and the socket device/inode must be
stable across the request. The engine must report the exact Linux Docker
Desktop name/OS, a Microsoft WSL2 kernel and Docker Desktop's address label.
The probe has fixed time and output bounds. A reachable local or remote Docker
Engine is deliberately insufficient; later frontend wiring must repeat this
proof and retain the explicit Unix endpoint for every Docker operation. Its
separate attach transport admits only a live-stream POST for an exact full
container ID, repeats the complete socket/peer proof, bounds the upgrade and
error response, and returns a context-bound duplex stream with explicit TTY
framing metadata and independent stdin half-close. Parent cancellation closes
the upgraded connection and unblocks I/O. Container lifecycle,
multiplexed-output decoding, terminal behavior, and signal forwarding remain
outside that primitive.

`internal/wslvolume` defines the WSL Docker-volume identity and bounded control
lifecycle. A volume name starts with `cb-<wsl-namespace>-`;
length-delimited group/logical segments prevent ambiguous owner encodings, and
exact namespace ownership is duplicated in the `cb.wsl_namespace` label.
Shared and project volumes retain the existing managed/kind/owner labels;
project roots are canonical absolute Linux paths hashed case-sensitively under
a separate versioned domain. Inspect, create, namespace discovery and
non-forced removal use `internal/wsldocker` rather than an ambient Docker CLI,
so every request repeats the Docker Desktop endpoint proof. Create validates
the response and re-inspects the volume; removal validates exact ownership
before mutation and verifies absence afterward. Prefix/label filtering remains
discovery-only: adoption, GC, backup, restore and deletion must match an exact
identity constructed by this package and its complete labels plus local
driver/scope. The package also preflights an entire stateful profile's
project/shared binding set, re-proves the exact project root before deriving
project identities, and ensures each distinct identity only after the complete
plan validates. Container creation and state-command wiring remain gated.

After the host runtime boundary is enforced, `cb self-update` is dispatched
before machine policy and registry loading. Release selection therefore remains
available when either local configuration source is missing or invalid without
allowing unsupported frontends to perform network work. `--check` performs only
bounded metadata queries and plan output. Explicit `--apply` first proves that
the running image is the installed `cb.exe` and that the supplied GitHub CLI is
an absolute regular file, then selects, privately stages and verifies the exact
release artifact before any installed bytes change.

After verification, the parent copies its already-bound installed bytes into a
protected same-volume helper directory and writes a bounded versioned request.
Only that exact helper filename plus hidden marker bypasses ordinary shim
dispatch. The helper waits with a two-minute bound for the parent to exit,
revalidates its own/request/staging layout, re-runs checksum and GitHub
provenance verification, and requires the new opaque result to equal the
parent-bound result. It then acquires the normal mutation lock, replaces the
management executable, reconciles only valid non-reserved shims proven to hold
the old bytes, and runs bootstrap version/shim-identity checks. Failure rolls
back the complete changed set where possible. A final fixed-system-PowerShell
process waits for the helper and deletes only its exact validated directory.
Windows staging, helper and recovery files use protected current-user-only
DACLs; installed replacements inherit installation-directory ACLs.
