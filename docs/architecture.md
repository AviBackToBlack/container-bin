# ContainerBin architecture

One Go binary, `cb.exe`, built from the standard library only. Everything else
is configuration (`container-bin.toml`), a generated lockfile
(`container-bin.lock`), Docker named volumes, and hardlinked shims.

## Dispatch pipeline

```
NAME.exe (hardlink to cb.exe)
  → argv[0] dispatch            main() inspects its own invocation name
  → registry profile lookup     container-bin.toml, schema-validated, fail-closed
  → argv normalization          repair PowerShell-split "-opt=" "value" pairs
  → path mapping                conservative Windows→container translation
  → provider assembly           stateless | python | stateful volume/env setup
  → image lock resolution       container-bin.lock digest, fail-closed
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

The registry is a deliberately tiny TOML subset: `[tools.NAME]` sections,
quoted strings, arrays of quoted strings, `schema_version`. The custom parser
rejects everything else — unknown keys, duplicate sections, malformed syntax,
newer schema versions. Misreading configuration silently would be worse than
refusing to run; this is a recurring design choice.

Three providers own lifecycle semantics:

- **stateless** — disposable container, no tool state. jq, yq, terraform,
  ffmpeg.
- **python** — legacy provider predating the generic one, kept for
  compatibility: per-project persistent `/venv` volume + shared pip cache +
  a bootstrap that creates the venv on first use. `pip` runs as
  `python -m pip` inside the same environment.
- **stateful** — generic declarative provider: `state_group` namespacing,
  `project_volumes` (scoped per project root), `shared_volumes`. Node/npm/npx,
  go/gofmt and everything `cb expose` creates use this.

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

## Image locking

`cb lock` pulls each unique configured image and records
`configured → repository@sha256:digest` entries in `container-bin.lock`
(section IDs are a hash of the configured reference, validated on load; the
file is rendered, re-parsed as a self-check, then written atomically).

Runtime resolution is fail-closed: lockfile present + configured image missing
from it = refuse to run and say exactly which command fixes it. Tools sharing
an image share one entry, so `node`, `npm`, `npx` and every npm-exposed tool
update together — by design: they are the same runtime and diverging them
would create unrepresentable states. The Node 22 family (`node22`, `npm22`,
`npx22` and anything exposed from `npm22`) is a separate `node:22-slim` entry.

## Atomic writes

Registry and lock mutations (expose, unexpose, uninstall, lock, update,
restore) validate the complete resulting document *before* replacement, then
write via temp file + rename with a short-lived `.bak` window (Windows cannot
reliably rename over an open file). A crash mid-operation leaves either the
old file, the new file, or the old file under `.bak` — never a torn write.
The next `cb` load automatically recovers `container-bin.toml` or
`container-bin.lock` from its `.bak` if the live file is missing, after
validating the backup. If the backup is unreadable or otherwise unusable,
loading stops with a hard error rather than falling back to defaults or an
unlocked state.

Atomic replacement protects file integrity, but it does not protect against
lost updates when two `cb` processes read, modify and write the same file.
Those mutations are therefore serialized through `container-bin.mutation.lock`
next to the registry: `install`, `setup`, `restore`, `expose`, `unexpose`,
`uninstall`, `lock` and `update` hold the lock across their whole
read-modify-write sequence, while the shim-dispatch path and all read-only
commands remain lock-free. `cb` loads the registry once before dispatch, outside the lock, only for
dispatch and read-only commands. Every mutating command re-reads the registry
after acquiring the lock, so the pre-dispatch snapshot is never used for a
write and cannot cause a lost update.
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
main            argv[0] dispatch, subcommand switch, version, usage,
                exit codes + fatalf/osExit, withMutationLock's signal wrapper
  ↓
internal/cli    setup, install, expose, unexpose, uninstall, inspect, trace,
                env, backup, restore, lock, update
  ↓
internal/diag   doctor, self-test, bugreport, verdict functions, redaction
  ↓
internal/dockerrun   docker run assembly, TTY decision, host-env selection,
                     mount specs
internal/state       cb state, cb gc
  ↓
internal/dockervol   docker volume primitives                        (leaf)
internal/lockfile    container-bin.lock, digest resolution
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
```

The exact import edges, from `go list -f '{{.ImportPath}} {{.Imports}}' ./...`,
project-internal imports only:

```
main         -> cli, diag, dockerrun, mutationlock, registry, state
cli          -> atomicio, diag, dockerrun, lockfile, pathmap, registry, toml
diag         -> dockerrun, dockervol, lockfile, pathmap, registry
dockerrun    -> dockervol, lockfile, pathmap, registry
state        -> dockervol, pathmap, registry
lockfile     -> atomicio, registry, toml
pathmap      -> registry
registry     -> atomicio, toml
atomicio, dockervol, mutationlock, toml -> (leaves)
```

Notably: `lockfile` and `pathmap` both depend on `registry` directly, not on
each other; `dockervol` is a true leaf with no internal dependencies at all
(not "beneath" `lockfile`/`pathmap` in any dependency sense — every one of
`diag`/`dockerrun`/`state` reaches it independently); and `mutationlock` is
reached only from `main`, unrelated to the `registry`/`lockfile`/`pathmap`
chain.

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
