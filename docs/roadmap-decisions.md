# Roadmap decisions and implementation queue

Status date: **2026-09-19**

This document records maintainer decisions for the remaining roadmap items in
issue #2. These decisions are authoritative scope for implementation work unless
a later maintainer decision explicitly supersedes them.

The goal is to prevent design-gated, conditional, or deliberately deferred
items from being repeatedly rediscovered as if they were immediately actionable.

## Decision summary

| Item | Decision | Implementation status / trigger |
|---|---|---|
| RM-24 Python / uv | **Keep both** | Decision complete. Built-in `python`/`pip` keep the dedicated Python provider; `uv`/`uvx` remain separate opt-in stateful profiles. |
| RM-26 Python global CLI exposure | **pipx yes; plain pip expose no** | Ready. Add a separate stateful pipx profile and managed pipx tool store; do not globally expose project/compat `/venv/bin` from plain pip. Generic shared-volume exposure already shipped. |
| RM-34 Cargo expose enhancement | **Intentionally deferred** | Existing expose-all and explicit binary selection are sufficient. Reopen only for a concrete unmet use case. |
| WSL2 | **Native WSL frontend** | Ready for design/implementation slices. Native Linux `cb`/shims inside WSL use Docker Desktop WSL integration. No Windows↔WSL path/state guessing. |
| Enterprise policy | **Machine-owned constraint layer** | Ready. Separate administrator policy validates the resolved user/project/CLI request and can only restrict, never be weakened by lower layers. |
| Image trust | **Policy-driven Sigstore/cosign at lock time** | Ready after enterprise-policy foundation. Digest locking remains default where policy permits. Required trust never silently falls back to digest-only. |
| Per-project overlays | **Explicit digest-bound, add-only trust model** | Ready after enterprise-policy foundation. Initial overlays exclude host mounts, env prefixes and shared cross-project volumes. |
| Plugin/provider architecture | **Intentionally deferred** | Reopen only after at least two concrete integrations cannot be expressed safely by the declarative model. |
| RM-31 self-update | **Explicit transactional, attestation-verifying update** | Ready after command/API slicing. Initial provenance verifier is `gh attestation verify`; Windows apply uses a post-exit helper and complete managed-set rollback. |
| RM-30 Authenticode | **Design accepted; externally blocked** | Implement only after a real code-signing certificate and protected signing mechanism exist. Stable and prerelease release artifacts are both signed. |
| RM-29 Windows ARM64 | **Lowest priority** | Native GitHub Windows ARM64 CI may be added later. Full support remains blocked on real Windows-on-Arm + Docker Desktop qualification. Do not delay other roadmap work. |
| Standalone Linux/macOS | **Demand-gated** | No support claim yet. WSL should create reusable narrow Linux host abstractions, but standalone hosts require their own contract and real Docker qualification. |
| RM-23 8.3 mount alias | **Intentionally deferred** | Current comma-path rejection remains supported behavior. Reopen only on demonstrated user demand. |
| RM-19 reserved-name migration | **Conditionally deferred** | Implement only when a future release actually proposes reserving a previously legal name. |
| Release SBOM | **Conditionally deferred** | Trigger on shipped third-party/runtime dependencies or a concrete consumer/compliance requirement. |
| Snyk | **Conditionally deferred** | Trigger only for a concrete coverage gap plus an owner/account/token and triage/outage policy. |
| govulncheck pin | **Recurring maintenance** | Never a one-time completion gate. Deliberately refresh when a newer stable upstream release is adopted. |

## Accepted contracts

### RM-24 — keep Python and uv separate

The built-in `python`, `python3`, `pip` and `pip3` profiles keep the
dedicated Python provider and its current per-project `/venv`, pip-cache,
project-marker and compatibility-fallback semantics.

`uv` and `uvx` remain separate opt-in stateful profiles. ContainerBin does
not automatically migrate unversioned Python commands to uv and does not choose
between the two providers heuristically.

### RM-26 — pipx is the classic-Python global application store

