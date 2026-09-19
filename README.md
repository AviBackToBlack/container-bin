# ContainerBin

**Run CLI tools on Windows through Docker-backed executable shims — without
installing the runtimes on the host.**

ContainerBin makes commands such as `python`, `pip`, `pipx`, `node`, `npm`, `npx`,
`uv`, `uvx`, `go`, `cargo`, `rustc`, `dotnet`, `ruby`, `gem`, `bundle`, `jq`, `yq`, `terraform` and `ffmpeg` look like ordinary
Windows executables while their real implementations run inside disposable
Linux containers on Docker Desktop. Your Windows installation stays clean: no
Python, pipx, Node, Go, Rust, uv, .NET SDK or Ruby on the host — just one small Go binary,
`cb.exe`.

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
docker run --rm ... repository@sha256:digest | sha256:local-image-id
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
- **Reproducible images:** `cb lock` pins registry images to an immutable
  `repository@sha256:digest` and locally built images to their exact Docker
  `sha256` image ID. Updates are explicit (`cb update`), never a side effect
  of a mutable tag moving.

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
| `node`, `npm`, `npx` (default aliases) | selected Node family | stateful |
| `node24`, `npm24`, `npx24` | `node:24-slim` | stateful (`node24` state group) |
| `node22`, `npm22`, `npx22` | `node:22-slim` | stateful (`node22` state group) |
| `go`, `gofmt` | `golang:1.24` | stateful (`go124` state group) |
| `rustc` | `rust:1.98.1-slim-bookworm` | stateless |
| `cargo` | `rust:1.98.1-slim-bookworm` | stateful (`rust198` state group) |
| `uv`, `uvx` | `ghcr.io/astral-sh/uv:0.12-python3.13-trixie-slim` | stateful (`uv012-py313` state group) |
| `pipx` | `ghcr.io/astral-sh/uv:0.12-python3.13-trixie-slim` | stateful (`pipx117-py313` state group; pinned `pipx==1.17.4`) |
| `dotnet` | `mcr.microsoft.com/dotnet/sdk:10.0` | stateful (`dotnet10` state group) |
| `ruby`, `gem`, `bundle` | `ruby:4.0-trixie` | stateful (`ruby40` state group) |
| `jq` | `ghcr.io/jqlang/jq:latest` | stateless |
| `yq` | `mikefarah/yq:latest` | stateless |
| `terraform` | `hashicorp/terraform:latest` | stateless (`-chdir` path semantics) |
| `ffmpeg` | `lscr.io/linuxserver/ffmpeg:latest` | stateless |

## The registry: declarative tool profiles

`container-bin.toml` lives next to `cb.exe` and describes every tool
declaratively:

Bootstrap commands do not read or validate that file: `cb version` (also
`--version`/`-V`), `cb help` (also `--help`/`-h` and bare `cb`) and `cb config`
remain available when the registry is missing, corrupt, or newer than the
installed binary. This keeps version-skew diagnosis usable before repair.

```toml
[tools.terraform]
image = "hashicorp/terraform:latest"
provider = "stateless"
path_equals = ["-chdir"]                # -chdir=HOST_PATH is rewritten safely
env_prefixes = ["TF_", "AWS_", "ARM_"]  # only these host vars enter the container
```

Semantics include `command`, `args_prefix`, `path_next`, `path_equals`,
`path_last`, `path_last_if_any`, `env_names`, `env_prefixes`, `env_set`,
`project_markers`, `state_group`, `project_volumes`, `shared_volumes`,
`project_root_mode`, `host_mounts`, `cwd_mode`, and the explicit
`default_family` / `default_version` / `default_alias` relationship. Unknown
keys **fail validation** instead of being silently ignored, and a
`schema_version` newer than the binary supports fails closed.
Edit the file, then run `cb install` to reconcile shims.

