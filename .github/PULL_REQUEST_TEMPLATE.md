# Pull Request

## What does this change?

<!-- Summary of the change and motivation. Link related issues. -->

## Type of change

- [ ] Bug fix
- [ ] New feature / enhancement
- [ ] Documentation
- [ ] CI / tooling
- [ ] Refactoring (no behavior change)

## Safety checklist

ContainerBin's core promises are conservative path mapping, fail-closed
locking, and never deleting state it cannot prove it owns.

- [ ] This change does not widen bind mounts beyond what arguments imply
- [ ] This change does not pass additional host environment variables by default
- [ ] This change does not replace fail-closed behavior with silent fallback
- [ ] `cb gc` / state deletion behavior is unchanged or strictly safer

## Validation

- [ ] `go test ./...` passes (containerized or native)
- [ ] `gofmt` / `go vet` clean
- [ ] For runtime behavior changes: `cb self-test` passed on real Windows +
      Docker Desktop (state which, or why not applicable)

## Notes for the reviewer

<!-- Risky spots, alternatives considered, anything needing extra scrutiny. -->
