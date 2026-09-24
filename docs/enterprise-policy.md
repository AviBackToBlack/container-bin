# Enterprise machine policy

ContainerBin can apply a fixed, administrator-owned authorization policy after
the user registry, lockfile and command line have resolved a request. The
policy is a constraint layer, not another registry: it cannot add profiles,
change their settings or be weakened by `container-bin.toml`.

## Location and protection

The location is compiled in and has no environment-variable or command-line
override:

- Windows: `C:\ProgramData\ContainerBin\policy.toml`
- native Linux and WSL: `/etc/container-bin/policy.toml`

A missing file means unmanaged operation and preserves the existing behavior.
A present file must be readable, valid and sufficiently protected. Otherwise
every non-bootstrap invocation fails before registry mutation or Docker use.
`cb version`, `cb config` and help remain available for recovery.

On Windows, neither the policy nor its parent directory may be a reparse point.
Both must be owned by `SYSTEM` or the built-in Administrators group. The policy
file may not grant write, modify, delete, ownership or permission-changing
rights to another principal. The parent may allow users to create entries, but
may not let them delete or replace protected children or change ownership or
permissions. A user-created lookalike still fails the file-owner check.

On Linux/WSL, both the file and parent directory must be real paths owned by
root and may not be group- or world-writable.

ContainerBin never creates or edits this file. Provision it and its ACL/mode
with the machine's normal administrator configuration-management mechanism.

## Schema 1 — image-origin and lock constraints

```toml
policy_version = 1
require_lock = true
allow_local_images = false
allowed_repositories = [
  "docker.io/library",
  "ghcr.io/acme/developer-tools",
  "registry.example.com:5443/platform",
]
expires_at = "2027-01-01T00:00:00Z"
```

Unknown or duplicate keys, sections, malformed values, unsupported versions
and expired policies are errors. `expires_at` is optional and, when present,
must be an RFC 3339 timestamp. At least one actual constraint
(`require_lock` or a non-empty repository allowlist) is required.

`require_lock = true` rejects every unlocked or stale request, including shim
execution, expose discovery, self-test tool execution and diagnostics that
would inspect an image. It does not prevent `cb lock` or `cb update` from
creating the required entry.

Local image-ID locks have no registry origin and are rejected by default under
a managed policy. `allow_local_images = true` is the explicit exception. It
does not make an unlocked local tag acceptable when `require_lock = true`.
Use `cb add TOOL --image IMAGE --local` when creating a profile for such an
image, then follow the reported explicit `cb lock --local TOOL` or
`cb update --local TOOL` command. ContainerBin never guesses local intent from
the daemon's current image metadata.

Repository rules are canonical namespace boundaries:

- `python:3.13` and `docker.io/python:3.13` normalize to
  `docker.io/library/python`;
- `index.docker.io` and `registry-1.docker.io` normalize to `docker.io`;
- tags and digests do not affect origin authorization;
- a host-only rule allows that registry; a longer rule allows that repository
  and descendants;
- a single-segment rule such as `python` means the Docker Hub namespace
  `docker.io/python`; it does not match the official image repository
  `docker.io/library/python`;
- an unqualified multi-segment rule such as `astral-sh/uv` means
  `docker.io/astral-sh/uv`;
- `ghcr.io/acme` does **not** allow `ghcr.io/acme-tools`.

Authorization occurs before a repository-mode `docker pull`, local-image
inspection, expose discovery or `docker run`. A managed-policy restore is
preflighted against the complete archived registry and lock before any state or
configuration is changed. Switching a runtime default likewise requires every
target profile to be authorized first.

The compiled-in state backup/restore helper is not a user-selected registry
profile and is outside `allowed_repositories`. It is an exact Alpine digest,
is never pulled implicitly, and runs with networking disabled and a read-only
container root. Docker daemon policy may still reject it, and state operations
fail if that exact helper image is not already available.

## Diagnostics and error contract

`cb doctor`, `cb inspect TOOL`, `cb trace TOOL ...` and `cb bugreport` report
whether operation is managed, the schema, effective switches, rule count,
expiry, fixed source path and SHA-256 fingerprint. They do not print the policy
file contents. `cb inspect` and `cb trace` also show the selected tool's
authorization result.

Policy failures have a stable bracketed code suitable for log processing:

- `policy.unreadable`
- `policy.ownership`
- `policy.syntax`
- `policy.version`
- `policy.expired`
- `policy.lock_required`
- `policy.local_image_denied`
- `policy.repository_denied`

