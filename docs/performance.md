# Startup performance

Every normal ContainerBin tool invocation creates a disposable Linux
container with `docker run --rm`. The Go shim also resolves project context,
maps Windows paths, checks the image lock and ensures managed volumes. Startup
latency is therefore dominated by the Docker Desktop control path, but the
split should be measured rather than assumed.

## Reproducible Windows benchmark

[`scripts/benchmark-startup.ps1`](../scripts/benchmark-startup.ps1) measures
five warm-cache paths:

1. an installed ContainerBin shim;
2. `docker run` with Docker's default `missing` pull policy;
3. the same cached image ID with `--pull=never`;
4. the same run with `--network none`;
5. `docker exec` in one temporary long-lived control container.

The script requires a fully qualified drive-absolute or UNC shim path,
non-interactive commands, and a Linux image that is already local and provides
`sh` plus `sleep` for the control container. It never pulls. Its one temporary
container has a GUID-scoped name and the label `cb.benchmark=rm28`; a `finally`
block removes that exact container after every attempted start and verifies it
is absent. If measurement and cleanup both fail, the original measurement
error is reported first with the cleanup error appended. The script emits one
versioned JSON document only after successful cleanup so results can be
compared without scraping a display table.

```powershell
Set-Location -LiteralPath 'D:\Work\GIT\container-bin'
& '.\scripts\benchmark-startup.ps1' `
  -ShimPath 'D:\Tools\ContainerBin\node22.exe' `
  -Image 'node:22-slim' `
  -ContainerCommand @('node', '--version') `
  -Warmups 3 `
  -Iterations 15
```

Run on an otherwise idle machine after Docker Desktop is warm. Repeat the
whole benchmark when comparing Docker Desktop or ContainerBin versions;
individual samples from different host states are not interchangeable. The
direct Docker controls intentionally do not recreate every ContainerBin mount
and volume, so they bound the Docker process/container cost rather than
isolating nanosecond-level Go overhead.

Save the JSON from each otherwise-identical run, then compare two or more
Docker Desktop / engine versions with the first file as the baseline:

```powershell
& '.\scripts\benchmark-startup.ps1' `
  -ShimPath 'D:\Tools\ContainerBin\node22.exe' `
  -Image 'node:22-slim' `
  -ContainerCommand @('node', '--version') |
  Set-Content -LiteralPath 'D:\Benchmarks\container-bin\engine-29.7.2.json' -Encoding utf8

& '.\scripts\compare-startup-benchmarks.ps1' -InputPath @(
  'D:\Benchmarks\container-bin\engine-29.7.2.json',
  'D:\Benchmarks\container-bin\engine-29.8.0.json'
) -Format Markdown
```

[`scripts/compare-startup-benchmarks.ps1`](../scripts/compare-startup-benchmarks.ps1)
emits a Markdown table or a versioned JSON report with p50/p95 percentage
changes. It fails closed instead of comparing confounded runs: Windows and
PowerShell versions, working directory, shim and arguments, image ID,
container command, warmup/iteration counts, and measurement names must match.
The PowerShell version stays in the signature because the measured wall-clock
region includes host-shell command dispatch and native-process startup, not
only time spent inside Docker.
Docker Engine version and capture time are intentionally allowed to differ and
are recorded on every row. Keep the raw JSON inputs as the durable benchmark
record; generated tables can always be reproduced from them.

## Observed baseline

One development-host run on 2026-09-14 used Windows NT 10.0.26200.0,
PowerShell 7.6.5, Docker Engine 29.7.2 in Linux-container mode, the installed
`node22.exe` shim and local image ID
`sha256:83f487e0a63425e5b4d146fb5e5be574bcbe1b7b843d3ebafdd95eaf7767a7e5`.
After three warmups, 15 samples produced:

| Path | p50 | p95 |
|---|---:|---:|
| ContainerBin `node22 --version` | 760.49 ms | 869.93 ms |
| `docker run` (default pull policy) | 561.70 ms | 615.89 ms |
| `docker run --pull=never` | 572.99 ms | 615.17 ms |
| `docker run --pull=never --network none` | 420.24 ms | 443.84 ms |
| `docker exec` control | 106.09 ms | 111.84 ms |

This is evidence from one host, not a universal performance claim. It shows
about 199 ms p50 beyond the minimal `docker run` control for ContainerBin's
project, lock, volume and argument work. More importantly for the design
choices below, `--pull=never` was not faster than the default in this run;
disabling networking saved about 141 ms but changes tool capability; and the
large `docker exec` delta comes with a different lifecycle/state model.

## Evaluation

Docker documents `missing` as the default pull policy: it uses a locally cached
image and pulls only when the image is absent. `--pull=never` changes the
missing-image behavior, but does not remove the cached-image lookup. See
[Docker's `docker run` pull-policy reference](https://docs.docker.com/reference/cli/docker/container/run/#set-the-pull-policy---pull).

With its normal lockfile present, ContainerBin executes immutable image
references. A missing locked image may therefore be fetched by its exact
digest without allowing a mutable tag to move. Adding `--pull=never` globally
would change recovery behavior (normal runs could no longer restore an absent
locked image) for little expected warm-start benefit. Keep the current default
unless measurements on supported hosts demonstrate a material improvement or
an explicit offline mode is designed under RM-32.

Docker also creates a built-in default bridge network when the engine starts,
and containers join it unless another network is selected. A separately
pre-created bridge does not avoid per-container network attachment; it adds
policy and lifecycle state without addressing this workload. `--network none`
is a useful benchmark control, but cannot be a global optimization because
package managers and many configured tools legitimately need outbound access.
See Docker's [networking overview](https://docs.docker.com/engine/network/)
and [bridge driver documentation](https://docs.docker.com/engine/network/drivers/bridge/).

`docker exec` can avoid container creation by starting commands inside a
running container, but Docker specifies that the command exists only while
that container's PID 1 is running and that its default environment and working
directory come from container creation. ContainerBin would need to reconcile
mounts, environment allowlists, CWD, TTY/stdio behavior, image-lock updates,
concurrent invocations, cleanup and failure recovery for every execution. That
is a new stateful service model, not a transparent launcher optimization. See
[Docker's `docker exec` reference](https://docs.docker.com/reference/cli/docker/container/exec/).

The recommendation is to keep disposable `docker run --rm` as the default.
If repeated measurements show `docker exec` savings large enough for a real
workload, prototype it only as an explicit opt-in per state group with an
equivalence test matrix for mounts, environment, stdio, exit codes and lock
rotation. Do not silently reuse containers across unrelated projects.