Tool names use lowercase letters, digits, `-`, and `_`. Names that collide
with ContainerBin or Windows devices are reserved. Release binaries named
`cb-v` followed immediately by a digit (for example, `cb-v1.2.3.exe`) also
remain reserved for the management CLI; ordinary names that merely start with
`cb-v` but have no digit there, such as `cb-vault`, are valid tool shims and
dispatch to their registered profile.

For an image whose entrypoint is already the desired command, `cb add` appends
a minimal stateless profile and reconciles the shim without pulling or running
the image:

```powershell
cb add jq-corp --image registry.corp.example/devtools/jq:1.8.1
```

If a lockfile exists, it becomes intentionally incomplete until you run
`cb update jq-corp` or `cb lock`; execution fails closed in the meantime.
State, environment allowlists, path rules, command overrides, and mounts still
require an explicit reviewed registry edit followed by `cb install`.

The existing `role` field is also used as an ownership boundary:
`cb expose` writes `role = "exposed"`, and `cb unexpose` only removes profiles
carrying that marker whose command and tool name agree and whose command stays
beneath a declared shared-volume mount. Known package-manager stores retain
their stricter store-directory and companion-volume checks.
Profiles generated by older versions have no marker and deliberately fail
closed; recreate one with `cb uninstall TOOL` followed by
`cb expose SOURCE TOOL` to migrate it.

### Explicit host bind mounts (`host_mounts`)

`host_mounts` lets a trusted profile declare fixed host paths that are always
bind-mounted into the container, regardless of whether they appear in the
command line. This is provider-agnostic: it works for `stateless`, `python`
and `stateful` profiles alike.

```toml
[tools.token-meter]
image = "example/token-meter:latest"
provider = "stateless"
host_mounts = [
  "%USERPROFILE%\\.claude:/root/.claude:ro",
  "%USERPROFILE%\\.codex:/root/.codex:ro",
]
```

Entries follow the `SOURCE:/CONTAINER_PATH:MODE` shape used by
`project_volumes`/`shared_volumes`, extended with a required third `:MODE`
segment. `ro` and `rw` are accepted; there is **no default** — every mount's
write access must be explicit in the registry line.

`%USERPROFILE%` is the only host variable container-bin expands; no other
`%...%` token is recognized or guessed. The source may also be a literal
Windows absolute path using a backslash after the drive letter
(e.g. `D:\Video`). The forward-slash drive form (`D:/Video`) embeds the `:/`
source/target delimiter and is rejected as ambiguous, so always use `X:\...`.

Targets under `/workspace`, `/cb`, `/venv` and `/root/.cache/pip` are reserved
for container-bin's own project workspace and managed state mounts (the last
two are the python provider's fixed venv/pip-cache paths) and cannot be
claimed by `host_mounts`, on any provider.
`project_volumes` and `shared_volumes` may intentionally use paths under
`/workspace` and `/cb`, but cannot target `/venv`, `/root/.cache/pip` or their
descendants because those paths are owned by the python provider.

> A `host_mounts` entry grants the configured Docker image direct access to the
> named host files or directories. ContainerBin is **not a security sandbox**;
> a `rw` mount exposes those host files to any code running in the container,
> exactly as a `docker run --mount` would. Use `ro` when the tool only needs to
> read, review every `rw` mount, and keep the registry and shim directory
> under your control.
>
> Exposed profiles created by `cb expose` do
> **not** inherit that source's `host_mounts`; host access never propagates
> implicitly to an auto-generated shim. A profile that genuinely needs a host
> mount must declare it explicitly.

### Isolated launcher mode (`cwd_mode`)

`cwd_mode` defaults to `"project"`, which preserves the normal behavior of
walking up from the current working directory to find project markers and
bind-mounting the project into the container at `/workspace`. Set it to
`"isolated"` for tools that may be launched from an arbitrary working directory
that is not itself meaningful — for example, a background service or a GUI/MCP
launcher that inherits `C:\Windows\System32` as its CWD and invokes a shim from
there. In isolated mode, ContainerBin skips project-root detection entirely,
sets `--workdir /root`, and does not bind-mount the host CWD at all. Any
argument that looks like a host path is still mapped, but because no path can be
"inside the project" it always uses the existing external-mount path and lands
under `/cb/mounts/N`. `cwd_mode = "isolated"` cannot be combined with:

