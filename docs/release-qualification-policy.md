# Release qualification policy v2 — risk-based manual qualification

**Status:** adopted on merge of the reviewed policy-v2 change.
**Scope:** v2.1.0 and later releases targeting Windows and the native WSL2 frontend.
**Companion procedure:** [release-matrix.md](release-matrix.md).

## Purpose and evidence standard

Qualify the **actual shipping artifacts** for functionality, data safety, supported
environments and verifiable distribution. A private test harness is not a shipping
dependency or an independent release gate.

Use four precise verdicts: **PASS** (performed and assertions passed), **FAIL**
(required product/safety behavior failed), **BLOCKED** (could not determine
trustworthily), **NOT RUN** (not attempted). An environment or test-script
failure is not automatically a product FAIL; it does not become a PASS either.
Report functionality, preservation, cleanup and evidence completeness
separately.

A release is **GO** only when all applicable mandatory gates pass. Any mandatory
FAIL, BLOCKED or NOT RUN is **NO-GO**. A maintainer may propose an exception only
as an explicit, documented scope/claim change, never by relabeling evidence.
Merge, tagging and publication each retain the repository's separate approval
requirements.

## 1. Candidate identity and supply chain — mandatory

Record the immutable candidate tag, actual source commit, release asset digests,
signed tag/provenance, pinned toolchain, CI results and independent byte-for-byte
reproduction. Require the existing release workflow's source and packaged-binary
vulnerability gates, supported native platform CI and required security checks.
Unresolved release-blocking security findings prevent GO. A historical PASS for
another commit, compiler or binary is not qualification of the current candidate.

A prospective stable release from a different source tree must be evaluated
against its **own** source, versioned binaries, signatures, attestations,
checksums and reproducibility. If the difference is demonstrably limited to
qualification policy/documentation/harness (not shipping source, dependencies,
toolchain or runtime assets), unchanged-product functional evidence may be
carried forward with a documented source-diff analysis; version-dependent
behavior and new stable artifacts still need fresh verification. Never claim
different binaries are byte-identical.

Real Windows-on-ARM Docker Desktop runtime support is not claimed. ARM64 CI,
packaging, reproduction and update-selection tests are not a substitute.

## 2. Windows shell / Docker Desktop matrix — mandatory

Test Windows 11 x64, Linux-containers mode, all three shells (inbox Windows
PowerShell 5.1, PowerShell 7.x and cmd.exe) on **two** representative Docker
Desktop stable versions: current and immediately previous at the time the cycle
is frozen. Record full Desktop versions/builds; do not move the goalposts during
a qualification cycle because a newer Desktop build appears.

For the v2.1.0 cycle, the owner's frozen assignment is:

| Host | Docker Desktop | Shells |
| --- | --- | --- |
| Z | 4.97.0 | PS5.1, PS7, cmd.exe |
| Z-LAPTOP | 4.96.3 | PS5.1, PS7, cmd.exe |

Each of the **six cells** must use the exact authenticated published candidate
in a fresh isolated installation and execute real `cb version`, `cb setup`,
`cb doctor` and the unmodified `cb self-test --release --json`.
Require **15 passed / 0 failed / 0 skipped** across the 15 fixed self-test IDs;
record and interpret all seven informational environment IDs. Document an
understood non-blocking doctor warning rather than silently ignoring it.
Reconcile and remove verified test-owned containers/volumes after each cell.
Self-test PASS alone does not establish state recovery, preservation or cleanup.

## 3. Persistent state and provider integration — mandatory

Perform **one complete integration suite on each Docker Desktop version**, not
one full state suite for every shell. Exercise the actual candidate and disposable
state:

- Python: new venv persistence, legacy symlink-based venv, project-marker and
  marker-free compatibility selection, successful checked backup/restore with
  post-restore package import, refusal to overwrite nonempty state, rejection
  of escaping/absolute archive links, and no successful-looking partial backup.
- pipx: install a disposable application, explicit and store-wide exposure,
  invoke the managed shim, unexpose and clean up.
