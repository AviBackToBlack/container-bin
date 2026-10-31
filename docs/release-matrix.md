# Windows and WSL2 / Docker Desktop release validation matrix

This is the **manual execution procedure** for
[Release Qualification Policy v2](release-qualification-policy.md).
It validates real release artifacts on real Windows 11 / Docker Desktop hosts;
CI alone cannot establish those runtime results. The strict forensic
qualification driver is optional, not a prerequisite.

## 1. Lock the candidate and supported test environments

Record the candidate tag, source commit, published artifact SHA-256 and signed
tag / GitHub attestation verification. Consult
[release-toolchain.md](release-toolchain.md) for mandatory CI, security scanning
and reproducibility gates. Do not treat a source-built binary as the published
candidate.

The Windows runtime matrix has **three shells × two Docker Desktop versions**.
All cells use Windows 11 x64, Linux-containers mode and a genuinely available
local Engine.

| Host for v2.1.0 | Docker Desktop version | Windows PowerShell 5.1 | PowerShell 7.x | cmd.exe |
| --- | --- | --- | --- | --- |
| Z | 4.97.0 | Required | Required | Required |
| Z-LAPTOP | 4.96.3 | Required | Required | Required |

The owner froze these versions for the v2.1.0 qualification cycle.
For future cycles, use the current and immediately previous stable versions
from [official release notes](https://docs.docker.com/desktop/release-notes/)
at the qualification cutoff and record their full builds. Do not chase new
Desktop releases in the middle of the frozen cycle.

Windows containers are not supported. Windows 10 is mentioned in the README,
but this qualification matrix specifically covers Windows 11; do not assert a
new Windows 10 Docker Desktop cell without evidence. Windows ARM64 is CI and
release-artifact qualified only, **not** real Docker Desktop E2E supported.

## 2. Short preflight per host

Before running product tests, verify and record:

1. Windows 11 build, actual Docker Desktop build, Engine and local context;
   Docker OSType is `linux`. Confirm the Engine answers fresh requests;
   do not confuse stale cached `docker info` with a live daemon.
2. PowerShell 5.1, PowerShell 7 and `cmd.exe`; enough disk space and a
   writable disposable local test directory.
3. The exact **published** candidate binary hash and expected tool images.
   Authenticate and pull required pinned images before the release self-test.
4. Resource Saver/power settings that would interrupt execution are disabled
   for the test period.
5. No conflicting pre-existing test volumes/installations. Record initial
   containers, volumes, networks, relevant images, persistent User/Machine
   PATH and protected ContainerBin state.

Use normal Windows/Docker commands and existing repository scripts. Preparation
does **not** require private execution manifests, preauthorized frozen
command/volume plans, backend PID tracking or an additional qualification
framework. Unknown user resources are protected and must not be deleted.

## 3. Required steps in **each** Windows shell cell

Use a fresh private install of the exact authenticated candidate in each cell;
record the actual shell executable/version, Desktop version/build and local
test directory. Run:

```text
cb version
cb setup
cb doctor
cb self-test --release --json
```

These represent commands invoked through the installed candidate/shims from
the shell under test. Preserve stdout, stderr, exit codes and the sanitized
self-test JSON (not raw credentials).

**PASS** requires all 15 checks (see
[README report schema](../README.md#cb-self-test---json-report-format)) to have
`status=pass`, `passed=15`, `failed=0`, `skipped=0`, `ok=true`;
capture the seven ordered `environment` facts. Those informational facts do
not affect the 15-check totals; inspect failures, warnings and unavailable
facts independently. A `cb doctor` warning can be accepted only when its
cause is documented and does not compromise a supported configuration.

Stock self-test uses randomly generated disposable project paths. It is not
claimed to satisfy any private frozen-plan bridge-attachment contract.

After each cell, remove only resources proven created by that cell, then record
actual remaining containers, volumes and protected-state differences.
A successful self-test **is not** a full preservation or backup/restore PASS.
Report functional and cleanup results separately.

Record the six independent cell results in the
[release qualification issue](../.github/ISSUE_TEMPLATE/release-qualification.yml).

## 4. One complete provider/state suite **per Docker Desktop version**

In one shell of choice on each of the two hosts, using the same published
candidate and fresh disposable projects, perform:

- Python new venv and legacy symlink-based venv after a real provider call;
  marker-selected project state and marker-free compatibility state.
- `cb backup` state round trips with checksums; refusal of restore into a
  nonempty destination; successful restore into absent/empty test-owned state;
  verify an installed package import afterwards. Reject absolute/escaping links
  and a failed backup that appears successful. Preserve the original archive
  hash until the round trip finishes.
- pipx disposable app install, both `cb expose pipx APP` and store-wide
  `cb expose pipx`, managed shim invocation and `cb unexpose APP`.
- Node stateful execution and persisted state; representative jq and Terraform
  stateless commands, e.g. `terraform -chdir=.\\tf validate`.
- stdin/EOF, separately captured stdout and stderr, nonzero exit codes, PTY
  where applicable, and relevant negative path/ownership/security cases.

The README [state/backup section](../README.md#backup-and-restore) and
[shell contract](shell-contract.md) define actual command behavior. Use only
disposable owned volumes, especially for fixed-name compatibility state. If
such a volume is occupied by protected user data, qualify in a genuinely
isolated environment; do not adopt or wipe it.

Record each scenario's real PASS/FAIL/BLOCKED/NOT RUN result, final cleanup,
and the exact test state left behind, if any. A broken state backup/restore
blocks the release even if stock self-tests are green.

## 5. One native WSL2 cell on the **current** Desktop version

Use the authenticated published `container-bin-VERSION-linux-amd64.tar.gz`
artifact, verify its checksum and GitHub provenance, and install it on a
normal isolated user distro with Docker Desktop integration (not an internal
Docker distribution). Use a short disposable distro name and check the
**complete** generated Unix socket pathname length before import/start.

Exercise both a distro-local project and one on a proven default Windows-drive
mount. Verify:

- `cb version`, `cb wsl prepare --apply`, `cb wsl install --apply` and
  installed `~/.local/bin/cb version`; `cb wsl lock --check` and representative
  stateless and stateful shims.
- Piped stdin/EOF; stdout/stderr separation; exact nonzero exit codes;
  true PTY size, SIGWINCH resize and SIGINT forwarding.
- Project-specific and shared volume persistence/isolation between the two
  projects; fail-closed mixed/unsafe/external path boundaries.
- Interrupt a disposable shim after container creation, then verify
  `cb wsl cleanup --check` identifies the orphan and
  `cb wsl cleanup --apply` removes only that proven object.
- Final zero disposable namespace residue and no changed/adopted native
  Windows user volumes.

Record Windows/Desktop/Engine/distro/kernel versions. The earlier source-built
WSL activation record is useful background, **not** a substitute for this
published-candidate cell. Do not claim unsupported ARM64 runtime coverage.

## 6. One published self-update cell on Windows amd64

Use an isolated install of the **actual published** previous supported release
(`v2.0.1` for the v2.1.0 candidate). Update it to the authenticated published
`v2.1.0-rc.1` via the documented explicit self-update mechanism.
Verify source/target hashes, release selection, verified download/attestation,
transactional replacement, correct final version, shims/hardlinks and absence
of temporary update/rollback/lock residue. Check a representative failure
/refusal/rollback path without risking an existing user installation.

The existing `scripts/qualify-self-update-published.ps1` builds *current
source* with an injected earlier version, then updates that simulated older
binary to the published target. It is valuable **supplementary** coverage of
the update transaction, but is **not the actual previously released v2.0.1
executable**. Report this distinction. A genuine published-to-published
transaction is mandatory.

Before stable publication, separately verify the final draft stable
artifact's version-dependent behavior and own supply-chain identity.

## 7. Preservation, evidence and final decision

On each host compare baseline and final: Engine/Desktop, containers, named
volumes, networks, relevant images, protected ContainerBin installations and
persistent User/Machine PATH. Confirm no unauthorized user state changes and
no leftover **owned** test containers/volumes. Record permitted downloaded
images and documented changes; do not infer ownership from a volume name.

This is endpoint-based preservation, **not** a claim of continuous forensic
event coverage. A historical forensic STOP remains recorded but does not
create an additional release gate under Policy v2. The existing
`scripts/qualification-preservation.ps1` is an **optional diagnostic** library;
neither it nor any private 75-command/21-volume driver is needed to qualify
the shipping CLI.

Per-host reports should be compact and sanitized: exact binaries, three shell
outcomes, the host's state suite, baseline/cleanup reconciliation and
remaining blockers. Consolidate supply-chain, six Windows cells, two state
suites, WSL2 and self-update. Use **PASS / FAIL / BLOCKED / NOT RUN** honestly.

**GO requires all applicable mandatory gates PASS.** A genuine product or
data-safety failure blocks release; a tooling/environment BLOCKED result is not
proof of product failure or permission to claim PASS. Merge, stable tag and
publication remain separate owner-authorized decisions.
