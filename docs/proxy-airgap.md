# Proxies, private registries, and air-gapped operation

ContainerBin does not implement a registry client or a network proxy. It invokes
the local Docker CLI, so image pulls, registry authentication, certificate trust,
and daemon networking remain Docker's responsibility. Tool processes inside a
container have a separate network path controlled by the tool profile and Docker.

This separation is deliberate: ContainerBin does not discover a mirror, rewrite
an image name, copy credentials into `container-bin.toml`, or silently fall back
to a different repository.

## Configure the right proxy layer

There are two distinct flows to configure:

1. **Docker Desktop and image pulls.** Configure Docker Desktop under **Settings
   → Proxies**. Docker's daemon-level `daemon.json` proxy settings are ignored by
   Docker Desktop; they apply to a separately managed Docker Engine instead.
2. **Outbound traffic from a tool container.** If the tool itself needs a proxy,
   allowlist the proxy variables in that tool's registry profile, or configure
   Docker CLI container proxies in the Docker client configuration.

For an explicit ContainerBin profile, prefer an allowlist such as:

```toml
[tools.terraform]
image = "registry.corp.example/devtools/terraform:1.13.3"
provider = "stateless"
path_equals = ["-chdir"]
env_names = ["HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"]
```

The profile passes only variables already present in the host environment. Do
not put a proxy password in `env_set` or embed credentials in an image name.
Proxy configuration can contain credentials and internal hostnames; sanitize it
before sharing `cb inspect`, `cb trace`, or registry excerpts.

Docker references:

- [Docker Desktop proxy settings](https://docs.docker.com/desktop/settings-and-maintenance/settings/#proxies)
- [Docker Engine daemon proxy configuration](https://docs.docker.com/engine/daemon/proxy/)
- [Docker CLI container proxy configuration](https://docs.docker.com/engine/cli/proxy/)

## Private registry and mirror profiles

The most predictable arrangement is to name the repository that Docker will
actually pull from:

```toml
[tools.jq-corp]
image = "registry.corp.example/devtools/jq:1.8.1"
provider = "stateless"
```

Authenticate Docker to the registry through its credential store, then reconcile
the shim and create the lock:

```powershell
docker login registry.corp.example
cb install
cb lock
cb lock --check
```

Never place registry credentials in `container-bin.toml`. ContainerBin delegates
authentication to Docker and does not need the credential itself.

A Docker daemon pull-through mirror can also be configured transparently with
Docker's `registry-mirrors` setting. In that model, keep the canonical image name
in the profile and let Docker route the pull through the mirror. See Docker's
[pull-through cache documentation](https://docs.docker.com/docker-hub/image-library/mirror/).

### Repository identity is checked, not guessed

`cb lock` accepts a pulled image only when Docker reports a `RepoDigest` for the
same repository named by the profile, with only Docker Hub's documented aliases
normalized. For example, these identities are intentionally different:

```text
profile:     mirror.corp.example/library/python:3.13-slim
RepoDigest: python@sha256:...
```

ContainerBin refuses that result. Selecting the first digest would silently
change the profile's repository identity and make a locally retagged image look
as though it had been pulled from the mirror.

For an explicitly named mirror or private registry, copy or push the image into
that repository and pull it back from that repository before running `cb lock`.
The resulting digest must name the mirror repository:

```text
mirror.corp.example/library/python@sha256:...
```

Do not work around a mismatch by deleting `container-bin.lock` or by substituting
a foreign digest. Fix the registry publication or profile reference. This is a
fail-closed boundary, not a mirror-discovery feature.

## Air-gapped image preparation

The supported locked model requires an image repository reachable from inside
the disconnected zone. Populate that registry through the organization's
approved transfer process, use its fully qualified repository names in every
profile, and run `cb lock` against it inside the zone. A typical target-side
sequence is:

```powershell
docker login registry.airgap.example
docker pull registry.airgap.example/devtools/python:3.13-slim
docker pull registry.airgap.example/devtools/node:24-slim
cb install
cb lock
cb lock --check
cb doctor
```

All configured images must be present: `cb lock` covers the complete registry,
not only the tool you plan to run first. Keep a copy of `cb.exe`,
`container-bin.toml`, and the resulting `container-bin.lock` in the controlled
transfer bundle.

`docker image save` and `docker image load` are useful transport primitives, but
a raw save/load round trip is not by itself a supported replacement for the
registry step. ContainerBin executes the locked `repository@sha256:digest`, and
Docker does not promise that an archive import reconstructs the original
repository digest identity. If an organization uses image archives to cross the
boundary, load them into a staging daemon, publish them to the in-zone registry,
then pull and lock them there.

Relevant Docker commands are documented under
[`docker image save`](https://docs.docker.com/reference/cli/docker/image/save/)
and [`docker image load`](https://docs.docker.com/reference/cli/docker/image/load/).
Always finish with `cb lock --check`; do not infer readiness merely because a tag
appears in `docker image ls`.

## What `cb backup` protects

Plain `cb backup` archives `container-bin.toml`, its detached signature when
present, the lockfile when present, and informational metadata. Under signed
registry policy the exact registry/signature snapshot is re-authenticated
before the archive is created. Add `--state` followed by explicit names from
`cb state` to include selected ContainerBin-managed Docker volumes:

```powershell
cb state
cb backup transfer.zip --state cb-node24-npm-global cb-go124-gobin
cb restore transfer.zip --state           # validate and check destinations
cb restore transfer.zip --state --apply   # import state, then registry/lock
```

State backup is deliberately explicit and fail-closed. It never sweeps Docker
volumes, accepts unlabeled state, or guesses a replacement project path. Every
selected volume must have consistent ContainerBin ownership labels and must not
be mounted by a running container. The manifest records labels, project identity,
archive sizes, and SHA-256 checksums. Restore validates every payload before it
changes Docker, refuses non-empty or label-mismatched destinations, and extracts
only inside a network-disabled helper container with a read-only root.

The helper image is immutable and must already be available; backup and restore
never pull it as a side effect:

```powershell
docker pull docker.io/library/alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40
```

Quiesce every ContainerBin invocation that could use the selected state for the
entire restore. The command checks for active volume users immediately before
import, but Docker has no reservation primitive that can eliminate the final
check-to-extract race.

Backups still exclude Docker images, registry credentials, Docker Desktop
settings, and host project files. Volume payloads can contain package-manager
credentials or other secrets, so handle the archive as sensitive data. Project
volume names encode the canonical Windows project path; after moving a project,
let ContainerBin create and identify the new destination and migrate into that
verified volume rather than renaming or guessing it.
