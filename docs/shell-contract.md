# Windows shell/process compatibility contract

This is the reference for the process-launcher semantics ContainerBin preserves,
rejects, or deliberately leaves unsupported when a Windows shell or process
spawns a `cb.exe` shim. It exists because these semantics have no dedicated test
net in the repository: many of them are inherited from Go's `os/exec`, from the
Windows console, and from `docker run`, and a confidently wrong claim here would
ship wrong without anything catching it. Each row or section below states the
code that supports it, or is explicitly labeled as documentation only.

## 1. Argv delivery

`main()` receives `os.Args` directly from the Windows process-launch boundary
(`main.go:201`). For shim invocations it calls `runTool(tool, os.Args[1:])`
(`main.go:213`), forwarding the caller's arguments to the container after
normalization and path mapping.

`runTool` builds the `docker` argument list and invokes it with
`exec.Command("docker", args...)` (`main.go:1515`). cb does **not** re-parse or
re-quote `userArgs` itself for that call. Quoting on the inbound
Windows→cb boundary is whatever the calling shell and the Go runtime already
did, and quoting on the cb→`docker.exe` boundary is `os/exec`'s standard
Windows argument-escaping, not custom cb logic.

Dispatch is driven by `invokedName(os.Args[0])` (`main.go:359`):

```go
// invokedName derives the dispatch name from argv[0]. Windows filenames are
// case-insensitive, so both base and extension are lowered before trimming —
// cmd.exe can hand us PYTHON.EXE, which must still dispatch as "python".
func invokedName(argv0 string) string {
	base := strings.ToLower(filepath.Base(argv0))
	return strings.TrimSuffix(base, filepath.Ext(base))
}
```

The lowering and extension-stripping are the entire dispatch normalization;
there is no further argv re-parsing.

## 2. PowerShell-specific native argument behavior

`normalizeToolArgs` (`main.go:1542`) rejoins the PowerShell native-command
split of `-opt=value` into two argv elements (`-opt=`, `value`), but **only** for
options a profile declares in `path_equals`:

```go
for _, opt := range t.PathEquals {
	prefix := opt + "="
	if arg == prefix && i+1 < len(args) {
		out = append(out, prefix+args[i+1])
		i++
		joined = true
		break
	}
}
```

