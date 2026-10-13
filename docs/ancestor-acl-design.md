# Windows ancestor ACL diagnostic: design review for #142

Status: **proposal; maintainer approval required before implementation**.
Baseline: main `5c27388912d47cc19ef36aba2afab58fe2644641`, including #144.
Reviewed 2026-10-14. No security enforcement or runtime behavior changes are
part of this document. The field report is a potential misleading diagnostic,
not a demonstrated exploit under a separate attacker's token.

## Threat model and scope

Assets are the installation namespace, `cb.exe`, registered shims, registry,
lockfile, and any trusted configuration or verification executable reached
through that namespace. An attacker is a different local account or service
identity with filesystem authority outside the intended trusted managers.
The attack is replacement, redirection, or mutation before a subsequent
invocation by a victim. Denial of service is a separate possible consequence;
deletion authority alone does not prove executable replacement succeeds.

For a per-user installation, propose trusting the installing user SID, SYSTEM,
and elevated Administrators. A machine-wide installation must use explicitly
approved manager identities: the inspecting user's SID is not automatically
a trusted updater merely because that user runs doctor. Administrators and
SYSTEM are within this model's trust boundary; arbitrary elevated compromise,
kernel compromise, malicious Docker images and malicious packages are outside
this diagnostic's promise. Service SIDs and domain groups require an explicit
manager decision, not a name-based exemption.

Coverage must include the literal installation chain, every resolved target
chain, and each name-redirection object. Also inspect individual managed
files: a protected directory does not repair a writable existing file. A
complete installation audit is larger than the current directory-only probe.
Do not expand machine-policy enforcement or self-update in this work item.

## Windows semantics that the design must preserve

