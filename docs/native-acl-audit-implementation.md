# C1 implementation and qualification plan

This implements the bounded direction approved in
[the #142 decision](https://github.com/AviBackToBlack/container-bin/pull/145#issuecomment-6158510705),
following C0. C0 and C1 remain separate dependent PRs; neither approves merging
#145/#146/#147 or changes execution enforcement.

## Components and contracts

- A new internal `aclaudit` package separates native descriptor acquisition,
  byte-level SID/ACE decoding, candidate classification, traversal, and output.
  No external runtime dependency or PowerShell/localized command parser.
- Qualify the updater using the Windows token's SID/profile and installation
  ownership beneath that profile. Being the caller alone grants no manager
  exemption. Outside that qualified per-user model, report incomplete; this
  implementation does not invent a machine-wide manager configuration.
- Inspect local NTFS only. Walk components from volume root toward each target,
  opening the component itself rather than following a final reparse point.
  Include executable, registered shims, registry and lockfile and their chains.
  Include the registry recovery backup when present; recheck ordinary absence.
  Query volume/object identity and resolved handle paths, then re-read identities
  and descriptors to detect sampled changes.
- Preserve owner, DACL control bits, ordered ACE type/flags/numeric masks and
  SID values. Applicable untrusted allows remain candidates despite denies.
  Distinguish content/ACL mutation, object DELETE, parent DELETE_CHILD, and
  creation-only authority. Never infer replacement from Modify or creation alone.
- Unsupported ACE/identity semantics, null DACLs, failed reads or missing objects,
  unsupported filesystems and unqualified scope produce WARN. A null DACL also
  produces a broad-access candidate; an empty DACL is handled distinctly.
- Junctions/symlinks/mount points are inspected as redirection objects and remain
  incomplete. Do not follow unqualified targets, which could introduce remote or
  uncontrolled I/O. SUBST/network roots remain incomplete before traversal.
  Ordinary paths require resolved-path/volume identity agreement. This does not
  reject those paths for dispatch or claim their target chains were audited.
- Bound targets/components/ACE data and output. A single read-only worker per
  process has an eight-second caller deadline; a stalled native call can finish
  later, but cannot create unbounded workers. Preserve collected candidates on
  timeout; report incomplete. There is no universal OS-call cancellation claim.
- Default diagnostics use stable asset/ancestor labels, SIDs, masks and flags,
  not arbitrary native error strings or private absolute ancestor names. A
  completed scan describes only the declared candidate model, never all real
  tokens, exploitability, future safety or absence of TOCTOU races.

## Qualification

Pure tests cover all rights categories, owner/deny/inheritance handling,
null/empty DACLs, malformed/unsupported ACEs and SIDs, depth/complexity/output
limits, errors, sampled races and retained evidence on timeout. Native tests use
owned temporary NTFS trees; snapshot descriptors, identities and bytes to prove
the reader does not mutate them. Include separate writable managed assets,
reparse fixtures and caller/profile qualification. Unsupported mapping cases
must remain explicit incomplete results, never fixture skips presented as proof.

Run full Linux tests/race/vet, changed Windows package vet without adding to the
two known unrelated baseline warnings, native Windows x64 regression on the
nonelevated host, and native Windows ARM64/x64 CI. Elevated CI must exercise
scope rejection explicitly; synthetic descriptors/reduced-token fixtures do not
constitute an independently authenticated second-user exploit demonstration.
CI also launches a same-user reduced-primary-token child and asserts native
`TokenElevation=0` before qualifying and auditing its owned per-user fixture.
Failure to obtain or verify that token fails qualification rather than being
silently skipped. It creates no account and grants no new authority.

Before the final stack retarget to main, remove the two temporary named stack
bases from CI after their dependent validation is no longer needed, and rerun
all main-targeted security/integration checks. No dependency PR is merged here.

No account creation, elevation, production ACL/policy changes, destructive
replacement experiments, new VM, or Z-LAPTOP work is authorized by this plan.
Machine-wide manager declarations and full qualification of redirection targets
remain future scope, explicitly incomplete in C1.