The fingerprint hashes the exact policy bytes. It is an audit correlation
value, not a signature. Image-signature policy remains a separate roadmap
stage and is not implied by either schema.

## Schema 2 — authenticated registry bytes

Schema 2 retains every schema 1 control and can additionally require a detached
Ed25519 signature for `container-bin.toml`:

```toml
policy_version = 2
require_lock = true
allowed_repositories = ["docker.io/library", "ghcr.io/acme"]
require_registry_signature = true
registry_signing_keys = [
  "ops-2026|BASE64_OF_RAW_32_BYTE_ED25519_PUBLIC_KEY|2026-01-01T00:00:00Z|2027-01-01T00:00:00Z",
  "ops-2027|BASE64_OF_RAW_32_BYTE_ED25519_PUBLIC_KEY|2026-12-01T00:00:00Z|2028-01-01T00:00:00Z",
]
revoked_registry_key_ids = ["compromised-2025"]
expires_at = "2027-06-01T00:00:00Z"
```

`registry_signing_keys` entries are
`KEY_ID|PUBLIC_KEY_BASE64|NOT_BEFORE|EXPIRES_AT`. Key IDs are case-sensitive,
start with a lowercase ASCII letter and then contain only lowercase letters,
digits, `.`, `_` or `-` (64 characters maximum). The public key is canonical
padded base64 of the raw 32-byte Ed25519 public key. Key timestamps are
whole-second UTC RFC 3339 values ending in `Z`; expiry is exclusive.

Enabling `require_registry_signature` requires at least one currently active,
non-revoked key. Duplicate IDs, duplicate public keys, malformed validity
windows and duplicate revocations reject the complete policy. Revocation wins
over presence in the trusted-key list. Multiple active keys are the supported
rotation window: provision overlapping old/new keys in policy, deploy that
policy, re-sign the registry with the new key, then revoke or remove the old
identity. Never remove the only signer before the new signature is deployed.

The detached file is exactly `container-bin.toml.sig` beside the registry and
uses this strict envelope:

```toml
signature_version = 1
algorithm = "ed25519"
key_id = "ops-2027"
signature = "BASE64_OF_RAW_64_BYTE_ED25519_SIGNATURE"
```

The Ed25519 message is the complete byte sequence of `container-bin.toml`
itself—no prehash, canonicalization, newline conversion, BOM removal or parsed
representation. Any comment, whitespace or line-ending change therefore needs
a new signature. The envelope is bounded to 16 KiB, must be a regular
non-symlink file and rejects unknown/duplicate fields, unsupported versions,
other algorithms and noncanonical base64.

ContainerBin does not generate keys, read a private key or sign registries.
Create the raw Ed25519 signature in the administrator's protected signing
system, construct the envelope, then provision the registry and envelope as one
configuration-management transaction. Private keys must never live beside the
registry or in the machine policy.

Authentication happens on the raw bytes before TOML parsing, default fallback,
`.bak` recovery, shim reconciliation or Docker use. A missing signed registry
does not fall back to the built-in registry and does not auto-restore an
unsigned backup. The parser acts only on the already authenticated in-memory
bytes, so a later on-disk change cannot alter that invocation's effective
registry.

Signed mode deliberately makes the registry read-only to ContainerBin.
`cb add`, `cb default set`, `cb expose`, `cb unexpose`, `cb uninstall` and
`cb restore --apply` fail before mutation. An administrator must produce and
provision the new registry/signature pair. `cb install` and `cb setup` skip
registry creation/upgrades but may reconcile shims from an already authenticated
registry; a missing signed registry still fails closed. Read-only commands and
lockfile-only operations remain available. `cb backup` includes a valid bounded
detached envelope and re-verifies the exact snapshot when signed mode is active.
An invalid optional envelope is skipped with a warning when policy is unmanaged;
a required invalid envelope still fails. Signed-policy `cb restore` can verify
and preview that archive, but applying it remains an administrator provisioning
operation. An unmanaged restore of an unsigned archive removes any stale
envelope and its backup.

Schema 1 remains supported unchanged. Registry-signature fields in schema 1
are rejected, and policy versions newer than 2 fail closed. This makes rollback
to a ContainerBin build that predates schema 2 fail visibly instead of silently
ignoring the authentication requirement.

Additional stable error codes are:

- `policy.registry_signature_missing`
- `policy.registry_signature_invalid`
- `policy.registry_signer_unauthorized`
- `policy.registry_signer_inactive`
- `policy.registry_signed_readonly`

Policy summaries report whether registry signatures are required plus trusted
and revoked key counts. They never print public-key material or signature
contents.