- Node persistent project behavior; representative jq/Terraform stateless runs.
- stdin/EOF, stdout/stderr separation, exact exit codes, PTY where applicable,
  and relevant path/ownership/security-negative cases.

Use stock commands and existing documented test procedures. The private
75-command/21-volume driver, immutable execution-plan manifests and continuous
forensic observers are **not** mandatory. An actual failed product state
round-trip blocks the release even with six green self-tests.

## 4. Native WSL2 — mandatory once per candidate

On the **current** Docker Desktop version, use the authenticated published Linux
amd64 artifact in a regular disposable user distro, not Docker's private WSL
distributions. Check the full generated Unix-socket pathname length before
creating the distro; use a short disposable name.

Exercise distribution-local and standard Windows-drive-mounted projects;
stateless/stateful tools; stdin, streams, exit codes, PTY size/resize/interrupt;
shared/project volume persistence and isolation; unsafe mixed/external path
rejection; interrupted disposable shim recovery; namespace-scoped cleanup.
Preserve and verify any pre-existing native Windows volumes. Record actual
Windows/Desktop/Engine/distro/kernel versions and final cleanup state.
A historical WSL source-build activation is not candidate qualification.

## 5. Published self-update — mandatory once per candidate

On Windows amd64, test an isolated transaction from the **actual published**
previous supported release (v2.0.1 for v2.1.0) to the authenticated **published**
v2.1.0-rc.1 artifact. Verify release selection, trusted download, SHA-256 and
attestation, atomic replacement, version output, hardlink/copy-shim behavior,
and absence of temporary/rollback/lock residue. Include documented
transaction refusal/rollback behavior for a failed verification or interrupted
update, using an isolated disposable installation.

`scripts/qualify-self-update-published.ps1` can supply additional evidence, but
its source-built binary with an injected earlier version is a **simulation of
the source version**, not execution of the actual prior published binary.
Do not quietly substitute that simulation for the mandatory published-to-published
test. The final stable draft's version-dependent behavior must also be checked
before stable publication.

## 6. Preservation, isolation and cleanup — mandatory

Prefer clean, disposable hosts or clearly isolated private installs and test
state. Before each qualification on a host, record the actual Engine/Desktop
identity; containers, volumes and networks; relevant image identities; protected
ContainerBin installs/registries and persistent User/Machine PATH. Reconcile
the same inventory after the run. Preserve unknown/unrelated resources.

PASS requires no unexpected modifications/deletions of existing user resources
or persistent PATH, and no residual **verified test-owned** containers/volumes
after safe cleanup. Record approved image downloads and other intentional
changes. Delete resources only with established test ownership, not by a
name prefix; do not invoke blanket cleanup on a shared environment.

Before/after equality **does not prove continuous lifecycle preservation**.
That limited claim is explicitly accepted, compensated by isolation, direct
state-safety integration tests and real cleanup checks. The
`scripts/qualification-preservation.ps1` library and private continuous
Docker/backend observers remain optional forensic diagnostics, **not mandatory
release gates**. Their historical STOP/FAIL evidence is retained as recorded.

## 7. Minimal preflight and reporting

Preflight: actual host/OS/Desktop version; Linux Engine/context and live
readiness; Resource Saver/power settings; correct candidate hash; local images,
shells and dependencies; sufficient disk; test resource collisions and private
work paths. Fix demonstrated operational problems directly, then run the real
tests. No mandatory synthetic replay of all future commands or private driver
qualification.

Retain a small sanitized **per-host** report with exact candidate identity,
three shell cells, one state integration suite, Docker version, before/after
preservation and cleanup, and evidence links. Consolidate the two host reports
with WSL2, self-update and supply-chain gates into one truthful GO/NO-GO.
Never publish raw credentials, environment secrets or private host logs.

## Migration and governance

After approval, this policy supersedes the mandatory forensic-driver portions
of the earlier release matrix. Preserve historical qualification evidence and
the optional strict helper; do not erase or retroactively reclassify results.
Review this documentation-only change with normal CI and automatic
CherylSnowVeil review. The owner separately authorizes merge and stable release.

**Ship the qualified product, not a qualification infrastructure project.**
