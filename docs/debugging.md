# Runtime debug tracing

`cb trace TOOL ARGS...` is a dry-run view of project and argument mapping.
When a failure depends on the final runtime choices, set `CB_DEBUG=1` to emit
one JSON line describing each actual `docker run` attempt to stderr:

```powershell
$env:CB_DEBUG = '1'
node '.\script.js'
Remove-Item Env:CB_DEBUG
```

The event contains a schema version, UTC timestamp, tool name, canonical host
CWD, detected project root, whether a project marker was found, container
workspace, and the exact post-mapping argv including the leading `docker`.
TTY selection, immutable image resolution, mounts, environment selection and
provider bootstrap arguments are therefore visible. Tool stdout remains clean;
the trace shares stderr with the tool because debug mode is explicitly opt-in.

To keep JSON Lines for a support report, also set an absolute Windows path:

```powershell
$env:CB_DEBUG = '1'
$env:CB_DEBUG_LOG = 'D:\TEMP\container-bin-debug.jsonl'
node '.\script.js'
Remove-Item Env:CB_DEBUG, Env:CB_DEBUG_LOG
```

The parent directory must already exist. ContainerBin appends one complete
line per invocation, using an OS file lock to serialize concurrent ContainerBin
processes, and fails closed before running Docker if it cannot open, lock,
seek, write, unlock or close the configured log. `CB_DEBUG_LOG` alone does
nothing; logging requires `CB_DEBUG=1` so a stale environment variable cannot
silently collect future commands.

## Sensitive data warning

Debug records are **not sanitized**. Exact argv can contain credentials or
other secrets supplied as command arguments or literal registry `env_set`
values. Records also expose host paths, mount destinations, image references
and tool arguments. Host environment variables selected only by name appear as
`-e NAME`, not with their inherited value, but that does not make the rest of
the record safe to publish.

Review and redact every line before attaching it to an issue. Delete the log
when the investigation is complete. `cb bugreport` deliberately does not
include this opt-in log automatically.

## Example event

```json
{"schema_version":1,"timestamp":"2026-09-14T12:34:56.1234567Z","event":"docker_run","tool":"node","cwd":"D:\\Work\\demo","project_root":"D:\\Work\\demo","project_found":true,"workspace":"/workspace/demo","argv":["docker","run","--rm","-i","--workdir","/workspace/demo","--mount","type=bind,src=D:\\Work\\demo,dst=/workspace/demo","node@sha256:...","--version"]}
```

The record describes an attempted execution. Docker can still reject the
request or the tool can return a nonzero exit code afterward; retain the normal
stderr and exit code alongside the trace.