The scope is deliberately narrow: this is not a general PowerShell-quoting or
argument-joining emulation layer. It fixes one specific, registry-declared shape
so that declared options such as `terraform -chdir=...` reach `docker run` as a
single argument. See
[docs/architecture.md](docs/architecture.md#why-powershell-argv-repair-exists)
for the design rationale.

## 3. cmd.exe behavior

README notes that "cmd.exe invocation of shims" "works for the common cases;
less battle-tested than PowerShell" (`README.md`,
[Platform support](README.md#platform-support)). The mechanism is that cb's
argv-repair logic in the previous section is described as PowerShell-specific in
comments and tests, but the underlying check is shape-based: it runs the same
loop for every caller and joins `opt=` + next value whenever the string matches a
`path_equals` declaration.

A search of `main.go` for caller-shell detection finds no such code. The only
occurrences of `cmd.exe` and `powershell` are the comment in `invokedName`, the
`doctor`/`bugreport` commands that shell out to PowerShell for host diagnostics,
and the same `normalizeToolArgs` comment. cb has no code path that inspects which
shell invoked it, no `PSModulePath` or `COMSPEC` heuristic, and no parent-process
query. The same normalization and repair fire regardless of caller, for better
or worse; the practical difference is that `cmd.exe` does not split `-opt=value`
this way, so the repair is usually a no-op under `cmd.exe`.

## 4. Stdin/stdout/stderr passthrough

`runTool` wires the streams directly to the `docker` child:

```go
cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Env = os.Stdin, os.Stdout, os.Stderr, os.Environ()
```

(`main.go:1516`). There is no cb-side buffering, line-editing, or encoding
translation on the tool-run path.

This is not the same as `captureStdout` (`main.go:1197`), which is a different,
narrow mechanism used only by `cb self-test` and `cb bugreport` to capture nested
command output for redaction. `captureStdout` is not part of the tool-run path
and must not be confused with the passthrough above.

## 5. TTY detection

`interactiveTerminal()` (`main.go:1367`) is the only TTY-decision function in
cb:

```go
// interactiveTerminal is intentionally conservative: Docker gets a TTY only
// when both stdin and stdout are character devices. Pipes/redirection and
// process-captured output remain plain -i, preserving automation semantics.
func interactiveTerminal() bool {
	in, err := os.Stdin.Stat()
	if err != nil || in.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	out, err := os.Stdout.Stat()
	return err == nil && out.Mode()&os.ModeCharDevice != 0
}
```

A search of the repository for any other TTY, terminal, or console capability
decision finds only `interactiveTerminal`; no color-forcing, terminal capability
negotiation, or alternate buffer control exists. `runTool` adds `docker run -t`
only when this function returns true (`main.go:1408`) and uses plain `-i`
otherwise.

## 6. Current working directory

`runTool` derives the container working directory as follows:

1. `os.Getwd()` (`main.go:1377`) gets the host process cwd.
2. `canonicalPath(cwd)` (`main.go:1381`) resolves to a cleaned, lowercase,
   symlink-resolved absolute path.
3. `findProjectRoot(cwd, projectMarkersFor(t))` (`main.go:1385`) walks upward
   looking for declared markers.
4. If no root is found, `root = cwd` (`main.go:1387`).
5. `workspaceRoot := workspaceRootFor(t, root)` (`main.go:1393`) and
   `containerWD` are built from that root (`main.go:1394`).
6. The `--workdir` flag is appended to the `docker run` argv (`main.go:1411`).

Because `containerWD` is always computed from the root that is also bind-mounted
as `/workspace`, the container's cwd is always inside that bind-mounted project
root (or the workspace root when no project marker is found). It is never an
arbitrary host path outside the mounted tree. See
[docs/architecture.md](docs/architecture.md#project-roots-and-volume-naming) for
the project-root discovery and volume-naming rationale.

## 7. Signal handling and Ctrl-C

Two separate code paths exist and must not be conflated.

### Mutation-lock path

`withMutationLock` (`main.go:3489`) installs a single `os.Interrupt` handler:

```go
c := make(chan os.Signal, 1)
done := make(chan struct{})
signal.Notify(c, os.Interrupt)
go func() {
	select {
	case <-c:
		release()
		osExit(exitInterrupted)
	case <-done:
	}
}()
```

`exitInterrupted` is the constant `130` (`main.go:2032`). This is the cited,
tested behavior: a `cb` process interrupted with Ctrl-C while holding the
mutation lock exits `130` and releases the lock. The authoritative exit-code
meaning is in [README.md's `## Exit codes`](README.md#exit-codes); see that
section for the table rather than restating it here.

### Tool-run path

`runTool` installs **no** signal handler. A search of `main.go` for
`signal.Notify` and `os.Interrupt` finds exactly one call site, inside
`withMutationLock`. `runTool` also sets no `SysProcAttr` on the `docker` child; a
search for `SysProcAttr` in the entire repository finds no matches.

> **Documentation only — inferred, not tested.** Because `exec.Command` on this
> path uses default process attributes, the child `docker` process is not placed
> in a new process group. On Windows, a console `Ctrl-C` (`CTRL_C_EVENT`) is by
> default delivered to every process attached to the same console unless the
> process opted out of that group. This is a documented Win32 console-signal-group
> behavior, not an automated cb test, and it is why `docker.exe` and `cb.exe`
> would receive the same console `Ctrl-C` while the tool is running. What
> `docker run` itself does on receipt of that event — whether it stops the
> container, whether `--rm` still cleans up, or how it forwards the signal to the
> Linux process — is outside what the cb codebase or this document can establish,
> and is not asserted here.

## 8. Environment variable case-folding

`selectedHostEnv` (`main.go:1745`) matches `EnvNames` and `EnvPrefixes` against
`os.Environ()` case-insensitively:

```go
exact := map[string]bool{}
for _, n := range t.EnvNames {
	exact[strings.ToUpper(n)] = true
}
...
upper := strings.ToUpper(name)
match := exact[upper]
if !match {
	for _, p := range t.EnvPrefixes {
		if strings.HasPrefix(upper, strings.ToUpper(p)) {
			match = true
			break
		}
	}
}
```

This matches Windows' own case-insensitive environment variable semantics. A
profile author can declare `env_names = ["Path"]` and the host `PATH` or `Path`
will match without enumerating every case variant.

The actual host values are resolved through `cmd.Env = os.Environ()`
(`main.go:1516`); `selectedHostEnv` only decides which variable *names* are
passed to `docker run` as `-e` flags.

## 9. What is deliberately NOT emulated

These are behaviors ContainerBin explicitly does not provide. Each item was
checked in the current codebase.

- **No re-quoting or re-escaping of `argv` beyond what Go's `os/exec` does**
  (`main.go:1515`). The two quoting boundaries are the calling shell's and
  `os/exec`'s; cb adds no custom quoting engine.
- **No shell detection of any kind.** cb does not inspect the parent process,
  `COMSPEC`, `PSModulePath`, or any other caller signature; `normalizeToolArgs`
  and `runTool` run the same code for PowerShell, `cmd.exe`, or any other Windows
  process launcher.
- **No signal forwarding into the container beyond what the OS-level console
  group already does.** cb does not translate `SIGTERM`/`SIGKILL` semantics or
  forward any signal to `docker run`; the only `signal.Notify` call is the
  `os.Interrupt` handler in `withMutationLock`.
- **No TTY color-forcing or terminal capability negotiation.** `interactiveTerminal`
  is the sole TTY decision; there is no `SetConsoleMode`, `ENABLE_VIRTUAL_TERMINAL_PROCESSING`,
  or capability negotiation code in the repository.
- **No locale or codepage translation** between the Windows console and the
  Linux container. Streams are passed byte-for-byte.
- **No Win32 console or process-control API calls.** A search for `SetConsoleMode`,
  `SetConsoleCtrlHandler`, `GenerateConsoleCtrlEvent`, `CreateProcess`, and
  `golang.org/x/sys/windows` finds no matches in the codebase; all process
  launching goes through `os/exec`.
