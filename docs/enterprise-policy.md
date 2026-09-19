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

## Schema 1

```toml
policy_version = 1
require_lock = true
allow_local_images = false
allowed_repositories = [
  "docker.io/library",
  "ghcr.io/acme/developer-tools",
  "registry.example.com:5443/platform"
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

Repository rules are canonical namespace boundaries:

- `python:3.13` and `docker.io/python:3.13` normalize to
  `docker.io/library/python`;
- `index.docker.io` and `registry-1.docker.io` normalize to `docker.io`;
- tags and digests do not affect origin authorization;
- a host-only rule allows that registry; a longer rule allows that repository
  and descendants;
- `ghcr.io/acme` does **not** allow `ghcr.io/acme-tools`.

Authorization occurs before a repository-mode `docker pull`, local-image
inspection, expose discovery or `docker run`. A managed-policy restore is
preflighted against the complete archived registry and lock before any state or
configuration is changed. Switching a runtime default likewise requires every
target profile to be authorized first.

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
value, not a signature. Registry-signature and image-signature policy are
separate roadmap stages and are not implied by schema 1.
