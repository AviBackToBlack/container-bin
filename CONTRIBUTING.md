# Contributing to ContainerBin

Thanks for your interest in improving ContainerBin.

## Ground rules

- **Windows-first project.** ContainerBin targets Windows + Docker Desktop with
  Linux containers. Changes must not regress that environment. Other platforms
  are currently out of scope unless a maintainer has agreed otherwise in an issue.
- **Zero third-party Go dependencies is intentional.** The binary is built from
  the standard library only. Adding a dependency requires prior discussion in an
  issue and a strong justification.
- **Fail closed, stay conservative.** Path mapping deliberately refuses to guess,
  the lockfile refuses to run stale configuration, and `cb gc` never deletes
  state it cannot prove it owns. Do not trade these properties for convenience.
- **Keep the host clean.** Development should not require installing Go on
  Windows; use Docker (see below).

## Building and testing without installing Go

All validation runs in a disposable container:

```powershell
docker run --rm -v "${PWD}:/src" -w /src -e GOTOOLCHAIN=local golang:1.27.3 sh -c "gofmt -l . ; go vet ./... && go test ./..."
```

Cross-compile the Windows binary the same way:

```powershell
docker run --rm -v "${PWD}:/src" -w /src -e GOFLAGS=-buildvcs=false -e GOTOOLCHAIN=local golang:1.27.3 sh -c "GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o cb.exe ."
```

If you *do* have Go installed, plain `gofmt`, `go vet ./...` and `go test ./...`
work too. CI runs tests on both Linux and Windows, with the race detector on Linux.

The exact Go patch in `.go-version` is the supported contributor, CI, vulnerability
scan and release-build toolchain. `GOTOOLCHAIN=local` prevents silent toolchain
downloads; use that exact version, not `stable`, for release/security validation.
`go.mod` retains Go 1.24 source compatibility because ContainerBin's built-in Go
shim uses that series. Compatibility builds through the shim are not patched
release builds or vulnerability evidence. The Go 1.27-only defaults directive
keeps shipping security defaults independent of the older language minimum.
See [release toolchain policy](docs/release-toolchain.md).

## Fuzzing security-sensitive inputs

Native Go fuzz targets exercise registry parsing, immutable image lockfile
round-trips, self-update checksum acceptance against an independent format
oracle, Windows argv normalization, and WSL workspace confinement with an
injected classifier. They do not launch Docker, inspect the real filesystem,
download releases, or mutate installed configuration. No additional test or
runtime dependencies are required.

`go test ./...` runs the checked-in seed corpora. CI additionally runs every
target for 30 seconds with two workers on both Linux and Windows. To run a
longer campaign from the repository root (or through the configured Go shim):

```powershell
go test ./internal/registry -run '^$' -fuzz '^FuzzParseTOML$' -fuzztime=5m -parallel=2
go test ./internal/lockfile -run '^$' -fuzz '^FuzzParseLockFile$' -fuzztime=5m -parallel=2
go test ./internal/selfupdate -run '^$' -fuzz '^FuzzChecksumManifest$' -fuzztime=5m -parallel=2
go test ./internal/pathmap -run '^$' -fuzz '^FuzzNormalizeWindowsToolArgs$' -fuzztime=5m -parallel=2
go test ./internal/wslpathmap -run '^$' -fuzz '^FuzzMapToolArgs$' -fuzztime=5m -parallel=2
```

Preserve minimized failures under the corresponding `testdata/fuzz/FuzzNAME/`
directory as regression inputs, fix the cause, and rerun the corpus and full
tests. Review inputs for secrets before committing them. A bounded campaign
is additional evidence, not proof that a parser or platform is vulnerability-free.

## What unit tests can and cannot prove

Unit tests cover pure logic: registry parsing, lockfile round-trips, path
classification, argv normalization, volume naming. They do **not** prove
end-to-end behavior against Docker Desktop. For that, build `cb.exe`, run
`cb setup`, and then `cb self-test` on a real Windows machine with Docker
Desktop in Linux-container mode. Mention in your PR which of the two levels
of validation you performed. For the full release-qualification procedure
and matrix, see [docs/release-matrix.md](docs/release-matrix.md).

## Pull requests

- Keep diffs focused; unrelated refactoring belongs in its own PR.
- Add or update tests for behavior you change, especially around the
  "dangerous boundaries": path translation, argv rewriting, registry/lockfile
  parsing, volume naming, and anything that deletes state.
- Update the README/docs when user-visible behavior changes.
- PRs run CI (format, vet, tests on Linux+Windows, Windows build, govulncheck,
  CodeQL). All checks must pass.

## Reporting bugs

Use the bug report issue template. Running `cb bugreport` will gather `cb
version`, `cb doctor` output, Windows and PowerShell versions (on Windows) and
a compact registry inventory into one paste-ready block with best-effort
secret redaction; add the result to the issue. You may still need to provide
Docker Desktop and shell versions separately.

**Never paste secrets.** Registry snippets, `cb inspect` output and
environment listings can contain tokens (cloud credentials passed via
`env_prefixes`, proxy URLs with passwords, private registry hosts). `cb
bugreport` redacts a few common patterns, but that is a convenience aid, not a
substitute for your own review — sanitize before posting.

## Security issues

Do not open public issues for suspected vulnerabilities — see
[SECURITY.md](SECURITY.md).
