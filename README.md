# ContainerBin

**Run CLI tools on Windows through Docker-backed executable shims — without
installing the runtimes on the host.**

ContainerBin makes commands such as `python`, `pip`, `node`, `npm`, `npx`,
`jq`, `yq`, `terraform` and `ffmpeg` look like ordinary Windows executables
while their real implementations run inside disposable Linux containers on
Docker Desktop. Your Windows installation stays clean: no Python, no Node, no
Terraform on the host — just one small Go binary, `cb.exe`.

```powershell
PS D:\Work\demo> pip install requests
PS D:\Work\demo> python -c "import requests; print(requests.__version__)"
2.32.3
PS D:\Work\demo> ffmpeg -i "D:\Video\input.mkv" "D:\TEMP\output.mkv"
PS D:\Work\demo> terraform -chdir=.\tf validate
Success! The configuration is valid.
```

> **⚠️ ContainerBin is not a security sandbox.** It is a convenience layer that
> runs the Docker images *you* configured, with the host paths *your commands*
> reference bind-mounted in and selected environment variables passed through.
> The registry and lockfile are part of the trust boundary. Read
> [docs/security-model.md](docs/security-model.md) before pointing it at
> anything sensitive.

## Development provenance

ContainerBin is developed using an AI-assisted, agentic workflow. The
maintainer owns requirements, design decisions, acceptance testing, and
releases; implementation and review make extensive use of coding agents. AI
involvement is intentionally preserved in commit and PR history rather than
hidden — see the `Co-Authored-By` trailers throughout the git log.

## How it works

```
PowerShell / cmd / any Windows process
        │  runs python.exe, jq.exe, terraform.exe, ...
        ▼
NAME.exe            (hardlink to cb.exe in one PATH directory)
        ▼
cb.exe              dispatches on argv[0]
        │  registry profile (container-bin.toml)
        │  argv normalization + conservative Windows→container path mapping
        │  image lock resolution (container-bin.lock)
        ▼
docker run --rm ... image@sha256:digest
        ▼
real Linux CLI/runtime in an ephemeral container
```

- **Process compatibility:** stdin/stdout/stderr, exit codes, piping,
  redirection, working-directory semantics and interactive vs. captured
  execution are preserved. Third-party software that probes for a working
  `python.exe` (validated with Claude CLI) accepts the shim as a real
  interpreter.
- **Persistent state where it matters:** `pip install` and `npm install`
  results survive across invocations in Docker named volumes, even though
  every container is disposable.
- **Reproducible images:** `cb lock` pins every configured image to an
  immutable `repository@sha256:digest`. Updates are explicit (`cb update`),
  never a side effect of a mutable tag moving.

## Platform support

| Environment | Status |
|---|---|
| Windows 10/11 + Docker Desktop (Linux containers) + PowerShell | **Supported** — this is the validated configuration |
| cmd.exe invocation of shims | Works for the common cases; less battle-tested than PowerShell |
| Linux / macOS hosts | **Not supported.** The program is Go and cross-compiles, but shim installation, path mapping and doctor checks are Windows-specific |
| Windows containers | Not supported; images are Linux images |

## Prerequisites

