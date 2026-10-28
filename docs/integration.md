# Integration guide

There are three supported integration surfaces. All use the same engine and the
same result/error semantics.

1. **Go library** — preferred for Go applications. Two calls, no daemon, no
   process boundary.
2. **Subprocess + NDJSON** — universal fallback for other languages, process
   isolation, or embedding in non-Go build systems.
3. **Loopback HTTP API** — for desktop apps that want a long-lived updater
   process, job polling, cancellation and SSE.

## Go library

Import the module and keep the integration to `New`, `Check` and `Apply`:

```go
import wmlupdate "github.com/yxxbc/wmlUpdate"

u, err := wmlupdate.New(wmlupdate.Options{
    ConfigPath:     "updater.json",
    CurrentVersion: appVersion,
})
if err != nil { return err }
defer u.Close()

update, err := u.Check(ctx)
if err != nil { return err }
if !update.Available { return nil }

result, err := u.Apply(ctx, update, wmlupdate.ApplyOptions{})
```

`Options.Config` can replace `ConfigPath` for a dependency-only setup. Events are
an optional callback. The library never exits or restarts the host. See the
[Go SDK guide](sdk.md) for the complete API, Windows deferred apply, programmatic
configuration and error codes.

## Subprocess + NDJSON

Run the binary directly. Do not invoke it through a shell: paths, arguments and
exit codes are safer when passed as an argv array.

```bash
wml-update --config /path/updater.json --json update --current-version 1.4.2
```

When `--json` is set:

- **stdout** contains only JSON objects, one per line.
- **stderr** contains human diagnostics, if any.
- Never parse prose from stdout.
- Read stdout line by line as the process runs; do not wait for exit before
  displaying progress.
- Treat the terminal `result` or `error` event as authoritative, and also check
  the process exit code.

### Event shapes

Progress:

```json
{"type":"progress","time":"2026-10-28T12:00:00Z","phase":"download","bytes":1048576,"total":4194304,"percent":25}
```

Log and warning:

```json
{"type":"log","time":"2026-10-28T12:00:00Z","phase":"source","message":"querying static source"}
{"type":"warning","time":"2026-10-28T12:00:00Z","message":"artifact has no SHA-256; security.allow_unverified_assets is enabled"}
```

Success:

```json
{
  "type": "result",
  "time": "2026-10-28T12:00:01Z",
  "data": {
    "status": "updated",
    "current_version": "1.5.0",
    "previous_version": "1.4.2",
    "latest_version": "1.5.0",
    "channel": "stable",
    "source": "static",
    "tag": "1.5.0",
    "published_at": "2026-10-27T09:00:00Z",
    "notes": "Release notes",
    "asset": {
      "name": "myapp-1.5.0-linux-amd64.tar.gz",
      "os": "linux",
      "arch": "amd64",
      "format": "tar.gz",
      "sha256": "0123...",
      "size": 4194304
    },
    "artifact_path": "/home/user/.local/state/wml-update/com.example.myapp/downloads/0123...__myapp-1.5.0-linux-amd64.tar.gz"
  }
}
```

Failure:

```json
{"type":"error","time":"2026-10-28T12:00:01Z","error":{"code":"integrity","message":"checksum mismatch: got ..., want ..."}}
```

### Status values

| Status | Command | Meaning |
| --- | --- | --- |
| `update_available` | `check`, `update --dry-run` | A newer (or forced) release is available |
| `up_to_date` | `check`, `update`, `fetch` | Current version is not older than the release |
| `updated` | `update` | Installation completed |
| `downloaded` | `update` with `install.type=external`, `fetch` | Verified artifact is available at `artifact_path` |
| `apply_scheduled` | `update --defer` | Staged; detached helper will apply it |
| `rolled_back` | `rollback` | Previous backup restored |
| `marked` | `mark` | External installation recorded |

`check` exits 0 even when an update is available. Branch on `result.data.status`.

### Error codes and exit codes

| Code | Exit | Meaning |
| --- | --- | --- |
| `config` | 2 | Invalid config, version or flags |
| `source` / `network` | 3 | Source unavailable, malformed response, HTTP failure |
| `no_release` / `no_asset` | 3 | Nothing eligible / no asset for this platform |
| `integrity` | 4 | Signature, checksum or size failure |
| `install` / `rollback` | 5 | Filesystem/install failure |
| `locked` / `state` / `io` | 6 | Concurrent update, state or I/O failure |
| `cancelled` | 130 | Context/signal cancellation |
| `internal` | 1 | Unexpected error |

### Choosing a command

- **`check`** is cheap and read-only. Use it on startup or a timer.
- **`update`** is the normal path: check + download + verify + install.
- **`fetch --output PATH`** verifies an artifact and copies it to a path so your
  code can install it. Follow with `mark --version ... --sha256 ...` only after
  your installer succeeds.
- **`update --defer`** is required when the target is running on Windows.

### Deferred apply (Windows and self-replacement)

```text
app process ──spawn──> wml-update update --defer --wait-pid <appPID> --json
    │                         │
    │                         ├─ download, verify, extract, write plan
    │                         ├─ copy updater to state dir/helper/<plan>/
    │                         ├─ start helper detached
    │                         └─ emit result apply_scheduled; exit 0
    │
    └─ exits (after receiving apply_scheduled)

helper ── waits for <appPID> ── acquires lock ── journaled apply ── relaunch
```