- `project_volumes` (a project-scoped volume conceptually requires a project identity);
- `project_markers` (dead configuration once project-root detection is skipped);
- `provider = "python"` (the python provider has its own project/compat venv split that
  isolated mode would otherwise silently collapse onto the shared global compat environment).

`shared_volumes`, `host_mounts` and environment allowlisting all work exactly as they do in
project mode. Exposed profiles created by `cb expose` do
**not** inherit that source's `cwd_mode`.

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

AI agents and automation launchers must not assume that a working-directory
change survives into a later shell call. See
[AI agent and automation invocation](docs/agent-invocation.md) for the
canonical execution contract and supported instruction files.

## Python and Node state model

**Python:** for a detected project (markers: `pyproject.toml`,
`requirements.txt`, `setup.py`, `setup.cfg`, `.git`), ContainerBin provisions a
persistent per-project `/venv` named volume, plus a shared pip cache volume.
`pip install requests` persists; different projects get isolated environments.
Outside any project, a compatibility "global" environment serves programs that
just invoke `python`/`pip` from anywhere.

**Node:** `node24`/`npm24`/`npx24` share the `node24` state group. The
unversioned `node`/`npm`/`npx` shims are aliases to one complete versioned
family; Node 24 is selected initially. Switch all three together without
changing the stable versioned shims:

```powershell
cb default
cb default set node 22
cb default set node 24
```

`cb inspect node` and `cb trace node ...` show the concrete profile currently
selected. Alias resolution reuses that profile's image, state group and volumes;
it does not copy tool configuration. Destructive profile commands fail closed
on aliases: `cb uninstall` and `cb unexpose` require the concrete profile name
instead of deleting an alias shim while leaving its family metadata intact.
`cb install` and `cb setup` migrate stock
schema-v1 Node profiles automatically. A customized legacy `node`/`npm`/`npx`
profile is not assigned a version by guesswork: the upgrade stops and asks you
to give it an explicit versioned name and alias metadata. Project
dependencies live in a project-scoped named volume mounted at the project's
`node_modules` (the host may show an empty `node_modules` mountpoint directory
— contents live in the volume). There's a shared npm cache and a persistent
npm global prefix. Projects are mounted with their **real basename**
(`D:\TEMP\node-demo-3` → `/workspace/node-demo-3`) because tools like
`npm init` derive metadata from it. Node 24 is the initial default runtime, but it is
not a guarantee that every npm package is ABI-compatible with it. For packages
whose native addons need a different Node ABI, `node22`/`npm22`/`npx22` are a
second, independent Node-major runtime with their own `node22` state group,
fully isolating project `node_modules`, the npm cache and the npm global prefix; upgrading an existing installation adds these profiles automatically, but they are not yet locked, so run `cb lock` or `cb update --all` before using them.

## Rust and Cargo state

`rustc` and `cargo` use the official `rust:1.98.1-slim-bookworm` image and
share one image-lock entry. Cargo's registry and Git caches persist in shared
named volumes. `cargo install` writes to a separate persistent
`/cb/cargo-global` volume that is on the container `PATH`, so installed Cargo
subcommands remain usable through `cargo`. Run `cb expose cargo <binary>` to
create a standalone Windows shim for an installed executable. That directory
intentionally precedes the Rust toolchain directories:
installing a binary named `cargo` or `rustc` shadows the image's toolchain
command inside this profile, so audit what you install into the shared store.