- Windows 10 or 11 (x64)
- [Docker Desktop](https://www.docker.com/products/docker-desktop/) running in
  **Linux containers** mode
- A directory on `PATH` where the shims will live

## Installation

1. Download `cb.exe` from [Releases](https://github.com/AviBackToBlack/container-bin/releases)
   (or build it yourself — see [CONTRIBUTING.md](CONTRIBUTING.md)) and place it
   in a dedicated directory, e.g. `D:\Tools\container-bin\`.

2. Put that directory **near the front of `PATH`** (System Properties →
   Environment Variables, or `settings` → "Edit environment variables").
   ContainerBin deliberately never edits `PATH` for you.

3. Disable the **Windows App Execution Aliases** for Python if present
   (Settings → Apps → Advanced app settings → App execution aliases → turn off
   `python.exe` / `python3.exe`). Otherwise the Microsoft Store stub can shadow
   the ContainerBin shim depending on PATH order. `cb doctor` warns about this.

4. Run:

```powershell
cb setup
```

   This writes the default registry (`container-bin.toml`), creates one
   `NAME.exe` hardlink per configured tool next to `cb.exe`, and runs
   `cb doctor` to verify Docker, PATH, shims, registry and lock state.

5. Pin your images:

```powershell
cb lock
```

## Default tools

| Shim | Image | Provider |
|---|---|---|
| `python`, `python3` | `python:3.13-slim` | python (project `/venv` in a named volume) |
| `pip`, `pip3` | `python:3.13-slim` | python |
| `node`, `npm`, `npx` | `node:24-slim` | stateful (`node24` state group) |
| `node22`, `npm22`, `npx22` | `node:22-slim` | stateful (`node22` state group) |
| `go`, `gofmt` | `golang:1.24` | stateful (`go124` state group) |
| `jq` | `ghcr.io/jqlang/jq:latest` | stateless |
| `yq` | `mikefarah/yq:latest` | stateless |
| `terraform` | `hashicorp/terraform:latest` | stateless (`-chdir` path semantics) |
| `ffmpeg` | `lscr.io/linuxserver/ffmpeg:latest` | stateless |

## The registry: declarative tool profiles

`container-bin.toml` lives next to `cb.exe` and describes every tool
declaratively:

```toml
[tools.terraform]
image = "hashicorp/terraform:latest"
provider = "stateless"
path_equals = ["-chdir"]                # -chdir=HOST_PATH is rewritten safely
env_prefixes = ["TF_", "AWS_", "ARM_"]  # only these host vars enter the container
```

Semantics include `command`, `args_prefix`, `path_next`, `path_equals`,
`path_last`, `path_last_if_any`, `env_names`, `env_prefixes`, `env_set`,
`project_markers`, `state_group`, `project_volumes` and `shared_volumes`.
Unknown keys **fail validation** instead of being silently ignored, and a
`schema_version` newer than the binary supports fails closed. Edit the file,
then run `cb install` to reconcile shims.

One exception applies to every path-forcing key (`path_next`, `path_equals`,
`path_last`): a value whose final element is `...` is never rewritten, because
Windows cannot represent such a directory and rewriting it would silently
change which files the tool acts on. See [Windows path mapping](#windows-path-mapping).

## Windows path mapping

ContainerBin translates Windows paths in arguments to container paths and
creates narrowly scoped bind mounts:

- absolute paths (`D:\Video\input.mkv`), explicit relatives (`.\x`, `..\x`),
  and existing relatives (`data\foo.json`) are mapped;
- paths inside the project root map into the workspace mount;
- paths outside it get their own narrow bind mounts (`ffmpeg -i "D:\Video\a.mkv"
  "D:\TEMP\b.mkv"` produces two separate mounts — never a whole drive);
- an argument whose final element is `...` is never treated as a path, so tool
  package patterns such as `./...` reach the tool unchanged;
- **plain strings are never guessed to be paths.** FFmpeg's `-i` is not forced
  to be a path because valid inputs include URLs, pipes, devices and lavfi
  expressions. Tools that need forced path semantics declare them
  (`path_equals = ["-chdir"]` for Terraform).

Several Windows path forms are **not** supported, for different reasons: UNC
paths (`\\server\share\...`) and `\\?\` long-path prefixes are not recognized
as paths and pass through unmapped; `subst` drives and mapped network drives
*are* mapped like any other drive letter, but Docker Desktop cannot share them;
and a junction inside the mounted project tree is not traversable from inside
the container, because bind mounts do not follow reparse points. See
[docs/windows-paths.md](docs/windows-paths.md) for the full classification.

PowerShell natively splits `terraform -chdir=.\tf validate` into
`-chdir=`, `.\tf`, `validate` before the process ever sees it. ContainerBin
detects this for declared `path_equals` options and rejoins the argv —
validated against real Terraform.

Use `cb trace TOOL ARGS...` to see raw → normalized → mapped argv and the
mounts that would be created, without running anything.

## Python and Node state model

**Python:** for a detected project (markers: `pyproject.toml`,
`requirements.txt`, `setup.py`, `setup.cfg`, `.git`), ContainerBin provisions a
persistent per-project `/venv` named volume, plus a shared pip cache volume.
`pip install requests` persists; different projects get isolated environments.
Outside any project, a compatibility "global" environment serves programs that
just invoke `python`/`pip` from anywhere.

**Node:** `node`/`npm`/`npx` share the `node24` state group. Project
dependencies live in a project-scoped named volume mounted at the project's
`node_modules` (the host may show an empty `node_modules` mountpoint directory
— contents live in the volume). There's a shared npm cache and a persistent
npm global prefix. Projects are mounted with their **real basename**
(`D:\TEMP\node-demo-3` → `/workspace/node-demo-3`) because tools like
`npm init` derive metadata from it. Node 24 is the default runtime, but it is
not a guarantee that every npm package is ABI-compatible with it. For packages
whose native addons need a different Node ABI, `node22`/`npm22`/`npx22` are a
second, independent Node-major runtime with their own `node22` state group,
fully isolating project `node_modules`, the npm cache and the npm global prefix; upgrading an existing installation adds these profiles automatically, but they are not yet locked, so run `cb lock` or `cb update --all` before using them.

## Dynamic npm CLI exposure

```powershell
npm install -g cowsay
cb expose npm
cowsay "hello from a container"
```

`cb expose` takes the name of any npm-shaped stateful profile already in the
registry (`npm`, `npm22`, ...) and discovers binaries in that profile's
persistent npm global prefix. It adds registry profiles for them that inherit
the source profile's image and `state_group`, and creates Windows shims —
`cowsay.exe` appears on PATH without Node ever touching the host. To expose a
binary installed under the Node 22 runtime, use `cb expose npm22 <binary>`.
Exposed profiles are keyed by binary name only, so a binary already exposed
from one runtime cannot also be exposed from the other under the same name —
`cb unexpose` it first if you need to switch which runtime backs it.
`cb unexpose cowsay` removes the shim and profile without deleting the
underlying npm state. Registry mutations are validated and written atomically;
a failed validation refuses the update.

## Image locking and explicit updates

```powershell
cb lock          # pull configured images, write container-bin.lock digests
cb lock --check  # verify lock completeness and local availability
cb update jq     # explicitly refresh one image
cb update --all  # explicitly refresh everything
```

Runtime behavior is fail-closed:

- no lockfile → backwards-compatible UNLOCKED mode;
- lockfile present → exact `repository@sha256:digest` execution;
- an image configured in the registry but missing from the lock → execution
  **fails** and asks for `cb update TOOL` or `cb lock`.

Tools sharing an image share one lock entry (`node`, `npm`, `npx` and all
npm-exposed tools ride the single `node:24-slim` entry). The Node 22 runtime
family (`node22`, `npm22`, `npx22` and anything exposed from `npm22`) ride a
separate `node:22-slim` lock entry.

## State inspection and garbage collection

```powershell
cb state           # volumes classified CURRENT / SHARED / COMPAT / OTHER / ORPHAN
cb gc              # dry-run for current project state
cb gc --apply      # delete only explicitly selected current project state
cb gc --orphans    # dry-run: labeled volumes whose project path no longer exists
cb gc --orphans --apply
```

Managed volumes carry labels (`cb.managed=true`, `cb.kind`, `cb.owner`,
`cb.project_path`, `cb.project_hash`) enabling genuine orphan detection.
Legacy/unlabeled volumes are **never** guessed to be orphans and never
auto-deleted.

## Backup and restore

```powershell
cb backup                       # zip of registry + lock into backups\
cb restore BACKUP.zip           # dry-run: validates and reports
cb restore BACKUP.zip --apply   # atomic replacement after validation
```

## Concurrency and the mutation lock

`cb install`, `cb setup`, `cb restore`, `cb expose`, `cb unexpose`,
`cb uninstall`, `cb lock` and `cb update` serialize through
`container-bin.mutation.lock` next to `cb.exe`. A second concurrent
mutation waits up to 5 seconds for the lock, then fails with a clear
message if the holder is still active.
A `cb` process interrupted with Ctrl-C while holding the lock exits `130`
and releases the lock automatically. A `cb` process that is hard-killed
(e.g. by SIGKILL or the task manager) while holding the lock may leave a
stale `container-bin.mutation.lock` behind; delete it manually before the
next mutating command.

## Diagnostics

```powershell
cb doctor     # Docker CLI/engine, container mode, registry schema,
              # lock completeness, PATH, shims, python resolution,
              # shim directory permissions, reparse points,
              # network storage, managed volumes
cb bugreport  # version, Windows/PowerShell version, registry inventory,
              # doctor output and docker/lock state in one paste-ready block,
              # with best-effort secret redaction (review before posting)
cb self-test [--json] [--release]  # offline end-to-end test using already-local locked images;
                       # reports every check instead of stopping at the first failure;
                       # --json emits a machine-readable report for CI;
                       # --release adds host/environment facts to the report
                       # python /venv persistence, external path mapping, node project
                       # state, jq relative paths, terraform -chdir normalization
cb trace ...  # dry-run argv/mount mapping for one command
cb inspect TOOL
cb env
cb list
```

`cb bugreport` assembles `cb version`, the Windows and PowerShell versions
(on Windows), a compact registry inventory and `cb doctor` output into one
block that is easy to paste into an issue. It applies a small, fixed set of
best-effort redactions (KV-style secrets, AWS access key IDs, GitHub tokens
and `Bearer` tokens) as defense-in-depth, but the real safety property comes
from the inputs: it never dumps `os.Environ()`, raw `PATH` values, lockfile
contents or per-tool inspect detail. Review the report before posting it
publicly — redaction is best-effort, not a general secret scanner.

`cb self-test` intentionally pulls nothing; it proves your existing locked
setup works end to end, then cleans up its temporary project volumes. It now
runs every check and reports all of them, instead of stopping at the first
failure.

### `cb self-test --json` report format

`--json` prints one JSON document to stdout and nothing else (no progress
output, no interleaved tool output) — safe to pipe into a script or CI step.
`schema_version` is `1`; future additions will increment it only if they
change the meaning of an existing field, not for new additive fields.

```json
{
  "schema_version": 1,
  "cb_version": "v1.2.3",
  "generated_at": "2026-08-18T12:00:00Z",
  "checks": [
    { "id": "docker", "status": "pass", "message": "docker 27.0.0" },
    { "id": "python-image-local", "status": "pass", "message": "image present" }
  ],
  "passed": 12,
  "failed": 0,
  "skipped": 0,
  "ok": true
}
```

With `--release` (and only with `--release`), an `environment` array is added
immediately after `checks`; with `--json` alone the key is omitted entirely, so
plain `cb self-test --json` output is unchanged. Environment entries use the
same `{ "id", "status", "message" }` shape and the same `pass`/`fail`/`skip`
vocabulary as `checks`, but they are informational only: they never count toward
`passed`, `failed`, `skipped`, or `ok`. The seven stable environment IDs, in
order, are:

1. `windows-version` — raw Windows build string (e.g. `Microsoft Windows NT 10.0.26200.0`). This does not distinguish "Windows 10" from "Windows 11" by name; only the build number differs (Windows 11 requires build ≥ 22000). A friendlier caption would need a slower WMI/CIM round-trip, so the raw build number is deliberate.
2. `powershell-version` — PowerShell version string.
3. `docker-engine-version` — the Docker Engine version reported by `docker version`; this is a Docker *Engine* version, not the separate Docker Desktop application version shown in Docker Desktop's Settings → About.
4. `docker-os-type` — `docker info` OSType (`linux` passes; `windows` fails because ContainerBin runs Linux images only).
5. `cwd-reparse-point` — whether the current working directory resolves through a junction/symlink.
6. `shim-dir-network-storage` — whether the shim directory is on a fixed/removable/network/UNC drive.
7. `cwd-network-storage` — whether the current working directory is on a fixed/removable/network/UNC drive.

The four PowerShell-dependent entries (`windows-version`, `powershell-version`,
`shim-dir-network-storage`, `cwd-network-storage`) are skipped on non-Windows
hosts; `docker-engine-version`, `docker-os-type`, and `cwd-reparse-point` are
not PowerShell-dependent and still run.

A `skip` status covers two different situations that share the same status
value but not the same message shape: a genuine "could not determine" (e.g.
not on Windows, Docker unreachable) is always messaged `skipped: ...`, while
a `warn` verdict from the reused `cb doctor` verdict functions — a real,
actionable qualification finding such as a UNC/mapped-drive shim directory or
a reparse-point-backed working directory — is messaged `warn: ...` instead.
Both report as `skip` (this schema does not add a fourth status value), but a
CI consumer that cares about the difference can distinguish them by the
message prefix.

Each entry in `checks` has `status` of `pass`, `fail`, or `skip`. A `skip`
means a dependency of that check did not pass (e.g. `docker` itself failed,
or the tool isn't registered) — `message` names the reason. A tool missing
from `container-bin.toml` reports its own check as `fail`, not `skip`: an
unconfigured tool was never actually verified, so `ok` cannot be `true` while
one is missing. The `checks` array always contains exactly these 12 IDs, in
this order: `docker`, `python-image-local`, `python-persist-write`,
`python-persist-read`, `python-external-path`, `node-image-local`,
`node-modules-write`, `node-modules-read`, `jq-image-local`,
`jq-relative-path`, `terraform-image-local`, `terraform-chdir`.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success. |
| tool's own code | The invoked tool's own exit status passes through unchanged. |
| `2` | Usage error: unknown subcommand. |
| `120` | cb infrastructure failure — registry, lock, doctor, or command errors; the message printed to stderr explains which; not distinguishable from a containerized tool that exits `120`. |
| `130` | Interrupted — Ctrl-C while a mutating command holds the registry lock; not distinguishable from a containerized tool that exits `130`. |

## Troubleshooting

- **`python` opens the Microsoft Store / does nothing** — disable the App
  Execution Aliases (see Installation) or move the shim directory ahead of
  `WindowsApps` in PATH. `cb doctor` detects both problems.
- **`image "X" is not locked` error** — you edited an image in the registry
  while a lockfile exists. That's fail-closed behavior working; run
  `cb update TOOL` or `cb lock`.
- **A path argument wasn't mapped** — only recognizable Windows path shapes are
  mapped (see path mapping above). Check with `cb trace TOOL ARGS...`; declare
  `path_next`/`path_equals` semantics for the tool if needed.
- **Docker not reachable** — start Docker Desktop; `cb doctor` shows what cb
  sees.

## Security model

Short version: ContainerBin executes what its configuration tells it to.
Whoever can write `container-bin.toml`, `container-bin.lock`, or the shim
directory controls execution. Path mapping is deliberately conservative, env
passing is allowlist-only, mounts are as narrow as possible, and lock
violations fail closed rather than falling back. Full details, including what
is and isn't a vulnerability: [docs/security-model.md](docs/security-model.md)
and [SECURITY.md](SECURITY.md).

## Architecture

See [docs/architecture.md](docs/architecture.md) for the full dispatch
pipeline, the provider model (stateless / python / stateful), volume naming,
and the reasoning behind apparently odd behavior (PowerShell argv repair,
FFmpeg's unforced paths, empty `node_modules` mountpoints, shared lock
entries, legacy Python compatibility state). The shell/process semantics of
that pipeline are in [docs/shell-contract.md](docs/shell-contract.md).

## Current limitations

- Windows + Docker Desktop (Linux containers) only.
- First invocation of a tool after `cb lock` may still need images present
  locally (`cb lock` pulls them; `cb self-test` never pulls).
- Container startup adds latency compared to native binaries (typically
  hundreds of milliseconds; interactive REPLs work but feel it).
- Concurrent `cb` commands that mutate the registry are serialized through
  `container-bin.mutation.lock`; a second concurrent mutation fails fast
  instead of waiting, and a lock file left by a killed process must be deleted
  manually.
- Go shims run a Linux Go toolchain: `go build` produces a Linux binary by
  default. Set `GOOS=windows` (allowed by the profile) to build a Windows
  executable, e.g. `$env:GOOS="windows"; go build`. `go test` must remain
  native to the container because a Windows test binary cannot run inside it.
- `cb expose` currently supports the npm global prefix only.

## Roadmap

Larger ideas (more package-manager integrations, self-update, signing) are
tracked in the
[roadmap issue](https://github.com/AviBackToBlack/container-bin/issues).
The `main.go` decomposition listed here previously is done; see
[docs/architecture.md](docs/architecture.md) for the resulting package layout.

## Contributing & license

Contributions welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) (containerized
build/test instructions; no Go installation required) and
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

Licensed under the [Apache License 2.0](LICENSE).

## Release verification

Release binaries are built by a tag-triggered GitHub Actions workflow with
`SHA256SUMS` checksums and GitHub build provenance attestation. Verify with:

```powershell
gh attestation verify cb.exe --repo AviBackToBlack/container-bin
```