Plain pip environments remain dependency environments. Console scripts in the
Python provider's `/venv/bin` are environment-local and are not eligible for
global `cb expose`; ContainerBin must not guess which project or compatibility
venv owns a global shim.

Add direct pipx support as a separate stateful profile with ContainerBin-owned
persistent `PIPX_HOME` and `PIPX_BIN_DIR`. `cb expose pipx` discovers and exposes every eligible binary in that managed
bin store; `cb expose pipx BINARY...` performs deterministic explicit selection.
Both forms must preserve the existing provenance, collision and fail-closed
unexpose rules.

`uv tool` remains the already-supported alternative. No automatic migration
or interoperability between pip, pipx and uv-tool stores is implied.

### RM-34 — no Cargo enhancement without a real need

The existing `cb expose cargo` store-wide discovery and
`cb expose cargo BINARY...` explicit selection are sufficient.

Do not add crate-name inference, install-history tracking or additional
Cargo-specific selection behavior solely for roadmap completeness. A future
package-aware mode may be reconsidered only for a concrete user-visible case
and must rely on authoritative Cargo-owned metadata, never filename guessing.

### WSL2 — native WSL process model

WSL2 support uses a native Linux `cb` binary and Linux shims/symlinks inside
the selected WSL distribution, with Docker Desktop's supported WSL integration.

The Windows `cb.exe` interop path and a Windows client talking to a Docker
engine exposed by a WSL distro are not supported WSL execution models.

Windows and WSL installations have separate config, lockfiles, shim layouts,
project identities and ContainerBin state namespaces. ContainerBin does not
infer identity equivalence between `C:\x`, `/mnt/c/x` or `\\wsl$\...`.
Native Linux path, permission, symlink, case, TTY and signal semantics apply.

The accepted native layout is fixed rather than XDG-configurable: the managed
binary is `~/.local/lib/container-bin/cb`; management and tool shims are under
`~/.local/bin`; the registry and lockfile are under
`~/.config/container-bin`; and private state is under
`~/.local/state/container-bin`. The home must be a canonical distribution-local
Linux path, never `/mnt/*`.

Docker state identity is the exact case-sensitive WSL distribution name,
canonical `/etc/machine-id` and numeric Linux UID, hashed under a versioned
domain into an opaque namespace. Every managed WSL volume must carry that
namespace in both its name and ownership labels, and all lifecycle operations
must filter by exact namespace. ContainerBin does not normalize identities or
silently adopt state across distributions, reinstalls or users.

### Enterprise policy — machine constraint layer

Enterprise policy is not another registry merge layer. User registry, future
project overlays and CLI choices first resolve an effective request; fixed
administrator policy then authorizes or rejects that request.

Initial host locations:

- Windows: `C:\ProgramData\ContainerBin\policy.toml`
- native Linux/WSL: `/etc/container-bin/policy.toml`

A missing policy means unmanaged operation. A present but unreadable, invalid,
unsupported or insufficiently protected policy fails closed before protected
mutation or Docker execution. Normal users cannot redirect policy location.

Initial controls are repository-boundary image-origin allowlisting after
canonical reference normalization, mandatory lock mode without implicit bypass,
and optional mandatory registry authentication using detached Ed25519
signatures over exact registry bytes. Lower layers may be more restrictive but
can never weaken machine policy.

### Image trust — Sigstore/cosign at lock time

Digest locking remains the compatibility default where machine policy permits
it. Signature verification is explicit per repository.

The initial signature mechanism is Sigstore/cosign. ContainerBin does not embed
the Sigstore dependency graph; an administrator-configured external cosign
binary is referenced by absolute path and pinned by SHA-256 in machine policy.

`cb lock` / `cb update` resolve the exact repository digest first and then
verify that exact digest. Structured trust evidence is recorded in a versioned
lock schema, including mechanism, repository, digest, signer identity/key,
issuer where applicable, log/bundle identity, verification time, verifier
identity/hash and effective trust-policy fingerprint.

