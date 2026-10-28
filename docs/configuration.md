# Configuration reference

WML Update reads one JSON file. JSON is intentional: every language can generate
it, it has no parser ambiguity, and unknown fields are rejected so typos fail
loudly.

A JSON Schema is published at
[`schema/config.schema.json`](https://github.com/yxxbc/wmlUpdate/blob/main/schema/config.schema.json). Editors that support
`$schema` will autocomplete the file.

Go applications can either point the SDK at this file (`Options.ConfigPath`) or
build the equivalent [`wmlupdate.Config`](sdk.md) in code. Programmatic
durations use `time.Duration`; JSON uses duration strings such as `"15m"`.

## Discovery

The configuration path is selected in this order:

1. `--config PATH` / `-c PATH`
2. `WML_UPDATE_CONFIG` environment variable
3. `./updater.json`
4. `./.wml-update.json`
5. `./wml-update.json`

Commands that do not operate on an installation (`version`, `keygen`, `sign`,
`verify`, `publish`, the hidden `__apply`) do not require a config unless
`verify` needs a default public key.

## Minimal example

```json
{
  "schema_version": 1,
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
  }
}
```

## Top-level fields

| Field | Required | Default | Meaning |
| --- | --- | --- | --- |
| `$schema` | no | — | Editor schema hint; ignored by the updater |
| `schema_version` | no | `1` | Configuration schema version. Only `1` is accepted |
| `app_id` | yes | — | Stable identifier, `[A-Za-z0-9][A-Za-z0-9._-]{0,127}` |
| `channel` | no | `stable` | Logical channel / prerelease policy |
| `current_version` | no | state | Fallback current version |
| `source` | yes | — | Release discovery source |
| `asset_rules` | no | auto-detect | Platform asset selection rules |
| `install` | yes | — | Installation strategy |
| `state_dir` | no | platform default | State, cache, lock and download directory |
| `security` | no | secure defaults | Signatures, host allowlist, HTTP policy |
| `download` | no | see below | Size, timeout and retry limits |

### Current version precedence

The version used for comparison comes from:

1. `--current-version` on `check`, `update`, `fetch` and `doctor`
2. `installed_version` recorded in updater state
3. `current_version` in the configuration

If none is available, the command fails. Production applications should pass
their compiled version on every invocation. State is preferred over the static
config value so that repeated checks after an updater-driven installation do not
keep comparing against a stale declared version; after a manual installation use
`mark` or the flag. Config is the initial fallback.

Versions are strict [SemVer 2.0.0](https://semver.org/). A leading `v` is
accepted and removed. Build metadata is ignored for ordering.

### Channels

- Self-hosted: the manifest's `channel` must match the configured channel.
- GitHub: `stable` ignores releases flagged prerelease and versions with a
  SemVer prerelease suffix. Any other channel includes prereleases. Setting
  `source.include_prerelease` to `true` also includes them.
- `--channel` overrides the configured channel for one command.

## `source`

Exactly one top-level source type is used. A source can itself be a chain of
mirrors.

### GitHub

```json
{
  "type": "github",
  "owner": "acme",
  "repo": "myapp",
  "api_base": "https://api.github.com",
  "tag_prefix": "v",
  "token_env": "GITHUB_TOKEN",
  "include_prerelease": false
}
```

| Field | Required | Notes |
| --- | --- | --- |
| `owner`, `repo` | yes | Repository coordinates |
| `api_base` | no | GitHub Enterprise API base, default `https://api.github.com` |
| `tag_prefix` | no | Strip this before SemVer parsing, e.g. `v` or `myapp-v` |
| `token_env` | no | Environment variable with a token. If set but unset at runtime, the updater fails rather than silently hitting anonymous rate limits |
| `include_prerelease` | no | Include prerelease releases regardless of channel |

The provider reads up to 100 most recent releases, skips drafts, parses tags as
SemVer, and selects the highest eligible version. Asset digests supplied by the
GitHub API are used when no `checksums.txt` entry overrides them.

With a token configured, release assets are downloaded through the API asset
endpoint, which supports private repositories. Without a token, public
`browser_download_url`s are used.

### Static / self-hosted

```json
{
  "type": "static",
  "manifest_url": "https://updates.example.com/stable/manifest.json",
  "headers": {
    "Authorization": "Bearer ${UPDATE_SERVER_TOKEN}"
  }
}
```

| Field | Required | Notes |
| --- | --- | --- |
| `manifest_url` | one of | URL of `manifest.json` |
| `manifest_urls` | one of | Ordered mirror URLs of the same manifest. The first usable mirror wins; relative assets are also resolved against the other bases for download failover |
| `headers` | no | Extra request headers for the manifest, signature and same-host assets |
| `asset_mirrors` | no | Asset URL templates tried when the primary asset host fails |

When `security.public_key` or `security.public_key_file` is configured, the
detached signature is fetched from `<manifest_url>.sig` and verified against the
exact manifest bytes before parsing. A missing or invalid signature is fatal.

Headers are forwarded to assets only when the asset has the same host (including
port) as the manifest. This prevents an authenticated manifest request from
leaking credentials to a third-party CDN.

### Chain / automatic failover

```json
{
  "type": "chain",
  "sources": [
    {
      "type": "github",
      "owner": "acme",
      "repo": "myapp",
      "tag_prefix": "v",
      "token_env": "GITHUB_TOKEN",
      "asset_mirrors": [
        "https://ghproxy.net/https://github.com/{owner}/{repo}/releases/download/{tag}/{asset}",
        "https://gitee.com/{owner}/{repo}/releases/download/{tag}/{asset}"
      ]
    },
    {
      "type": "static",
      "manifest_url": "https://cdn.jsdelivr.net/gh/acme/myapp@main/updates/manifest.json"
    },
    {
      "type": "static",
      "manifest_url": "https://gitee.com/acme/myapp/raw/main/updates/manifest.json"
    },
    {
      "type": "static",
      "manifest_url": "https://updates.example.com/stable/manifest.json"
    }
  ]
}
```

`chain.sources` is ordered. The updater fetches metadata from each source until
one returns a valid, authenticated release; later sources are fallbacks. A
failure in one mirror does not abort the chain.

If metadata succeeds but the artifact download fails, the loader tries:

1. the asset's own URL;
2. `asset_mirrors` from the winning source;
3. for relative static assets, the same path on the other `manifest_urls`;
4. releases for the same version from the other chain sources.

Every fallback artifact must match the original checked SHA-256. A mirror can
provide availability, never a different version or different bytes.

Mirror templates support:

| Placeholder | Value |
| --- | --- |
| `{url}` | Original absolute asset URL |
| `{owner}`, `{repo}` | GitHub coordinates (empty for static sources) |
| `{tag}` | Release tag, path-escaped |
| `{version}` | Normalized version, path-escaped |
| `{asset}`, `{name}` | Asset filename, path-escaped |
| `{os}`, `{arch}` | Target platform |

Nested chains are rejected. Keep the first source authoritative and mirrors in
sync; because metadata selection is first-success, a reachable but stale first
mirror will be used rather than a newer fallback.

### Asset mirrors

`asset_mirrors` can be used without a chain, for example to keep GitHub Releases
as the metadata source while downloading through a proxy:

```json
{
  "type": "github",
  "owner": "acme",
  "repo": "myapp",
  "asset_mirrors": [
    "https://ghproxy.net/https://github.com/{owner}/{repo}/releases/download/{tag}/{asset}",
    "https://updates.example.com/{version}/{asset}"
  ]
}
```

Mirrors receive no source `headers`/token. Credentials are only sent to the
asset URL's original host (and same-host fallbacks).

## `asset_rules`

GitHub assets have no platform metadata, and self-hosted manifests may omit it.
Rules provide deterministic selection.

```json
"asset_rules": [
  {
    "os": "darwin",
    "arch": "arm64",
    "match": "myapp-{version}-macos-arm64.zip",
    "format": "zip"
  },
  {
    "os": "windows",
    "arch": "amd64",
    "match": "myapp-{version}-windows-amd64.zip",
    "format": "zip"
  }
]
```

Rules are evaluated in order. A rule applies when its `os`/`arch` are empty or
match the target platform. `match` is a Go `path.Match` glob applied to the
asset name after replacing:

| Placeholder | Value |
| --- | --- |
| `{version}` | Normalized SemVer without `v`, e.g. `1.2.3` |
| `{tag}` | Raw source tag, e.g. `v1.2.3` |
| `{os}` | Normalized OS: `windows`, `darwin`, `linux` |
| `{arch}` | Normalized architecture: `amd64`, `arm64`, ... |

If a rule matches multiple assets, the command fails rather than guessing.
`format` overrides the format inferred from the filename.

If no rule matches, selection proceeds as follows:

1. A self-hosted manifest asset whose `os` and `arch` fields exactly match.
2. A single architecture-neutral asset for the OS.
3. A conservative filename heuristic (for example `myapp-1.2.3-macos-arm64.zip`).
4. Otherwise, an error listing available assets.

Explicit rules are strongly recommended for production.

Supported formats: `zip`, `tar.gz`, `tar`, `gz`, `raw`, `bin`, `exe`, `msi`,
`pkg`, `dmg`.

## `install`

### `directory`

Replaces an entire application directory with the contents of a verified
archive.

```json
{
  "type": "directory",
  "path": "/opt/myapp",
  "strip_components": 1,
  "preserve": ["data/**", "config/**"],
  "keep_backups": 1,
  "relaunch_path": "/opt/myapp/bin/myapp",
  "relaunch_args": []
}
```

| Field | Default | Meaning |
| --- | --- | --- |
| `path` | — | Target directory |
| `paths` | — | Per-OS or per-OS-arch overrides |
| `strip_components` | `0` | Leading archive path components to remove |
| `preserve` | `[]` | Relative doublestar globs copied from the old install into the new tree |
| `keep_backups` | `1` | Number of previous installations retained (0–10; 0 becomes 1 for replace strategies) |
| `relaunch_path` | — | Executable started after a successful apply |
| `relaunch_args` | `[]` | Arguments for relaunch; placeholders are expanded |

The archive must be zip, tar or tar.gz. It is extracted into a sibling staging
directory, preserved paths are copied in, and then the old directory is renamed
to a backup and the staged directory is renamed into place. Both renames happen
on the same filesystem.

**The target directory is managed as a unit.** Files present in the old install
but absent from the new archive are removed unless a `preserve` pattern matches
them. Symlinked targets are rejected. The filesystem root, the user home
directory, and any path overlapping the state directory are rejected.

`preserve` patterns use doublestar syntax (`*`, `**`, `?`, character classes).
A matched directory preserves its whole subtree. Preserved content wins over
content from the new archive at the same path.

Directory replacement preserves file modes from the archive and the target
root's mode, but it does not copy ownership, ACLs, xattrs or resource forks from
the previous tree. If those matter, create the install directory with the
appropriate owner/ACL and use the `command` strategy with a platform package
installer, or reapply metadata in a post-install step.

### macOS quarantine and ad-hoc signing

macOS may mark files as quarantined or report a modified bundle as damaged.
Optional directory post-install handling is available:

```json
{
  "type": "directory",
  "path": "/Applications/MyApp.app",
  "macos": {
    "remove_quarantine": true,
    "ad_hoc_sign": false,
    "allow_signer_change": false
  }
}
```

| Field | Default | Meaning |
| --- | --- | --- |
| `remove_quarantine` | `false` | Run `xattr -dr com.apple.quarantine` on the installed target. This bypasses Gatekeeper's first-launch evaluation |
| `ad_hoc_sign` | `false` | Run `codesign --force --deep --sign -` on a `.app` target |
| `allow_signer_change` | `false` | Permit replacing a signed `.app` with a bundle from a different Developer ID team |

Both default off. `remove_quarantine` weakens a macOS security boundary and
should only be enabled for updates whose source and SHA-256/Ed25519 trust you
control. `ad_hoc_sign` **destroys Developer ID signatures and notarization**;
use it only for unsigned app bundles, never for a notarized distribution. A
quarantine-removal command failure is a warning; ad-hoc signing failure rolls
back the directory swap. These actions only run when the updater itself is
running on macOS.

When the installed target is a signed `.app`, the staged app must pass
`codesign --verify --deep --strict` and have the same `TeamIdentifier` as the
installed app. Set `allow_signer_change` only for an intentional Developer ID
migration. Ad-hoc signing bypasses continuity because it deliberately replaces
the signer.

### `file`

Replaces a single file, typically a standalone executable.

```json
{
  "type": "file",
  "path": "C:\\Program Files\\MyApp\\myapp.exe",
  "file_mode": "0755",
  "keep_backups": 1
}
```

The new file is staged in the target directory, permissions are set from
`file_mode` (or preserved from the existing file), and the old file is renamed
to a backup before an atomic rename. The downloaded artifact is used as-is.

### `command`

Runs a platform installer after verification.

```json
{
  "type": "command",
  "command": "msiexec.exe",
  "args": ["/i", "{artifact}", "/qn", "/norestart"],
  "timeout": "10m",
  "relaunch_path": "C:\\Program Files\\MyApp\\myapp.exe"
}
```

Placeholders:

| Placeholder | Value |
| --- | --- |
| `{artifact}` | Absolute path to the verified downloaded artifact |
| `{install_dir}` / `{target}` | Resolved install path (empty if none) |
| `{version}` | New version |
| `{previous_version}` | Current version |
| `{state_dir}` | State directory |

`command`, arguments and environment values are expanded. The command is executed
directly, not through a shell. Its combined stdout/stderr (up to 1 MiB) is
reported. A non-zero exit or timeout fails the update. The installer owns
rollback semantics; WML Update cannot roll back an arbitrary system installer.

### `external`

Downloads and verifies the artifact, then returns its path. The application
installs it itself.

```json
{"type": "external"}
```

After a successful custom install, call:

```bash
wml-update mark --version 1.2.0 --sha256 <artifact-sha256>
```

`update` with `external` returns status `downloaded`. `fetch` is the
non-installing equivalent.

### Path selection

`install.paths` selects a path by target platform:

```json
"paths": {
  "windows": "C:\\Program Files\\MyApp",
  "darwin": "/Applications/MyApp.app",
  "linux": "/opt/myapp",
  "linux-arm64": "/opt/myapp"
}
```

The exact `os-arch` key wins, then the `os` key, then `path`.

### Command placeholders and relaunch

After a successful install, `relaunch_path` is started detached with
`relaunch_args`. A relaunch failure is a warning: the update itself succeeded.
The process is not waited for.

## `security`

```json
{
  "public_key": "",
  "public_key_file": "keys/signing-key.pub.pem",
  "checksums_asset": "checksums.txt",
  "checksums_signature_asset": "checksums.txt.sig",
  "allowed_hosts": ["api.github.com", "github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com"],
  "allow_insecure_http": false,
  "allow_unverified_assets": false
}
```

| Field | Default | Meaning |
| --- | --- | --- |
| `public_key` | — | Inline PEM or base64 raw Ed25519 public key |
| `public_key_file` | — | Public key file, relative to this config |
| `checksums_asset` | `checksums.txt` | GitHub release checksum asset name |
| `checksums_signature_asset` | `<checksums_asset>.sig` | GitHub checksum signature asset |
| `allowed_hosts` | `[]` | Optional allowlist for every request and redirect |
| `allow_insecure_http` | `false` | Permit plain HTTP to non-loopback hosts |
| `allow_unverified_assets` | `false` | Permit downloads with no known SHA-256 (dangerous) |

Set only one of `public_key` and `public_key_file`.

When a public key is configured:

- Self-hosted sources require a valid `<manifest_url>.sig`.
- GitHub sources require both checksum and signature assets. The signature is
  verified over the exact checksum bytes before hashes are trusted.

`allowed_hosts` accepts exact hosts, hosts with ports, and wildcard subdomains
such as `*.example.com`. It also applies to redirects. For GitHub downloads you
must include the asset redirect hosts you use.

`allow_unverified_assets` is a last-resort escape hatch. It prints a warning and
records the computed SHA-256, but it cannot authenticate what was downloaded.

## `download`

```json
{
  "max_bytes": 2147483648,
  "timeout": "30m",
  "retries": 3,
  "proxy": "http://127.0.0.1:7890"
}
```

| Field | Default | Meaning |
| --- | --- | --- |
| `max_bytes` | 2 GiB | Maximum artifact size and extraction-expansion budget |
| `timeout` | `30m` | Overall HTTP client timeout |
| `retries` | `3` | Retries for transient source/download failures (0–10) |
| `proxy` | env | Explicit HTTP, HTTPS, SOCKS5 or SOCKS5H proxy. Overrides `HTTP_PROXY`/`HTTPS_PROXY`. Supports `${ENV_VAR}` |

Retries honor `Retry-After` for 429/503 responses. Downloads resume from a
`.part` file using HTTP Range requests. A final file only appears after size and
SHA-256 verification.

A configured proxy applies to metadata, signatures, artifacts and redirects.
Proxy credentials are not logged. An explicit proxy is trusted configuration;
the proxy can observe destination hosts and URLs.

## Environment expansion and paths

`${NAME}` and `$NAME` are expanded in selected configuration fields. Referencing
an unset variable is an error, not a silent empty string. Expansion applies to:

- `current_version`
- `source.manifest_url`
- `source.headers` values
- `security.public_key_file`
- `state_dir`
- `install.path`, `install.paths`, `install.workdir`, `install.relaunch_path`
- `install.env` values

A leading `~` and `~/` in paths are expanded to the current home directory.
Relative paths resolve against the configuration file's directory, not the
process working directory.

Command-line `--os` and `--arch` override the target platform for selection and
path resolution. This is useful for testing cross-platform packages.

## Default state directories

| OS | Default |
| --- | --- |
| Windows | `%LOCALAPPDATA%\wml-update\<app_id>` |
| macOS | `~/Library/Application Support/wml-update/<app_id>` |
| Linux/BSD | `$XDG_STATE_HOME/wml-update/<app_id>` or `~/.local/state/wml-update/<app_id>` |

The state directory contains `state.json`, the update lock, source metadata
cache, partial/final downloads, helper plans and backups (backups themselves are
siblings of the install target). It is created with owner-only permissions.