Cargo build output deliberately stays in the host project tree rather than a
Docker volume. Cargo uses `project_root_mode = "outermost"`, so a member
`Cargo.toml` or nested `rust-toolchain` does not hide a parent workspace from
the container mount. Run Cargo from that project (or a subdirectory), as usual.
Paths supplied to `--target-dir` and `--manifest-path` are mapped into the
container, including their `--option=PATH` forms. Path-valued host
variables such as `CARGO_HOME`, `CARGO_TARGET_DIR` and `RUSTUP_HOME` are not
forwarded because their Windows values are not meaningful inside Linux;
registry-specific Cargo variables and selected non-path settings are forwarded
explicitly.

Existing installations gain these profiles on `cb install`. Because the new
image is not present in an older lockfile, run `cb update cargo` (or regenerate
the lock with `cb lock`) before first use in locked mode.

## uv state model

`uv` and `uvx` use Astral's `uv 0.12` image line with Python 3.13. Each
project gets a persistent environment mounted at `/cb/uv-project-env`; the uv
package cache, installed tool environments and tool executables are shared
across projects in separate managed volumes. The cache and environments are on
different filesystems, so the profile deliberately sets `UV_LINK_MODE=copy`
instead of letting uv attempt hardlinks and warn on every sync.

The project environment's `/cb/uv-project-env/bin` intentionally comes first
on the `uv` profile's `PATH`, followed by the shared tool-bin directory. This
lets project commands resolve normally, but it also means installing another
`uv` executable into that environment shadows the image's pinned `uv`; avoid
doing that unless the override is deliberate.

Automatic Python downloads are disabled. A project that requires a different
interpreter fails explicitly instead of silently downloading an untracked
runtime; use a separately configured uv image/profile for that interpreter.
Index, offline, TLS and proxy settings are forwarded from a narrow allowlist,
while path-valued Windows settings such as `UV_PROJECT`, `UV_CACHE_DIR` and
`VIRTUAL_ENV` are not.

`uv tool install ruff` persists its environment and executable, and `uvx ruff`
reuses the shared cache. Run `cb expose uvx ruff` to create a standalone
`ruff.exe` Windows shim backed by those same managed volumes. Prefer `uvx` as
the expose source because its environment contains only global tool state;
`uv` also carries project-only `VIRTUAL_ENV` and `UV_PROJECT_ENVIRONMENT`
settings whose project volume is deliberately not inherited by exposed tools.
This is the pipx-style global Python-tool workflow; arbitrary pip environments
and generic shared-volume paths are not exposed implicitly.

Equals-form global path options (`--cache-dir=`, `--directory=`, `--project=`,
and `--config-file=`) are translated to their container paths. The mapper stops
forced option handling at `--`, so same-named options intended for a command
launched by `uv run` or `uvx` are not mistaken for uv options; ordinary
path-shaped arguments still receive the generic mapping.

Existing installations gain `uv` and `uvx` on `cb install`. An older lockfile
does not include their image, so run `cb update uv` (or regenerate the lock with
`cb lock`) before first use in locked mode.

## pipx global application state

`pipx` is the classic-Python global application workflow. It is a separate
stateful profile: installed application environments live under
`/cb/pipx/home`, their executables live under `/cb/pipx/bin`, and the pinned
pipx launcher cache lives under `/cb/pipx/launcher-cache`. These directories
share one managed state volume so their relative links and cached launcher
remain portable together. That volume is not the Python provider's project
`/venv` or pip cache, and it is not shared with uv's tool store.

The locked uv/Python image launches the exact `pipx==1.17.4` release with
`uvx`. First use therefore needs package-index access to populate the dedicated
launcher cache; later invocations can use that cache offline. The image lock
pins the launcher image, while package-index trust and the pipx package download
remain governed by the profile's narrowly forwarded uv/pip index, TLS and proxy
settings. Automatic Python downloads are disabled and pipx uses the Python 3.13
interpreter already in the locked image. After a successful pipx command, a
fail-closed wrapper changes pipx-owned absolute links to relative links within
the state volume and copies only the known image interpreter into its launcher
cache and application venvs; this keeps `cb-pipx117-py313-state` portable
through selected-volume backup/restore without relaxing archive link validation.
`pipx run` environments remain ephemeral because `PIPX_CACHE_DIR` is not placed
on the managed volume; each `pipx run` may resolve and download its application
again and therefore requires the configured package index to be reachable.