Runtime still executes the pinned digest and does not invoke cosign on every
tool launch. A changed digest, verifier or trust-policy fingerprint makes prior
evidence stale and fails closed until explicit re-verification. Mirrors do not
inherit another repository's trust merely because the digest matches.

### Per-project overlays — add-only and explicitly trusted

A project may define `.container-bin.toml` at its canonical project root.
The overlay is executable project configuration and is never active merely
because the repository was cloned.

The initial model is add-only. Project overlays may add new project-local tools
but may not replace global tools, change default families/aliases, weaken
machine policy or implicitly merge conflicts. Collisions fail closed.

Initial overlays exclude `host_mounts`, `env_prefixes`, shared cross-project
volumes and similarly broad host capabilities. Exact environment names may be
requested and must be shown in trust review.

Trust is stored outside the repository and bound to the canonical project root
plus a SHA-256 digest of the security-relevant overlay. Moving the project or
changing the overlay invalidates trust. Interactive first use requires explicit
trust; non-interactive use fails unless trust was explicitly pre-provisioned.
Project trust never overrides machine policy.

### Plugin/provider architecture — deferred

Do not build a plugin framework as an extensibility feature by itself.
Reconsider only after at least two concrete provider integrations cannot be
expressed safely and reasonably through `stateless`, `python`, `stateful`,
`cb add`, package-manager-aware expose, or `cb expose --shared-file`.

If later introduced, discovery is explicit and trusted, protocol versions and
I/O are bounded, core retains final Docker argv/security validation, and
plugins are treated as trusted executable code unless a real sandbox exists.

### RM-31 — transactional self-update

`cb self-update` is explicit; there are no background or automatic updates.
Stable is default. Prereleases, exact versions and downgrades require explicit
options. Development builds and unprovable installations refuse normal update.

Selection/download/verification are read-only against installed bytes.
Downloads use the canonical repository and private same-volume staging.

GitHub build-provenance attestation is the authentication gate. The initial
verifier is `gh attestation verify`, enforcing the expected repository,
release workflow, release ref and downloaded artifact digest. Checksums are
additional consistency evidence and never an authentication fallback.

After verification, a narrowly scoped temporary helper waits for the parent
process to exit, serializes with other ContainerBin mutations, proves ownership
of the managed installation, preserves a validated rollback binary, replaces
the management executable, reconciles the complete proven managed shim set and
runs bootstrap version/shim-identity checks. Failure restores the prior managed
set where possible and preserves actionable recovery artifacts otherwise.
Normal operation does not schedule replacement for reboot.

### RM-30 — Authenticode release contract

Every published stable and prerelease Windows release artifact is signed once
the signing infrastructure exists. Ordinary CI/dev artifacts remain unsigned.

Unsigned reproducibility comparison happens first. The raw `cb.exe` is then
signed with SHA-256 plus RFC 3161 SHA-256 timestamp, verified for expected
publisher/chain/timestamp, and only the signed bytes are packaged, checksummed
and covered by final GitHub provenance.

The private key must be non-exportable or equivalently protected by a managed
signing service, HSM or hardware-backed mechanism and unavailable to untrusted
PR workflows. Signing/timestamp/publisher verification failure aborts release;
there is no unsigned fallback.

Implementation is blocked until the maintainer provisions a real certificate
and protected signing mechanism.

### RM-29 — Windows ARM64 is last priority

Native Windows ARM64 CI may use GitHub-hosted Windows 11 ARM64 runners for
build, native management-command execution, filesystem/shim behavior and other
non-Docker qualification.

x64-host ARM emulation is not accepted as Docker Desktop support evidence.
Full support still requires real Windows-on-Arm hardware running a supported
Docker Desktop ARM configuration and passing the real Docker release E2E suite.

Do not hold higher-value roadmap work for RM-29.

### Standalone Linux/macOS — demand gated

Do not claim standalone Linux or macOS support without a host-specific contract,
release artifacts, installer/update path, maintained CI and real Docker E2E.

