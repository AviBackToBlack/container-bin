# Remaining roadmap implementation requirements

This document turns the remaining work in
[roadmap issue #2](https://github.com/AviBackToBlack/container-bin/issues/2)
into implementation and acceptance requirements. Issue #2 remains the
authoritative live roadmap: re-read it, current `main`, open pull requests and
the relevant code before starting any item.

Maintainer product/security decisions accepted on **2026-09-19** are recorded
in [roadmap-decisions.md](roadmap-decisions.md). That decision ledger is the
canonical disposition for design-gated, blocked, dormant and implementation-
ready items. Requirements below remain useful acceptance detail, but an older
"decision required" sentence must not be interpreted as reopening an accepted
decision.

Status snapshot: **2026-09-25**. The earlier 2026-09-17 snapshot counted every
unchecked roadmap line as unfinished work; that is no longer an accurate model.
RM-26 pipx, per-project overlay trust, signed-registry policy, the RM-31
selection/check/staging/verification slices, the WSL host boundary and native
layout identity, native Windows ARM64 CI, and reproducible ARM64 release
packaging have since shipped. The
maintainer has also explicitly accepted product/security dispositions for the
remaining design gates. Use the readiness table below plus
[roadmap-decisions.md](roadmap-decisions.md), not checkbox count or unmerged pull
request coverage, to decide whether work is actionable.

## Requirements that apply to every item

Every implementation must preserve ContainerBin's existing contract:

- Fail closed when configuration, trust evidence, path identity or ownership
  is ambiguous. An error must identify the rejected input and the recovery
  action; it must not guess a replacement.
- Preserve conservative Windows path mapping, narrow mounts, allowlist-only
  environment passing, dry-run-by-default destructive operations and
  label-proven ownership before deletion.
- Preserve existing command names and behavior unless the roadmap item
  explicitly authorizes a compatibility change and includes a migration path.
- Keep the binary standard-library-only unless a separate design decision
  demonstrates that a dependency is necessary, supportable and safer than a
  small local implementation.
- Keep reservation rules synchronized with dispatch and shim creation. Data
  discovered inside a container is untrusted input.
- Keep registry and lockfile mutations serialized, fully validated and atomic.
  New file formats need recovery behavior and version-skew tests.
- Keep Windows first-class. Portable unit tests are necessary but do not
  replace Windows tests or real Windows + Docker Desktop qualification where
  the behavior depends on those platforms.
- Update user documentation, architecture/security documentation and release
  qualification instructions whenever behavior or a trust boundary changes.

The minimum delivery gate for a code change is:

1. Focused unit and regression tests, including negative/fail-closed cases.
2. `gofmt -l .` returns no files.
3. `go vet ./...` succeeds.
4. `go test -race ./...` succeeds on the portable runner.
5. Windows CI runs `go test -v ./...`, builds `cb.exe` and smoke-tests version
   output.
6. The release-style `-ldflags "-X main.version=..."` check still succeeds.
7. Any item that touches Docker behavior is qualified on a real supported
   Windows + Docker Desktop host, with evidence recorded using the existing
   release/self-test format.

## Readiness and prerequisite summary

| Item | Current disposition | Next action / trigger |
|---|---|---|
| govulncheck pin | **Recurring maintenance** | Deliberately refresh when adopting a newer stable upstream release; never a one-time completion gate |
| RM-19 reserved-name migration | **Conditionally deferred** | Wake only when a future release proposes reserving a name accepted by a published older release |
| RM-23 8.3 path alias | **Intentionally deferred** | Keep explicit comma-path rejection; reconsider only on demonstrated user demand |
| RM-24 Python/uv provider choice | **Decision complete — keep both** | No provider migration; Python provider and uv/uvx remain separate |
| RM-26 Python global CLI exposure | **Completed in PR #74** | Stateful pipx + `cb expose pipx` shipped; plain pip `/venv/bin` remains intentionally unexposed |
| RM-29 Windows ARM64 | **Native CI and release packaging shipped / update and hardware work remain** | PR #78 added native hosted ARM64 CI and PR #86 added reproducible release packaging; ARM64 self-update selection and real Windows-on-Arm + Docker Desktop E2E remain |
| RM-30 Authenticode | **Design complete / externally blocked** | Provision real code-signing certificate and protected signing mechanism |
| RM-31 self-update | **Selection, staging and verification shipped / transaction implemented** | PRs #76, #81 and #82 shipped the read-only plan, fail-closed staging and provenance verification; temporary helper, user-facing apply wiring, broader architecture support and release E2E remain |
| RM-34 Cargo expose enhancement | **Intentionally deferred** | Existing expose-all/explicit selection are sufficient; reopen only for concrete unmet use case |
| Linux/macOS hosts | **Demand-gated** | WSL may factor reusable Linux host code; standalone support needs its own demand and qualification |
| Enterprise policy | **Foundation and signed registry shipped / image trust remains** | PRs #75 and #84 shipped the machine-owned constraint layer and authenticated registry; image trust remains |
| Image trust | **Design complete / sequenced** | Implement after signed-registry policy using policy-driven Sigstore/cosign verification |
| Plugin/provider architecture | **Intentionally deferred** | Reopen only after at least two real integrations cannot fit the declarative model |
| WSL2 | **Host boundary and layout identity shipped / implementation remaining** | PRs #77 and #83 shipped the fail-closed host boundary and fixed native layout/state identity; filesystem preparation is implemented but unexposed, while frontend wiring, Docker Desktop integration and real WSL qualification remain |
| Per-project overlays | **Completed in PR #80** | Add-only digest-bound trust model shipped on the merged enterprise-policy foundation |
| Release SBOM | **Conditionally deferred** | Trigger on shipped third-party/runtime dependencies or concrete compliance/consumer demand |
| Snyk | **Conditionally deferred** | Trigger only for a real coverage gap plus owner/account/token and triage/outage policy |
| Issue #69 | **Completed** | Superseded by merged implementation; no remaining roadmap dependency |

## Recurring govulncheck pin maintenance

This is maintenance, not a one-time feature. The workflow currently installs
`golang.org/x/vuln/cmd/govulncheck@v1.8.0` because Dependabot cannot update a
version embedded in a `go install` command.

Implementation requirements:

- Establish the candidate from an upstream stable module tag. Do not track a
  branch, pseudo-version or mutable tag.
- Change only the full version pin and directly related comments unless the
  upstream release requires a separately reviewed workflow change.
- Verify that the tag resolves through the Go module proxy and that the
  installed binary reports the expected version.
- Run `govulncheck ./...` and the normal repository validation. A clean result
  means no called vulnerability was found; it is not a general security
  certification.
- Keep the vulnerability database live at scan time. Do not pin or cache an
  old database merely to make results reproducible.
- Record the refresh date in issue #2. Do not mark this recurring item complete.

## RM-19 — Reserved-name migration path

### Entry condition

Do not implement this merely to rearrange current validation. It becomes
necessary when a future release proposes reserving a tool name that at least
one already-published ContainerBin version accepted. Existing never-valid
names such as Windows device names must remain hard errors.

### Required behavior

- Separate **permanently invalid names** from **newly reserved legacy names**.
  The former still fail registry parsing. The latter may load only in a
  migration-safe mode that clearly reports the collision.
- A legacy collision must never dispatch, create/reconcile a shim, become a
  default alias, or be accepted by `cb add`/`cb expose`/registry upgrades.
- Read-only inspection, `cb doctor`, backup and the command that removes or
  renames the offending profile must remain usable.
- `cb doctor` must name the profile, the newly reserved name and the exact
  supported recovery command. It must not delete or rename anything itself.
- Mutation must validate the final registry under current rules. Unrelated
  mutations must not silently preserve an unsafe dispatch state.
- The same classification must be used by parser validation, dispatch,
  default aliases, discovered binaries and shim reconciliation.

### Acceptance evidence

- A fixture produced by the previous release loads for diagnosis and can be
  repaired without hand-editing.
- Fresh creation of the same name fails through every creation path.
- No conflicting shim is installed or invoked before repair.
- Permanently invalid names retain today's fail-closed errors.
- Upgrade/downgrade behavior and the recovery procedure are documented.

Likely areas: `internal/registry`, CLI mutation commands, dispatch in
`main.go`, shim reconciliation, doctor/inspect output and security-model docs.

## RM-23 — Windows 8.3 alias for unrepresentable mount sources

This is a capability-gated compatibility feature, not a general path
normalizer. Docker's `--mount` field grammar cannot escape a comma in a source
path, while Windows 8.3 aliases may not exist because short-name creation can
be disabled per volume. Microsoft documents both
[`GetShortPathNameW`](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-getshortpathnamew)
and the configurable
[`fsutil 8dot3name`](https://learn.microsoft.com/en-us/windows-server/administration/windows-commands/fsutil-8dot3name)
behavior.

### Required behavior

- Attempt aliasing only after the real canonical source is rejected solely
  because it cannot be represented in `--mount`. Do not use it for ordinary
  paths or to bypass UNC, mapped-drive, `subst`, reparse or trust decisions.
- Query an existing short path through a Windows API. Never enable 8.3 name
  generation, modify filesystem policy, require elevation or create an alias.
- Require the complete alias used by Docker to be absolute, local,
  Docker-shareable and itself free of `--mount` delimiters.
- Prove that the long and short paths identify the same existing directory
  after canonicalization. Failure to obtain or prove the alias returns the
  existing explicit comma-path error.
- Preserve the long canonical path for project identity, volume hashing,
  labels, diagnostics and user output. Use the alias only as the Docker mount
  source; otherwise the same project could acquire two state identities.
- Do not let `EvalSymlinks`, case folding or short-name expansion silently
  change containment decisions. Perform security classification against the
  canonical long path before substituting the mount-only alias.
- Scope the first implementation to the project-root bind mount unless a
  separate test proves external argument mounts and explicit `host_mounts`
  retain their current narrow-mount and trust semantics.
- `cb trace`, debug logs and errors should expose both paths when an alias is
  used, with a clear `mount alias` label.

### Acceptance evidence

- Real Windows tests cover: available alias, disabled/unavailable alias,
  Unicode/space names, a comma in an ancestor, a comma in the basename, alias
  still unrepresentable, reparse involvement and identity mismatch.
- The same project invoked through long and short spelling uses one project
  hash and one state set.
- Docker Desktop successfully mounts the aliased source and reads/writes the
  intended project on a real host.
- Existing unsupported path forms remain unsupported, and the no-alias path
  fails with no Docker invocation or volume creation.

Likely areas: Windows-specific helpers in `internal/pathmap`, mount assembly in
`internal/dockerrun`, trace/doctor diagnostics and `docs/windows-paths.md`.

## RM-24 — Python and uv provider decision — completed

Decision accepted: keep both providers. The dedicated Python provider retains
its existing per-project `/venv`, shared pip cache and compatibility/global
fallback semantics; uv/uvx remain separate opt-in stateful profiles with their
own managed tool store. There is no automatic migration or heuristic provider
selection.

### Guardrails if this decision is revisited later

- Versioned profiles must remain stable. Changing an unversioned default must
  use the existing family/default machinery rather than rewriting user tools.
- Migration must be explicit, previewable and reversible. Do not relabel or
  reuse a volume unless its contents and ownership are compatible and proven.
- Existing lock entries and backups must either remain valid or fail with an
  actionable version-skew/migration error.
- `python`, `pip`, stdin, exit codes, CWD mapping and Windows-targeted build
  guidance need real compatibility tests.
- Documentation must state semantic differences; an apparently faster setup
  is not sufficient evidence of compatibility.

Likely areas: provider assembly in `internal/dockerrun`, default registry,
registry migration, state/backup logic, self-test and Python documentation.

## RM-26 — pipx global CLI exposure — completed

The provider-neutral shared-volume primitive shipped in PR #72 as
`cb expose --shared-file`. The accepted Python scope then shipped in PR #74:
plain pip environments are dependency environments and are not a source for
global exposed shims; pipx is the classic-Python global application store.

The requirements and acceptance evidence below are retained as the shipped
contract, not as remaining implementation work.

### pipx requirements

- Add a separate stateful pipx profile with explicit ContainerBin-owned
  persistent `PIPX_HOME` and `PIPX_BIN_DIR`; do not reuse the Python
  provider's `/venv`, pip cache, or the uv tool store.
- `cb expose pipx` discovers and exposes every eligible binary in the managed
  pipx bin store. `cb expose pipx BINARY...` performs deterministic explicit
  selection from the same store.
- Discovery stays inside the selected locked pipx profile and managed bin store;
  do not search host `PATH`, project venvs, unrelated container directories or
  other package-manager stores.
- Preserve the source profile's image, state group, mounts, env policy and lock
  resolution in generated profiles.
- Reject missing, ambiguous, non-regular or directory entries. Normalize and
  validate names through the same reserved/case-collision rules as other
  exposed commands.
- Mark generated profiles with explicit ownership metadata so `cb unexpose`
  removes only proven managed profiles and never a similar hand-written one.
- Plain pip console scripts under the Python provider's `/venv/bin` are
  deliberately not eligible for global `cb expose`.

### Acceptance evidence

- Windows + Docker Desktop E2E installs a disposable pipx application, verifies
  store-wide expose, explicit selection, shim invocation and `cb unexpose`.
- Negative tests cover absent binary, duplicate/case-colliding name, reserved
  name, unlocked image, wrong store shape and a hand-written profile that merely
  resembles a generated one.
- Backup/restore and lock updates preserve exposed pipx tools without inventing
  new state ownership.
- README/help/security documentation distinguish project pip dependencies,
  pipx-managed global applications and the already-supported uv-tool workflow.

Likely areas: default registry/profile definitions, `internal/cli`
expose/unexpose, registry ownership fields, lock-aware discovery and expose
documentation.

## RM-34 — Enhance Cargo binary exposure

PR #64 shipped the current `cb expose cargo [BINARY ...]` workflow as one
RM-26 slice. Do not change it merely because “enhance” is broad enough to
permit implementation. Start only after the desired additional discovery or
selection behavior is stated as a concrete user-visible case.

### Scope requirements

- Describe the current result, the desired result and why explicit existing
  binary selection does not already satisfy the use case. Do not infer a
  package/crate identity from a filename or silently choose among candidates.
- Keep discovery inside Cargo's explicit `/cb/cargo-global` store. Do not scan
  unrelated shared volumes, container `PATH` or host paths.
- Define behavior for packages that install multiple binaries, explicitly
  requested names that are absent, case collisions, registry collisions and
  Cargo/Rust command-name collisions. Every ambiguity must fail or be reported
  explicitly rather than selecting a “best” candidate.
- Preserve the generated profile's fixed inherited-policy boundary. An
  enhancement must not implicitly copy `project_volumes`, `host_mounts`,
  `cwd_mode`, source argument/path rules or default-family metadata.
- Preserve `role = "exposed"` ownership and the existing fail-closed
  `cb unexpose` checks. A similar hand-written profile must never become
  managed merely because its command points into the Cargo volume.
- Keep existing `cb expose cargo`, explicit selection and missing-name output
  backward compatible unless the scoped use case explicitly justifies and
  documents a CLI change.

### Acceptance evidence

- Regression tests retain expose-all, explicit-selection, missing-name,
  reserved-name, case-collision, existing-registry-name and provenance checks.
- Focused tests cover every newly specified discovery/selection branch,
  including its negative and ambiguous forms.
- Windows + Docker Desktop E2E installs a disposable Cargo package, exercises
  the enhancement, runs the generated shim and removes it with `cb unexpose`
  without changing an unrelated profile or volume.
- README, help and security-model text distinguish the new behavior from the
  existing Cargo store scan and state which policy fields are inherited.

Likely areas: `internal/cli` expose selection/discovery, generated-profile
rendering, unexpose provenance tests and Cargo expose documentation.

## RM-29 — Windows ARM64 release target

Cross-compilation proves only that Go can emit a PE file. Support may be
claimed only after qualification on real Windows ARM64 hardware running Docker
Desktop in Linux-container mode.

PR #78 shipped native hosted ARM64 CI for non-Docker qualification. PR #86
shipped the architecture-specific ARM64 archive, checksum coverage, independent
byte-for-byte reproduction and release provenance while preserving existing
amd64 asset names. PR #76's self-update foundation still deliberately rejects
architectures other than Windows/amd64. ARM64 install/update selection and real
Windows-on-Arm + Docker Desktop qualification therefore remain; CI and a
provenanced archive alone are not a full support claim. The first three bullets
below are the shipped PR #86 contract. Install/update selection and the
hardware-backed qualification record remain.

### Shipped release contract and remaining implementation

- Add an explicit `windows/arm64` release matrix entry and an unambiguous asset
  name such as `container-bin-VERSION-windows-arm64.zip`. Keep the current
  amd64 name and contents stable.
- Produce per-architecture `cb.exe`, deterministic archive, checksum entry and
  build-provenance attestation through the release workflow. Never copy or
  rename the amd64 artifact.
- Keep unsigned reproducibility comparison per architecture. The rebuilt ARM64
  bytes must match the unsigned release-stage ARM64 bytes.
- Make installation/update selection use `GOARCH`/native architecture rather
  than processor-name guessing. An unsupported architecture fails explicitly.
- Extend release documentation and the qualification record with hardware,
  Windows build, Docker Desktop/engine version and `cb self-test --release`.

### Acceptance evidence

- Native execution of management commands and shim dispatch on Windows ARM64.
- Real Docker E2E for stateless, stateful and Python providers; path mapping;
  stdin/stdout/exit codes; lock/update; expose; backup/restore; and release
  attestation verification.
- amd64 CI, artifact names, checksums and installation remain unchanged.

## RM-30 — Authenticode signing

This item cannot ship without an approved code-signing certificate and a
protected signing mechanism. Microsoft recommends SHA-256 signatures and RFC
3161 timestamps, and explains why timestamping is required for validity after
certificate expiry in its
[Authenticode timestamping guidance](https://learn.microsoft.com/en-us/windows/win32/seccrypto/time-stamping-authenticode-signatures).

### Required signing design

- Document certificate owner, renewal/revocation procedure, permitted release
  identities and who can trigger signing. Private key material must not be
  committed, logged or exposed to untrusted pull-request workflows.
- Use a protected signing service, hardware-backed key or equivalently
  controlled release secret. The release job must use least privilege and only
  run for protected release tags.
- Sign each raw `cb.exe` with SHA-256 and an RFC 3161 SHA-256 timestamp. Verify
  the resulting signature, certificate chain, subject and timestamp before
  packaging.
- Preserve reproducibility by comparing unsigned build outputs first. Signing
  and timestamping are intentionally nondeterministic; the signed binary then
  becomes the input to the final ZIP, checksums and provenance attestations.
- A signing or verification failure must abort publication. Never publish an
  unsigned fallback under the normal signed asset name.
- Stable and prerelease release artifacts are both signed. Document certificate
  rotation so old/new expected publisher identities overlap explicitly during
  the migration window.

### Acceptance evidence

- `Get-AuthenticodeSignature` and SignTool verification succeed on a clean
  supported Windows host and show the documented publisher and valid timestamp.
- ZIP extraction preserves the signature; checksum and GitHub attestation
  cover the signed bytes users download.
- Pull-request workflows cannot reach signing credentials.
- Revocation/expiry and timestamp-service failure paths have a rehearsed,
  fail-closed release procedure.

Likely area: `.github/workflows/release.yml`, release qualification docs and
security/release verification docs. This is not a runtime dependency.

## RM-31 — Attestation-verifying self-update

Self-update combines remote version selection, supply-chain verification,
Windows executable replacement and hardlink reconciliation. Treat those as
separate phases with explicit boundaries. GitHub documents both
[release asset downloads](https://docs.github.com/en/rest/releases/assets)
and [artifact-attestation verification](https://docs.github.com/en/actions/concepts/security/artifact-attestations).

PR #76 shipped the command surface, release selection, check/dry-run behavior,
strict Windows/amd64 asset selection and bounded metadata rules. Private
same-volume staging and provenance verification followed. This tree also
implements the unexposed rollback-safe Windows replacement transaction and
complete proven-shim reconciliation. The temporary helper, user-facing apply
wiring, broader architecture support and release E2E remain incomplete.

### Command and selection requirements

- Provide a dry-run/check mode that reports current version, selected target,
  architecture, asset names and verification policy without changing files.
- Default to stable releases; prerelease or an exact version requires an
  explicit option. Refuse downgrades unless explicitly requested.
- Query only the canonical repository over HTTPS, bound response and download
  sizes, require exact architecture-specific asset/checksum names and reject
  duplicates or redirects outside the permitted GitHub asset flow.
- Never update a `dev` build or an installation whose ownership/layout cannot
  be proven without an explicit, documented recovery path.

### Verification requirements

- Download to a private temporary file on the destination volume. Verify the
  checksum and GitHub build-provenance attestation against the expected
  repository/workflow/ref policy **before** any installed file changes.
- Do not treat a hash from the same unverified response as authentication.
  Attestation identity/policy is the authentication step; checksums protect
  packaging and corruption.
- The initial verifier is external `gh attestation verify`, enforcing the
  expected repository/workflow/ref and exact downloaded artifact digest.
  ContainerBin remains standard-library-only. No silent “checksum only” fallback
  is allowed.
- If RM-30 has shipped, also verify the expected Authenticode publisher and
  timestamp. Define whether both controls are mandatory or one is a recovery
  policy; do not infer policy from which tool happens to be installed.

### Windows replacement requirements

- Account for the running image and for ContainerBin's hardlinked shims. Merely
  replacing `cb.exe` can leave every existing shim linked to the old bytes.
- Stage and verify first, then launch a narrowly scoped helper that waits for
  the parent to exit, replaces the management binary and reconciles only shims
  proven to belong to that installation. Never overwrite an unrelated file.
- Keep a validated rollback copy until the new binary passes a bootstrap
  `cb version` smoke test and shim identity check. On failure, restore the complete
  prior managed set or leave an actionable recovery artifact.
- Serialize with registry/shim mutations. Preserve ACL expectations and avoid
  elevation unless the installation already requires it.
- Prefer an immediate same-volume replacement. A reboot-scheduled operation is
  a separately disclosed fallback: Microsoft notes that
  [`MoveFileEx`](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-movefileexa)
  can schedule work but cannot report whether the later boot-time move actually
  succeeded.

### Acceptance evidence

- Tests cover no update, upgrade, explicit downgrade rejection, wrong arch,
  missing/duplicate assets, checksum mismatch, bad identity/attestation,
  truncated download and network loss.
- Windows E2E covers replacement from `cb.exe` and from a shim, all managed
  hardlinks dispatching the new version, a locked file, insufficient
  permissions, helper interruption and rollback.
- No installed byte changes occur before all remote verification succeeds.

## Linux and macOS hosts

This remains demand-gated. Before code, write a host contract for each selected
OS rather than labeling the existing Windows behavior “portable.”

Requirements include:

- choose symlink/shim installation layout, config/state locations and atomic
  replacement rules;
- define native path classification, case sensitivity, symlink containment,
  Docker socket/context selection, UID/GID ownership and TTY/signal behavior;
- isolate host-specific implementations behind build-tagged or narrow
  interfaces without weakening Windows semantics;
- provide maintained CI plus real Docker E2E hosts for every claimed OS/arch;
- document cross-host registry/lock portability and reject incompatible host
  fields explicitly.

Support is complete only when release artifacts, installer, self-test, shell
matrix, backup/restore and release qualification exist for that host.

## Enterprise policy controls

PR #75 shipped the machine-owned policy foundation: fixed policy location and
ownership checks, schema/versioning, stable diagnostics, effective-request
authorization, repository/image allowlisting, mandatory lock enforcement, and
diagnostics. Lower-precedence configuration cannot weaken that policy.

Authenticated registry files and image trust remain separate follow-up slices.
Host-mount/environment restrictions are not part of schema 1 and are not
implied by the merged foundation. Follow-ups must extend the merged policy
boundary rather than introducing a parallel precedence model.

### Required policy model

- Define an administrator-controlled policy location, ownership requirements
  and precedence above user registry, project overlay and CLI flags. A missing
  optional policy differs from an unreadable or invalid configured policy; the
  latter fails closed.
- Registry allowlisting must canonicalize registry hosts, handle implicit
  Docker Hub references deliberately and match host/repository boundaries—not
  substrings. Digests do not excuse a disallowed registry origin.
- Mandatory lock mode must reject every unlocked execution path, including
  expose discovery, diagnostics that would pull, local-image exceptions and
  future plugins, unless the policy explicitly defines them.
- Signed-registry policy must define canonical bytes, signature envelope,
  trusted keys/identities, expiry/revocation and rotation. Verify before parsing
  or acting on any registry content.
- `cb doctor`, `cb inspect` and `cb bugreport` should report effective policy
  and source without disclosing secrets. Policy failures need stable
  machine-readable diagnostics.
- Changes require tests showing lower-precedence configuration cannot weaken
  policy and that corrupt, stale, unknown-version and unauthorized policy all
  fail before Docker invocation.

## Image trust at lock time

Do not start with Docker Content Trust/Notary v1: Docker has announced its
[retirement](https://docs.docker.com/engine/security/trust/). Select a
maintained signature system such as Sigstore/cosign or Notation and document
the supported registry coverage.

### Required trust contract

- Define policy per repository: trusted key or keyless issuer/subject,
  transparency-log requirement, certificate time validity, offline behavior
  and whether unsigned images are ever explicitly allowed.
- Resolve the tag to a digest first, then verify signatures for that exact
  digest. A signature on a tag or a different manifest/platform is not enough.
- Record structured verification evidence in a versioned lockfile schema:
  mechanism, verified digest, signer identity/key reference, issuer, relevant
  bundle/log identity and verification time. Do not store only a boolean.
- Runtime still executes the pinned digest. Define when lock evidence must be
  reverified and how key rotation/revocation affects an existing lock.
- A verifier missing, registry unreachable, signature absent, identity
  mismatch or malformed evidence fails according to explicit policy; no
  automatic downgrade to digest-only locking.
- Cover multi-architecture indexes versus selected platform manifests, private
  registries, mirrors and air-gapped bundles.

This changes the lockfile schema and trust boundary, so it needs migration,
old-binary/new-lock tests and security-model documentation.

## Plugin/provider architecture

This is justified only by concrete providers that cannot be expressed by the
current declarative stateful model. External executable discovery is code
execution and must not become a generic `PATH` scan.

Requirements:

- Write a versioned protocol for capabilities, validation, run-plan assembly,
  diagnostics and errors. Keep final Docker argv and security validation under
  core control wherever possible.
- Discover plugins only from explicit, trusted directories or signed registry
  declarations. Exact names, file ownership and duplicate precedence must be
  deterministic and inspectable.
- Use bounded structured I/O, timeouts, cancellation and output limits. Reject
  unknown protocol versions and fields that would be ignored unsafely.
- A plugin cannot request unrestricted host environment, arbitrary broad
  mounts, Docker socket access or unlabelled state. Core applies the same
  reserved namespaces, path classification, env allowlists and lock policy.
- Define install/update/removal, provenance, compatibility and crash behavior.
  A missing plugin referenced by a profile fails closed.
- Threat-model malicious plugins explicitly: if plugins are fully trusted code,
  say so; do not market process separation as sandboxing.

## WSL2 interoperability model — native WSL frontend selected

Decision accepted: a WSL invocation uses a native Linux shim/binary inside the
distribution and Docker Desktop's supported WSL integration. Windows `cb.exe`
interop and a Windows client talking to a Docker engine exposed by WSL are not
the supported WSL models.

PR #77 shipped the fail-closed host runtime boundary and explicit Windows/WSL
separation. Native Linux config/shim/state layout, Docker Desktop WSL
integration, project behavior and real WSL qualification remain.

Implementation must define native config/shim location, Docker endpoint,
project identity, named-volume behavior, file permissions, case sensitivity,
symlinks, stdin/TTY/signals and mixed-invocation rejection. Windows and WSL do
not share registry/lock/state identity implicitly, and ContainerBin never guesses
equivalence between Windows paths and `/mnt/<drive>` paths.

Qualification must include both Windows-filesystem and WSL-filesystem projects
plus mixed invocation rejection cases.

## Per-project registry overlays

An overlay makes a cloned repository capable of requesting images, mounts and
environment access. Treat it like executable project configuration.

Requirements:

- Define a deterministic, documented merge schema. Duplicate tools, conflicting
  defaults, schema-version skew and attempts to weaken machine policy fail
  closed; array/map merging must never be implicit.
- On first use, show a reviewable summary of images, env names/prefixes,
  read-write host mounts, state and generated shims. Require explicit trust in
  an interactive terminal; non-interactive use fails unless trust was
  pre-provisioned by an explicit command/policy.
- Bind trust to canonical project identity and a cryptographic digest of the
  security-relevant overlay. Moving or changing it invalidates trust unless a
  deliberate policy says otherwise.
- Store trust outside the repository. Never honor a trust marker committed by
  the same project.
- Provide `cb inspect`, `cb trust`, `cb untrust` and `cb doctor` visibility,
  with no automatic execution during review. Symlink/reparse and ownership
  checks must follow the Windows path classification.
- The initial overlay capability set excludes `host_mounts`, `env_prefixes`
  and shared cross-project volumes. Exact `env_names` may be requested and
  must be shown in the trust summary.

## Release SBOM

Keep this conditional while the Go binary remains standard-library-only.
Trigger implementation when third-party/runtime dependencies appear or a
consumer/compliance requirement creates value.

When triggered:

- Generate an SPDX or CycloneDX SBOM in the release workflow from the exact
  source and module graph used for each release, using a full-SHA-pinned tool.
- Include module versions, checksums, tool version, commit and artifact
  relationship; validate the document before publication.
- Attest the SBOM and associate it with the exact release artifact. GitHub's
  attestation model supports
  [SBOM attestations](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations).
- Produce it through the canonical release workflow only. Do not manually
  replace release assets or claim that an SBOM proves vulnerability absence.

## Snyk

This remains conditional while CodeQL, govulncheck and Dependabot cover a
standard-library-only binary. Adoption requires an identified security owner,
an external account/token and a stated coverage gap.

Requirements if adopted:

- Document which additional artifact/ecosystem Snyk scans and how findings are
  triaged; avoid a duplicate required check with no owner.
- Pin every action to a full commit SHA, grant least permissions, prevent token
  exposure to forked/untrusted workflows and bound uploaded source/artifacts.
- Establish severity/allowlist/expiry policy before making the check required.
  Baselines cannot hide new findings indefinitely.
- A service outage must follow the documented branch-protection policy; do not
  weaken existing CodeQL, govulncheck, dependency review or Dependabot gates.

## Issue #69 — Unify registry section-header parsing — completed

Completed by PR #71 and issue #69 is closed. The shared section-header parsing
work is retained in repository history and tests; there is no remaining
implementation task in this roadmap document.

## Recommended implementation order

Merged foundations are not remaining queue entries: RM-26 shipped in PR #74,
enterprise-policy foundation in PR #75, RM-31 selection/check in PR #76, the
WSL host boundary in PR #77, native Windows ARM64 CI in PR #78, and
reproducible ARM64 release packaging in PR #86.

1. Per-project overlay trust foundation.
2. Signed-registry enterprise policy.
3. Image trust at lock time, after signed-registry policy merges.
4. Remaining RM-31 helper/user-facing apply wiring, broader architecture support and E2E.
5. Remaining WSL2 native layout, Docker Desktop integration and real E2E.
6. RM-30 Authenticode only after certificate/protected-signing prerequisites exist.
7. RM-29 ARM64 self-update selection and real Windows-on-Arm + Docker Desktop
   qualification last; do not delay higher-value work for them.

RM-19, RM-23, RM-34, standalone Linux/macOS, plugins, SBOM and Snyk are dormant
until their documented triggers occur. The govulncheck pin is recurring
maintenance.

This ordering is advisory. Re-read the live issue, decision ledger, merged state
and open PR coverage before every implementation unit.