```powershell
pipx install cowsay==6.1
cb expose pipx cowsay       # explicit deterministic selection
cowsay "hello from pipx"

cb expose pipx              # expose every other eligible app in the store
cb unexpose cowsay
```

The generated application profiles preserve the pipx image, state group,
volumes and environment policy. Exposure discovery reads only the managed bin
directory within the state volume through the selected locked profile; it does
not search a project venv, the host `PATH`, uv's store, or other container
directories.
Plain `pip` remains for project dependencies, so scripts in `/venv/bin` are
deliberately not eligible for global exposure.

Existing installations gain `pipx` on `cb install`. Because it shares the same
image reference as uv, a lock that already contains that reference can resolve
it; otherwise run `cb update pipx` (or regenerate the lock with `cb lock`) before
first use in locked mode.

## .NET SDK state

`dotnet` uses Microsoft's .NET 10 LTS SDK image. NuGet packages, user-level
NuGet configuration and global .NET tools persist in shared managed volumes;
`/root/.dotnet/tools` is on the container `PATH`. Telemetry and first-run setup
noise are disabled by the profile. NuGet package-source
credentials and selected runtime/network controls are forwarded explicitly,
while path-valued Windows settings such as `DOTNET_ROOT`, `DOTNET_CLI_HOME` and
`NUGET_PACKAGES` are not.

For `dotnet watch` on a Docker Desktop bind mount, set
`DOTNET_USE_POLLING_FILE_WATCHER=1` on the host if filesystem notifications do
not cross the Windows/Linux boundary reliably; the profile forwards that
non-path opt-in.

Project `bin` and `obj` directories deliberately remain in the host project
tree instead of Docker volumes, so build output is visible to editors and
other Windows processes. The SDK runs on Linux: framework-dependent IL remains
portable, but native apphosts and self-contained publishes target Linux unless
you explicitly select a Windows runtime identifier such as `-r win-x64`.

`dotnet tool install --global TOOL` persists under `/root/.dotnet`; inspect it
later with `dotnet tool list --global`. Run `cb expose dotnet <binary>` to
create a standalone Windows shim backed by the same managed .NET home. The
global-tools directory intentionally comes first on the profile's `PATH`, so a
global tool named `dotnet` would shadow the SDK command inside that profile.

Existing installations gain `dotnet` on `cb install`. An older lockfile does
not include the SDK image, so run `cb update dotnet` (or regenerate the lock
with `cb lock`) before first use in locked mode.

## Ruby and gem state

`ruby`, `gem` and `bundle` use the full official Ruby 4.0 image rather than the
slim variant, because development workflows frequently need its compiler and
system headers for native extensions. They share a Ruby-ABI-specific `ruby40`
state group and one image-lock entry.

`gem install rake` writes to a persistent shared gem home that is on the
container `PATH`, so later Ruby invocations can load the gem or find its
executables (for example, `ruby -S rake`). Bundler dependencies live in a
per-project `/cb/bundle` volume, while its download cache is shared. `Gemfile`
and `Gemfile.lock` remain in the host project; installed Linux gems and native
extensions remain in Docker volumes rather than leaking onto Windows.

The managed gem bin directory intentionally precedes the image toolchain on
`PATH`. A gem that installs an executable named `ruby`, `gem` or `bundle`
therefore shadows that command inside these profiles, so audit executables
added to the shared gem home.

