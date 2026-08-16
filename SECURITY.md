# Security Policy

## What ContainerBin is — and is not

ContainerBin is **not a security sandbox**. It is a convenience layer that
launches Docker containers you configured. Anything the registry
(`container-bin.toml`) or lockfile (`container-bin.lock`) points at runs with:

- bind mounts of host paths you pass on the command line,
- selected host environment variables (`env_names` / `env_prefixes`),
- persistent named volumes,
- whatever the configured Docker image contains.

The registry and lockfile are therefore **inside the trust boundary**: whoever
can edit them can run arbitrary images with access to the paths and variables
they select. Treat them like you treat your PowerShell profile. See
[docs/security-model.md](docs/security-model.md) for the full model.

## Supported versions

Only the latest released version receives security fixes.

## Reporting a vulnerability

Please use **GitHub private vulnerability reporting**
(Security → "Report a vulnerability" on the repository) rather than a public
issue. Include:

- the ContainerBin version (`cb version`),
- a reproduction or proof of concept,
- the impact you believe it has (e.g. mounts a path the user never referenced,
  passes environment variables not selected by the profile, executes an image
  not in the lockfile).

You should receive an initial response within 7 days.

## In scope

Examples of reports we absolutely want:

- path mapping that mounts more of the host filesystem than the arguments imply;
- lockfile bypass (running an image the lockfile does not pin, without an error);
- registry/lockfile/backup parsing that leads to writing files outside the
  cb directory (path traversal, symlink tricks);
- shim installation overwriting files it should not;
- `cb gc` deleting volumes it cannot prove ContainerBin owns;
- environment leakage beyond configured `env_names`/`env_prefixes`.

## Out of scope

- Malicious Docker images doing malicious things *inside* their granted mounts
  and variables — that is the documented trust model, not a vulnerability.
- Attacks requiring write access to `container-bin.toml`, `container-bin.lock`,
  or the shim directory (that access already equals code execution).
- Docker Desktop / Docker Engine vulnerabilities (report those to Docker).
