# Windows ACL diagnostic coverage

## C0 coverage notice

The Windows `cb doctor` immediate shim-directory ACL result describes that
directory's existing probe only. It does not certify the installation's ancestor
directories, redirected paths, or all managed files. A safe directory result
therefore appears alongside one `WARN ancestor ACL coverage incomplete` message.
The coverage warning also remains when the immediate probe fails; failure is
never converted into a safe result.

This adds one warning to an otherwise clean Windows doctor run. Warning counts
and `cb bugreport` output consequently change. Warnings retain their existing
exit behavior; failure counts, installation, execution and policy enforcement
are unchanged. Non-Windows runs do not acquire a Windows-only coverage warning.
Scripts should interpret the named warning rather than treating a zero-warning
summary as a complete security audit.

The warning contains no installation paths or identities. Review the
[installation ACL guide](windows-acl.md), including literal and resolved ancestor
chains and managed files, before considering changes. Do not recursively rewrite
drive/shared-parent ACLs or grant broad groups write access as an automatic fix.

This is stage C0 of the [approved #142 plan](https://github.com/AviBackToBlack/container-bin/pull/145#issuecomment-6158510705).
A separate C1 native audit uses numeric security descriptors and explicitly
limited candidate findings. Neither stage promises effective access for every
identity, universally secure installation state, or protection against future
TOCTOU path changes.

## C1 native candidate audit

Current C1 source replaces the unconditional C0 coverage notice with a bounded
native report. The legacy immediate-directory result remains separate. C1
qualifies only an installation beneath the native token user's profile whose
directory is owned by that specific user SID, with no intervening unqualified
redirection. The declared trusted managers are that validated updater SID,
SYSTEM and Administrators. An arbitrary caller, custom path or machine-wide
installation receives an incomplete result; no manager list is inferred.

The audit reads local NTFS security descriptors through handles and inspects the
literal ancestor chain, ordinary resolved handle paths, volume GUID/file IDs,
`cb.exe`, registered shims and registry. The lockfile and registry recovery backup
are included if present, with absence rechecked; lock-policy enforcement remains
separate. Hardlinked files retain their opened names and shared native identity.
Numeric owner SIDs, ordered ACE
types, flags, masks and descriptor hashes are retained for classification and
sampled rechecks. Unsupported identities (including undeclared service/domain
principals), ACE semantics, inaccessible/missing objects and changed snapshots
produce warnings. A common root owned by an undeclared service SID can therefore
remain incomplete; no extra trusted identity is silently added.

Applicable untrusted allow ACEs are candidates even when denies also exist.
Messages distinguish object DELETE, parent DELETE_CHILD, creation-only rights,
content mutation and ACL/owner changes. They do not claim real-token effective
access or demonstrated replacement. A completed scan can say only that no
candidates were found within this model, never that the installation is secure.

Junctions, symlinks and mount-point redirections remain incomplete after
inspection of the redirection object; C1 does not follow unqualified target
chains. SUBST/network roots, case-sensitive directories, unsupported filesystems
and resolved aliases also remain incomplete. Existing path dispatch remains
supported as before. This warning is not an instruction to remove a working
junction or rewrite a shared drive's ACLs.

The caller waits at most eight seconds for the native audit. If native I/O
stalls, one read-only worker may finish later; additional workers are not
accumulated. Observed progress is retained and timeout reports incomplete.
Traversal is limited to 64 components per chain, 256 objects/assets, 4096 ACEs,
64 KiB per descriptor and 24 output lines. Asset and ancestor labels avoid
printing private absolute paths; `ancestor[n]` means the nth parent of the named
asset/installation. Output and warning counts can differ from C0's single notice.
No scan binds future invocations to these handles or prevents TOCTOU changes.

Machine-policy files, externally selected verification tools, project trees and
temporary update staging are not certified by this installation candidate
model. They retain their existing independent trust/enforcement boundaries.
See the [implementation/qualification plan](native-acl-audit-implementation.md)
for native fixture evidence and explicitly unsupported scope.
