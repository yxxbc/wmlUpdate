# Self-hosted manifest format

A self-hosted update channel is a static directory containing:

```text
manifest.json
manifest.json.sig        # required when a public key is configured
checksums.txt            # generated for GitHub releases; optional for static hosts
checksums.txt.sig        # optional signed checksums
myapp-1.2.0-linux-amd64.tar.gz
myapp-1.2.0-windows-amd64.zip
...
```

The updater fetches `manifest.json`, verifies the detached signature when a
public key is configured, validates the manifest, selects the asset for the
running platform, and uses the manifest's SHA-256 to verify the download.

JSON Schema: [`schema/manifest.schema.json`](https://github.com/yxxbc/wmlUpdate/blob/main/schema/manifest.schema.json).

## Example

```json
{
  "schema_version": 1,
  "app_id": "com.example.myapp",
  "channel": "stable",
  "version": "1.2.0",
  "published_at": "2026-10-28T12:00:00Z",
  "notes": "Fixes and performance improvements.",
  "mandatory": false,
  "assets": [
    {
      "name": "myapp-1.2.0-macos-arm64.zip",
      "os": "darwin",
      "arch": "arm64",
      "url": "myapp-1.2.0-macos-arm64.zip",
      "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
      "size": 18432000,
      "format": "zip"
    }
  ]
}
```

## Fields

| Field | Required | Notes |
| --- | --- | --- |
| `schema_version` | yes | Must be `1` |
| `app_id` | yes | Must equal the updater configuration's `app_id` |
| `channel` | yes | Must equal the configured channel (case-insensitive) |
| `version` | yes | Strict SemVer 2.0.0, with optional leading `v` |
| `published_at` | no | RFC 3339 timestamp |
| `notes` | no | Release notes, at most 1 MiB |
| `mandatory` | no | Passed through to the client UI; the CLI does not force installation |
| `assets` | yes | 1–256 entries |

### Asset fields

| Field | Required | Notes |
| --- | --- | --- |
| `url` | yes | Absolute, or relative to `manifest.json` |
| `sha256` | yes | 64 hexadecimal characters |
| `name` | no | Defaults to the URL basename; used by `asset_rules` and checksums |
| `os` | no | Go-style OS (`windows`, `darwin`, `linux`) or common aliases |
| `arch` | no | Go-style architecture (`amd64`, `arm64`, ...) or common aliases |
| `size` | no | Byte size; when present, enforced before and after download |
| `format` | no | `zip`, `tar.gz`, `tar`, `gz`, `raw`, `exe`, `msi`, `pkg`, `dmg` |

Relative URLs resolve against the manifest URL. Asset metadata is used when no
`asset_rules` match. `asset_rules` always take priority.

Unknown fields are rejected.

## Signing

Signatures are detached Ed25519 signatures over the **exact bytes** of the
file. The signature file is standard-base64 text (raw 64-byte signatures are
also accepted when verifying).

```bash
wml-update keygen --out-dir keys
wml-update publish --spec publish.json --out public/stable --key keys/signing-key.pem
wml-update verify --manifest --public-key-file keys/signing-key.pub.pem public/stable/manifest.json
```

Outputs:

- `manifest.json`
- `manifest.json.sig`
- `checksums.txt` (GNU `sha256sum` format, sorted by asset name)
- `checksums.txt.sig` when signing is enabled

The public key goes into the client configuration:

```json
"security": { "public_key_file": "keys/signing-key.pub.pem" }
```

or as an inline base64 raw key:

```json
"security": { "public_key": "base64-encoded-32-byte-key" }
```

The client fetches `<manifest_url>.sig`. A manifest with a missing, malformed or
invalid signature is rejected before any JSON parsing or asset URL resolution.
Treat the private key as a release credential: store it in a secret manager or
CI protected secret, never in the web root.

## Publishing

`publish` reads a spec relative to the spec file and writes into `--out`:

```bash
wml-update publish \
  --spec publish.json \
  --out public/stable \
  --key ../keys/signing-key.pem \
  --base-url https://updates.example.com/stable
```

Spec:

```json
{
  "app_id": "com.example.myapp",
  "version": "1.2.0",
  "channel": "stable",
  "notes_file": "CHANGELOG.md",
  "assets": [
    {
      "dir": "dist/MyApp",
      "url": "myapp-1.2.0-linux-amd64.tar.gz",
      "os": "linux",
      "arch": "amd64",
      "format": "tar.gz"
    },
    {
      "path": "dist/MyApp.exe",
      "url": "myapp-1.2.0-windows-amd64.exe",
      "os": "windows",
      "arch": "amd64",
      "format": "raw"
    }
  ]
}
```

Each asset sets exactly one of `dir` (archive it) or `path` (copy the prebuilt
file). `url` is the relative output path and the default manifest asset name.
`name` overrides the manifest name. `base_url` (or `--base-url`) prepends a
public URL to every asset URL; without it, URLs remain relative to the manifest.

If a signing key is supplied, `checksums.txt.sig` is also written. GitHub
releases can upload `checksums.txt` and `checksums.txt.sig` directly.

## Deployment notes

- Upload artifacts first, then `manifest.json`, then `manifest.json.sig`. Never
  expose a manifest that references bytes not yet present.
- Use a short cache lifetime for `manifest.json` and immutable, long-lived cache
  headers for versioned assets.
- The updater uses ETag / Last-Modified conditional requests for manifests.
- Keep previous manifests and artifacts for rollback of the update service.
- Use TLS. Plain HTTP is only accepted for loopback or with an explicit client
  opt-in.
- If assets live on a different host and require credentials, do not put those
  credentials in `source.headers`: headers are only forwarded to the manifest's
  own host. Use a public CDN or a host that does not require the secret.
