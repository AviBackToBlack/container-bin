# Windows and WSL2/Docker Desktop release validation matrix

This is the manual release-qualification matrix for ContainerBin on Windows
and its native WSL2 frontend.
It is what `docs/shell-contract.md` and `docs/windows-paths.md` are validated
against on a real host: a set of documented steps a maintainer runs before a
release because GitHub-hosted CI cannot cover this ground.

The exact build toolchain and packaged-binary vulnerability gates are defined
in [release-toolchain.md](release-toolchain.md). A compiler upgrade requires
fresh qualification of its actual candidate outputs; a prior release's green
matrix is not evidence for binaries built with a different standard library.

The roadmap text for RM-7b is:

> Define and maintain the validation matrix: Windows 11; Windows PowerShell 5.1;
> PowerShell 7.x; `cmd.exe`; representative Docker Desktop versions;
> Linux-containers mode. GitHub-hosted CI should continue covering pure/unit
> logic and Windows compilation, but should not pretend to provide full Windows
> + Docker Desktop E2E coverage. Prefer a documented manual release matrix first;
> a self-hosted Windows runner only if the maintenance cost is justified. Natural
> consumer of RM-5e.

This document implements the "documented manual release matrix first" part. It
does not scope, design, or commit to a self-hosted Windows runner.

## What the matrix covers

### Precondition: Linux-containers mode

