# container-bin v0.9.0 — reproducible image locking

v0.9 adds an immutable image lockfile next to `container-bin.toml`.

## New commands

```powershell
cb lock
cb lock --check
cb update TOOL
cb update --all
```

`container-bin.toml` remains the human intent/configuration file. `container-bin.lock`
is machine-generated and stores exact Docker image digests.

Runtime behavior:

- no lockfile: configured tags are used (backward-compatible UNLOCKED mode)
- lockfile exists: tools run by the exact `repository@sha256:digest`
- registry contains an image missing from the lockfile: execution fails closed and asks for `cb update TOOL` or `cb lock`

## First migration from v0.8

```powershell
cb version
cb inspect terraform
cb lock
cb lock --check
cb inspect terraform
```

Before `cb lock`, inspect reports `UNLOCKED`. Afterwards it reports the configured
image plus its immutable locked digest.

## Controlled updates

```powershell
cb update terraform
```

Pulls the configured Terraform tag, resolves its current digest, and updates only
that image entry. Shared images are locked once: `node`, `npm`, `npx`, and npm-exposed
commands all share the single `node:24-slim` lock entry.

```powershell
cb update --all
```

Explicitly refreshes every unique configured image.

## Lockfile model

Example:

```toml
lock_version = 1

[images.0123456789ab]
configured = "node:24-slim"
resolved = "node@sha256:..."
digest = "sha256:..."
```

The section id is derived from the configured image string and validated when loading.
The lockfile is rewritten atomically and validated before replacement.

## v1.0.0 productionization

New commands:

- `cb setup` — initialize/upgrade registry, install shims, run doctor.
- `cb doctor` — validates Docker CLI/engine, registry schema, lock completeness/local exact images, PATH/shims, Python resolution, and managed-volume inspectability.
- `cb backup [FILE.zip]` — atomically package registry and optional lockfile. Defaults to `backups/` next to cb.exe.
- `cb restore BACKUP.zip [--apply]` — validates backup; dry-run by default, writes registry/lock only with `--apply`.
- `cb self-test` — offline end-to-end tests using already-local images: Python state persistence, external Windows path mapping, Node project volume persistence, jq relative paths, and Terraform `-chdir`; temporary project volumes are cleaned afterward.

This release intentionally does not mutate Windows PATH automatically and does not auto-pull images during self-test.
