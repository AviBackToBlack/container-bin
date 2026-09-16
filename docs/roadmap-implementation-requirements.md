# Remaining roadmap implementation requirements

This document turns the unchecked work in
[roadmap issue #2](https://github.com/AviBackToBlack/container-bin/issues/2)
into implementation and acceptance requirements. Issue #2 remains the
authoritative live roadmap: re-read it, current `main`, open pull requests and
the relevant code before starting any item. This document is a design aid, not
a second completion checklist.

Status snapshot: **2026-09-16**, after v1.1.0 and PRs #64-#68 merged. At that
point issue #2 had 16 unchecked entries: one recurring maintenance task, seven
numbered enhancements and eight speculative/future items. A later section
also specifies [issue #69](https://github.com/AviBackToBlack/container-bin/issues/69),
which is related work but is not itself an unchecked issue #2 item.

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

| Item | Status before implementation | Required prerequisite or trigger |
|---|---|---|
| govulncheck pin | Recurring maintenance | A newer stable upstream release and a deliberate refresh |
| RM-19 reserved-name migration | Conditional design | A proposal to reserve a name that was legal in a published release |
| RM-23 8.3 path alias | Speculative prototype | Confirmed user need and a safe Windows-only identity proof |
| RM-24 Python/uv provider choice | Product decision | Written compatibility and migration decision |
| RM-26 expose beyond current stores | Implementable in slices | Define direct pip/pipx stores and the generic source contract separately |
| RM-29 Windows ARM64 | Hardware-gated | Real Windows ARM64 hardware with Docker Desktop |
| RM-30 Authenticode | External-resource-gated | Code-signing certificate and protected signing mechanism |
| RM-31 self-update | Design- and trust-gated | Stable release API, attestation verifier and Windows replacement design |
| Linux/macOS hosts | Demand-gated | Concrete users and maintained qualification hosts |
| Enterprise policy | Product/security design | Policy authority, precedence and deployment model |
| Image trust | Ecosystem/security design | Supported signature system and identity policy |
| Plugin/provider architecture | Demand/security design | At least two concrete external-provider use cases |
| WSL2 | Model decision | Select exactly one interoperability model first |
| Per-project overlays | Trust-UX design | Durable trust identity and non-interactive policy |
| Release SBOM | Conditional | Dependency growth or a compliance/consumer requirement |
| Snyk | Conditional/external | Dependency growth plus account, token and ownership approval |
| Issue #69 | Ready now | PR #65 is merged; no remaining dependency |

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

## RM-24 — Decide whether uv replaces the built-in Python provider

No provider replacement should start until a short architecture decision is
accepted. The present designs are materially different: the legacy Python
provider owns a per-project `/venv`, shared pip cache and compatibility/global
fallback, while uv/uvx are independent stateful profiles with a managed global
tool store.

### Decision requirements

The decision must compare at least:

- existing `python`/`pip` command compatibility and project-marker behavior;
- venv location and persistence, including the no-project compatibility case;
- lockfile/image identity and offline behavior;
- package installation semantics and whether existing volumes are reusable;
- `requirements.txt`, `pyproject.toml`, editable installs and console scripts;
- environment allowlists and Windows path variables;
- startup performance, image size and first-run network behavior;
- downgrade/recovery for existing registries, locks and state backups.

The acceptable outcomes are explicit: keep both providers, make uv opt-in for
new installs, or migrate the unversioned Python family to uv. “Automatically
choose whichever works” is not acceptable.

### Requirements if replacement is selected

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

## RM-26 — Direct pip/pipx and generic shared-volume expose

Implement this as at least two reviewable units: direct Python entry points,
then a provider-neutral shared-volume primitive. Do not infer arbitrary files
from every mounted volume.

### Direct pip/pipx requirements

- Define the supported store(s) and image/profile shapes precisely. A pip
  environment, pipx home and uv tool store are distinct ownership domains.
- Discover commands inside the selected locked profile without searching host
  `PATH` or unrelated container directories.
- Preserve the source profile's image, state group, mounts, env policy and
  lock resolution in the generated profile.
- Reject missing, ambiguous, non-regular or directory entries. Normalize and
  validate names through the same reserved/case-collision rules as other
  exposed commands.
- Mark generated profiles with explicit ownership metadata so `unexpose` can
  remove them without treating a similar hand-written profile as managed.

### Generic shared-volume requirements

- The user must name an existing source profile, one of its declared shared
  volumes and a container-absolute file path beneath that volume's mount.
  There is no whole-volume search and no host-path mode.
- Normalize the requested path and prove it remains under the selected mount;
  reject `..`, mount-root escape, reserved container namespaces and collisions
  with other declared mounts.
- Discovery may confirm the file and executable contract, but must not mutate
  the store, pull an unlocked image or use a different runtime.
- The generated tool must retain an exact command path and source identity.
  Runtime failure is preferable to falling back to a same-named executable on
  container `PATH`.
- CLI output and `cb inspect` must show where the exposed command came from.

### Acceptance evidence

- Positive E2E coverage for one direct Python store and one generic custom
  shared volume on Windows + Docker Desktop.
- Negative tests for path escape, wrong volume, duplicate/case-colliding name,
  reserved name, absent file, unlocked image and a custom profile that only
  resembles a generated one.
- Backup/restore and lock updates preserve generated tools without inventing
  new state ownership.

Likely areas: `internal/cli` expose/unexpose, registry ownership fields,
lock-aware discovery and README expose documentation.

## RM-29 — Windows ARM64 release target

Cross-compilation proves only that Go can emit a PE file. Support may be
claimed only after qualification on real Windows ARM64 hardware running Docker
Desktop in Linux-container mode.

### Required implementation

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
- Decide and document whether prereleases are signed and how certificate
  rotation changes expected publisher identity.

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
- Decide the verifier dependency explicitly. Bundling a Sigstore verifier,
  invoking `gh`, or shipping a narrowly scoped verifier have different
  bootstrap, offline and standard-library implications. No silent “checksum
  only” fallback is allowed.
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
  `version` smoke test and shim identity check. On failure, restore the complete
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

Implement policy before individual switches so precedence cannot be bypassed
by later features.

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
- `cb doctor`, `inspect` and `bugreport` should report effective policy and
  source without disclosing secrets. Policy failures need stable machine-
  readable diagnostics.
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

## WSL2 interoperability model

Select exactly one model in an architecture decision before implementation:

1. Windows process → Windows `cb.exe` → Docker Desktop (current model).
2. WSL process → Linux shim/binary → Docker Desktop WSL integration.
3. Windows `cb.exe` → Docker engine exposed from a WSL distribution.

Docker documents its supported
[WSL 2 integration](https://docs.docker.com/desktop/features/wsl/); that does
not by itself choose ContainerBin's process or path model.

The decision must specify process boundary, config/shim location, Docker
endpoint, `C:\x` ↔ `/mnt/c/x` mapping, project identity, named-volume sharing,
file permissions, case sensitivity, symlinks, stdin/TTY/signals and whether a
Windows and WSL invocation share locks and state. Cross-boundary guessing is
forbidden. Qualification must include both Windows-filesystem and WSL-
filesystem projects plus mixed invocation rejection cases.

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
- Provide `inspect`, `trust`, `untrust` and doctor visibility, with no automatic
  execution during review. Symlink/reparse and ownership checks must follow the
  Windows path classification.
- Decide whether overlays may define host mounts or env prefixes at all; the
  safe initial slice may prohibit those capabilities.

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

## Issue #69 — Unify registry section-header parsing

Issue #69 was originally marked blocked on PR #65. PR #65 is merged, so the
work is now independent and ready. The defect is semantic drift among
`upgradeV1Registry`, `SetDefaultVersion`, `RewriteWithoutTools` and
`defaultSections`; the main `ParseTOML` parser is a fifth consumer of the same
header grammar.

### Shared helper contract

Add a small helper to `internal/toml` with a result that distinguishes:

1. a valid basic-table header and its normalized inner text;
2. a line that is not a section header; and
3. malformed/unsupported header-looking syntax.

An API shaped like

```go
func ParseSectionHeader(line string) (section string, isHeader bool, err error)
```

is sufficient; the exact name is not part of the public API. Its behavior must
be:

- apply the existing quote-aware `StripComment`, then trim whitespace;
- return `isHeader=false` for blank/comment/key-value lines, including a quoted
  value containing `#`;
- accept exactly one basic table header such as `[tools.node]`, including an
  inline comment after the closing bracket;
- preserve the inner section text for the registry layer to validate and
  normalize—`internal/toml` must not learn tool/default naming rules;
- reject empty headers, missing brackets, extra closing content,
  `[tools.node] garbage`, nested/extra bracket forms and array-table syntax
  such as `[[tools.node]]`;
- return an error for any nonblank line beginning with `[` that is not a valid
  supported basic-table header. It must never degrade malformed syntax into a
  normal data line.

### Integration requirements

- Make `ParseTOML` the reference error path and migrate all four line-oriented
  scanners to the shared helper. There must be one definition of where a
  supported header ends and an inline comment begins.
- `upgradeV1Registry` must recognize `[tools.NAME] # comment`, preserve the
  user's original comment/newline style where practical and never partially
  migrate a validated profile because its scanner disagreed with `ParseTOML`.
- `SetDefaultVersion` must recognize commented `[defaults.FAMILY]` headers and
  update only the exact `version` key in that section. It must not change a
  similarly prefixed family or a commented/string value.
- `RewriteWithoutTools` must retain its PR #65 quote-aware behavior while using
  the shared helper. A malformed source is rejected before writing, and the
  rewritten result is parsed before atomic replacement.
- `defaultSections` consumes the compiled-in `DefaultTOML`. Since malformed
  built-in data is a programmer invariant violation, it may propagate an
  error to initialization or panic with a precise invariant message; it must
  not silently return partial default sections.
- Use the helper for the registry parser itself. Consider the lockfile parser
  only as a separate, low-risk follow-up if it uses the identical basic-table
  subset; do not broaden #69 into a general TOML parser rewrite.

### Regression matrix

Add table-driven helper tests plus caller-specific regression tests for:

- `[tools.node]`, leading/trailing whitespace and CRLF input;
- `[tools.node] # note` and `[defaults.node] # note`;
- `value = "literal # value" # real comment` returning non-header;
- `[[tools.node]]`;
- `[tools.node`, `tools.node]`, `[]`, `[[ ]]`, `[tools.node]]`,
  `[tools.node] trailing` and `[tools.node] # comment` followed by another
  valid section;
- exact/case-normalized tool and family matching without prefix collisions;
- v1 migration, default update, tool removal and default-section extraction
  all agreeing on the same commented headers;
- malformed input producing no file change.

Completion requires the normal full validation gate and a focused diff showing
that scanner behavior—not the supported TOML language—changed.

## Recommended implementation order

1. Implement #69 as a small independent correctness PR.
2. Split RM-26 into direct Python discovery and generic shared-volume expose.
3. Resolve product/security decisions for RM-24, enterprise policy, image
   trust, WSL2 and overlays before code.
4. Implement RM-29 and RM-30 only when hardware/certificate prerequisites are
   available; design RM-31 against their final artifact contracts.
5. Keep RM-19, RM-23, new hosts, plugins, SBOM and Snyk dormant until their
   stated triggers occur.

This ordering is advisory. The live issue, merged state and open PR coverage
must be checked again before every implementation unit.
