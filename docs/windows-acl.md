# Windows installation directory ACLs

ContainerBin's Windows executables and configuration belong to the **local code-execution trust boundary**. A user who can replace a shim, `cb.exe`, `container-bin.toml`, or `container-bin.lock` can potentially control commands launched by other users of that installation. `cb doctor` checks part of this boundary; a clean doctor result is **not** a complete ACL audit.

This guide covers the **native Windows installation**. The WSL frontend has a separate Linux permission model; see [WSL](wsl.md).

## Choose a location with a trusted parent chain

Prefer a **per-user installation** below an appropriately protected user-profile directory, for example `%LOCALAPPDATA%\Programs\ContainerBin`, when the user will run `cb setup`, `cb install`, `cb expose`, or `cb self-update` without elevation. A machine-wide installation under `C:\Program Files\ContainerBin` normally requires elevation for mutations, unless a deliberately managed administrator-approved access policy exists.

A custom path such as `D:\Tools\ContainerBin` can also work, **but inspect every ancestor**. If `Authenticated Users` has `Modify` on `D:\` or `D:\Tools`, locking down only `D:\Tools\ContainerBin` is insufficient: rights to delete or replace children in the parent directory may allow the whole installation directory to be replaced. In particular, `DELETE_CHILD` on a parent and `DELETE` on the object are separate Windows access rights. Avoid changing permissions on an entire drive just to fix ContainerBin; changing `D:\` can affect unrelated software and data. Prefer moving the installation to a path with a trusted parent chain, or have an administrator review the parent ACLs.

## Required access, by principal

| Principal | Installation directory | `cb.exe` and managed files | Reason |
| --- | --- | --- | --- |
| Trusted installing/updating account | Write/create/rename/delete (usually `Modify` or `Full control`) | Read/execute **and write** (usually `Modify` or `Full control`) | `cb setup`/`install`/`expose` reconcile files and must create temporary files and replace shims; NTFS hardlink creation also requires sufficient rights on the **source** `cb.exe` |
| `SYSTEM` and `Administrators` | Usually `Full control` | Usually `Full control` | Standard Windows management and recovery access |
| Other users who only need to invoke the shims | At most `Read & execute` | At most `Read & execute` | Invocation does not require the right to replace the executable or registry |
| Untrusted accounts/groups (`Everyone`, `Authenticated Users`, `BUILTIN\Users`) | **No write/create, `Modify`, `Full control`, `Delete`/`DeleteChild`, `ChangePermissions` (`WRITE_DAC`), or `TakeOwnership` (`WRITE_OWNER`) rights** | **No write/replace, `Delete`, `ChangePermissions`, or `TakeOwnership` rights** | Prevent another local account from altering commands, configuration, ownership or ACLs |

**Important:** `Authenticated Users` includes the current user *and* other authenticated users. Grant required write access to the **specific trusted account SID**, not to `Authenticated Users` or `BUILTIN\Users`. If multiple accounts must manage one installation, treat every manager as fully trusted and use an intentionally administered ACL.

On NTFS, test results showed that `Read & execute` on the source `cb.exe` is **not sufficient** to create a hardlink, whereas granting the trusted account `Write`, `Modify`, or `Full control` on that source allows hardlink creation when the destination directory is writable. `Modify` is a convenient default; `Full control` was also validated. The source-file rights requirement is separate from write access to the installation directory.

## Read-only diagnosis

Use PowerShell (no elevation needed for readable ACLs):

```powershell
$dir = 'D:\Tools\ContainerBin' # Change to your installation directory

# See both the directory and the hardlink source's ACL.
icacls.exe $dir
icacls.exe (Join-Path $dir 'cb.exe')

# Inspect the literal ancestor chain, including the drive root.
# A reparse point or SUBST drive needs an additional resolved-path audit.
$reparseFound = $false
$p = (Get-Item -LiteralPath $dir -Force -ErrorAction Stop).FullName
while ($p) {
    $entry = Get-Item -LiteralPath $p -Force -ErrorAction Stop
    Write-Host "--- literal path: $p ---"
    if ($entry.Attributes -band [System.IO.FileAttributes]::ReparsePoint) {
        $reparseFound = $true
        Write-Warning "Reparse point: $p (target: $($entry.Target -join ', '))"
        & icacls.exe $p /L # Inspect the link object, not just its target.
        if ($LASTEXITCODE -ne 0) { throw "Failed to inspect reparse point ACL: $p" }
    }
    & icacls.exe $p
    if ($LASTEXITCODE -ne 0) { throw "Failed to inspect ACL: $p" }
    $parent = Split-Path -Path $p -Parent
    if (-not $parent -or $parent -eq $p) { break }
    $p = $parent
}

# SUBST drive letters redirect the root without appearing as reparse points.
$root = [System.IO.Path]::GetPathRoot($dir)
$substMappings = @(& subst.exe)
if ($LASTEXITCODE -ne 0) { throw 'Failed to inspect SUBST mappings' }
$substForRoot = @($substMappings | Where-Object {
    $_ -match ('^\s*' + [regex]::Escape($root) + '\s*:')
})
if ($substForRoot.Count -gt 0) { Write-Warning ($substForRoot -join '; ') }
if ($reparseFound -or $substForRoot.Count -gt 0) {
    throw 'ACL audit incomplete: inspect link/mapping objects and the resolved target ancestor chain separately.'
}

# Inspect whether shims share the same NTFS file with cb.exe.
fsutil.exe hardlink list (Join-Path $dir 'cb.exe')

