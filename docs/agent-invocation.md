# AI agent and automation invocation

ContainerBin shims are ordinary Windows processes, so their project identity
starts with the process working directory. Agent harnesses, IDEs, scheduled
jobs, MCP hosts and GUI launchers do not all preserve a shell's directory
between calls. A successful `Test-Path` in one shell therefore says nothing
about the working directory of a later `node script.js` invocation.

## Canonical execution contract

For every ContainerBin-backed command on Windows:

1. Treat the inherited working directory as untrusted.
2. When the tool uses project-relative files or project-scoped state, set the
   intended project directory and invoke the shim in the **same process or
   shell execution**. Prefer a launch API's explicit working-directory field
   when it has one.
3. For a file argument that does not need project-relative state, an absolute
   Windows path is safe and explicit. It does **not** select the project root:
   project detection and project-volume identity still start from the process
   working directory.
4. Never rely on `cd` or `Set-Location` performed by a previous, separate tool
   call. Never search for a similarly named file or guess the intended root.
5. If resolution fails, report the actual CWD and inspect the exact mapping
   with `cb trace` before changing `PATH`, installing a native runtime, or
   blaming Docker.

PowerShell example for a project-aware command:

```powershell
Set-Location -LiteralPath 'D:\Work\project'
node '.\script.js'
```

Those two statements must run in the same PowerShell execution. With an API
that exposes a working-directory option, set it to `D:\Work\project` and run
`node .\script.js` directly.

An explicit file-only invocation can instead use:

```powershell
node 'D:\Work\project\script.js'
```

Use that form only when it is acceptable for project discovery and persistent
state to remain tied to the launcher's actual CWD. For example, a Node command
that must reuse the project's `node_modules` volume should use the first form.

## Failure triage

Run the existence check, trace and real command from the same directory:

```powershell
Set-Location -LiteralPath 'D:\Work\project'
Get-Location
Test-Path -LiteralPath '.\script.js'
cb trace node '.\script.js'
node '.\script.js'
```

`cb trace` is deliberately dry-run only. It already prints the canonical host
`cwd`, detected project `root`, container `workspace`, raw/normalized/mapped
argv, and additional mounts. That is enough to distinguish a launcher-CWD bug
from a path-classification or Docker-sharing problem, so RM-14 adds no second
diagnostic mode. If the trace shows the wrong CWD, fix the launcher boundary;
ContainerBin will not search for a more plausible project.

For a genuinely projectless background tool, a registry profile may opt into
`cwd_mode = "isolated"`. That mode intentionally disables project discovery
and the project bind mount; it is not a workaround for commands that need
project files or project-scoped volumes. See the registry documentation in the
[README](../README.md#isolated-launcher-mode-cwd_mode).

## Instruction files by agent

Keep the contract above in the repository-root `AGENTS.md` as the canonical
copy. Product-specific files should point to it instead of duplicating text.

| Agent or launcher | Repository mechanism | ContainerBin recommendation |
|---|---|---|
| OpenAI Codex | Codex loads `AGENTS.md` from the project root down to its current directory. [Official OpenAI documentation](https://developers.openai.com/codex/guides/agents-md) | Put the contract in root `AGENTS.md`; start the task in the intended repository/worktree and still set CWD on each independent shell call. |
| Claude Code | Claude loads `CLAUDE.md`; its documentation recommends importing an existing `AGENTS.md` with `@AGENTS.md`. [Official Anthropic documentation](https://code.claude.com/docs/en/memory#agentsmd) | Keep the checked-in `CLAUDE.md` as the one-line import `@AGENTS.md`, especially on Windows where a symlink may require elevated privileges or Developer Mode. |
| GitHub Copilot CLI and cloud agent | Copilot supports `AGENTS.md`, `CLAUDE.md`, `.github/copilot-instructions.md`, and path-specific instruction files, with support varying by surface. [Official GitHub documentation](https://docs.github.com/en/copilot/reference/custom-instructions-support) | Use root `AGENTS.md` for the shared contract. Add Copilot-specific files only for guidance that is genuinely Copilot-specific, and avoid conflicting copies. |
| Devin | Devin reads `AGENTS.md` before coding and recommends repository setup for additional environment context. [Official Devin documentation](https://docs.devin.ai/onboard-devin/agents-md) | Use root `AGENTS.md` and configure the repository setup/launcher CWD to the intended checkout. |

For another automation system, inject the same contract through its native
repository-instruction mechanism and set the child process working directory
explicitly. Instruction discovery helps the agent choose correctly; the
launcher CWD remains the actual process boundary ContainerBin receives.

Research for this compatibility table was refreshed on 2026-09-14 against the
linked vendor documentation.
