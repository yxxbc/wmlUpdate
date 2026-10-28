# WML Update

A small, secure, language-agnostic updater for **Windows, macOS and Linux**.

WML Update gives any application a complete update system without linking a
language-specific SDK. Run one static binary from your app, parse a stable JSON
line protocol, and get release discovery, verified downloads, transactional
installation, rollback, deferred Windows-safe replacement and an optional local
HTTP API.

[中文文档](README.zh-CN.md)

## Why

Application updaters are deceptively hard. They need to work across three
operating systems, survive interrupted downloads, reject tampered artifacts,
avoid zip-slip and symlink escapes, replace a running Windows executable, keep a
rollback path, and expose progress to a UI in whatever language the app is
written in.

WML Update packages those concerns into one auditable Go binary:

- **One dependency-free binary** for Windows, macOS and Linux (`amd64`/`arm64`).
- **GitHub Releases or self-hosted static manifests** as update sources.
- **Any language** via CLI + newline-delimited JSON, or the local HTTP API.
- **SHA-256 for every artifact**, optional **Ed25519 detached signatures** for
  manifests and checksum files, TLS enforcement and host allowlists.
- **Transactional directory/file replacement** with backups, crash recovery and
  `rollback`.
- **Safe archive handling** with path traversal, symlink escape, hardlink,
  special-file, entry-count and expansion-size defenses.
- **Resumable downloads** with bounded retries and `.part` files.
- **Deferred apply helper** for Windows, where the application cannot be
  replaced while it is running.
- **Self-hosting tools**: archive, hash, sign, manifest and checksums generation.

## Install

Download a release archive from
[GitHub Releases](https://github.com/yxxbc/wmlUpdate/releases), verify
`checksums.txt`, and put `wml-update` (or `wml-update.exe`) on `PATH`.

```bash
wml-update version
```

Build from source:

```bash
go install github.com/yxxbc/wmlUpdate/cmd/wml-update@latest
```

## Quick start: GitHub Releases

`updater.json`:

```json
{
  "$schema": "https://raw.githubusercontent.com/yxxbc/wmlUpdate/main/schema/config.schema.json",
  "schema_version": 1,
  "app_id": "com.example.myapp",
  "channel": "stable",
  "current_version": "1.0.0",
  "source": {
    "type": "github",
    "owner": "acme",
    "repo": "myapp",
    "tag_prefix": "v",
    "token_env": "GITHUB_TOKEN",
    "include_prerelease": false
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
    "preserve": ["data/**", "config/**"],
    "keep_backups": 1
  }
}
```

```bash
wml-update --config updater.json check
wml-update --config updater.json update --current-version 1.0.0
```

For GitHub releases, attach archive assets named exactly as your `asset_rules`
expect, plus a `checksums.txt` file. GitHub's asset `digest` is used when
present. Configure `security.public_key_file` and a `checksums.txt.sig` release
asset to require Ed25519-signed checksums.

## Quick start: self-hosted

Package, sign and publish:

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
    {"dir": "dist/MyApp", "url": "myapp-1.2.0-linux-amd64.tar.gz", "os": "linux", "arch": "amd64", "format": "tar.gz"},
    {"dir": "dist/MyApp", "url": "myapp-1.2.0-windows-amd64.zip", "os": "windows", "arch": "amd64", "format": "zip"}
  ]
}
```

Upload `manifest.json`, `manifest.json.sig` and the assets to any static host.
Your updater config points at the manifest:

```json
{
  "app_id": "com.example.myapp",
  "channel": "stable",
  "current_version": "1.0.0",
  "source": {
    "type": "static",
    "manifest_url": "https://updates.example.com/stable/manifest.json"
  },
  "install": {
    "type": "directory",
    "path": "/opt/myapp"
  },
  "security": {
    "public_key_file": "keys/signing-key.pub.pem"
  }
}
```

See [self-hosting](docs/self-hosting.md).

## Integrate from any language

Run the updater as a subprocess with `--json`. stdout is a stream of JSON
objects; logs and progress are data, never mixed with prose. `stderr` remains
available for diagnostics.

```bash
wml-update --config updater.json --json update --current-version 1.0.0
```

```json
{"type":"progress","phase":"download","bytes":1048576,"total":4194304,"percent":25}
{"type":"result","status":"updated","previous_version":"1.0.0","current_version":"1.1.0"}
```

Use `check` for a non-mutating check, `fetch` to only download a verified
artifact, and `mark` when your application installs an artifact itself. See
[integration](docs/integration.md), with examples in
[Python](examples/python), [Node.js](examples/node), [Go](examples/go) and
[shell](examples/shell).

For a long-lived desktop app, `wml-update serve` exposes a local, token-
authenticated HTTP API with asynchronous jobs and an SSE event stream.

## Common commands

| Command | Purpose |
| --- | --- |
| `check` | Discover the latest release; no download |
| `update` | Check, download, verify and install |
| `update --defer --wait-pid <pid>` | Stage now; apply from a detached helper after the app exits |
| `fetch --output app.zip` | Verified download only |
| `rollback` | Restore the last retained backup |
| `status` | Show installed version, backups and pending transactions |
| `serve` | Local HTTP API |
| `publish` | Build self-hosted manifest, checksums and signatures |
| `keygen` / `sign` / `verify` | Ed25519 release-signing tools |
| `doctor` | Diagnose config, permissions, state and source connectivity |

## Security defaults

- Downloads without a SHA-256 are refused. GitHub's API digest or a
  `checksums.txt` asset supplies the expected hash.
- With `security.public_key_file` configured, self-hosted manifests **must** have
  a valid detached signature, and GitHub releases must have signed checksums.
- HTTPS is required; plain HTTP is accepted only for loopback or with the
  explicit `security.allow_insecure_http` opt-in.
- Redirect targets are re-checked against the scheme, optional host allowlist
  and redirect count.
- Archives reject absolute paths, traversal, escaping symlinks, hardlinks,
  device files and excessive expansion.
- Directory installs are staged and swapped transactionally. The previous
  installation is retained for rollback; a journal repairs interrupted swaps.
- API tokens are required, and the server binds loopback unless
  `--allow-remote` is explicitly set.

Read the [security model](docs/security.md). The directory install strategy
manages the target directory: files not present in the new release and not
matched by `install.preserve` are removed. Point it at an application-managed
directory, not a user home directory.

## Documentation

- [Architecture](docs/architecture.md)
- [Configuration reference](docs/configuration.md)
- [Manifest format](docs/manifest.md)
- [Integrating any language](docs/integration.md)
- [Self-hosting updates](docs/self-hosting.md)
- [GitHub Releases](docs/github-releases.md)
- [Security model](docs/security.md)
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

MIT. See [LICENSE](LICENSE).
