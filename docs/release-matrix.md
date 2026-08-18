# Windows/Docker Desktop release validation matrix

This is the manual release-qualification matrix for ContainerBin on Windows.
It is what `docs/shell-contract.md` and `docs/windows-paths.md` are validated
against on a real host: a set of documented steps a maintainer runs before a
release because GitHub-hosted CI cannot cover this ground.

The roadmap text for RM-7b is:

> Define and maintain the validation matrix: Windows 11; Windows PowerShell 5.1;
> PowerShell 7.x; `cmd.exe`; representative Docker Desktop versions;
> Linux-containers mode. GitHub-hosted CI should continue covering pure/unit
> logic and Windows compilation, but should not pretend to provide full Windows
> + Docker Desktop E2E coverage. Prefer a documented manual release matrix first;
> a self-hosted Windows runner only if the maintenance cost is justified. Natural
> consumer of RM-5e.

This document implements the "documented manual release matrix first" part. It
does not scope, design, or commit to a self-hosted Windows runner.

## What the matrix covers

### Precondition: Linux-containers mode

Linux-containers mode is a **precondition**, not a matrix axis. The README
[Platform support](../README.md#platform-support) table already lists Windows
containers as "not supported", and `cb doctor`'s `docker-os-type` check fails if
Docker Desktop is in Windows-containers mode. Every cell below assumes Linux
containers; there is nothing to vary.

### Host OS

**Windows 11** — the RM-7b text names Windows 11 specifically. The README
platform-support table currently says "Windows 10/11" more broadly; this
is a discrepancy between the two documents and this doc flags it rather than
silently resolving either one.

### Shell

The three shells are:

- **Windows PowerShell 5.1** (`powershell.exe`, the in-box legacy shell)
- **PowerShell 7.x** (`pwsh`, PowerShell Core)
- **`cmd.exe`**

These are the exact three named in the RM-7b text and already used as the
placeholder value in `.github/ISSUE_TEMPLATE/bug_report.yml`:

```yaml
      placeholder: "PowerShell 7.4.5 / Windows PowerShell 5.1 / cmd.exe"
```

### Docker Desktop version

Use two representative versions, determined at qualification time:

1. **the current stable Docker Desktop release**, and
2. **the previous major Docker Desktop release still receiving updates**.

Specific version numbers (e.g. `4.32`) are deliberately not hardcoded: this doc
has no CI that would catch drift, so a pinned number would be stale within one
release cycle. The person qualifying the release looks up the current versions
and records them in the issue template.

### Grid

That is 3 shells × 2 Docker Desktop versions = **6 cells**, all on Windows 11,
with Linux-containers mode as a shared precondition.

| Host OS | Shell | Docker Desktop version |
|---|---|---|
| Windows 11 | Windows PowerShell 5.1 | current stable |
| Windows 11 | Windows PowerShell 5.1 | previous major (still receiving updates) |
| Windows 11 | PowerShell 7.x | current stable |
| Windows 11 | PowerShell 7.x | previous major (still receiving updates) |
| Windows 11 | `cmd.exe` | current stable |
| Windows 11 | `cmd.exe` | previous major (still receiving updates) |

## Per-cell procedure

For each cell, on a clean Windows 11 host with Docker Desktop in Linux-containers
mode:

1. Run `cb setup` from a fresh shim install. This re-creates the default registry
   and `cb.exe`-hardlink shims.
2. Run `cb doctor`. The output must be clean, or must show only `warn` verdicts
   that are already understood and documented (for example, a reparse-point or
   mapped-drive warning from the checks in `docs/windows-paths.md` that does not
   actually block the tool invocation).
3. Run `cb self-test --release`. This uses already-local locked images and
   reports every check. Its output is documented in README's
   [`cb self-test --json` report format](../README.md#cb-self-test---json-report-format)
   section: 12 `checks` IDs, 7 `environment` IDs, the `pass`/`fail`/`skip`
   vocabulary, and the `skip`-vs-`warn` message-prefix distinction. See that
   section for the full schema rather than restating it here, because the schema
   is the kind of detail that drifts.
4. (Recommended) Run one or two real smoke-test commands that actually exercise a
   `docker run` through a shim, from the shell under test. The README's own
   quickstart demonstrates exactly these:

   - `pip install requests`
   - `terraform -chdir=.\tf validate`

   `cb self-test` alone does not fully replace this, because it uses its own
   temp project volumes and already-local locked images rather than a first-run
   user project.

Record the results in a release-qualification issue using
[`.github/ISSUE_TEMPLATE/release-qualification.yml`](../.github/ISSUE_TEMPLATE/release-qualification.yml).

## What CI already covers vs. what only this matrix proves

The `test-windows` job in `.github/workflows/ci.yml` is:

```yaml
  test-windows:
    name: Test and build (Windows)
    runs-on: windows-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6
        with:
          go-version-file: go.mod
          check-latest: true
      - name: go test
        run: go test ./...
      - name: Build cb.exe
        run: go build -trimpath -o cb.exe .
      - name: Smoke-test version output
        run: |
          $v = .\cb.exe version
          Write-Host $v
          if ($v -notmatch '^container-bin \S+') { exit 1 }
        shell: pwsh
```

That proves the following, and no more:

- It runs on `windows-latest`. GitHub's own `actions/runner-images`
  documentation describes `windows-latest` as a Windows Server runner image,
  not a Windows 11 client install.
- The only shell-dependent step uses `pwsh` (PowerShell 7.x / PowerShell Core).
  It does not exercise **Windows PowerShell 5.1** (`powershell.exe`) or
  **`cmd.exe`**.
- It has **no Docker Desktop** and makes **no `docker run` call**. `go test ./...`
  on Windows exercises pure logic and any `runtime.GOOS == "windows"`-gated
  unit tests — `docs/windows-paths.md` notes that its Windows-gated tests are
  what runs here — not an actual containerized tool invocation.
- The "Smoke-test version output" step only proves the binary starts and
  dispatches `cb version`; it never reaches `runTool` or `docker run`.
- It does not vary Docker Desktop version, the Linux-versus-Windows-containers
  mode switch, or real bind-mount/volume behavior.

So CI validates compilation and pure/unit logic on a GitHub-hosted Windows
runner. The matrix is what validates the `docs/shell-contract.md` semantics on a
real Windows 11 + Docker Desktop host before a release.

## Why not a self-hosted runner (yet)

RM-7b's text is explicit: start with a documented manual release matrix, and
consider a self-hosted Windows runner only if the maintenance cost of the manual
process becomes the bottleneck. This document is the manual process. It does not
scope or design a self-hosted runner.

## Where results are recorded

Open a release-qualification issue using
`.github/ISSUE_TEMPLATE/release-qualification.yml`. The template asks for the
`cb version` being qualified and one block per matrix cell, capturing:

- the Docker Desktop version used,
- the `cb doctor` output (sanitized),
- the `cb self-test --release` result, and
- any smoke-test commands run.
