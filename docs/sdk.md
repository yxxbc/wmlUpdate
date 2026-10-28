# Go SDK

[API reference on pkg.go.dev](https://pkg.go.dev/github.com/yxxbc/wmlUpdate){ .md-button }

The Go package is the primary integration surface. It is a library, not a
framework: no daemon, no required directory layout, no lifecycle hooks and no
global state. Your application owns startup, UI, process exit and restart.

```go
import wmlupdate "github.com/yxxbc/wmlUpdate"
```

## Design contract

The public API is intentionally two operations:

```go
update, err := u.Check(ctx)     // discover and select a release
result, err := u.Apply(ctx, update, wmlupdate.ApplyOptions{})
```

`Check` performs no writes to the application tree. `Apply` downloads, verifies
and installs a previously checked release. Progress is optional through a
callback; the terminal outcome is the return value.

The library also uses a small state directory for locks, downloads, backups and
recovery. That is internal bookkeeping, not a requirement on your application
structure.

## Minimal integration

```go
package main

import (
    "context"
    "fmt"
    "os"

    wmlupdate "github.com/yxxbc/wmlUpdate"
)

func checkForUpdates(ctx context.Context) error {
    u, err := wmlupdate.New(wmlupdate.Options{
        ConfigPath:     "updater.json",
        CurrentVersion: appVersion,
        Events: func(e wmlupdate.Event) {
            if e.Type == wmlupdate.EventProgress && e.Phase == "download" {
                fmt.Printf("\r%d%%", int(e.Percent))
            }
        },
    })
    if err != nil {
        return err
    }
    defer u.Close()

    update, err := u.Check(ctx)
    if err != nil {
        return err
    }
    if !update.Available {
        return nil
    }

    result, err := u.Apply(ctx, update, wmlupdate.ApplyOptions{})
    if err != nil {
        return err
    }
    fmt.Printf("\ninstalled %s (was %s)\n", result.Version, result.PreviousVersion)
    return nil
}
```

That is the whole integration. Your UI decides whether to prompt, when to
check, and when to restart.

## Configuration

### From updater.json

`Options.ConfigPath` loads the same configuration format used by the CLI,
including environment expansion, signature keys and all install strategies.

```go
u, err := wmlupdate.New(wmlupdate.Options{
    ConfigPath:     "/path/to/updater.json",
    CurrentVersion: "1.4.2",
})
```

### Programmatic

For a dependency-only setup, avoid shipping a separate config file:

```go
u, err := wmlupdate.New(wmlupdate.Options{
    CurrentVersion: "1.4.2",
    BaseDir:        appDataDir, // resolves relative install/key paths
    Config: &wmlupdate.Config{
        AppID:   "com.example.myapp",
        Channel: "stable",
        Source: wmlupdate.SourceConfig{
            Type:  wmlupdate.SourceGitHub,
            Owner: "acme",
            Repo:  "myapp",
        },
        AssetRules: []wmlupdate.AssetRule{
            {OS: "darwin", Arch: "arm64", Match: "myapp-{version}-macos-arm64.zip", Format: "zip"},
            {OS: "windows", Arch: "amd64", Match: "myapp-{version}-windows-amd64.zip", Format: "zip"},
            {OS: "linux", Arch: "amd64", Match: "myapp-{version}-linux-amd64.tar.gz", Format: "tar.gz"},
        },
        Install: wmlupdate.InstallConfig{
            Type:    wmlupdate.InstallDirectory,
            Paths:   map[string]string{
                "darwin":  "/Applications/MyApp.app",
                "windows": `C:\Program Files\MyApp`,
                "linux":   "/opt/myapp",
            },
            Preserve: []string{"data/**", "config/**"},
        },
    },
})
```

Programmatic durations use `time.Duration`; use `ConfigPath` for the JSON string
form. `BaseDir` defaults to the process working directory for relative paths.

### Chains, mirrors, proxy and macOS

`SourceConfig.Sources` supports ordered chains. `SourceConfig.ManifestURLs`,
`SourceConfig.AssetMirrors` and `DownloadConfig.Proxy` expose the same failover
and network options as the JSON configuration. `InstallConfig.MacOS` controls
optional quarantine removal, ad-hoc signing and signer-change policy. See the
[configuration reference](configuration.md) for semantics and security
trade-offs.

### Current version

Pass the version compiled into the running application:

```go
CurrentVersion: buildinfo.Version
```

It is the only reliable source. If omitted, the SDK falls back to recorded
updater state and then `Config.CurrentVersion`. After an in-place successful `Apply`, the
Updater updates its internal current version so the next `Check` is correct.

## Events

`Options.Events` receives:

| Type | Meaning |
| --- | --- |
| `EventLog` | Phase transition or diagnostic |
| `EventWarning` | Non-fatal security or recovery warning |
| `EventProgress` | Download bytes/total/percent |

The callback runs synchronously on the updater's goroutine. Marshal UI updates
to your own event loop if necessary. No callback is required.

## Applying

```go
result, err := u.Apply(ctx, update, wmlupdate.ApplyOptions{})
```

Result statuses:

| Status | Meaning |
| --- | --- |
| `StatusUpdated` | Installed in-process |
| `StatusDownloaded` | `install.type=external`; artifact path returned |
| `StatusApplyScheduled` | Deferred helper scheduled |

### Windows / running executable

Use deferred apply when the target is running or when the updater is replacing
itself:

```go
result, err := u.Apply(ctx, update, wmlupdate.ApplyOptions{
    Defer:    true,
    WaitPIDs: []int{os.Getpid()},
})
```

The call stages the update and starts a detached helper, then returns
`StatusApplyScheduled`. The host should exit promptly. The helper waits for the
listed PIDs, applies the journaled transaction and optionally relaunches via
`Config.Install.RelaunchPath`.

### External installation

With `InstallExternal`, `Apply` downloads and verifies the artifact and returns
`ArtifactPath`; your application installs it. This keeps custom native
installers, package managers or platform-specific logic outside the library.

## Errors

Use `Code` for stable branching:

```go
if err != nil {
    switch wmlupdate.Code(err) {
    case "no_update":
    case "integrity":
    case "source", "network":
    case "install":
    }
}
```

`errors.Is(err, wmlupdate.ErrNoUpdate)` also works. Codes match the CLI/API
protocol: `config`, `source`, `network`, `no_release`, `no_asset`, `integrity`,
`install`, `rollback`, `locked`, `state`, `io`, `cancelled`, `no_update`.

## Lifetime and concurrency

- `New` opens the state directory and configures an HTTP client.
- `Close` releases idle HTTP connections. It does not cancel in-flight calls.
- `Check` is read-only and safe to call concurrently.
- `Apply` takes a per-application file lock; mutating calls are serialized.
- An `Update` is an opaque checked snapshot. Call `Check` and `Apply` in the same
  process; do not serialize it or construct it manually.
- `Apply` does not exit or restart the host. That remains your decision.
- No daemon, service, web server or global registration is required.

## Rollback

The library retains a backup according to `Install.KeepBackups`. The optional
CLI exposes:

```bash
wml-update --config updater.json rollback
```

The SDK intentionally does not expose rollback in its minimal surface; use the
CLI for operational recovery or keep an `install.type=external` path under your
own control.

## Other languages

A Go library cannot be loaded directly by Python, Node.js or Java. Those
applications use the same engine through the sidecar CLI's NDJSON protocol or
the loopback HTTP API. See [integration](integration.md). This is an adapter, not
a requirement for Go applications.
