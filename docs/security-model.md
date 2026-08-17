# ContainerBin security model

## The one-sentence version

ContainerBin is a **convenience layer, not a sandbox**: it runs the Docker
images your configuration names, with the host paths your commands reference
mounted in, and the environment variables your profiles select passed through.

## Trust boundary

Inside the boundary (whoever controls these controls execution):

- `container-bin.toml` — names the images, the env allowlists, the volumes,
  the path semantics. Arbitrary registry write access ≈ arbitrary code
  execution with your Docker privileges.
- `container-bin.lock` — pins image digests. Whoever can rewrite it can pin a
  malicious digest.
- The shim directory on `PATH` — whoever can write executables there doesn't
  need ContainerBin to attack you.
- Docker Desktop itself, and every image you configure or `docker pull`.

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
  mounted because one argument lives on them.
- **Conservative path rewriting.** Arguments are only treated as paths when
  their shape is unambiguous or the tool profile explicitly declares the
  semantics. Unknown strings pass through untouched.
- **Fail-closed configuration.** Unknown registry keys, duplicate tool
  sections, newer schema versions, incomplete lock entries, and
  registry-image-not-in-lock all refuse to run rather than guess.
- **Reserved shim names.** Tool names that would collide with `cb` itself or
  Windows device names (`con`, `nul`, `com1`, …) are rejected at validation,
  including for binaries discovered from the npm global prefix (which are
  untrusted input).
- **Conservative deletion.** `cb gc` is dry-run by default, deletes only
  explicitly selected current-project state with `--apply`, and only considers
  a volume an orphan when *its own labels* record a project path that no
  longer exists. Unlabeled volumes are never deleted.
- **Validated atomic writes.** Registry/lock mutations parse the complete
  resulting file before atomically replacing the original; backups are
  restored the same way and only with `--apply`.
- **Restore extracts nothing else.** `cb restore` reads exactly
  `container-bin.toml` and `container-bin.lock` from the ZIP by name; other
  archive members, paths, and symlinks are ignored, so a crafted backup cannot
  write outside those two files.

## What ContainerBin does NOT protect against

- **Malicious or compromised images.** A hostile image receives your mounted
  project (and any explicitly referenced external paths) read-write, plus the
  allowlisted environment variables. `cb lock` gives you *reproducibility* —
  the same digest every time — not *safety* of that digest's contents.
- **Malicious packages.** `pip install` / `npm install -g` execute inside
  containers, but the packages can read/write the mounted project and persist
  in state volumes; an exposed npm binary runs whenever you invoke its shim.
- **Secrets you pass through.** `env_prefixes = ["AWS_"]` exists so Terraform
  can authenticate — which means your AWS credentials enter that container.
  That is the feature working as designed; scope prefixes deliberately.
- **Container escape / Docker vulnerabilities.** Out of scope; keep Docker
  Desktop updated.
- **Anything with local write access** to the registry, lockfile, or shim
  directory (see trust boundary).

## Practical guidance

- Run `cb lock` immediately after setup and after every deliberate registry
  change; review `cb update` diffs (old → new digest) when refreshing.
- Prefer specific tags (`python:3.13-slim`) over `latest` in profiles you care
  about; the lockfile pins either way, but intent stays readable.
- Audit `cb expose npm` output — every exposed binary is a new command on your
  PATH.
- Before pasting `cb inspect` / registry snippets into issues, strip
  credentials: env allowlists tell attackers what's worth stealing, and
  proxy/registry URLs may embed passwords.
- Back up (`cb backup`) before hand-editing the registry.

## Reporting

Vulnerability reports (things violating the *restrictive-by-design* list
above, e.g. a mount broader than arguments imply, an env var passed that no
profile selected, a lock bypass): see [SECURITY.md](../SECURITY.md). Malicious
images doing malicious things inside their granted access are the documented
trust model, not a vulnerability.