cb doctor
```

**Reparse points and redirected drives:** This literal-path walk is **not** a complete physical-path audit when a directory junction, symlink, mount point, or `subst` drive is involved. The code flags such cases and deliberately stops instead of reporting an incomplete audit as safe. Inspect the link object using `icacls.exe <link-path> /L` and review its containing literal parents; separately resolve its actual target (including relative link targets) and inspect **every directory in the resolved target's ancestor chain**, up to that volume root. `Get-Item.FullName` and a string-parent walk are not evidence that a junction target was fully resolved. If the full mapping or effective access cannot be established, choose a non-redirected, trusted installation path or ask an administrator to review it.

Look for dangerous allow rights granted to broad groups on the installation directory **or its parents**, and on `cb.exe` and other managed files. This includes `Modify` (`M`), `Write` (`W`), `Full control` (`F`), object-level `Delete` (`DE`), parent-level `DeleteChild` (`DC`), `ChangePermissions` / `WRITE_DAC` (`WDAC`), and `TakeOwnership` / `WRITE_OWNER` (`WO`). The specific rights `WD` (**write data/add file**) and `AD` (**append data/add subdirectory**) are also unsafe grants to untrusted principals; unlike `WDAC`, `WD` does **not** mean change permissions. Also inspect deny entries: interpreting effective access requires more than matching one ACE. An inherited ACE marked `(I)` still applies; `(IO)` means *inherit-only* and does not grant access to the directory itself.

Do not interpret a hardlink list as a security audit. Hardlinks identify the same NTFS file; copies can work correctly but are not the preferred deployment mode.

## `cb install` reports `(copy fallback)` for every shim

This can happen **after hardening ACLs**. The `cb install` code first attempts an NTFS hardlink from `cb.exe` to a temporary filename in the same directory, then copies the bytes if linking fails. If the trusted user can write the directory but only has `Read & execute` on **`cb.exe` itself**, the hardlink attempt can fail with access denied. `cb doctor` then reports warnings such as `shim cargo is a copy, not a hardlink`.

Do not recursively grant access to `Authenticated Users` to make this warning disappear. First verify the installation lives under a trusted parent chain, close processes using shims if necessary, and restore rights to the **trusted account only**. For a per-user installation, a focused repair might be:

```powershell
$dir = 'D:\Tools\ContainerBin' # Only after reviewing the ancestor ACLs
$me = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value

# Run from an appropriately authorized account; grant to this exact SID.
# Native commands need explicit exit-code checks in Windows PowerShell and pwsh.
& icacls.exe $dir /grant "*${me}:(OI)(CI)M"
if ($LASTEXITCODE -ne 0) { throw "icacls directory grant failed (exit $LASTEXITCODE); stopping" }

& icacls.exe (Join-Path $dir 'cb.exe') /grant "*${me}:M"
if ($LASTEXITCODE -ne 0) { throw "icacls cb.exe grant failed (exit $LASTEXITCODE); stopping" }

& cb install
if ($LASTEXITCODE -ne 0) { throw "cb install failed (exit $LASTEXITCODE); stopping" }

& fsutil.exe hardlink list (Join-Path $dir 'cb.exe')
if ($LASTEXITCODE -ne 0) { throw "hardlink inspection failed (exit $LASTEXITCODE); stopping" }

cb doctor
```

Review ACL changes before making them; save a backup with `icacls.exe <directory> /save <file> /T` when modifying existing installations. If any `icacls` command fails, **stop** rather than continuing. The example does **not** fix writable parent directories or repair unrelated ACEs. Do not apply `icacls /reset /T` or blanket drive-wide ACL changes as a ContainerBin workaround.

The source `cb.exe` and existing hardlinks share one NTFS file and thus its file-level ACL. The copy-fallback shims created earlier are separate files until successfully replaced by `cb install`.

## `cb doctor` says `could not determine shim directory permissions`

This warning means the probe **could not classify** the directory; it is **not** proof that the ACL is safe or unsafe. One confirmed issue in **v2.0.1** is Windows PowerShell 5.1 inheriting a PowerShell 7 `PSModulePath` from its parent process. The Windows PowerShell child may fail to load `Microsoft.PowerShell.Security` / `Get-Acl`, return exit code 0 with invalid output, and produce this warning. See [issue #141](https://github.com/AviBackToBlack/container-bin/issues/141).

Temporary diagnostic workaround (modifies the environment of **only the current PowerShell process**, and restores it immediately afterward):

```powershell
$previous = $env:PSModulePath
try {
    Remove-Item Env:PSModulePath -ErrorAction SilentlyContinue
    cb doctor
}
finally {
    $env:PSModulePath = $previous
}
```

If this changes the result to `OK shim directory is not writable by other users`, that is strong evidence of the **probe environment issue**; it does **not** certify the parent directories. Do not delete `PSModulePath` permanently or disable the ACL check. Inspect ACLs with `icacls` independently.

## Verification checklist

1. The executable resolves to the intended installation: `Get-Command cb, cargo | Select-Object Name, Source`.
2. The directory, registry, lockfile and shims cannot be modified or replaced by untrusted accounts; the **entire parent chain** has been reviewed.
3. The trusted updating account can modify the directory **and the hardlink source `cb.exe`**.
4. `cb install` reports `(hardlink)` for managed shims, and `fsutil hardlink list` shows the expected executable names.
5. `cb doctor` reports no ACL or shim warnings (after accounting for the documented `PSModulePath` issue), and tool execution succeeds.

For the broader execution and image-trust boundaries, see [security model](security-model.md).