Only selected non-path Ruby/Bundler settings, repository credentials and proxy
variables cross into the container. Host values such as `GEM_HOME`, `GEM_PATH`,
`BUNDLE_PATH` and `BUNDLE_GEMFILE` are deliberately ignored because Windows
paths are meaningless in the Linux image. Run `cb expose ruby <binary>` to
create a standalone Windows shim for an executable installed by RubyGems. Use
the `ruby` source for exposure: it mounts the same gem home without forwarding
the `gem`/`bundle` profiles' `RUBYGEMS_API_KEY`, `HTTP_PROXY_USER`, or
`HTTP_PROXY_PASS` credentials to arbitrary installed gem code.

Existing installations gain all three profiles on `cb install`. An older
lockfile does not include their image, so run `cb update ruby` (or regenerate
the lock with `cb lock`) before first use in locked mode.

## Dynamic global CLI exposure

```powershell
npm install -g cowsay
cb expose npm
cowsay "hello from a container"

go install golang.org/x/tools/cmd/stringer@v0.36.0
cb expose go stringer
stringer -help

cargo install just
cb expose cargo just
just --version

uv tool install ruff
cb expose uvx ruff
ruff --version

pipx install cowsay==6.1
cb expose pipx cowsay
cowsay "hello from pipx"

dotnet tool install --global dotnet-ef
cb expose dotnet dotnet-ef
dotnet-ef --version

gem install rake
cb expose ruby rake
rake --version

# For a custom stateful profile whose shared_volumes includes
# "tools:/opt/acme", expose one explicit executable without store guessing:
cb expose --shared-file acme tools /opt/acme/bin/acme-lint
acme-lint --version
```

`cb expose` takes a stateful source profile with one supported global binary
store: the npm prefix (`npm`, `npm22`, ...), Go's shared `/go/bin` (`go`),
Cargo's install root (`cargo`), uv's tool-bin directory (`uv`, `uvx`), pipx's
managed bin directory (`pipx`), .NET's global tool home (`dotnet`), or the
RubyGems home (`ruby`, `gem`, `bundle`). It
adds registry profiles that inherit the source image, `state_group`,
project-root markers and mode, shared volumes, and environment policy, then
creates Windows shims — `cowsay.exe`, `stringer.exe`, `just.exe`, `ruff.exe`,
`dotnet-ef.exe`, or `rake.exe` appears on PATH without Node, Go, Rust, Python,
.NET, or Ruby touching the host. To
expose a binary installed under the Node 22 runtime, use
`cb expose npm22 <binary>`; for `go install` output, use
`cb expose go <binary>`; for `cargo install`, use
`cb expose cargo <binary>`; for `uv tool install`, use
`cb expose uvx <binary>`; for a pipx application, use
`cb expose pipx <binary>`; for a global .NET tool, use
`cb expose dotnet <binary>`; for a Ruby gem executable, use
`cb expose ruby <binary>`.

Managed-store discovery uses the already-locked local source image with pulls
and networking disabled, a read-only container root, an explicit shell
entrypoint, and read-only mounts for the selected store and any required
companion volume. The source image must provide a POSIX-compatible `sh`;
distroless images without one cannot use automatic discovery. Discovery never
mutates package-manager state.

For a custom stateful profile, `cb expose --shared-file TOOL VOLUME FILE`
selects one logical name from that profile's `shared_volumes` and one absolute
container file beneath the selected mount. The file's basename becomes the
Windows shim name. ContainerBin does not search other volumes or `PATH`, and it
rejects traversal, mount escapes, invalid/reserved or flag-shaped shim names,
directories, symlinks (including parent-directory escapes), and files without
the executable bit. Discovery requires the named
Docker volume and source image to exist already, mounts only that volume
read-only, disables networking, prevents image pulls, and runs with a read-only
container root. The source image must provide a POSIX-compatible `sh`;
distroless images without one cannot use automatic discovery. The generated
profile then inherits the same deliberately limited source fields described
below. A volume mounted at one of the supported package-manager store targets
must use the normal `cb expose TOOL [BINARY ...]` mode so its store-specific
companion-volume and ownership checks still apply.

