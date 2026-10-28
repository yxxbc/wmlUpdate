<p align="center">
  <img src="assets/logo.png" width="120" alt="WML Update">
</p>

# WML Update

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue.svg" alt="Apache-2.0"></a>
  <a href="https://yxxbc.github.io/wmlUpdate/"><img src="https://img.shields.io/badge/docs-online-0891b2.svg" alt="Docs"></a>
</p>

A lightweight update **library** for Go applications, with a release CLI and an
optional sidecar for other languages.

Embed it as a dependency and expose only two operations to your app:

```go
update, err := updater.Check(ctx)
if update.Available {
    result, err := updater.Apply(ctx, update, wmlupdate.ApplyOptions{})
}
```

No daemon. No framework. No global registration. No required application
architecture. The host app keeps control of its UI, lifecycle and restart.

[中文文档](README.zh-CN.md)

## Why a library

Application updaters are deceptively hard: interrupted downloads, tampered
artifacts, three filesystem models, running Windows executables, rollback, crash
recovery, signatures and progress reporting. WML Update packages those concerns
behind a deliberately small Go API:

```go
u, err := wmlupdate.New(wmlupdate.Options{
    ConfigPath:     "updater.json",
    CurrentVersion: appVersion,
})
if err != nil { return err }
defer u.Close()

update, err := u.Check(ctx)
if err != nil || !update.Available { return err }

result, err := u.Apply(ctx, update, wmlupdate.ApplyOptions{})
```

`Check` is read-only. `Apply` installs the exact checked release, downloading and
verifying the artifact. Progress is an optional callback. The terminal result is
the return value.

The same engine is exposed as:

- a **release CLI** for packaging, signing, manifests and checksums;
- an optional **sidecar CLI + NDJSON + HTTP API** for languages that cannot import
  a Go library.

## Features

- **Go library** with a two-call public API: `Check` and `Apply`.
- **Non-intrusive** — no daemon, service, UI toolkit, event loop or app
  lifecycle integration.
- **Windows, macOS and Linux**, `amd64` and `arm64`, using one CGO-free binary
  for the optional sidecar.
- **GitHub Releases or self-hosted static manifests**, with an ordered **source
  chain** and same-version/SHA-256 mirror failover.
- **SHA-256 for every artifact**, optional **Ed25519 detached signatures**,
  TLS enforcement and host allowlists.
- **Transactional directory/file installs** with backups, crash recovery and
  rollback data.
- **Safe archive extraction**: traversal, symlink escape, hardlink, special-file,
  entry-count and expansion-size defenses.
- **Resumable downloads** with bounded retries, explicit proxy support and
  `.part` files.
- **Deferred apply** for Windows when the application is still running, plus
  optional macOS quarantine removal and ad-hoc signing.
- **Release tooling**: archive, hash, sign, manifest and checksum generation.

## Install

```bash
go get github.com/yxxbc/wmlUpdate
```

```go
import wmlupdate "github.com/yxxbc/wmlUpdate"
```

For the CLI, download a verified release archive or build it from source:

```bash
go install github.com/yxxbc/wmlUpdate/cmd/wml-update@latest
```

Release checksums are signed with the project's Ed25519 key. Verify them with
`signing/release-signing-key.pub.pem`; see
[signing/README.md](signing/README.md).

## Configuration

The library accepts either a complete `updater.json` or a programmatic
`wmlupdate.Config`. A minimal GitHub configuration:

```json
{
  "schema_version": 1,
  "app_id": "com.example.myapp",
  "channel": "stable",
  "source": {
    "type": "github",
    "owner": "acme",
    "repo": "myapp",
    "tag_prefix": "v",
    "token_env": "GITHUB_TOKEN"
  },
  "asset_rules": [
    {"os": "darwin", "arch": "arm64", "match": "myapp-{version}-macos-arm64.zip", "format": "zip"},
    {"os": "windows", "arch": "amd64", "match": "myapp-{version}-windows-amd64.zip", "format": "zip"},
    {"os": "linux", "arch": "amd64", "match": "myapp-{version}-linux-amd64.tar.gz", "format": "tar.gz"}
  ],
  "install": {
    "type": "directory",
    "paths": {
      "darwin": "/Applications/MyApp.app",
      "windows": "C:\\Program Files\\MyApp",
      "linux": "/opt/myapp"
    },
    "preserve": ["data/**", "config/**"]
  }
}
```

Or configure entirely in Go:

```go
u, err := wmlupdate.New(wmlupdate.Options{
    CurrentVersion: appVersion,
    Config: &wmlupdate.Config{
        AppID: "com.example.myapp",
        Source: wmlupdate.SourceConfig{
            Type: wmlupdate.SourceGitHub, Owner: "acme", Repo: "myapp",
        },
        Install: wmlupdate.InstallConfig{
            Type: wmlupdate.InstallDirectory, Path: "/opt/myapp",
        },
    },
})
```

See the [SDK guide](docs/sdk.md) and [configuration reference](docs/configuration.md).

## Integrate in Go

