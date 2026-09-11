# AGENTS.md

Repository-wide instructions for coding agents and automation working on ContainerBin.

## Project intent

ContainerBin provides Windows shims that execute development tools inside Docker containers so the host does not need native Python, Node.js, Terraform, and similar runtimes installed. Preserve Windows path semantics, stdin/stdout behavior, exit codes, and predictable container isolation.

## Development

- Run Go formatting, vetting, tests, and release-style version checks before proposing changes.
- Keep Windows behavior first-class; changes that work only on Linux are incomplete.
- Preserve backwards compatibility for existing shim names and command-line behavior unless a change is explicitly intentional and documented.
- Keep the core binary dependency surface minimal. New runtime dependencies require a strong justification.

## Safety and supply chain

- Do not commit credentials, local secrets, tokens, or machine-specific private configuration.
- Keep GitHub Actions dependencies pinned to full commit SHAs with readable version comments.
- Do not weaken CI, dependency review, vulnerability scanning, release provenance, or branch protections to make a change pass.
- Release artifacts must be produced by the repository release workflow; do not manually replace published assets.

## Documentation

Update README and relevant docs when user-visible behavior, supported tools, defaults, setup, configuration, or release behavior changes.