Recommended sequence:

1. Spawn the updater with `update --defer --wait-pid <yourPID> --json`.
2. Read until the `result` event.
3. If `status == "apply_scheduled"`, exit the application promptly.
4. Configure `install.relaunch_path` so the helper restarts the new version.

The parent updater must be allowed to finish emitting `apply_scheduled` before
the application exits. The helper is copied outside the managed directory, so it
can replace files even when the updater binary itself lives inside the target.

A helper result file is written at:

```text
<state_dir>/plans/<plan_id>.json.result
```

with `{"ok":true}` or an error. On failure the plan is retained for diagnosis;
the next update recovers any pending journal.

### Language examples

- [Python](../examples/python/update.py)
- [Node.js](../examples/node/update.js)
- [Go sidecar](../examples/go-cli/main.go)
- [C#](../examples/csharp/Update.cs)
- [Shell](../examples/shell/update.sh)

All examples follow the same rules: argv array, line-delimited JSON, wait for a
terminal event, check the exit code.

## Loopback HTTP API

Start the server:

```bash
wml-update --config updater.json serve --json
```

Example ready line (a token is generated when none is supplied):

```json
{"type":"ready","url":"http://127.0.0.1:52143","address":"127.0.0.1:52143","pid":4242,"token_source":"generated","token":"..."}
```

If a token is supplied with `--token`, `--token-env` or `--token-file`, it is not
echoed; the caller already knows it. `WML_UPDATE_TOKEN` is read automatically if
no flag is set.

The server binds loopback by default. `--allow-remote` permits another address;
terminate TLS and additional authentication at a reverse proxy.

### Authentication

Every endpoint except `/v1/health` requires one of:

```http
Authorization: Bearer <token>
X-WML-Token: <token>
```

Tokens must be at least 16 characters. Comparison is constant-time. There is no
CORS opt-in.

### Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/v1/health` | Liveness; no auth |
| GET | `/v1/info` | App ID, channel, platform, source type |
| GET | `/v1/status` | Persisted state |
| POST | `/v1/check` | Synchronous check |
| POST | `/v1/update` | Start asynchronous update job |
| POST | `/v1/fetch` | Start asynchronous verified-download job |
| POST | `/v1/rollback` | Start asynchronous rollback job |
| POST | `/v1/mark` | Record an externally installed version |
| GET | `/v1/jobs` | List jobs |
| GET | `/v1/jobs/{id}` | Job snapshot with events |
| POST | `/v1/jobs/{id}/cancel` | Cancel a running job |
| GET | `/v1/jobs/{id}/events` | SSE event stream |

Only one mutating job (`update`, `fetch`, `rollback`) can run at a time. A second
returns HTTP 409 with code `job_conflict`.

### Check

```http
POST /v1/check
Authorization: Bearer ...
Content-Type: application/json

{"current_version":"1.4.2"}
```

Returns the same result object used by the CLI (`status`, versions, notes,
asset). HTTP status reflects whether the check itself succeeded; `up_to_date` is
a 200, not an error.

### Update job

```http
POST /v1/update
Authorization: Bearer ...

{
  "current_version": "1.4.2",
  "force": false,
  "defer": false,
  "wait_pids": []
}
```

Returns HTTP 202:

```json
{
  "id": "9d3f...",
  "kind": "update",
  "status": "running",
  "created_at": "2026-10-28T12:00:00Z"
}
```

Poll `GET /v1/jobs/{id}`:

```json
{
  "id": "9d3f...",
  "kind": "update",
  "status": "succeeded",
  "result": {"status":"updated","previous_version":"1.4.2","current_version":"1.5.0"},
  "events": [
    {"type":"progress","phase":"download","bytes":100,"total":100,"percent":100},
    {"type":"log","phase":"install","message":"artifact staged"}
  ]
}
```

`status` is `running`, `succeeded`, `failed` or `cancelled`. Errors use
`error.code` and `error.message` with the same stable codes as the CLI.

### SSE events

```http
GET /v1/jobs/{id}/events
Authorization: Bearer ...
Accept: text/event-stream
```

Each event is an SSE `data:` frame containing the same JSON event object. The
job emits a terminal `result` or `error` event, then the stream closes after all
buffered events are delivered. The server polls internally every 250 ms.
Reconnect with `Last-Event-ID` to resume by index.

### Minimal Python HTTP client

```python
import json, urllib.request

def call(base, token, path, payload=None):
    req = urllib.request.Request(
        base + path,
        data=json.dumps(payload or {}).encode(),
        headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
        method="POST" if payload is not None else "GET",
    )
    with urllib.request.urlopen(req) as r:
        return json.load(r)
```

## Concurrency and lifetime

- The CLI is safe to call concurrently; the per-app file lock serializes writes.
- The HTTP API keeps jobs in memory and loses them on restart. Call `/v1/status`
  for durable state.
- Cancel is cooperative: it cancels the context, which stops downloads and
  source requests. A filesystem apply already in progress is short and may
  finish; rollback remains available.
