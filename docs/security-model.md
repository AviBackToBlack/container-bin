# ContainerBin security model

## The one-sentence version

ContainerBin is a **convenience layer, not a sandbox**: it runs the Docker
images your configuration names, with the host paths your commands reference
mounted in, and the environment variables your profiles select passed through.

## Trust boundary

Inside the boundary (whoever controls these controls execution):

- `container-bin.toml` — names the images, the env allowlists, the volumes,
  the `host_mounts`, and the path semantics. Arbitrary registry write access
  ≈ arbitrary code execution with your Docker privileges; `host_mounts` with
  `rw` access hand the image write access to those host paths.
- `container-bin.lock` — pins image digests. Whoever can rewrite it can pin a
  malicious digest.
- The shim directory on `PATH` — whoever can write executables there doesn't
  need ContainerBin to attack you.
- Docker Desktop itself, and every image you configure or `docker pull`.

An optional administrator-owned machine policy sits above this user-controlled
boundary. Its fixed path, owner and permissions are validated before use. It
can require locking, restrict image origins and authenticate exact registry
bytes through a detached Ed25519 signature. It cannot grant mounts, environment
access or commands, and it does not yet authenticate image signatures. See
[enterprise machine policy](enterprise-policy.md).

Treat the registry and lockfile like your PowerShell `$PROFILE`: yours,
readable, and dangerous to let others edit.

## What ContainerBin deliberately does — restrictive by design

- **Allowlist-only environment passing.** Only `env_names` exact matches and
  `env_prefixes` prefix matches cross into the container, plus literal
  `env_set` values. The full Windows environment is never forwarded, and no
  profile defaults to wildcards.
- **Narrow mounts.** The project root is mounted; paths outside it get
  individual narrow bind mounts (for files: the parent directory; for
  not-yet-existing outputs: nearest existing ancestor). Whole drives are never
  mounted because one argument lives on them. `host_mounts` are the explicit,
  mode-required exception: a trusted profile can declare a fixed host source
  and container target, but every entry must spell out `ro` or `rw` and is
  validated as a `SOURCE:/CONTAINER_PATH:MODE` binding with the same fail-closed
  checks as the volume fields.
- **Conservative path rewriting.** Arguments are only treated as paths when
  their shape is unambiguous or the tool profile explicitly declares the
  semantics. Unknown strings pass through untouched.
- **Fail-closed configuration.** Unknown registry keys, duplicate tool
  sections, newer schema versions, incomplete lock entries, and
  registry-image-not-in-lock all refuse to run rather than guess.
- **Fail-closed host boundary.** Non-bootstrap work currently runs only in a
  native Windows process. Windows binaries launched through detected WSL
  interoperability, WSL1, recognized-but-not-yet-enabled native WSL2,
  standalone Linux and other hosts refuse before registry or Docker work.
  WSL2 classification requires Microsoft WSL2 kernel markers; environment
  variables alone cannot turn ordinary Linux into a supported host.
- **Machine policy cannot be redirected or weakened.** A present enterprise
  policy is loaded only from the fixed OS path, requires administrator/root
  ownership and restrictive permissions, and authorizes the already-resolved
  image request before pulls, image inspection or execution. Missing means
  unmanaged; unreadable, malformed, expired or unsupported means stop.
- **Reserved shim names.** Tool names that would collide with `cb` itself or
  Windows device names (`con`, `nul`, `com1`, …) are rejected at validation,
  as are versioned management-binary names beginning with `cb-v` plus a digit.
  Names such as `cb-vault` that lack that version digit remain available to
  registered tools. The same validation applies to binaries discovered from
  managed global stores or selected from a shared volume (which are untrusted
  input). Case-colliding names fail closed because Windows shims cannot
  represent both safely.
- **Conservative deletion.** `cb gc` is dry-run by default, deletes only
  explicitly selected current-project state with `--apply`, and only considers
  a volume an orphan when *its own labels* record a project path that no
  longer exists. Unlabeled volumes are never deleted. `cb unexpose` likewise
  requires the explicit `role = "exposed"` ownership marker plus a matching
  command/name beneath a declared shared-volume mount; recognized global
  stores additionally require their exact bin directory and companion-volume
  shape. A similar-looking custom profile is not treated as generated state.
- **Read-only exposure discovery.** `cb expose` requires the locked source image
  to exist locally, disables pulls and networking, uses a read-only container
  root and volume mounts, and overrides the image entrypoint with the discovery
  shell or pipx's embedded Python scanner. Non-pipx source images must provide a
  POSIX-compatible `sh`; distroless images without one cannot use automatic
  discovery. Explicit shared-file
  discovery also rejects a final symlink or a parent directory that resolves
  outside the selected volume mount.
