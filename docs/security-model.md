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
- A trusted project `.container-bin.toml` — names additional project-local
  images, exact environment variables, commands and project state. Trust is
  stored outside the repository and bound to the canonical root plus the
  SHA-256 digest of the exact overlay bytes; cloned or changed overlays are not
  active by themselves.
- The shim directory on `PATH` — whoever can write executables there doesn't
  need ContainerBin to attack you.
- Docker Desktop itself, and every image you configure or `docker pull`.

An optional administrator-owned machine policy sits above this user-controlled
boundary. Its fixed path, owner and permissions are validated before use. It
can require locking, restrict image origins and authenticate exact registry
bytes through a detached Ed25519 signature. Schema 3 can declare exact
repository-bound image-signature requirements; lock/update can produce
schema-2 evidence, and covered image execution requires that evidence to remain
fresh against current machine policy. Policy cannot grant mounts, environment
access or commands. See [enterprise machine
policy](enterprise-policy.md).

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
- **Digest-bound, add-only project configuration.** A project overlay cannot
  replace a global tool or alias, define defaults, request host mounts or
  environment prefixes, or use shared cross-project volumes. First use needs
  explicit interactive trust (or explicit `--yes` pre-provisioning), stored
  outside the project. A moved project, changed byte or invalid trust store
  fails before Docker or registry mutation. A discovered overlay with an
  unresolvable project-root reparse chain also fails because canonical identity
  cannot be proven. The legacy `python` provider is excluded because its
  implicit pip-cache mount is shared across projects. Exact environment names
  and project-volume requests are included in the review output; every
  project-controlled value is quoted so control characters cannot spoof review
  labels. Runtime workspace mounts and project-volume IDs for overlay tools are
  fixed to the approved overlay root; an ancestor marker cannot silently widen
  that boundary.
- **Repository-bound image trust policy.** Schema 3 pins an external cosign
  executable and declares exact keyless or public-key trust for canonical
  repository boundaries; schema 4 additionally pins the exact Sigstore
  TrustedRoot required by offline rules. Lock schema 2 can retain strictly validated,
  repository/digest/verifier/policy-bound structured evidence; schema 1 remains
  the digest-only compatibility format. The invocation layer executes only
  authenticated verifier/key snapshots from a protected private directory,
  bounds time and output, scrubs ambient environment state, checks staged bytes
  again after execution, and re-verifies every downloaded bundle locally
  against the exact digest, cosign predicate, and configured identity/key.
  Offline verification authenticates and stages the pinned root and passes both
  offline/new-bundle mode and `--trusted-root`, so incomplete proof cannot fall back
  to transparency-log or TUF access. Lock/update records a result only when
  exactly one authenticated transparency bundle fits the schema-2 evidence
  contract. Runtime accepts a covered digest only when that evidence still
  matches the current repository, digest, verifier pin, complete policy
  fingerprint, mechanism and signer/key identity. Missing or stale evidence
  never falls back to digest-only locking. Private-registry credentials remain
  excluded until an explicit non-ambient bridge is implemented.
- **Fail-closed host boundary.** Non-bootstrap work runs only in a native
  Windows process or a distribution-identified native WSL2 process. Windows
  binaries launched through detected WSL interoperability, WSL1, standalone
  Linux and other hosts refuse before registry or Docker work. WSL2
  classification requires Microsoft WSL2 kernel markers and a canonical
  `WSL_DISTRO_NAME`; environment variables alone cannot turn ordinary Linux
  into a supported host. Native WSL preparation validates or creates only the
  fixed current-user layout and loads no registry or machine policy.
  Installation validates that layout before fixed-path policy/registry access,
  authenticates signed registries when required, and reconciles only the fixed
  managed binary and provenance-checked symlinks. Ordinary managed tool
  execution then revalidates that fixed installation and uses the proof-bound
  Docker Desktop WSL transport; unsupported native management commands remain
  rejected.
- **Native WSL installation does not adopt ambient files.** The bootstrap
  executable is the exact OS-reported running image and must be a bounded,
  current-user-owned regular non-symlink file with safe executable permissions.
  Copying and hashing share one byte stream, and its digest must match the
  preflight digest before the destination is atomically published in the fixed
  private directory at mode `0755`; an existing foreign or unsafe target fails
  closed. Unsigned registries are recovered/created/upgraded only at the fixed
  mode-`0600` path. Before a missing primary can recover from `.bak`, that
  candidate must independently prove current-user ownership, regular-file and
  non-symlink identity, distribution-root device, and exact mode `0600`;
  signed-registry policy disables automatic registry mutation. The whole apply
  transaction uses the fixed registry mutation lock and revalidates the layout
  beneath it. Randomly named staging residue from a killed process is never
  adopted or pattern-swept as proof of ownership.
- **Native WSL tool shims have explicit provenance.** Preflight and mutation
  derive direct-child paths only from valid registry names and the fixed
  layout. Existing tool shims must be current-user-owned symlinks whose
  canonical target is the fixed managed binary; foreign files, owners or
  targets fail closed. Missing tool shims are published without clobbering
  through a no-follow, inode-pinned directory handle and revalidated after
  mutation. ContainerBin never replaces foreign objects or sweeps unrelated
  filename lookalikes.