That inheritance set is deliberately fixed. Generated profiles do **not** copy
the source's `project_volumes`, `host_mounts`, or `cwd_mode`; a custom source
using those fields must treat the generated profile as a separate access policy
and edit it explicitly before use. In particular, host access never propagates
implicitly to an auto-generated shim, as described above. Source-command
argument rules (`args_prefix` and `path_*`) and default-family metadata are not
copied either: an exposed binary has its own argv semantics and is always a
concrete profile, never a runtime alias.

With no binary arguments, every valid executable in that source store is
considered. Explicit names are safer on a long-lived store. Invalid and
reserved Windows shim names are ignored, and names that differ only by case
fail closed because they cannot coexist on Windows. When explicit names are
given, each name that is not present in the selected store is reported instead
of being silently ignored alongside successful matches.

Generated profiles carry `role = "exposed"` as explicit provenance.
`cb unexpose` requires that marker plus a matching command/name and containment
beneath a declared shared-volume mount. Known package-manager stores also have
to satisfy their store-directory and companion-volume rules, so a hand-authored
profile is not deleted merely because its command resembles generated state.

Exposed profiles are keyed by binary name only, so a binary already exposed
from one runtime cannot also be exposed from the other under the same name —
`cb unexpose` it first if you need to switch which runtime backs it.
`cb unexpose cowsay` removes the shim and profile without deleting the
underlying npm state. Registry mutations are validated and written atomically;
a failed validation refuses the update.

## Image locking and explicit updates

```powershell
cb lock          # pull registry images and write the complete lockfile
cb lock --check  # verify lock completeness and local availability
cb lock --local mytool  # lock mytool's current image ID; repeat for more local tools
cb update jq     # explicitly refresh one image
cb update --all  # explicitly refresh everything
cb update --local mytool     # switch/refresh one entry as a local image ID
cb update --registry mytool  # switch/refresh one entry from its registry
```

Runtime behavior is fail-closed:

- no lockfile → backwards-compatible UNLOCKED mode;
- lockfile present → exact `repository@sha256:digest` or local `sha256`
  image-ID execution;
- an image configured in the registry but missing from the lock → execution
  **fails** and asks for `cb update TOOL` or `cb lock`.

Tools sharing an image share one lock entry. The Node 24 family
(`node24`, `npm24`, `npx24`, its aliases when selected, and anything exposed
from `npm24`) rides the single `node:24-slim` entry. The Node 22 family
(`node22`, `npm22`, `npx22`, its aliases when selected, and anything exposed
from `npm22`) rides a separate `node:22-slim` lock entry.

Use `cb lock --local TOOL` for an image produced by `docker build -t` or loaded
from an archive. The option is explicit because current Docker engines can
report `RepoDigests` for both local and pulled images; ContainerBin refuses to
guess which identity you intended. Repeat `--local TOOL` for each local image
in a full lock operation. Because locks are keyed by configured image, related
tools sharing that image switch together.

Plain `cb update TOOL` preserves an existing entry's identity mode: rebuild the
same local tag, then update to record the new ID. If that tag is missing, the
update fails instead of silently pulling a registry image. Use the explicit
`--local` or `--registry` override to switch modes. Images with non-matching,
foreign `RepoDigests` still fail closed in registry mode; ContainerBin does not
guess that a retagged registry image should be treated as a local build.

An image-ID lock is deliberately host-local: it makes execution immutable on
that Docker daemon, but it does not make the image portable or pullable. Keep
the Dockerfile/build inputs or export the image separately for recovery.

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