- **Serialized pipx exposure.** Pipx commands create and hold the volume-local
  lock exclusively before mutating state. Discovery mounts the state read-only,
  reports no applications when the lock does not exist yet, and otherwise holds
  it shared while scanning. This prevents exposure from observing partially
  updated application state without granting the discovery container write
  access.
- **Validated atomic writes.** Registry/lock mutations parse the complete
  resulting file before atomically replacing the original; backups are
  restored the same way and only with `--apply`.
- **Restore extracts nothing onto the host.** Without `--state`, `cb restore`
  reads exactly `container-bin.toml` and `container-bin.lock` by name and ignores
  state payloads. With explicit `--state`, it validates the versioned manifest,
  archive names, sizes, SHA-256 hashes, tar paths/types/link targets, volume
  labels, project identity, quiescence, and destination emptiness before apply.
  Tar extraction occurs only inside the named Docker volume through an immutable,
  network-disabled helper with a read-only container root; no archive member is
  turned into a Windows host path.
- **Fail-closed self-update selection.** `cb self-update --check` accepts only a
  release-qualified Windows/amd64 build, queries the canonical GitHub repository
  over HTTPS with a bounded response, and requires exact canonical release and
  asset URLs, names and sizes. Downgrades and prereleases require explicit
  flags. This phase performs no asset download and changes no installed files;
  later phases must require both checksums and GitHub provenance without a
  fallback before replacement is possible.

## What ContainerBin does NOT protect against

- **Malicious or compromised images.** A hostile image receives your mounted
  project (and any explicitly referenced external paths) read-write, plus the
  allowlisted environment variables. `cb lock` gives you *reproducibility* —
  the same digest every time — not *safety* of that digest's contents.
- **Malicious packages.** `pip install`, `pipx install`, `npm install -g`, and
  `cargo install` execute inside containers, but the packages can read/write the
  mounted project and persist in state volumes; an exposed global binary runs
  whenever you invoke its shim.
- **pipx launcher bootstrap.** The pipx profile's image digest is locked, but
  its exact-version `pipx==1.17.4` launcher is populated from the configured
  Python package index into a dedicated cache on first use. Index overrides,
  TLS settings and proxies therefore remain part of that bootstrap's trust
  boundary; the image lock does not attest package-index artifacts.
- **Fail-closed pipx link normalization.** After every pipx command, including
  failed commands and interrupts, the
  profile makes pipx-owned absolute links relative within its one state volume
  and copies the exact image interpreter only at known venv/cache locations.
  Any other absolute link fails the command; archive traversal checks remain
  unchanged.
- **Secrets you pass through.** `env_prefixes = ["AWS_"]` exists so Terraform
  can authenticate — which means your AWS credentials enter that container.
  That is the feature working as designed; scope prefixes deliberately.
- **Container escape / Docker vulnerabilities.** Out of scope; keep Docker
  Desktop updated.
- **Anything with local write access** to the registry, lockfile, or shim
  directory (see trust boundary).
- **TOCTOU path re-pointing.** A path whose target is re-pointed between
  classification and `docker run` is mounted as re-pointed; this is inside the
  local-write-access boundary and is stated explicitly so it reads as a
  decision rather than an omission.

## Practical guidance

- Run `cb lock` immediately after setup and after every deliberate registry
  change; review `cb update` diffs (old → new digest) when refreshing.
- Prefer specific tags (`python:3.13-slim`) over `latest` in profiles you care
  about; the lockfile pins either way, but intent stays readable.
- Audit `cb expose` output — every exposed binary is a new command on your
  PATH.
- Before pasting `cb inspect` / registry snippets into issues, strip
  credentials: env allowlists tell attackers what's worth stealing, and
  proxy/registry URLs may embed passwords.
- Treat `CB_DEBUG=1` output and `CB_DEBUG_LOG` files as sensitive. They contain
  exact tool/Docker arguments and host paths and are not included in
  `cb bugreport`; review and redact them before sharing. See
  [runtime debug tracing](debugging.md).
- Back up (`cb backup`) before hand-editing the registry.

## Reporting

Vulnerability reports (things violating the *restrictive-by-design* list
above, e.g. a mount broader than arguments imply, an env var passed that no
profile selected, a lock bypass): see [SECURITY.md](../SECURITY.md). Malicious
images doing malicious things inside their granted access are the documented
trust model, not a vulnerability.