- **Native WSL project roots are proven, not translated.** The unexposed
  classifier accepts only canonical existing directories with no symlink
  components. Native projects stay on the distribution root device; Windows
  projects require a proven default `/mnt/<drive>` DrvFs/virtiofs mount and
  cannot select an entire drive. Custom mounts and mixed Windows spellings fail
  closed, and exact Linux case remains part of project identity.
- **Native WSL Docker provenance fails closed.** The unexposed detector uses a
  direct Engine API request on the pinned local Unix socket, never an ambient
  CLI/config/context. It rejects Docker endpoint/TLS/API overrides, validates
  root ownership, non-world-writable permissions, a root socket peer and stable
  device/inode identity, then requires Docker Desktop name, OS, Microsoft WSL2
  kernel and address-label evidence. The proof must be repeated per operation;
  merely reaching an in-distribution or remote Docker Engine is not accepted.
- **Native WSL volumes are namespace-bound.** The unexposed volume lifecycle
  places the opaque distribution/machine/user namespace in both every managed
  name and `cb.wsl_namespace` label, and length-delimits owner components so
  hyphenated names cannot collide. Project hashes preserve exact canonical
  Linux path case. Inspect/create/remove requests repeat the proof-bound Docker
  Desktop control boundary instead of invoking an ambient CLI. Creation is
  accepted only after the response and a fresh inspect match the exact name,
  complete labels, local driver and local scope. Removal is never forced: it
  requires that same proof immediately before deletion and verifies absence
  afterward. Prefix/label filters are discovery-only and cannot authorize
  adoption or mutation. Windows and other WSL scopes remain foreign state.
- **Native WSL run containers are transaction-bound.** The create
  primitive accepts no raw Engine body, endpoint, privilege or Docker-socket
  mount controls. It admits at most one existing, symlink-free canonical project
  bind, rejects both fixed Docker-socket spellings and their source ancestors,
  and freshly proves each named volume's exact local name and complete ownership
  labels before use. It requires an explicit retention mode and labels each run
  with a generated 128-bit identity, exact WSL namespace and tool. The returned full
  container ID is not exposed until a fresh inspect proves those labels,
  stopped state, every attach/stdin flag, requested TTY and retention
  configuration. Post-create validation failure uses a separate bounded context
  and re-proves exact ownership before any non-force rollback. Later cleanup
  accepts only the immutable returned identity, re-proves ownership, refuses a
  running container or changed retention mode, deletes without force or anonymous-volume
  removal, and verifies absence; a racing auto-remove 404 is accepted only after
  another inspection proves absence. A malformed create response without a
  valid full ID fails closed rather than guessing a cleanup target.
- **Native WSL runtime failure cleanup is bounded.** Normal tool runs retain the
  owned container until Engine wait captures the exact status, closing the
  auto-remove race for fast processes. Stream, resize, forwarding or wait
  failure cancels live operations, sends SIGKILL only to the immutable owned
  container, waits under a fresh bound and then invokes the same non-force
  proof-bound removal. Cleanup errors are never hidden by the original failure.
- **Machine policy cannot be redirected or weakened.** A present enterprise
  policy is loaded only from the fixed OS path, requires administrator/root
  ownership and restrictive permissions, and authorizes the already-resolved
  image request before pulls, image inspection or execution. Missing means
  unmanaged; unreadable, malformed, expired or unsupported means stop.
- **Reserved shim names.** Tool names that would collide with `cb` itself or
  Windows device names (`con`, `nul`, `com1`, …) are rejected at validation,
  as are the private `cb-update-helper` dispatch name and versioned
  management-binary names beginning with `cb-v` plus a digit. Names such as
  `cb-vault` that lack that version digit remain available to registered tools.
  The same validation applies to binaries discovered from managed global stores
  or selected from a shared volume (which are untrusted input). Case-colliding
  names fail closed because Windows shims cannot represent both safely.
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
- **Fail-closed transactional self-update.** `cb self-update --check` accepts only a
  release-qualified Windows/amd64 or Windows/arm64 build selected from native
  `GOARCH`, queries the canonical GitHub repository
  over HTTPS with a bounded response, and requires exact canonical release and
  asset URLs, names and sizes. Downgrades and prereleases require explicit
  flags. Check mode performs no asset download and changes no installed files.
  Explicit `--apply` additionally requires the installed `cb.exe`, an absolute
  Authenticode-valid GitHub CLI path and an explicit token. It downloads exact
  advertised bytes plus `SHA256SUMS` to protected same-volume staging, accepting
  only the canonical URL or one HTTPS redirect to GitHub's release-asset host,
  then requires checksum and GitHub provenance without fallback. On ARM64,
  archive checksum and provenance are verified before an exact three-file
  archive layout is parsed and `cb.exe` is extracted.
- **Self-update mutation stays behind a re-verifying helper.** No installed byte
  changes before the parent has verified the complete result. A protected
  helper copy waits for the parent, revalidates the versioned request and exact
  paths, repeats remote verification, and requires the same opaque result before
  entering the serialized rollback-safe transaction. Only proven managed shims
  are reconciled. Timeout, changed inputs, malformed requests, lock contention,
  smoke-test failure and unrelated executable files all fail closed.

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