Linux-containers mode is a **precondition**, not a matrix axis. The README
[Platform support](../README.md#platform-support) table already lists Windows
containers as "not supported", and `cb doctor` fails its Docker OS-type check if
Docker Desktop is in Windows-containers mode (the same verdict logic reports as
the `docker-os-type` ID in `cb self-test --release`'s environment facts). Every
cell below assumes Linux containers; there is nothing to vary.

### Host OS

**Windows 11** — the RM-7b text names Windows 11 specifically. The README
platform-support table currently says "Windows 10/11" more broadly; this
is a discrepancy between the two documents and this doc flags it rather than
silently resolving either one.

### Shell

The three shells are:

- **Windows PowerShell 5.1** (`powershell.exe`, the in-box legacy shell)
- **PowerShell 7.x** (`pwsh`, PowerShell Core)
- **`cmd.exe`**

These are the exact three named in the RM-7b text and already used as the
placeholder value in `.github/ISSUE_TEMPLATE/bug_report.yml`:

```yaml
      placeholder: "PowerShell 7.4.5 / Windows PowerShell 5.1 / cmd.exe"
```

### Docker Desktop version

Use two representative versions, determined at qualification time:

1. **the current stable Docker Desktop release**, and
2. **the previous stable Docker Desktop release still available from the
   official release notes**.

Specific version numbers (e.g. `4.32`) are deliberately not hardcoded: this doc
has no CI that would catch drift, so a pinned number would be stale within one
release cycle. The person qualifying the release looks up the current and
immediately preceding versions in Docker's
[release notes](https://docs.docker.com/desktop/release-notes/) and records their
exact version and build numbers in the issue template.

### Grid

That is 3 shells × 2 Docker Desktop versions = **6 cells**, all on Windows 11,
with Linux-containers mode as a shared precondition.

| Host OS | Shell | Docker Desktop version |
|---|---|---|
| Windows 11 | Windows PowerShell 5.1 | current stable |
| Windows 11 | Windows PowerShell 5.1 | previous stable |
| Windows 11 | PowerShell 7.x | current stable |
| Windows 11 | PowerShell 7.x | previous stable |
| Windows 11 | `cmd.exe` | current stable |
| Windows 11 | `cmd.exe` | previous stable |

## Per-cell procedure

### Preservation evidence on a persistent qualification host

The clean-host procedure below does not authorize adopting an existing install,
volume, account or protected path. A persistent host needs an explicitly approved
isolation and preservation plan. An occupied fixed compatibility store remains
blocked. Do not relax an invariant to obtain a green cell.

Use `scripts/qualification-preservation.ps1` as a dot-sourced evidence library.
Importing it does not contact Docker, capture host data or write files. It has no
wake, restart, cleanup, qualification, automatic reconnect or rebaseline command.
Its offline suite uses only synthetic fixtures and requires neither Docker nor
network access. This library is **not** a release-qualification runner: the
approved run driver must supply full inventories, receipt verification and
continuous observer records. Missing inputs must STOP the run.

1. Finish offline preparation first. Retain a separate cold inventory and exact
   authorization digest. If a wake is needed, obtain the applicable authorization
   and retain its original operation receipts. Do not assume read-only Docker
   commands wake the Engine: Desktop can serve cached responses in Resource Saver.
2. Establish live readiness on the expected context, endpoint, Engine ID and
   version. `Get-QPEngineObservation` captures one direct named-pipe `/info`
   response and one independently started CLI `info` response, with raw bodies,
   HTTP bytes, executable identity, host UTC, monotonic ticks and process/request
   provenance. It rejects Docker environment overrides and context/endpoint drift.
   Call it before and after inventory capture, separated by at least one second;
   pass the concatenated observations to `Set-QPWarmBaseline`. Both transports
   must advance consistently. A constant guest clock offset alone is not a
   failure; frozen body time, conflicting transports, reused requests or ambiguous
   timing are. HTTP Date, ping success and CLI exit zero alone prove no freshness.
3. Reconcile the cold inventory with protected resources while live. An explicitly
   receipt-bound prebaseline default-bridge ID/Created transition can be recorded
   separately; its configuration and every other network must match exactly.
   This is a recorded lifecycle transition, **not a preservation PASS**. Missing
   wake authority or any other protected change means STOP. Capture exactly one
   warm baseline after reconciliation; never discard the cold record.
4. Observe the entire interval from before baseline through final reconciliation.
   Supply the required `ObservationStartTicks` to `New-QPPreservationSession`
   before the first live-readiness request and warm inventory capture. Any
   authorized wake must be completed and recorded before this strict interval.
   Coverage checks start at that recorded tick, not at baseline completion;
   lifecycle/protected-object events during readiness or capture therefore STOP.
   The driver needs container and network event streams plus independent backend
   lifecycle/cache observation. Engine ID or Desktop backend PID alone does not
   identify a continuously running daemon epoch. Record raw event/log sources,
   observer process identities, continuous coverage intervals, stream exits,
   deadlines, log rotation/truncation and every gap. Do not infer continuous
   coverage from matching endpoint inventories or an existing summary file.
5. At quiescent checkpoints, bracket inventories with new live observations and
   call `Assert-QPPreservation`. All baseline network identities, including
   `bridge`, must remain identical throughout the interval. Identical network
   configuration does not prove unchanged identity. Receipt-verified test-object
   events are the only allowed resource events; no events on protected objects
   are authorized. Unexpected lifecycle/cache transitions, changed bridge IDs,
   interrupted/missing observers, missed coverage, unknown objects or uncertain
   results mean STOP. Never silently reconnect, wake, resume or create a new
   baseline after drift. A subsequent run requires separate authorization.
   Terminal session state and the first STOP reason are retained unchanged even
   if an accidental later call attempts to resume or recapture a baseline.

Keep checks strict for all existing image identities/references/configuration,
network identities/configuration/attachments, volume creation identity/full
labels/driver/scope/options, containers including stopped mount references,
protected installations/registries/locks/shims, ACLs, settings and persistent
PATH. The library compares full supplied protected inventories; it does not
discover every installation or certify volume contents. Retain the discovery
scope and content checks required by the approved plan. Collect full inventories
before classifying any resource as test-owned. Do not filter by name prefix or
silently omit unknown resources. Quiescent checkpoints require test resources
already reconciled under their separate receipts; this library grants no removal
authority. Reconcile cleanup and preservation as separate outcomes.

#### PATH and private evidence

`Get-QPPathSnapshot` retains exact process and unexpanded registry User/Machine
PATH values, presence, registry kinds, UTF-8/no-BOM hashes and ordered segments.
Present values must be strings; User/Machine kinds must be String or ExpandString.
MultiString, Binary, DWord/QWord and other kinds STOP without being coerced into
fabricated raw text. Public clock arguments are validated as integral values
before any conversion; fractional caller ticks are never rounded into acceptance.
Empty entries, duplicate entries, whitespace and spelling remain significant.
An absent value has `present=false`, null raw/hash and no segments; a present
empty value retains its empty-string hash and one empty segment. They differ.
It records UTC, coordinator PID/start time, executable/hash, SID, CWD, shell/profile
mode, command line and observed parent provenance. Supply the actual launcher
recipe and intentional child-environment overlays; ancestry alone cannot recover
them. Command lines and environment values may contain secrets.

Compare the coordinator before/after in the same demonstrably consistent process
context using `Assert-QPPathPreserved`. A process restart or different launcher is
not an automatic exception. Missing historical raw PATH, incomplete provenance,
hash disagreement or differing persistent values must never yield PASS. Keep
intentional child PATH overlays separate using `Get-QPChildEnvironment`; it
returns a recipe without modifying the parent or launching a process. Do not
normalize PATH or exclude Codex-related entries to excuse a mismatch.

Provision an owner-private evidence directory outside the repository in advance.
`Write-QPPrivateEvidence` requires local drive-letter paths and owner/SYSTEM-only
ownership/access grants, rejecting explicit Administrators grants as well as
other identities. Windows privileged administrators can still take ownership;
this check is not protection against a compromised privileged host. The writer
pins every directory ancestor with native handles that deny delete/rename
sharing, rejects repositories and reparse ancestors, uses exclusive `CreateNew`
writes, and records SHA-256 payload and
previous-record hashes. It does not change ACLs. Retain each returned record
digest and independently seal the final anchor; `Assert-QPPrivateEvidenceChain`
checks an explicit ordered record list against that anchor. Hashes detect changes
relative to the retained anchor; they do not authenticate an untrusted collector.
Never upload raw host/environment evidence as CI artifacts or commit it. Public
summaries should contain only deliberately reviewed, sanitized results/digests.
Append new records; never edit sealed evidence or reclassify historical FAILs.

#### Driver data contract

The library accepts dictionaries/JSON data, retaining ISO timestamp labels as
strings on PowerShell 5.1 and 7. Converted DateTime values are rejected rather
than silently reserialized. Dictionary keys are case-sensitive, arrays retain
order, and `[]` differs from `[null]`. Canonical v1 records support integral Int64
numbers only: fractional/out-of-range values STOP rather than acquire different
hashes under the two JSON parsers. Unmodified raw source JSON remains retained.
The driver must order inventory arrays
deterministically by exact object identity without dropping fields or objects.

- Identity fields: `context`, `endpoint`, `engine_id`, `version`.
- Inventory fields: `engine` (that identity), `images`, `networks`, `containers`,
  `volumes`, `protected_host` (including `path`), and `capture_window` with
  `start_ticks`, `end_ticks`, `frequency` from the coordinator monotonic clock.
  Capture starts after the first pipe/CLI pair and finishes before the second.
- Optional wake receipt: exact `before_networks`/`after_networks`,
  `authorization_sha256`, and `live_verified` based on the retained wake evidence.
  This is supplied proof, not permission for the library to wake anything.
- Coverage: one record each for `container`, `network`, `lifecycle`, including
  `observer_id`, `provenance` (PID/start/executable digest/source), `start_ticks`,
  `end_ticks`, `complete`, `exit_reason`, `gaps`, `events`, plus exact `raw` record
  JSON and `raw_sha256`. Raw records must bind those fields to retained source
  evidence; a hash or caller assertion alone does not establish observation.
  A successfully finalized stream uses `coordinator-finalized`, never EOF or a
  deadline. Resource events include `ticks`, exact `object_id` and
  `receipt_verified`; every event needs an integral coordinator `ticks` value
  before window filtering. Missing/null/ambiguous ticks STOP, even if the event
  could otherwise appear outside the window. Lifecycle events inside the window
  always STOP. The driver
  must derive these records from actual uninterrupted observers, not manufacture
  empty records from endpoint snapshots. The synthetic test builders are not a
  production observer and must never be used to claim live coverage.

Record the driver revision/hash, authorization, source/candidate identity, all
capture/observer provenance, state transitions and final outcomes in the private
append-only packet. These helpers validate supplied evidence; they are not a
security boundary against a dishonest or compromised driver.

#### Immutable RC versus harness revision

Published `v2.1.0-rc.1` is built from
`84b5ff7f3fc7e5c237616a324359704a6db6bb52`. These later harness/documentation/CI
changes do not modify that release, its assets, provenance or historical results.
Future qualification must separately record the exact published candidate hashes
and the harness commit used. A future stable source containing this revision is
a different source tree; do not claim it is byte-identical to the RC or reuse RC
qualification as proof for a different candidate. Follow the release gates for
the actual stable source and artifacts.

### Commands in each cell

For each cell, on a clean Windows 11 host with Docker Desktop in Linux-containers
mode:

1. Run `cb setup` from a fresh shim install. This re-creates the default registry
   and `cb.exe`-hardlink shims.
2. Run `cb doctor`. The output must be clean, or must show only `warn` verdicts
   that are already understood and documented (for example, a reparse-point or
   mapped-drive warning from the checks in `docs/windows-paths.md` that does not
   actually block the tool invocation).
3. Run `cb self-test --release`. This uses already-local locked images and
   reports every check. Its output is documented in README's
   [`cb self-test --json` report format](../README.md#cb-self-test---json-report-format)
   section: 15 `checks` IDs, 7 `environment` IDs, the `pass`/`fail`/`skip`
   vocabulary, and the `skip`-vs-`warn` message-prefix distinction. See that
   section for the full schema rather than restating it here, because the schema
   is the kind of detail that drifts.
4. (Recommended) Run one or two real smoke-test commands that actually exercise a
   `docker run` through a shim, from the shell under test. The README's own
   quickstart demonstrates exactly these:

   - `pip install requests`
   - `terraform -chdir=.\tf validate`

   When qualifying a release that changes managed application exposure, also
   exercise the pipx path end to end: install a disposable app, run both
   `cb expose pipx APP` and store-wide `cb expose pipx`, invoke the generated
   shim, then remove it with `cb unexpose APP`.

   `cb self-test` alone does not fully replace this, because it uses its own
   temp project volumes and already-local locked images rather than a first-run
   user project.

5. Require real Python state backup/restore round trips in both marker-selected
   project state and marker-free compatibility state. Use only fresh,
   disposable volumes: if the fixed compatibility name already exists, this
   part requires an empty independent Docker environment, not adoption or
   deletion of normal state. Cover a newly created venv and a legacy
   symlink-based venv after one invocation of the candidate's Python provider.
   Record the selected volume's exact labels and the untouched backup hash;
   require backup success, refusal to restore over non-empty state, then
   successful restore into an absent/empty test-owned destination and a
   working import of installed package state. Reconcile/remove only the proven
   disposable resources. Also require that arbitrary absolute/escaping links
   fail closed and a failed backup leaves no purportedly successful output.

   A 15/15 release self-test does **not** establish these round trips. An
   untouched Python-state backup rejected by restore blocks the release even
   when the base matrix is green.

Record the results in a release-qualification issue using
[`.github/ISSUE_TEMPLATE/release-qualification.yml`](../.github/ISSUE_TEMPLATE/release-qualification.yml).

## What CI already covers vs. what only this matrix proves

The Linux verification job additionally runs an opt-in real Docker regression
using pinned Python/helper images and fresh, ownership-checked project and
compatibility volumes. It covers new/legacy/partially migrated venv round trips,
non-empty refusal, package preservation, stdin/EOF, split streams and exit
codes. This is Linux Docker Engine evidence, not Windows shell, Docker Desktop
or native WSL qualification. The ordinary Go suite skips that integration
fixture unless `CB_STATE_DOCKER_TEST=1`; CI explicitly enables it in its
dedicated step and fails if an expected disposable volume already exists.

The x64 `test-windows` job in `.github/workflows/ci.yml` is equivalent to the
following example. For a self-contained snippet, `GOTOOLCHAIN: local` is shown
at job level; the actual workflow sets it once at workflow level for every job.

```yaml
  test-windows:
    name: Test and build (Windows)
    runs-on: windows-latest
    timeout-minutes: 20
    env:
      GOTOOLCHAIN: local
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version-file: .go-version
          check-latest: false
      - name: go test
        run: go test -v ./...
      - name: Test benchmark comparison (Windows PowerShell 5.1)
        run: powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File .\scripts\test-compare-startup-benchmarks.ps1
        shell: pwsh
      - name: Build cb.exe
        run: go build -trimpath -o cb.exe .
      - name: Smoke-test version output
        run: |
          $v = .\cb.exe version
          Write-Host $v
          if ($v -notmatch '^container-bin \S+') { exit 1 }
        shell: pwsh
```

That proves the following, and no more:

- It runs on `windows-latest`. GitHub's own `actions/runner-images`
  documentation describes `windows-latest` as a Windows Server runner image,
  not a Windows 11 client install.
- The management smoke test uses `pwsh` (PowerShell 7.x / PowerShell Core). A
  dedicated benchmark-comparison fixture is also executed by Windows PowerShell
  5.1 (`powershell.exe`), but CI does not invoke ContainerBin tools through that
  shell and does not exercise **`cmd.exe`**.
- It has **no Docker Desktop** and makes **no `docker run` call**. `go test ./...`
  on Windows exercises pure logic and any `runtime.GOOS == "windows"`-gated
  unit tests — `docs/windows-paths.md` notes that its Windows-gated tests are
  what runs here — not an actual containerized tool invocation.
- The "Smoke-test version output" step only proves the binary starts and
  dispatches `cb version`; it never reaches `runTool` or `docker run`.
- It does not vary Docker Desktop version, the Linux-versus-Windows-containers
  mode switch, or real bind-mount/volume behavior.

The separate `test-windows-arm64` job runs on GitHub's native
`windows-11-arm` runner. It asserts both the process and Go toolchain report
ARM64, runs the full unit suite, builds a release-style `cb.exe`, and dispatches
a copied `jq.exe` shim to a controlled, compiled `docker.exe` stub. That proves native
management execution, argv[0] shim dispatch and tool exit-code propagation on
ARM64 hardware. It still has no Docker Desktop engine, so it does not qualify
bind mounts, volumes or providers. The release workflow separately
cross-builds an architecture-specific ARM64 executable and archive, reproduces
both byte-for-byte on an
independent runner, checksums them and includes them in release provenance.
That supply-chain coverage is not a Windows ARM64 support claim. Support remains
gated on real Windows ARM64 + Docker Desktop E2E evidence.

The self-update pipeline selects the ARM64 archive from native `GOARCH`,
verifies its canonical three-entry checksum manifest and GitHub provenance,
then extracts only the exact `cb.exe` entry inside the private staging
directory. The final apply path copies the proven installed binary into a
protected wait helper, repeats verification after the parent exits, and applies
the rollback-safe complete managed-set transaction. Hosted CI qualifies that
control flow and native process boundary, not Docker-backed runtime behavior or
real published-release E2E.

So CI validates compilation and pure/unit logic on a GitHub-hosted Windows
runner. The matrix is what validates the `docs/shell-contract.md` semantics on a
real Windows 11 + Docker Desktop host before a release.

### Native WSL release artifact

V2 release candidates add one deterministic
`container-bin-VERSION-linux-amd64.tar.gz` archive containing exactly `cb`,
`LICENSE` and `README.md`. The release workflow executes the contained binary,
checks its injected version, binds it in the separate one-entry
`SHA256SUMS-WSL` manifest, independently reproduces it byte-for-byte and attests
it with the Windows assets. The existing three-entry Windows `SHA256SUMS`
contract stays unchanged so released v1.x clients can self-update directly to
v2.

For the real WSL2 qualification cell, retain the archive checksum and
`gh attestation verify` output, extract it only on the distribution-local Linux
filesystem, run its `cb version`, and use that exact binary for
`cb wsl prepare --apply` and `cb wsl install --apply`. Confirm the installed
`~/.local/bin/cb version` matches before exercising managed tool shims. The
archive is not standalone-Linux evidence and does not qualify WSL on Windows
ARM64 hardware.

### Native WSL2 runtime cell

Run this cell in a normal user distribution with Docker Desktop WSL integration
enabled, never in Docker Desktop's private distributions. Record Windows,
Docker Desktop, Engine, distribution and WSL kernel versions plus the exact
candidate tag, source commit, archive checksum and attestation result.

Use both a distribution-local project and a project below a proven default
`/mnt/<drive>` mount. With the exact installed candidate:

1. Run `cb wsl lock --check` and representative stateless and stateful shims.
2. Verify piped stdin/EOF, separately captured stdout/stderr, a fast successful
   process and exact nonzero exit-code propagation in both projects.
3. In a real PTY, verify a positive initial size, a live `SIGWINCH` resize,
   SIGINT forwarding and the tool's resulting exit code in both projects.
4. Verify project volumes persist without colliding across the two project
   identities and shared state persists between them. Record `cb state`.
5. Verify mixed-storage, external, symlink/nested-mount and literal Windows
   path spelling fail before Docker mutation.
6. Kill one disposable shim after its container starts. Confirm
   `cb wsl cleanup --check` reports the exact orphan, `--apply` reconciles it,
   and a final check reports zero active/orphaned containers or lease residue.
7. Record the WSL namespace's exact volumes and prove that pre-existing native
   Windows ContainerBin volumes were neither adopted nor removed.

The v2 activation baseline passed on 2026-10-09 using Docker Desktop 4.94.0,
Engine 29.8.2, Ubuntu 26.04 and kernel
`6.18.33.2-microsoft-standard-WSL2`. That baseline activates the frontend; the
steps above still run against every exact release candidate and its published
artifact.

The [activation qualification record](qualification/wsl2-activation.md) contains
the source-build baseline and subsequent Windows preservation results. It does
not replace the published-candidate checks above.

### Live published-release self-update qualification

The repository includes a Windows/amd64 harness that builds the current source
with an injected earlier version, installs isolated hardlink and byte-identical
managed shims, and updates that private installation to an exact canonical
published release.

For a release candidate, set the source version to the previous supported
release and the target to the exact already-published candidate tag, then retain
the emitted JSON in the release-qualification issue:

```powershell
$previousVersion = 'v1.1.0'
$candidateVersion = 'v2.0.0-rc.1'
$env:GH_TOKEN = gh auth token --hostname github.com
pwsh -NoProfile -File .\scripts\qualify-self-update-published.ps1 `
  -FromVersion $previousVersion `
  -TargetVersion $candidateVersion `
  -GitHubCLI (Get-Command gh.exe).Source
```

Run it from the repository root. `go` may be a native executable or a
ContainerBin shim; the harness sets the intended repository working directory
and explicit Windows/amd64 build target in the same child process. The selected
GitHub CLI must be the official Authenticode-valid executable, and an explicit
`GH_TOKEN` or `GITHUB_TOKEN` is required. The harness never prints the token.

This exercises release selection, canonical bounded downloads, checksum and
GitHub build-provenance verification, the private helper handoff, repeated
verification after the parent exits, transactional replacement, bootstrap
version smoke testing, hardlink reconciliation for both originally hardlinked
and byte-identical shims, bounded child-process execution, and an exact
post-transaction survivor allowlist that catches staging/helper/rollback
directories, replacement temporary files and mutation-lock residue. It uses a
unique system-temp installation and removes only that validated path.

Qualification evidence on 2026-10-04: current `main` at `e625ab6` was built as
`v1.0.0` and updated to the canonical published `v1.1.0` release on native
Windows/amd64. The exact published asset (`cb.exe`, 3,383,808 bytes), checksum
manifest and GitHub Actions provenance passed; both managed shims were
reconciled as hardlinks and no private update artifact remained.

### Lower-level live attestation verifier check

`internal/selfupdate` includes an opt-in native-Windows integration test for the
external verifier boundary. It must be run with an Authenticode-valid GitHub CLI
and the published v1.1.0 `cb.exe` and `SHA256SUMS` assets:

```powershell
$env:GH_TOKEN = gh auth token --hostname github.com
$env:CB_TEST_REAL_GH = (Get-Command gh.exe).Source
$env:CB_TEST_RELEASE_CB = (Resolve-Path .\cb.exe).Path
$env:CB_TEST_RELEASE_SUMS = (Resolve-Path .\SHA256SUMS).Path
go test -run '^TestVerifyRealGitHubCLIRelease$' ./internal/selfupdate
```

The assets must come from the canonical v1.1.0 release URLs. The test exercises
the real argument surface and JSON shape, Authenticode publisher check,
fixed-directory and cancelable subprocess handling, environment allowlist,
checksum parsing, exact workflow-and-tag certificate identity, attestation
policy and post-verification re-hash. The token authenticates only to the fixed
`github.com` API; it is never printed or included in test output.

## Why not a self-hosted runner (yet)

RM-7b's text is explicit: start with a documented manual release matrix, and
consider a self-hosted Windows runner only if the maintenance cost of the manual
process becomes the bottleneck. This document is the manual process. It does not
scope or design a self-hosted runner.

## Where results are recorded

Open a release-qualification issue using
`.github/ISSUE_TEMPLATE/release-qualification.yml`. The template asks for the
`cb version` being qualified and one block per matrix cell, capturing:

- the Docker Desktop version used,
- the `cb doctor` output (sanitized),
- the `cb self-test --release` result, and
- any smoke-test commands run.