```go
u, err := wmlupdate.New(wmlupdate.Options{
    ConfigPath:     "updater.json",
    CurrentVersion: appVersion,
    Events: func(e wmlupdate.Event) {
        if e.Type == wmlupdate.EventProgress {
            reportProgress(e.Percent)
        }
    },
})
if err != nil { return err }
defer u.Close()

update, err := u.Check(ctx)
if err != nil { return err }
if !update.Available {
    return nil
}

result, err := u.Apply(ctx, update, wmlupdate.ApplyOptions{})
if err != nil { return err }
log.Printf("updated %s -> %s", result.PreviousVersion, result.Version)
```

On Windows, when the target is running:

```go
result, err := u.Apply(ctx, update, wmlupdate.ApplyOptions{
    Defer:    true,
    WaitPIDs: []int{os.Getpid()},
})
// StatusApplyScheduled: exit the app and let the detached helper finish.
```

The library does not call `os.Exit` or restart your process. That remains your
application's decision.

## Packaging and releases

The CLI includes a release workflow for GitHub Releases or any static host:

```bash
wml-update keygen --out-dir keys
wml-update publish --spec publish.json --out public/stable --key keys/signing-key.pem
```

`publish.json`:

```json
{
  "app_id": "com.example.myapp",
  "version": "1.2.0",
  "channel": "stable",
  "notes": "Fixes and improvements",
  "assets": [
    {"dir": "dist/app-linux", "url": "myapp-1.2.0-linux-amd64.tar.gz", "os": "linux", "arch": "amd64", "format": "tar.gz"},
    {"dir": "dist/app-windows", "url": "myapp-1.2.0-windows-amd64.zip", "os": "windows", "arch": "amd64", "format": "zip"}
  ]
}
```

Outputs include `manifest.json`, `manifest.json.sig`, `checksums.txt` and
`checksums.txt.sig`. Upload the assets first, then the manifest. See
[self-hosting](docs/self-hosting.md) and [GitHub Releases](docs/github-releases.md).

## Other languages

A Go library cannot be imported directly by Python, Node.js, Java or C#. Those
applications use the optional sidecar adapter:

```bash
wml-update --config updater.json --json update --current-version 1.0.0
```

stdout is one JSON object per line; stderr carries diagnostics. The same engine
is also available through:

```bash
wml-update --config updater.json serve --json
```

which provides a token-authenticated loopback HTTP API with jobs and SSE. See
[integration](docs/integration.md) and examples for
[Python](examples/python), [Node.js](examples/node), [C#](examples/csharp),
[shell](examples/shell), the [Go SDK example](examples/go) and the
[Go CLI example](examples/go-cli).

## CLI commands

The CLI is optional for Go applications. It contains the release tool and the
sidecar adapter.

| Command | Purpose |
| --- | --- |
| `publish` | Build self-hosted manifest, checksums and signatures |
| `keygen` / `sign` / `verify` | Ed25519 release-signing tools |
| `check` / `update` | Sidecar check and apply; `--json` for NDJSON |
| `update --defer --wait-pid <pid>` | Deferred apply for a running target |
| `fetch --output app.zip` | Verified download only |
| `rollback` / `status` / `doctor` | Operational recovery and diagnostics |
| `serve` | Local HTTP integration API |

## Security defaults

- Downloads without a SHA-256 are refused. GitHub's API digest or a
  `checksums.txt` asset supplies the expected hash.
- With `security.public_key_file` configured, self-hosted manifests **must** have
  a valid detached signature, and GitHub releases must have signed checksums.
- HTTPS is required; plain HTTP is accepted only for loopback or with an explicit
  `security.allow_insecure_http` opt-in.
- Redirect targets are re-checked against the scheme, optional host allowlist and
  redirect count.
- Archives reject absolute paths, traversal, escaping symlinks, hardlinks,
  device files and excessive expansion.
- Directory installs are staged and swapped transactionally. The previous
  installation is retained for rollback; a journal repairs interrupted swaps.
- The HTTP adapter requires a token and binds loopback unless `--allow-remote` is
  explicitly set.

Read the [security model](docs/security.md). The directory strategy manages the
target directory as a unit: files not present in the new release and not matched
by `install.preserve` are removed. Point it at an application-managed directory,
not a user home directory.

## Documentation

**Online documentation: <https://yxxbc.github.io/wmlUpdate/>**

- [Go SDK](docs/sdk.md)
- [Architecture](docs/architecture.md)
- [Configuration reference](docs/configuration.md)
- [Versioning and compatibility](docs/versioning.md)
- [Manifest format](docs/manifest.md)
- [Integration and sidecar protocol](docs/integration.md)
- [Self-hosting updates](docs/self-hosting.md)
- [GitHub Releases](docs/github-releases.md)
- [Security model](docs/security.md)
- [Testing](docs/testing.md)
- [Troubleshooting](docs/troubleshooting.md)
- [Architecture decision records](docs/adr)

## Development

Requires Go 1.24+.

```bash
make test
make check
make build
```

Cross-compilation is CGO-free:

```bash
GOOS=windows GOARCH=amd64 go build ./cmd/wml-update
GOOS=darwin  GOARCH=arm64 go build ./cmd/wml-update
GOOS=linux   GOARCH=amd64 go build ./cmd/wml-update
```

## License

Licensed under the [Apache License, Version 2.0](LICENSE). See [NOTICE](NOTICE)
for attribution. The WML Update name and logo are project marks; the Apache-2.0
license does not grant trademark rights or permission to imply endorsement.