1. File deletion/rename can be authorized by `DELETE` on the object or
   `FILE_DELETE_CHILD` on its parent. A deny on the child must not be assumed
   to cancel the parent's independent authority. An actual replacement also
   depends on creating the replacement name, directory/nonempty behavior,
   sharing modes, attributes and the operation used. Test these separately;
   do not label every broad parent ACE a confirmed replacement exploit.
   [DeleteFileW](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-deletefilew)
   and [access constants](https://learn.microsoft.com/en-us/windows/win32/fileio/file-access-rights-constants).
2. Rights are object-specific: `FILE_ADD_FILE` and `FILE_ADD_SUBDIRECTORY`
   create children; `WRITE_DAC`, `WRITE_OWNER`, ownership and privileges can
   change future authority. Broad creation rights alone on a drive root are
   not evidence of authority to replace an existing protected child. Inspect
   each parent/child edge, including the volume root and the first child below
   it; the volume root has no ordinary filesystem parent to walk.
3. Access checks depend on the actual token and ordered DACL, rather than
   independent SID/allow/deny string comparisons. Enabled groups, deny-only
   groups, restricted tokens, UAC elevation, owner behavior, privileges,
   generic mappings and conditional ACEs matter. A current-user check cannot
   establish the rights of every other user. [AccessCheck semantics](https://learn.microsoft.com/en-us/windows/win32/secauthz/how-dacls-control-access-to-an-object)
   and [privileges](https://learn.microsoft.com/en-us/windows/win32/secauthz/privileges).
4. Inherited effective ACEs apply; inherit-only ACEs do not grant access to
   the containing object. Protected inheritance, creator-owner substitution,
   propagation flags and the actual existing child's DACL must be considered.
   [Inheritance rules](https://learn.microsoft.com/en-us/windows/win32/secauthz/ace-inheritance-rules).
   Traverse restrictions alone are not a safe boundary because bypass-traverse
   privilege is common. Parent DACLs are not a substitute for child DACLs.
   [File security](https://learn.microsoft.com/en-us/windows/win32/fileio/file-security-and-access-rights).

The current `shimDirACLVerdict` drops all allow findings for a SID if it sees
any write-capable deny for that SID, and lacks inheritance flags and numeric
masks. Its `OK` is not an effective-access proof. Reusing it recursively would
multiply that limitation. Fixing #141's module environment does not fix it.

## Proposed validation semantics

Recommend an explicitly limited, read-only ancestor diagnostic first, pending
approval. It must report scope and uncertainty even when the legacy
directory-only result is OK. Keep execution and installation exit behavior
unchanged in the first implementation; an unreadable or incomplete audit is
`WARN`, never a successful security certification.

Proposed outcomes:

| Outcome | Meaning and output |
| --- | --- |
| Observed authority | WARN with path, SID, numeric mask, right names, parent/child edge and evidence type; distinguish a candidate ACE from a verified effective grant |
| Incomplete | WARN with the unresolved path/stage and reason; retain any previously observed risks |
| Limited scan complete | State that all declared chains were inspected and no candidate authority was found within the declared identity/ACE model; explicitly deny any claim about all possible tokens or future races |

Do not print `installation secure` or `not writable by other users` as the
conclusion of the new ancestor scan. Unknown ACE types, unresolved identities,
missing descriptors, failed native calls, unexpected filesystem types, null
DACLs, or an unresolved trust set cannot silently become a clean result. A
null DACL grants broad access; an empty DACL denies ordinary discretionary
access, but owner/privilege and parent-edge authority still require analysis.

Proposed read-only steps, not an implementation commitment:

1. Resolve the installation from the actual executable; record the caller's
   token/elevation and declared manager scope. Reject an ambiguous input for
   this audit without changing whether ordinary tool dispatch is supported.
2. Open each directory/object for metadata and security inspection. Inspect
   reparse objects separately, using appropriate native handle flags; opening
   only the final component with `OPEN_REPARSE_POINT` does not establish that
   intermediate components were not traversed. Obtain security descriptors
   from handles where possible. Preserve numeric masks, owner, ACE order and
   flags. Do not use localized `icacls` text as a parser protocol.
3. Walk literal parents and resolved target parents to their volume roots.
   Track parent/child edges and stable object identities; bound nodes and
   redirection depth, detect cycles, and report incomplete on limits. For each
   edge, assess child mutation/deletion/ACL authority and parent deletion and
   replacement-name creation authority separately.
4. Report dangerous applicable allow ACEs as candidates. Do not subtract a
   deny on the same SID as a shortcut. Effective verification requires a
   specified real token and Windows access-check APIs, plus operation-level
   fixtures. Neither group membership expansion nor fabricated tokens prove
   every possible attacker's rights. If this distinction is too confusing in
   default doctor output, ship only the coverage warning until a stronger
   design is approved.
5. Recheck object/volume identities and descriptors at the end. Changed data
   makes the scan incomplete. This detects some changes; it does not make the
   result atomic or bind a future Windows launch to the inspected handles.

Use bounded, deterministic diagnostics. SID and path identify a local finding
but may be private; bugreport redaction/export behavior needs review before
adding more host paths. Do not dump complete DACLs, group membership or raw
PowerShell errors by default. Never automatically rewrite ancestors or
recommend broad writable groups as a repair.

## Redirection, volume identity and races

Junctions, symlinks (including relative targets), mount points and nested
redirections require inspecting both the containing namespace and the target.
A final resolved string alone can lose vulnerable link objects. Unknown
reparse tags must remain incomplete; some tags are filter-defined rather than
ordinary links. Existing junction support in `docs/windows-paths.md` must not
be silently removed. [Reparse points](https://learn.microsoft.com/en-us/windows/win32/fileio/reparse-points)
and [CreateFile flags/sharing](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilew).

SUBST and mapped drives are DOS device mappings, not necessarily filesystem
reparse points. Query their mappings in the relevant caller context and bind
inspection to the underlying volume/object, not the drive-letter spelling.
Record volume identity plus file ID; handle volume roots, same-target aliases,
8.3 names and casing without string-prefix trust tests. A per-user/session
mapping may differ in an elevated token or Docker service context. Native
extended-length paths returned by an inspection API are internal evidence;
they do not expand supported user path syntax. Remote shares and unfamiliar
filesystems remain incomplete in an NTFS-focused initial design.
[QueryDosDevice](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-querydosdevicew)
and [GetFinalPathNameByHandle](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-getfinalpathnamebyhandlew).

Retaining handles without delete sharing can interfere with legitimate
installers and does not secure future path-based launches after doctor exits.
Repeated snapshots cannot prevent an attacker changing and restoring state
between samples. A tamper-prevention claim would require a separately designed
execution boundary and is out of scope. Preserve the existing documented
TOCTOU limitation; do not turn a best-effort inspection into enforcement.

## Alternatives and compatibility

| Approach | Benefit | Cost / reason not to assume approval |
| --- | --- | --- |
| Coverage warning plus existing manual guide | Smallest truthful change; works for supported redirections | Does not automatically identify the vulnerable edge |
| Conservative native descriptor/ACE scan (proposed first automated stage) | Specific candidates and complete chains without domain enumeration | False positives; must never claim effective access or universal safety |
| Authz/AccessCheck for explicit real identities | Better token-specific results | Tokens, domain availability, conditional ACEs, privileges and operation semantics remain complex; does not quantify every user |
| Enforce protected path layouts or reject every redirection | Easier rule | Breaks supported custom/junction installations and needs a public compatibility decision |
| Hold handles through future launch / redesign updater | Potentially stronger boundary | Architectural change beyond doctor; locking and launch integration need a separate threat model |

[AuthzAccessCheck](https://learn.microsoft.com/en-us/windows/win32/api/authz/nf-authz-authzaccesscheck)
evaluates access for a client context, not all possible clients.
No new runtime dependency is selected here. Any proposed Win32 bindings must
follow repository dependency policy and be reviewed separately. Default
doctor warning volume and bugreport output compatibility need acceptance.
Keep Windows x64/ARM64, Windows PowerShell 5.1/PowerShell 7 launch environments,
non-English systems and nonelevated operation in qualification; no claim of
new Windows-version or filesystem support is made.

## Required test matrix before a security claim

These are planned fixtures, **not executed tests or an exploit demonstration**.
Use owned temporary NTFS trees for read-only scanning; compare descriptors,
object identities and data before/after to prove the scan did not mutate them.
Actual deletion/replacement probes belong in separately authorized isolated
fixtures under a real distinct token, never on the production installation.

| Fixture | Expected distinction |
| --- | --- |
| Protected per-user tree / administrator-owned machine tree | Declared managers and full chain coverage; no universal effective-access assertion |
| Broad parent DELETE_CHILD, protected child denying DELETE | Parent authority still reported; separately test file, empty directory, nonempty directory rename/replacement |
| Child DELETE with parent create rights / DELETE without create | Replacement candidate versus deletion-only authority |
| Parent create-only / broad root create-only | Do not infer replacement of an existing protected child |
| WRITE_DAC, WRITE_OWNER, untrusted owner, OWNER RIGHTS SID | Indirect authority and owner behavior handled or explicitly incomplete |
| Explicit/inherited allow/deny, partial deny, differing ACE orders | No same-SID deny cancellation shortcut; compare real-token access results |
| IO/OI/CI/NP and protected inheritance, CREATOR_OWNER | Current-object applicability separate from future inheritance |
| Everyone, Authenticated Users, Users, custom local/domain groups | SIDs retained; group/token uncertainty explicit |
| Unelevated/elevated admin, deny-only and restricted tokens | Token-specific outcomes; no implicit elevated trust for a different identity |
| Null/empty DACL, unknown/conditional ACE, failed SID lookup | Broad grant versus restricted/incomplete; never silent OK |
| Safe directory with writable registry/shim/lock | Directory-only success must not certify installation assets |
| Relative symlink, junction, mount point, nested links, cycle, unknown tag | Both namespaces, bounded walk, correct volume roots or incomplete |
| SUBST, same target through two drives, network mapping, 8.3/case alias | Stable volume/file identity; mapping uncertainty explicit |
| Unreadable/missing component, timeout, malformed output | WARN and retained earlier findings |
| Rename/reparse/ACL race; change-and-restore race | Detect sampled changes; document undetectable races |
| Read-only attribute, open executable, sharing violation | Operation failure distinguished from ACL security proof |
| x64/ARM64, PS5.1/PS7, localized Windows | Identical numeric/SID semantics without localized string parsing |

## Approval gate and unresolved decisions

Before implementation, the maintainer must select the initial approach and
accept its output semantics; choose per-user versus machine-wide manager
identification; decide whether limited scans may ever emit OK; approve the
candidate-ACE/effective-grant distinction and handling of complex ACLs;
approve supported redirection/filesystem scope and warning compatibility;
and authorize a real second-identity qualification environment if an exploit
or effective-replacement claim is desired. Existing hosts and Z-LAPTOP are
not implicitly authorized for account creation or infrastructure changes.

Unresolved: reliable enumeration of all relevant attacker tokens; domain and
conditional-ACE evaluation when offline; exact directory replacement outcomes
under each supported Windows build, filesystem and sharing mode; complete
handling of filter-defined reparse points; resistance to adversarial races;
and host-path redaction across doctor/bugreport. These are reasons to limit
claims or report incomplete, never reasons to infer a safe result.

Task C stops at this document. Tasks #141 and #143 neither implement nor
approve these semantics. No implementation should begin until the human
maintainer explicitly approves the design.
