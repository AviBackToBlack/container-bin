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
docker run --rm -v "${PWD}:/src" -w /src golang:1.24 sh -c "gofmt -l . ; go vet ./... && go test ./..."
```

Cross-compile the Windows binary the same way:

```powershell
docker run --rm -v "${PWD}:/src" -w /src -e GOFLAGS=-buildvcs=false golang:1.24 sh -c "GOOS=windows GOARCH=amd64 go build -trimpath -o cb.exe ."
```

If you *do* have Go installed, plain `gofmt`, `go vet ./...` and `go test ./...`
work too. CI runs tests on both Linux and Windows, with the race detector on Linux.

Note on versions: the `golang:1.24` image intentionally leads the module's
declared minimum (`go 1.23` in `go.mod`); CI resolves its toolchain from
`go.mod`, so code must not rely on stdlib additions newer than the module
minimum even if it passes in the newer local container.

## What unit tests can and cannot prove

Unit tests cover pure logic: registry parsing, lockfile round-trips, path
classification, argv normalization, volume naming. They do **not** prove
end-to-end behavior against Docker Desktop. For that, build `cb.exe`, run
`cb setup`, and then `cb self-test` on a real Windows machine with Docker
Desktop in Linux-container mode. Mention in your PR which of the two levels
of validation you performed.

## Pull requests

- Keep diffs focused; unrelated refactoring belongs in its own PR.
- Add or update tests for behavior you change, especially around the
  "dangerous boundaries": path translation, argv rewriting, registry/lockfile
  parsing, volume naming, and anything that deletes state.
- Update the README/docs when user-visible behavior changes.
- PRs run CI (format, vet, tests on Linux+Windows, Windows build, govulncheck,
  CodeQL). All checks must pass.

## Reporting bugs

Use the bug report issue template. It asks for `cb version`, `cb doctor`
output, Windows/Docker Desktop/PowerShell versions, and a reproducible
command.

**Never paste secrets.** Registry snippets, `cb inspect` output and
environment listings can contain tokens (cloud credentials passed via
`env_prefixes`, proxy URLs with passwords, private registry hosts). Sanitize
before posting.

## Security issues

Do not open public issues for suspected vulnerabilities — see
[SECURITY.md](SECURITY.md).