WSL implementation should factor reusable narrow Linux host behavior so future
standalone Linux can reuse it, while keeping WSL-specific Docker Desktop and
identity behavior out of the generic host layer.

### RM-23 — retain comma-path rejection

ContainerBin continues to reject Windows mount-source paths that cannot be
represented safely in Docker's `--mount` grammar, including canonical project
roots containing commas.

Do not enable 8.3 generation, change filesystem policy, create aliases or
junctions, use `subst`, or rewrite project identity merely to avoid the error.
Revisit an existing-short-path fallback only after demonstrated user demand.

### RM-19 — migration only when a migration exists

Do not add reserved-name migration machinery until a future release actually
proposes reserving a name accepted by a published earlier release.

Permanently invalid names remain normal fail-closed parse errors. If a new
reservation is proposed, that same release must provide a migration-safe
diagnostic/repair path before the reservation ships; the conflicting legacy
profile may never dispatch or create a shim before repair.

### SBOM and Snyk

Release SBOM remains conditional while the shipped binary remains
standard-library-only. Trigger it when third-party/runtime dependencies become
part of the shipped product or a concrete consumer/compliance requirement
exists.

Snyk remains conditional while CodeQL, govulncheck, Dependabot and current
dependency/workflow controls cover the repository. Adoption needs a concrete
coverage gap, a security owner, external account/token and explicit triage,
exception, expiry and outage policy.

## Implementation queue

This is priority/order guidance, not permission to merge.

1. **RM-26 pipx support**
   - add a stateful pipx profile with explicit ContainerBin-owned home/bin state;
   - add `cb expose pipx BINARY...`;
   - add positive/negative Windows + Docker Desktop E2E;
   - document the pip vs pipx vs uv-tool contract.

2. **Enterprise policy foundation**
   - fixed machine policy location + ownership validation;
   - policy schema/versioning and stable diagnostics;
   - effective-request authorization boundary;
   - repository-origin allowlisting and mandatory-lock enforcement;
   - diagnostics/inspect visibility.

3. **Per-project overlay trust foundation**
   - project overlay parsing independent of global registry;
   - add-only collision rules;
   - external trust store bound to canonical root + overlay digest;
   - `cb trust` / `cb untrust` / inspect/doctor;
   - initial restricted capability set.

4. **Registry-signature enterprise policy**
   - detached Ed25519 signature envelope over exact registry bytes;
   - trusted-key rotation/revocation policy;
   - verify before parsing/acting on registry content.

5. **Image trust**
   - cosign verifier configuration and verifier hash validation;
   - per-repository trust policy;
   - lock schema/evidence migration;
   - online/offline verification and stale-evidence behavior.

6. **RM-31 self-update**
   - selection/check/dry-run API;
   - bounded canonical GitHub release download/staging;
   - `gh attestation verify` policy integration;
   - Windows helper transaction, managed-shim reconciliation and rollback;
   - release/self-test E2E.

7. **WSL2**
   - narrow reusable Linux host interfaces;
   - native WSL config/shim/state layout;
   - Docker Desktop WSL integration;
   - project identity and cross-boundary rejection tests;
   - real WSL Docker E2E.

8. **RM-30 Authenticode**
   - only after certificate/protected signing prerequisites exist.

9. **RM-29 Windows ARM64**
   - **lowest priority**;
   - native hosted ARM64 CI may precede real Docker qualification;
   - support claim only after real Windows-on-Arm + Docker Desktop E2E.

## Dormant / recurring items

The following are not implementation backlog until their trigger occurs:

- RM-19 newly-reserved legacy-name migration;
- RM-23 Windows 8.3 alias fallback;
- RM-34 additional Cargo selection behavior;
- standalone Linux/macOS support;
- plugin/provider protocol;
- release SBOM;
- Snyk.

The govulncheck tool pin is recurring maintenance, not a feature-completion
checkbox. Refresh deliberately when adopting a newer stable upstream release.
