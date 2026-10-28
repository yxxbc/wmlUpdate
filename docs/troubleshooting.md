# Troubleshooting

Start with:

```bash
wml-update --config updater.json doctor --current-version 1.2.3
wml-update --config updater.json status
```

`doctor` checks config parsing, state directory writability, install parent,
target symlinks, signing key loading, pending transactions and source
connectivity.

## No configuration file found

The updater looks for `updater.json`, `.wml-update.json` and
`wml-update.json` in the current directory, or `WML_UPDATE_CONFIG`. Relative
paths inside the config resolve against the config file, not the shell's current
directory. Prefer an absolute `--config` path from a desktop app.

## `current version is unknown`

Pass the application's compiled version:

```bash
wml-update update --current-version 1.2.3
```

`current_version` in JSON and the recorded state are fallbacks. A stale state
file can cause repeated or missed updates; the flag is authoritative.

## GitHub API rate limit

Set `source.token_env` and provide a token at runtime:

```bash
export GITHUB_TOKEN=github_pat_...
wml-update check
```

The error mentions the rate limit. If `token_env` is set but missing, the updater
fails rather than proceeding anonymously. Token scopes: public repositories need
public read; private repositories need release read access.

## `no_asset`: no matching release asset

The error lists available asset names. Add an explicit `asset_rules` entry:

```json
{"os": "linux", "arch": "amd64", "match": "myapp-{version}-linux-x86_64.tar.gz", "format": "tar.gz"}
```

Check:

- `{version}` has no leading `v`; use `{tag}` if your filename includes it.
- Asset names are case-sensitive for `path.Match`.
- The release is not a draft.
- The release is not flagged prerelease when channel is `stable`.
- `--os` / `--arch` are not overriding the platform unexpectedly.

## `no_release`: nothing eligible

- Tag is valid strict SemVer after `tag_prefix` is removed.
- Stable channel excludes prereleases.
- Draft releases are always skipped.
- The repository has releases, not only tags.

Use `--channel beta` or `source.include_prerelease: true` for prereleases.

## Signature failures

```text
manifest signature: signature verification failed
```

- The public key must match the private key that signed `manifest.json`.
- The signature must cover the exact bytes served. Re-run `wml-update publish`;
  editing `manifest.json` after signing invalidates it.
- For GitHub, both `checksums.txt` and `checksums.txt.sig` must be release assets
  with the configured names.
- A `.sig` file may be base64 text or raw 64 bytes; both are accepted.
- Verify manually:

```bash
wml-update verify --manifest --public-key-file key.pub manifest.json
```

## Checksum mismatch

The manifest or checksum file expects different bytes than the server returned.
Common causes:

- Asset overwritten after publishing
- CDN serving stale/manipulated content
- Signed manifest points at an old filename with new bytes
- Partial upload

Republish artifacts, then manifest. Clear `.part` files if you suspect a stale
resume:

```bash
find "$STATE_DIR/downloads" -name '*.part' -delete
```

A truncated download reports `unexpected EOF` as an integrity error and retries
from scratch.

## HTTP refused / insecure URL

Plain HTTP to a non-loopback host is refused. Use HTTPS. For isolated test
networks only:

```json
"security": { "allow_insecure_http": true }
```

## Host allowlist rejected

Add every host used, including redirect targets:

```json
"allowed_hosts": [
  "api.github.com",
  "github.com",
  "objects.githubusercontent.com",
  "release-assets.githubusercontent.com",
  "updates.example.com"
]
```

For GitHub, the exact asset redirect host may differ. Check the error message,
which names the rejected host.

## Directory install fails: file in use / access denied (Windows)

Close the application and update, or use the deferred flow:

```bash
wml-update update --defer --wait-pid <PID> --json
```

The application should exit after it receives `apply_scheduled`. A detached
helper performs the replacement and can relaunch via `install.relaunch_path`.

If it still fails, another process (antivirus, DLL host, crash reporter) holds a
file. Check the target directory with Process Explorer or close the locking
process.

## Permission denied

The updater user must be able to:

- create staging and backup directories next to the install target,
- rename the target,
- write inside the target.

`/Applications`, `/opt` and `C:\Program Files` commonly require elevation.
Prefer per-user install locations. If elevation is unavoidable, run the updater
elevated and protect its config/public key from unprivileged modification.

## `another update is already running`

A lock exists at `<state_dir>/update.lock`. Find the other updater process or
wait. Locks are advisory and released when the process exits; a stale file is
harmless.

If the lock holder crashed during an install, the next mutating command recovers
the journal automatically. Check `wml-update status` for `Pending install`.

## Pending transaction after a crash

The next `update` or `rollback` runs recovery while holding the lock:

- An interrupted swap restores the previous version.
- A swap that completed before the journal update is finalized.
- A command installer journal cannot be verified and is cleared with a warning.

If recovery itself fails, inspect `state.json` and the sibling
`.wml-update-backup-*` directory. Do not delete a backup until you know which
version is live. A manual restore is:

```text
rename target -> target.failed
rename .wml-update-backup-<app>-<id> -> target
```

Run `wml-update status` before and after.

## No backup available for rollback

- The install was a `command` or `external` install (no backup is created).
- The previous update already rolled back.
- The backup directory was removed manually.

## mac app fails Gatekeeper after update

Replacing files changes a macOS bundle. The new bundle must be signed and
notarized before packaging. WML Update does not re-sign or strip quarantine.
Build, sign, notarize, staple, then archive. Verify with:

```bash
codesign --verify --deep --strict /Applications/MyApp.app
spctl --assess --type execute /Applications/MyApp.app
```

## Command installer failed

Run with `--json` and inspect the log event containing captured output. The
command is not executed through a shell, so pipes/redirection do not work.
Wrap complex logic in a script and point `install.command` at it.

Check placeholders:

- `{artifact}` is the verified downloaded file.
- `{install_dir}` may be empty for command installs without a configured path.
- Use the platform-appropriate installer, for example
  `msiexec.exe /i {artifact} /qn` on Windows.

## Local API returns 401

- Use `Authorization: Bearer <token>` or `X-WML-Token: <token>`.
- A generated token is printed only in the `serve --json` ready line.
- If you use `--token-env`, the variable must be set in the server's environment.
- Tokens under 16 characters are rejected.

## Local API returns 409 `job_conflict`

Only one `update`, `fetch` or `rollback` job may run at a time. Poll the current
job or cancel it before starting another.

## SSE stream is empty

The stream closes when the job has finished and all events have been delivered.
Connect while the job is running, or poll `GET /v1/jobs/{id}` for the complete
snapshot. Reconnect with `Last-Event-ID` to resume.

## State file is corrupt

The updater fails rather than silently resetting. Inspect the file. If no
transaction is pending and the recorded version is no longer useful, stop the
app, back up the file, then remove it; the next run recreates state. Never delete
state while `pending` is set without first inspecting the target and backup.

## Debugging with `--json`

JSON mode is the best source of truth:

```bash
wml-update --config updater.json --json update --current-version 1.0.0 2>stderr.log
```

Every progress/log/warning is a JSON line on stdout; `stderr.log` contains any
human diagnostics. The terminal `error` object has a stable code suitable for
support tickets.