# Explicit, checksummed named-volume backup (names come from `cb state`)
cb backup BACKUP.zip --state cb-node24-npm-global cb-go124-gobin
cb restore BACKUP.zip --state           # validate/check destinations only
cb restore BACKUP.zip --state --apply   # restore state, then registry/lock
```

State backup never sweeps Docker volumes. Every selected name must carry
consistent `cb.managed`, kind, owner, and project-identity labels, and no running
container may mount it. The versioned manifest records those labels plus each
tar stream's size and SHA-256. Restore revalidates every archive before changing
Docker, never remaps a project path, and refuses label-mismatched or non-empty
destinations. Tar extraction happens only inside a network-disabled helper
container with a read-only root; no archive path is extracted onto Windows.
Keep ContainerBin tool invocations stopped for the entire restore: the command
rechecks running volume users immediately before import, but no filesystem API
can reserve a Docker volume against a new container starting in the remaining
check-to-extract window.

The immutable helper image must already be local; ContainerBin never pulls it
as a backup side effect:

```powershell
docker pull docker.io/library/alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40
```

Backups do not include Docker images, registry credentials, Docker Desktop
configuration, or host project files. Output archives are created exclusively:
choose a new filename instead of overwriting an existing backup. Volume data can
itself contain package credentials or other secrets, so store and transfer the
archive as sensitive data even though ContainerBin requests owner-only file mode.

See [proxies, private registries, and air-gapped operation](docs/proxy-airgap.md)
for mirror identity rules, disconnected image preparation, and the complete
state-backup boundary.

## Concurrency and the mutation lock

`cb install`, `cb add`, `cb setup`, `cb backup`, `cb restore`, `cb expose`, `cb unexpose`,
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
                       # python /venv persistence, external path mapping, Node 24/22
                       # project state, jq relative paths, terraform -chdir normalization
cb trace ...  # dry-run argv/mount mapping for one command
cb inspect TOOL
cb env
cb list
cb default
cb default set node 22
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
failure. The Node 22 checks run when the `node22` profile is registered. An
older registry without that newer default gets an actionable skip rather than
a false failure; run `cb setup` to append the current default profiles.

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
  "passed": 15,
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
one is missing. The `checks` array always contains exactly these 15 IDs, in
this order: `docker`, `python-image-local`, `python-persist-write`,
`python-persist-read`, `python-external-path`, `node-image-local`,
`node-modules-write`, `node-modules-read`, `node22-image-local`,
`node22-modules-write`, `node22-modules-read`, `jq-image-local`,
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
that pipeline are in [docs/shell-contract.md](docs/shell-contract.md). Startup
benchmark methodology and the disposable-container tradeoff are in
[docs/performance.md](docs/performance.md).

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
- `cb expose` supports the npm global prefix, Go's shared `/go/bin`, Cargo's
  managed install root, uv's tool bin, pipx's managed application bin, .NET's
  global tool home, and RubyGems executables, plus an explicitly named
  executable beneath any declared shared volume; plain pip project environments
  and unmanaged pipx stores are not supported.

## Roadmap

Larger ideas (more package-manager integrations, self-update, signing) are
tracked in the
[roadmap issue](https://github.com/AviBackToBlack/container-bin/issues/2).
Accepted product/security dispositions and the current implementation queue are
recorded in
[docs/roadmap-decisions.md](docs/roadmap-decisions.md).
Detailed entry gates, implementation requirements and acceptance evidence for
the remaining actionable items are in
[docs/roadmap-implementation-requirements.md](docs/roadmap-implementation-requirements.md).
The `main.go` decomposition listed here previously is done; see
[docs/architecture.md](docs/architecture.md) for the resulting package layout.

## Contributing & license

Contributions welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) (containerized
build/test instructions; no Go installation required) and
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

Licensed under the [Apache License 2.0](LICENSE).

## Release verification

Release binaries are built by a tag-triggered GitHub Actions workflow with
`SHA256SUMS` checksums and GitHub build provenance attestation. The checksum
can detect accidental corruption or a mismatched download, but the manifest is
not itself an authentication mechanism. Compare the downloaded binary hash
with the `cb.exe` entry in `SHA256SUMS`:

```powershell
Get-FileHash .\cb.exe -Algorithm SHA256
```

Then establish artifact authenticity by verifying its GitHub provenance
attestation:

```powershell
gh attestation verify cb.exe --repo AviBackToBlack/container-bin
```
