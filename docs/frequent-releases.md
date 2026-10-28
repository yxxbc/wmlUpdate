# Frequent releases

WML Update is designed for channels that publish often, including several
builds per day. The library itself does not schedule checks or impose a release
cadence; the host application decides when to check, and the update feed is just
static metadata with hashes and optional signatures.

This model follows the approach used by fast-moving desktop apps: a dedicated
feed, immutable version numbers, background staging, and a deliberate restart.

## This project's own nightly channel

The repository includes `.github/workflows/nightly.yml`. On its daily schedule
(or a manual `workflow_dispatch`) it:

1. runs the test suite;
2. derives the next minor from the newest reachable stable tag, e.g. `v1.0.0`
   becomes base `v1.1.0`;
3. tags the current `main` as
   `v1.1.0-nightly.YYYYMMDD.<run-number>.<attempt>`;
4. builds and publishes a GitHub prerelease with the normal archives, packages,
   checksums and (when configured) `checksums.txt.sig`.

Nightly tags are valid SemVer prereleases. Stable clients ignore them; a nightly
client opts in explicitly. The docs workflow does not publish a docs version for
nightly tags; `latest` remains the newest stable release.

The workflow does not create a new stable release or move `latest`. Stable
releases stay deliberate, annotated tags.

## Publish a dedicated feed, not a GitHub API call per check

Use a static manifest (or a small dynamic endpoint serving the same shape) for
the hot path:

```text
https://updates.example.com/nightly/manifest.json
https://updates.example.com/nightly/myapp-1.8.1341-linux-amd64.tar.gz
```

```json
{
  "schema_version": 1,
  "app_id": "com.example.myapp",
  "channel": "nightly",
  "version": "1.8.1341",
  "published_at": "2026-10-28T08:00:00Z",
  "notes": "Nightly build",
  "assets": [
    {
      "name": "myapp-1.8.1341-linux-amd64.tar.gz",
      "os": "linux",
      "arch": "amd64",
      "url": "myapp-1.8.1341-linux-amd64.tar.gz",
      "sha256": "<64 hex characters>",
      "size": 12345678,
      "format": "tar.gz"
    }
  ]
}
```

A manifest fetch is small, cacheable with ETag/Last-Modified, and sidesteps
GitHub API rate limits. Mirror it through a `source.type: chain` for availability.

## Version every build uniquely

- Use a monotonic SemVer patch/build counter, e.g. `1.8.1341` → `1.8.1342`.
- Do **not** rely on build metadata (`1.8.1341+sha`) to distinguish versions;
  SemVer ignores build metadata in ordering.
- Pre-releases use `1.9.0-beta.3`, not an arbitrary suffix.
- The manifest version must match the asset content. Never reuse a version with
  different bytes.

The updater refuses downgrades by default and uses the exact SHA-256 from the
checked manifest.

## Keep channels separate

Publish separate manifests for stable, beta and nightly:

```text
https://updates.example.com/stable/manifest.json
https://updates.example.com/beta/manifest.json
https://updates.example.com/nightly/manifest.json
```

The manifest `channel` must match the client's configured channel. "Stable"
policy (weekly promotion, holdbacks, urgent releases) belongs in your release
service; WML Update only consumes the selected feed.

## Check on the host's schedule

The SDK has no background loop by design. A host can check at startup and then
every N minutes:

```go
ticker := time.NewTicker(6 * time.Hour)
defer ticker.Stop()

for {
    select {
    case <-ctx.Done():
        return
    case <-ticker.C:
        update, err := u.Check(ctx)
        if err != nil || !update.Available {
            continue
        }
        // Stage now. The helper installs when the application exits and retries
        // the relaunch, so a long-running app does not need to restart now.
        if _, err := u.Apply(ctx, update, wmlupdate.ApplyOptions{
            Defer:    true,
            WaitPIDs: []int{os.Getpid()},
        }); err != nil {
            log.Printf("update staging failed [%s]: %v", wmlupdate.Code(err), err)
        }
        return
    }
}
```

Call the deferred apply once per checked version. If the app stays alive, the
helper waits; a later check that stages another version would create another
helper, so the host should either exit or avoid scheduling a second one until
the first is consumed.

For applications that own their installation, use `install.type=external` and
call `Apply`; the result contains the verified artifact path.

## Stage safely

- Download and verify before touching the live tree.
- Keep the previous backup for rollback.
- On Windows, use `ApplyOptions{Defer: true, WaitPIDs: ...}`; the detached helper
  waits for the app, retries transient file-sharing failures, and can relaunch.
- On macOS, verify signer continuity for signed `.app` bundles; optionally remove
  quarantine or ad-hoc sign only for unsigned distributions.
- Do not auto-remove quarantine on a channel you do not control.

## Make availability independent of one host

```json
{
  "source": {
    "type": "chain",
    "sources": [
      {
        "type": "static",
        "manifest_urls": [
          "https://cdn.jsdelivr.net/gh/acme/myapp@main/updates/nightly/manifest.json",
          "https://gitee.com/acme/myapp/raw/main/updates/nightly/manifest.json",
          "https://updates.example.com/nightly/manifest.json"
        ]
      }
    ]
  }
}
```

Downloads try the asset URL, `asset_mirrors`, other manifest bases, and
same-version chain sources. A fallback must match the checked SHA-256.

## Publication order

1. Upload versioned assets.
2. Upload `manifest.json.sig`.
3. Upload `manifest.json` last.

Keep old manifests and assets available for clients that are mid-update and for
operational rollback. Clients with long update intervals should pin their
channel manifest separately.

## Avoid making every check download

The SDK separates discovery from installation:

- `Check` fetches metadata only.
- `Apply` downloads and stages, or installs immediately.
- A checksum-addressed download cache makes repeated attempts idempotent.

Do not treat an update check as a request to restart. Let the host decide when
the user is ready; the deferred helper handles the eventual exit.

## What this borrows from fast updaters

The design adopts the operational lessons of high-frequency desktop updaters:

- a dedicated feed separate from the code host;
- immutable, unique versions;
- mirror and proxy support with hash-bound fallback;
- background staging and a restart deferred to the host;
- Windows file-sharing and antivirus retries;
- macOS signer continuity and relaunch retries;
- old-version retention for rollback.

It deliberately leaves scheduling, UI prompts and restart policy to the
embedding application.
