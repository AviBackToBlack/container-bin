# Native WSL2 activation qualification

The activation baseline ran on 2026-10-08 and was repeated against the final
code on 2026-10-09. This record belongs to the activation change based on
`60446b8162d987b45ee6fe77d7e77223afe1c0b6`. It covers source-built runtime
activation; the published candidate's archive, provenance and exact-target
self-update checks remain separate release gates in
[the release matrix](../release-matrix.md).

## Environment and binary identity

| Input | Observed value |
|---|---|
| Windows | Windows 11 x64, NT 10.0.26300.0 |
| Docker Desktop | 4.94.0, Linux-container mode |
| Docker Engine | 29.8.2 |
| User distribution | `Ubuntu-26.04`, Ubuntu 26.04.1 LTS |
| WSL kernel | `6.18.33.2-microsoft-standard-WSL2` |
| Linux account | UID 1000, member of the Docker socket's group |
| Project storage | Distribution root filesystem and default `/mnt/d` DrvFs |
| Go toolchain | go1.24.13 linux/amd64, through the existing Go shim |
| Injected build version | `v2.0.0-qualification.6` |
| Windows PowerShell | 5.1.26100.9444 |
| PowerShell | 7.6.6 |

Both binaries used `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false` and
`-ldflags "-s -w -X main.version=v2.0.0-qualification.6"`:

```text
linux/amd64 cb:
e9b5ecf82c8b7541e257d1a0ebf626b295b5f14711a5de968dad7b9e36c908f6
windows/amd64 cb.exe:
63fd6973088ec66bcb85bd756c4c30eb732620724f26aba03ee83ced95321986
```

The Linux binary was staged on the distribution filesystem and installed using
`cb wsl install --apply`. A following installed-binary `--check` reported
`INSTALLATION READY`, 28 ready tool shims, and `RUNTIME/STATE ENABLED`.
`cb wsl lock --check` verified all 12 configured image references without
mutation. The exact qualification lockfile's SHA-256 was
`74e5f435edef2c8862273133c4a40eceffee6f9a75d36006897ed33abccaba48`.

## Runtime results

Every row below passed in both a distribution-local project and the
Windows-backed project. The latter selected the enclosing repository's `.git`
root, so its container working directory correctly retained the project
subdirectory rather than silently choosing a different root.

| Check | Observed result |
|---|---|
| Representative tools | jq 1.8.2, npm 11.19.0, Python in the selected workspace |
| Piped stdin and EOF | jq returned the exact `stdin-marker` and exited |
| Separate output | Python stdout contained only `stdout-marker`; stderr only `stderr-marker` |
| Exit status | Python's requested exit 23 reached the caller as 23 |
| Fast exit | Short-lived successful jq execution completed and cleaned up |
| Initial terminal size | Python observed 80 columns by 24 rows immediately |
| Live resize | Changing the outer PTY and delivering SIGWINCH produced 100 columns by 40 rows |
| Host signal forwarding | SIGINT sent to the exact shim PID reached Python; requested exit 42 was preserved |
| Literal Ctrl-C | Sending byte `0x03` to the interactive PTY produced `ctrl-c-received` and exit 42 |
| Crash recovery | SIGKILL of a ready shim left one `orphan-running` container; explicit cleanup reconciled that exact run |

The terminal tests asserted both initial and resized dimensions inside the
container. The backgrounded signal tests explicitly connected the shim's
stdin/stdout/stderr to `/dev/tty`; otherwise shell job control would substitute
non-terminal stdin and invalidate the test. Ctrl-C was also exercised separately
through the foreground interactive stream rather than inferred from host
signal forwarding.

Project-state tests wrote different marker contents into each Node project's
managed `node_modules` volume, proved they survived later invocations without
cross-project collision, and removed the markers. A marker in the shared npm
cache was visible from both projects and was likewise removed. `cb state`
classified the corresponding project identities and shared volumes separately.
Seven WSL volumes carried the exact derived namespace in both their names and
ownership labels. Namespace discovery did not adopt native Windows volumes.

Live negative checks rejected each of the following with infrastructure exit
120 before tool execution:

- bare `C:\qualification\missing.py`, `c:/qualification/missing.py` and UNC spelling;
- registry-declared `terraform -chdir=C:\qualification`;
- distribution-to-Windows and Windows-to-distribution external paths;
- a project descendant symlink targeting `/etc/passwd`;
- selecting the entire `/mnt/d` drive as the fallback project root.

Nested-mount rejection, exact-case mismatch, missing-output ancestry and
create-before-lease races retain their portable integration coverage. A real
nested-mount mutation was not part of this baseline.

After each controlled crash, the check/apply/check sequence reported:

```text
before apply: active=0 orphaned=1 reconciled=0
apply:        active=0 orphaned=1 reconciled=1
after apply:  active=0 orphaned=0 reconciled=0
              lease_active=0 lease_residue=0 lease_reaped=0
```

## Native Windows preservation

A disposable Windows installation used the same default profiles and verified
image lock. `cb self-test --release --json` passed under each of PowerShell
7.6.6, Windows PowerShell 5.1 and `cmd.exe`: **15 passed, 0 failed, 0 skipped**.
The three final reports were generated at 11:40:46Z, 11:41:01Z and 11:41:14Z on
2026-10-09. They cover Python persistence/external paths, Node 24 and Node 22
project-state persistence, jq relative paths and Terraform `-chdir`.

Qualification found a pre-existing false TTY classification when JSON capture
redirected output to `NUL`. The trace showed every failing Docker command
incorrectly received `-t`; Docker rejected non-terminal stdin. Requiring
`GetConsoleMode` success for both Windows handles fixed the same JSON checks.
Native Windows regression tests independently prove NUL remains non-TTY and
both console handles must validate.

The initial 59 native Windows ContainerBin volumes were outside every WSL
cleanup target. Windows self-tests removed only their temporary project
volumes; existing shared caches were preserved. The final inventory contained
seven WSL namespace volumes and 61 other managed volumes, including shared
state created by the qualification tool invocations.

Formatting, `git diff --check`, `go vet ./...`, `go test -race ./...`, native
Windows terminal regression tests, the 11 embedded Python tests and
release-style version injection all passed. CI still supplies the complete
native Windows/ARM64 unit suites and independent release-bundle reproduction.
