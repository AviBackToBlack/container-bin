# Windows ACL diagnostic coverage

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
A separate C1 native audit will use numeric security descriptors and explicitly
limited candidate findings. Neither stage promises effective access for every
identity, universally secure installation state, or protection against future
TOCTOU path changes.
