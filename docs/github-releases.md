# GitHub Releases

GitHub releases are the fastest way to ship updates. The updater works with
public repositories out of the box and with private repositories when a token is
configured.

## Release naming

Use a SemVer tag, optionally with a prefix:

```text
v1.5.0
myapp-v1.5.0
```

Set `source.tag_prefix` when there is a prefix. A leading `v` is accepted even
without a prefix setting.

Attach one asset per platform/architecture. Either name them so the built-in
heuristic can find them:

```text
myapp-1.5.0-macos-arm64.zip
myapp-1.5.0-macos-amd64.zip
myapp-1.5.0-windows-amd64.zip
myapp-1.5.0-linux-amd64.tar.gz
myapp-1.5.0-linux-arm64.tar.gz
```

or, preferably, declare explicit rules:

```json
"asset_rules": [
  {"os": "darwin",  "arch": "arm64", "match": "myapp-{version}-macos-arm64.zip", "format": "zip"},
  {"os": "darwin",  "arch": "amd64", "match": "myapp-{version}-macos-amd64.zip", "format": "zip"},
  {"os": "windows", "arch": "amd64", "match": "myapp-{version}-windows-amd64.zip", "format": "zip"},
  {"os": "linux",   "arch": "amd64", "match": "myapp-{version}-linux-amd64.tar.gz", "format": "tar.gz"}
]
```

If multiple assets match for the target platform, the updater fails rather than
guessing.

## Hashes

A GitHub release should include a `checksums.txt` asset in GNU format:

```text
<sha256>  myapp-1.5.0-linux-amd64.tar.gz
<sha256>  myapp-1.5.0-macos-arm64.zip
<sha256>  myapp-1.5.0-windows-amd64.zip
```

The `publish` command writes this file alongside a manifest:

```bash
wml-update publish --spec publish.json --out dist --key signing-key.pem
```

Upload `dist/checksums.txt` to the release. The updater also consumes GitHub API
`digest` fields when present. If neither a checksum entry nor a digest exists
for the selected asset, the update is refused.

### Signed checksums (recommended)

```bash
wml-update publish --spec publish.json --out dist --key signing-key.pem
```

Upload both:

- `checksums.txt`
- `checksums.txt.sig`

Then configure:

```json
"security": {
  "public_key_file": "keys/signing-key.pub.pem",
  "checksums_asset": "checksums.txt",
  "checksums_signature_asset": "checksums.txt.sig"
}
```

When a public key is configured, a missing or invalid signature is fatal. The
signature covers the exact checksum file bytes. Individual artifact signatures
are unnecessary because the signed checksum file authenticates every hash.

Do not attach `manifest.json` to a GitHub release unless you want it as a
separate convenience; GitHub sources read release assets directly and ignore
self-hosted manifests.

## Channels and prereleases

- Configured channel `stable`: skip releases with GitHub's prerelease flag and
  tags containing SemVer prereleases (`v1.5.0-rc.1`).
- Any other channel, or `source.include_prerelease: true`: include both stable
  and prerelease builds, selecting the highest SemVer.
- Draft releases are always skipped.

Tags that are not valid SemVer are skipped, not coerced.

## Private repositories

Create a fine-grained or classic token with read access to releases for that
repository. Do not put the token in the JSON file:

```json
"source": {
  "type": "github",
  "owner": "acme",
  "repo": "private-app",
  "token_env": "GITHUB_TOKEN"
}
```

At runtime, `GITHUB_TOKEN` must be present. If it is configured but missing, the
updater fails clearly instead of silently falling back to anonymous rate limits.

Asset downloads go through the GitHub API asset endpoint with
`Accept: application/octet-stream`, which supports private release assets. Go's
HTTP client strips the Authorization header when GitHub redirects to signed
storage, so the token is not leaked to the CDN.

For public repositories, a token is optional but strongly recommended to avoid
the 60 requests/hour anonymous limit. The updater surfaces rate-limit errors with
guidance.

## Network allowlist

If you set `security.allowed_hosts`, include every host involved in metadata and
redirects. A typical set is:

```json
"allowed_hosts": [
  "api.github.com",
  "github.com",
  "objects.githubusercontent.com",
  "release-assets.githubusercontent.com"
]
```

The exact asset redirect host can vary. If you do not need an allowlist, leave
it empty. It is still protected by HTTPS and signature/hash verification.

For GitHub Enterprise, set `source.api_base` to your instance's API base and
include that host in `allowed_hosts`.

## CI example

```yaml
name: release
on:
  push:
    tags: ["v*"]

permissions:
  contents: write

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Build artifacts
        run: ./scripts/build-release.sh   # your cross-build
      - name: Generate signed checksums
        env:
          WML_SIGNING_KEY: ${{ secrets.WML_SIGNING_KEY }}
        run: |
          umask 077
          printf '%s' "$WML_SIGNING_KEY" > "$RUNNER_TEMP/key.pem"
          wml-update publish --spec publish.json --out dist --key "$RUNNER_TEMP/key.pem"
          rm -f "$RUNNER_TEMP/key.pem"
      - name: Create release
        env:
          GH_TOKEN: ${{ github.token }}
        run: |
          gh release create "$GITHUB_REF_NAME" dist/* \
            --title "$GITHUB_REF_NAME" \
            --notes-file release-notes.md
```

`publish` signs `checksums.txt` even if you only use the GitHub source, so the
same build output works for self-hosting later.

## Updating WML Update itself

The project's release workflow builds one archive per platform, each containing
`wml-update`, `LICENSE` and `README.md`. A self-update config can install the
archive into a dedicated tool directory:

```json
{
  "app_id": "dev.wml.update",
  "channel": "stable",
  "source": {
    "type": "github",
    "owner": "yxxbc",
    "repo": "wmlUpdate",
    "tag_prefix": "v"
  },
  "asset_rules": [
    {"os": "linux", "arch": "amd64", "match": "wml-update_{version}_linux_amd64.tar.gz", "format": "tar.gz"},
    {"os": "darwin", "arch": "arm64", "match": "wml-update_{version}_darwin_arm64.tar.gz", "format": "tar.gz"},
    {"os": "windows", "arch": "amd64", "match": "wml-update_{version}_windows_amd64.zip", "format": "zip"}
  ],
  "install": {
    "type": "directory",
    "paths": {
      "linux": "/opt/wml-update",
      "darwin": "/usr/local/opt/wml-update",
      "windows": "C:\\Tools\\wml-update"
    },
    "strip_components": 0
  }
}
```

The target must be a directory dedicated to the updater; directory installs
replace the whole tree. On Windows, run with `--defer --wait-pid <pid>` because
the updater binary itself may be running.

For a single raw binary, publish a raw artifact (not a zip/tar archive) and use:

```json
"install": {"type": "file", "path": "/home/me/.local/bin/wml-update"}
```

The `file` strategy installs the downloaded artifact as-is; it does not unpack
archives.

## Limitations

- The provider reads the 100 most recent releases. If the newest eligible
  release is older than that in a burst of non-SemVer tags, it may not be seen.
- GitHub's API is paginated; 100 is the maximum page size used.
- GitHub releases have no custom channels beyond prerelease/stable. Use tags,
  separate repositories, or self-hosted manifests for arbitrary channels.
