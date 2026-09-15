# AGENTS.md

Repository-wide instructions for coding agents and automation working on ContainerBin.

## Project intent

ContainerBin provides Windows shims that execute development tools inside Docker containers so the host does not need native Python, Node.js, Terraform, and similar runtimes installed. Preserve Windows path semantics, stdin/stdout behavior, exit codes, and predictable container isolation.

## Development

- Run Go formatting, vetting, tests, and release-style version checks before proposing changes.
- Keep Windows behavior first-class; changes that work only on Linux are incomplete.
- Preserve backwards compatibility for existing shim names and command-line behavior unless a change is explicitly intentional and documented.
- Keep the core binary dependency surface minimal. New runtime dependencies require a strong justification.

## ContainerBin-backed tool invocation on Windows

- Treat an inherited shell working directory as untrusted. Set the intended project directory and invoke the shim in the same shell execution, or configure the launcher process's working directory explicitly.
- Use fully qualified, Docker-shareable drive-letter paths (for example, `D:\Work\file.js`) for file arguments when project-relative state is not required. UNC and extended-length paths are not mapped, while `subst` and mapped-network drives cannot be shared by Docker Desktop; see [docs/windows-paths.md](docs/windows-paths.md). An absolute argument does not select the project root; tools that use project-scoped state still require the intended project working directory.
- Never rely on `Set-Location` or `cd` from an earlier, separate shell/tool call.
- On a file-not-found or unexpected-mount result, capture `Get-Location` and run `cb trace TOOL ARGS...` from the same directory before changing `PATH` or installing a native runtime.
- Do not search for or guess the intended file. Fail closed and report the CWD, requested path, and trace result.

## Safety and supply chain

- Do not commit credentials, local secrets, tokens, or machine-specific private configuration.
- Keep GitHub Actions dependencies pinned to full commit SHAs with readable version comments.
- Do not weaken CI, dependency review, vulnerability scanning, release provenance, or branch protections to make a change pass.
- Release artifacts must be produced by the repository release workflow; do not manually replace published assets.

## Documentation

Update README and relevant docs when user-visible behavior, supported tools, defaults, setup, configuration, or release behavior changes.
