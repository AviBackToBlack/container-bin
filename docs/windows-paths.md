# Windows path-form classification

This is the reference for the Windows path shapes ContainerBin handles,
rejects, or deliberately leaves unsupported. It exists because the path
mapper's failure mode is a silently wrong mount or a silently altered
argument, and "it worked on the runner" is not a classification. For each
row the table states the verdict, what the code actually does today, and how
that claim is pinned — either by a test name, an existing test, or the words
"documentation only".

These rows are also exercised together in `pathnormalization_test.go`, a table-driven corpus that runs combinations of these shapes end-to-end through `mapToolArgs` and is Windows-gated the same way the P-row tests are.

## Classification table

| ID | Path form | Classification | What happens today | How it is proven |
|---|---|---|---|---|
| P1 | A junction or symlink on the way *to* the project root or to an argument | **Supported** | `canonicalPath` resolves it (`EvalSymlinks`) for both `cwd` and every classified argument, so root and arguments are compared and mounted as resolved real paths | `TestPathFormP1JunctionToRoot` — skips where the runner cannot create a directory symlink |
| P2 | A junction or symlink *inside* the project root whose target is outside it | **Supported** | The argument resolves outside `root`, `pathWithin` fails, and it gets its own narrow `/cb/mounts/N` bind mount | `TestPathFormP2JunctionEscapesRoot` — skips where the runner cannot create a directory symlink |
| P3 | Traversing such a link from *inside* the container | **Documented as unsupported** | Docker Desktop bind mounts do not follow host reparse points, so a junction inside the mounted project tree is not usable from the container | documentation only — not covered by CI |
| P4 | A reparse point `EvalSymlinks` cannot resolve (cloud-storage placeholders, dedup reparse points, app-execution aliases) | **Documented as unsupported, best-effort** | `canonicalPath` ignores the `EvalSymlinks` error and keeps the unresolved absolute path; mapping proceeds against that path | documentation only — not covered by CI |
| P5 | Case-insensitive equivalence | **Supported** | `canonicalPath` lowercases on Windows; `mapArg` lowercases the external-mount dedup key; `volumeHash` lowercases before hashing | `TestPathFormP5CaseInsensitiveEquivalence`; see also `TestMapToolArgsExternalDedupCaseInsensitive` |
| P6 | The container-side workspace basename is lowercased for stateful tools | **Supported, user-visible** | `root` is lowercased, so `workspaceRootFor` sees a lowercased basename (`My-App` becomes `my-app`) | `TestPathFormP6WorkspaceBasenameLowercased` |
| P7 | A UNC path as an argument (`\\server\share\x`) | **Documented as unsupported** | `isWindowsAbsPath` requires a drive letter, so the string is not recognized as absolute and normally reaches the container unmapped — but see the sharp edge below: the existing-relative probe can match a coincidental `server\share\x` subtree under `cwd` | `TestPathFormP7UNCArgumentDeclined`, `TestPathFormP7UNCCoincidentalSubtreeIsMapped` |
| P8 | A UNC path as the project root / current directory | **Documented as unsupported** | `root` reaches `mountSpec` as the canonicalized UNC path; Docker Desktop cannot bind-mount a UNC source | documentation only — not covered by CI |
| P9 | Long-path / extended-length syntax (`\\?\C:\...`, `\\?\UNC\...`) | **Documented as unsupported** | Same mechanism as P7: the leading backslash defeats `isWindowsAbsPath` | `TestPathFormP9LongPathSyntaxDeclined` |
| P10 | `subst` drives and mapped network drives (`S:\`, `Z:\`) | **Documented as unsupported** | Indistinguishable from a fixed drive at the string level; cb maps them and hands Docker a source it cannot share | documentation only — not covered by CI |
| P11 | Trailing dots or spaces in a path component (`.\foo.`, `.\foo `) | **Supported** | `filepath.Abs` uses `GetFullPathNameW`, which strips trailing dots and spaces from the component, so cb acts on `foo` | `TestPathFormP11TrailingDotsAndSpaces` |
| P12 | An argument whose final path element is `...` (package pattern) | **Rejected** | `hasPackagePatternSuffix` declines it before any other classification; it is never treated as a path | `TestPackagePatternSuffix`, `TestMapToolArgsPackagePattern` |
| P13 | Separator normalization of a declined argument | **Never done** | Declined arguments pass byte-for-byte unchanged; backslashes and other characters are not rewritten | `TestPathFormP13DeclinedArgumentsPassByteForByte` |
| P14 | The target of a path changes between classification and `docker run` (TOCTOU) | **Out of scope by design** | cb resolves, then invokes `docker run`; a re-pointed path is mounted as re-pointed | documentation only — by design |
| P15 | A comma anywhere in the project root's path (`D:\My, Project\...`) | **Rejected** | `mountSpec`'s `src` check fails closed on `root` itself, unconditionally, for every provider — before any container-side sanitization could matter | `TestRootBindMountCommaFailsClosedOnSrc` |

## Per-row rationale

### P1 — Junction or symlink on the way to the root or argument

`canonicalPath` is called on `cwd` in `runTool` before `findProjectRoot` walks
upward, and it is called on every argument that the classifier accepts. That
covers both the project root and the argument: a user working through
`D:\link\proj` sees the real directory mounted and compared.

The P1 and P2 tests need a real reparse point, which they create with
`os.Symlink`. Creating a directory symlink on Windows can require privilege or
developer mode, so both tests skip rather than fail when it is unavailable —
meaning these two rows are proven only on runners where that succeeds. If the
CI runner ever loses that ability, the rows quietly become documentation
rather than tests; the alternative, failing the build on an environment
capability, would be worse.

### P2 — Junction or symlink inside the root whose target is outside it

This is the correct conservative outcome: the content is not silently folded
into the workspace mount, and the mount stays narrow. A reader may expect
"inside the project root" to be decided lexically, but it is decided after
resolution, because resolution-then-mapping is the safe order.

### P3 — Traversing such a link from inside the container

Docker Desktop bind mounts do not follow Windows reparse points. A junction
inside the mounted project tree is not usable from the container even though
the host path resolves fine. The remedy is to reference the target directly as
an argument, which P2 then mounts properly.

### P4 — Reparse points `EvalSymlinks` cannot resolve

`canonicalPath` deliberately ignores the `EvalSymlinks` error and keeps the
unresolved absolute path. Failing every path whose resolution is imperfect
would reject ordinary setups. The residual risk is plain: an unresolved path
may compare differently against `root` than its real target would.

### P5 — Case-insensitive equivalence

`D:\Proj\A.TXT` and `d:\proj\a.txt` are one path everywhere it matters:
`canonicalPath` lowercases on Windows, `mapArg` lowercases the external-mount
dedup key, and `volumeHash` lowercases before hashing.

### P6 — Lowercased stateful workspace basename

A project at `D:\TEMP\My-App` mounts at `/workspace/my-app`, not
`/workspace/My-App`. Tools that derive metadata from the working-directory
basename (npm) see the lowercase form.

### P7 — UNC argument

`isWindowsAbsPath` requires `drive-letter + colon + separator`. A UNC path
starts with a backslash, so it is not recognized as absolute. It is not an
explicit relative either, so it falls to the third branch, which joins it onto
`cwd` and stats the result. Normally nothing is there, so it is declined and
reaches the container verbatim.

**The sharp edge:** that last step is a real filesystem probe, not a shape
test. `filepath.Join` collapses the leading separators, so `\\server\share\x`
probes `<cwd>\server\share\x` — and if a subtree of that name happens to exist,
the argument *is* classified as a path and mapped to it. A UNC argument would
then silently act on an unrelated local directory. It requires a coincidence to
trigger, but "requires a coincidence" is not "cannot happen", and
`TestPathFormP7UNCCoincidentalSubtreeIsMapped` pins the behavior so it is
rediscovered by a reader rather than by a user.

Why not reject it explicitly? To reject a string, cb would first have to assert
that it *is* a path — the same guess in the opposite direction — and would
corrupt a legitimate literal argument that merely looks UNC (a share name a
tool handles itself, a regex, an escaped string).

The residual sharp edge: for an *output* argument the tool will create a file
literally named `\\server\share\x` inside the container rather than writing
to the share. Nothing is destroyed and nothing on the host changes, but the
user must notice the absence rather than being told.

### P8 — UNC project root or current directory

If `cwd` or the found project root is a UNC path, `mountSpec("bind", root,
workspaceRoot)` is called with it. `canonicalPath` has already lowercased and
cleaned the string by then, but nothing rewrites the UNC form itself. Docker
Desktop cannot bind-mount a UNC source, and the resulting error is loud but
comes from Docker, not cb.
Turning that into an actionable cb-side diagnostic is chartered as **RM-5c**
(`cb doctor` filesystem/path diagnostics), not this task.

### P9 — Long-path / extended-length syntax

`\\?\C:\...` and `\\?\UNC\server\share\...` start with a backslash, so
`isWindowsAbsPath` returns false for the same reason as P7. They are declined
and pass through unmapped.

Unlike P7, this one is structurally safe rather than merely unlikely: the
existing-relative probe would have to stat `<cwd>\?\C:\Windows`, and `?` is a
reserved character that cannot name a directory on Windows. The coincidental
subtree that makes P7's edge reachable therefore cannot exist here.

### P10 — `subst` and mapped network drives

`S:\proj` satisfies `isWindowsAbsPath` and is mapped normally, because the
string shape is indistinguishable from a fixed drive. `EvalSymlinks` does not
resolve `subst` drives or mapped network drives to their targets, because they
are object-manager symbolic links rather than filesystem reparse points. cb
therefore maps them and hands Docker a source it cannot share, because such
drives are per-session, per-user constructs invisible to the Docker service.
The remedy is to work from the underlying real path. A doctor warning for this
is **RM-5c**.

### P11 — Trailing dots and spaces

`filepath.Abs` on Windows goes through `GetFullPathNameW`, which strips
trailing dots and spaces from a component. `foo.` and `foo ` both resolve to
`foo` before ContainerBin touches them. That matches what every ordinary
Windows API does with the same string, and such a file cannot be created
through normal APIs anyway.

This is the single most important contrast in the table. Both P11 and P12
involve trailing dots, but they are classified oppositely because `.\foo.` is
a *path the user got slightly wrong*, and Windows' own answer is the right
one, whereas `...` is not a path at all — it is a *package pattern* that the
tool itself understands. The rule is not "trailing dots are dangerous"; it is
"do not canonicalize a string that was never a path".

### P12 — Package pattern `...`

Delivered by RM-6d. `hasPackagePatternSuffix` returns true when the final
element is exactly `...` and the prefix is empty or ends with a separator. The
classifier returns `("", false, nil)` before any path handling, so the
argument passes through unchanged even under forced path semantics. The
existing tests `TestPackagePatternSuffix` and `TestMapToolArgsPackagePattern`
pin this; do not duplicate them.

### P13 — Declined arguments pass through byte-for-byte

A declined `.\cmd\...` reaches the Linux container with backslashes, and the
tool reports no match. The remedy is to retype it with forward slashes. This
was raised in review on RM-6d and is decided here, once, for every argument
shape.

Rewriting a string that has just been classified as *not a path* reintroduces
the guessing the mapper exists to avoid — this time a guess about which tools
want POSIX separators — and it would silently mutate a legitimate literal
argument. Classified paths are already emitted with forward slashes by
`filepath.ToSlash` in `mapArg`, so the rule is clean: separators are
normalized only in strings cb has classified as paths and rewritten; anything
cb declines is passed through untouched.

### P14 — TOCTOU between classification and `docker run`

cb classifies and resolves paths, then invokes `docker run`. A junction
re-pointed inside that window is mounted as re-pointed. This is not a
defensible boundary: anyone able to re-point a path in that window already has
local write access, which the security model places inside the trust boundary
alongside the registry, the lockfile and the shim directory. Closing it would
require holding handles across the `docker run` and would still not be
airtight. This is recorded explicitly so a future reader finds a decision
rather than an oversight.

### P15 — Comma anywhere in the project root's path

Every tool invocation builds a `bind` mount for the project root:
`mountSpec("bind", root, workspaceRoot)`, where `workspaceRoot` is
`workspaceRootFor(t, root)`. `mountSpec` checks `src` for a comma before it
checks `dst`, and `workspaceRootFor`'s output can only contain a comma when
`root`'s own basename does — the container-side path is built from a
substring of the host path, never an independent string. So a comma anywhere
in `root` fails the `src` check first, for every provider, before
`workspaceRoot`'s content is ever examined.

This closes out an item the roadmap originally scoped as "sanitize
`workspaceRootFor` so comma-named project directories work" (tracked as
RM-6c). That fix cannot deliver what it promised: sanitizing the
container-side basename does not change which mountSpec branch fires, because
the same call already fails on `src` first. Docker's `--mount` syntax has no
escape for a literal comma in a field value, and `src` must be the real host
path — there is no string to sanitize on that side without pointing Docker at
the wrong directory. Making this genuinely work would need a different
mechanism entirely, such as bind-mounting a comma-free alias of the real
directory (a Windows 8.3 short path, where available) — a materially larger
change than a documentation task, and not undertaken here.

`TestMountSpecWorkspaceRootComposition` in `mountspec_test.go` predates this
finding and pairs a comma-carrying workspace root with an artificially clean
`src` — a combination that cannot occur at the real call site. It is kept as
a unit-level check of `mountSpec`'s `dst` branch, now labeled as such;
`TestRootBindMountCommaFailsClosedOnSrc` pins the real call site and asserts
that the error a user actually sees names the *source* path, not the
destination.

## Guidance for profile authors

`path_next` and `path_equals` match anywhere in `argv`, and `path_last`
matches the final operand regardless of what it means. Any tool with
pass-through arguments (`go run PKG ARGS...`, `gofmt -r 'RULE'`, `npx`,
anything after `--`) will have those arguments corrupted into container paths.

The safest guidance is to **prefer declaring nothing.** The container's
working directory is the project directory, so bare and explicit-relative
operands already resolve correctly through the general rules. Both Go profiles
declare no path semantics at all, which is simultaneously the most
conservative and the fully functional choice; Terraform's
`path_equals = ["-chdir"]` is justified because `-chdir=` has no pass-through
grammar.
