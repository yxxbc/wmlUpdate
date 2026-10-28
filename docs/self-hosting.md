# Self-hosting updates

A self-hosted channel is just static files. No update server process is needed.
This makes it easy to put behind nginx, Caddy, S3/R2, GitHub Pages, a CDN, or an
internal object store.

## Layout

```text
/var/www/updates/myapp/stable/
├── manifest.json
├── manifest.json.sig
├── checksums.txt
├── checksums.txt.sig
├── myapp-1.5.0-darwin-arm64.zip
├── myapp-1.5.0-linux-amd64.tar.gz
└── myapp-1.5.0-windows-amd64.zip
```

Clients fetch only `manifest.json` and one matching asset. `checksums.txt` is
useful for GitHub releases and manual verification; static clients trust the
hashes inside the signed manifest.

## 1. Generate signing keys once

```bash
wml-update keygen --out-dir release-keys
```

Keep `signing-key.pem` in a secret manager / protected CI secret. Commit or
distribute `signing-key.pub.pem`, or embed its base64 raw form in client config.
The private key is 32 bytes of authority over every client channel: do not put it
in the web root or a repository.

Key rotation: distribute a new client release that trusts the new public key
before switching the channel to the new key. A client supports one public key.

## 2. Build platform artifacts

Use your normal build pipeline. Requirements:

- Each asset is a zip, tar.gz, tar, or raw file supported by your install
  strategy.
- Asset names are stable and understood by `asset_rules`, or the manifest
  carries `os` and `arch`.
- Archives contain the exact directory tree the install strategy expects.
- macOS `.app` bundles are signed and notarized **before** archiving. Replacing
  files inside an existing bundle invalidates its code signature.
- Windows executables are Authenticode-signed before archiving if you rely on
  SmartScreen/enterprise trust.
- `install.strip_components` matches whether the archive has a top-level folder.

`publish` can create archives from directories:

```json
{
  "app_id": "com.example.myapp",
  "version": "1.5.0",
  "channel": "stable",
  "notes_file": "release-notes.md",
  "assets": [
    {"dir": "build/myapp-linux", "url": "myapp-1.5.0-linux-amd64.tar.gz", "os": "linux", "arch": "amd64", "format": "tar.gz"},
    {"dir": "build/myapp-macos", "url": "myapp-1.5.0-darwin-arm64.zip", "os": "darwin", "arch": "arm64", "format": "zip"},
    {"dir": "build/myapp-windows", "url": "myapp-1.5.0-windows-amd64.zip", "os": "windows", "arch": "amd64", "format": "zip"}
  ]
}
```

`dir` archives the directory's **contents**. If your install expects a wrapper
folder, pass a parent directory or set `install.strip_components` accordingly.
Raw installers use `path` plus `format`.

## 3. Publish and sign

```bash
wml-update publish \
  --spec publish.json \
  --out dist/stable \
  --key release-keys/signing-key.pem \
  --base-url https://updates.example.com/myapp/stable
```

Outputs are deterministic enough to verify:

```bash
wml-update verify --manifest \
  --public-key-file release-keys/signing-key.pub.pem \
  dist/stable/manifest.json
```

`--base-url` is optional. Without it, asset URLs are relative to the manifest,
which is preferable when the host or CDN domain may change.

## 4. Upload atomically

Order matters:

1. Upload new versioned assets.
2. Upload `manifest.json` and `manifest.json.sig` last.
3. Keep the previous version's files available.

A client may fetch the manifest at any instant. Publishing the manifest before
its assets creates a window where every update fails. Some object stores allow
copy-and-swap or versioned objects; if not, uploading assets first is sufficient.

Deploy manifests with a short cache lifetime, for example `Cache-Control:
no-cache` or `max-age=60, must-revalidate`. Versioned assets can be immutable:

```http
Cache-Control: public, max-age=31536000, immutable
```

The updater honors ETag/Last-Modified and uses conditional requests.

## 5. Serve it

### nginx

```nginx
server {
    listen 443 ssl http2;
    server_name updates.example.com;

    root /var/www/updates;

    location = /myapp/stable/manifest.json {
        add_header Cache-Control "no-cache, must-revalidate";
        types { }
        default_type application/json;
    }

    location = /myapp/stable/manifest.json.sig {
        add_header Cache-Control "no-cache, must-revalidate";
        default_type text/plain;
    }

    location ~* \.(zip|tar\.gz|exe|msi|pkg|dmg)$ {
        add_header Cache-Control "public, max-age=31536000, immutable";
    }

    # Optional basic auth for a private channel. The client supports
    # source.headers; see "Private channels" below.
    # auth_basic "updates";
    # auth_basic_user_file /etc/nginx/updates.htpasswd;
}
```

### Caddy

```caddyfile
updates.example.com {
    root * /var/www/updates
    file_server
    header /myapp/stable/manifest.json Cache-Control "no-cache, must-revalidate"
    header /myapp/stable/manifest.json.sig Cache-Control "no-cache, must-revalidate"
}
```

## 6. Client configuration

```json
{
  "app_id": "com.example.myapp",
  "channel": "stable",
  "source": {
    "type": "static",
    "manifest_url": "https://updates.example.com/myapp/stable/manifest.json"
  },
  "asset_rules": [
    {"os": "linux", "arch": "amd64", "match": "myapp-*-linux-amd64.tar.gz", "format": "tar.gz"},
    {"os": "darwin", "arch": "arm64", "match": "myapp-*-darwin-arm64.zip", "format": "zip"},
    {"os": "windows", "arch": "amd64", "match": "myapp-*-windows-amd64.zip", "format": "zip"}
  ],
  "install": {
    "type": "directory",
    "paths": {
      "linux": "/opt/myapp",
      "darwin": "/Applications/MyApp.app",
      "windows": "C:\\Program Files\\MyApp"
    },
    "keep_backups": 1
  },
  "security": {
    "public_key_file": "keys/signing-key.pub.pem",
    "allowed_hosts": ["updates.example.com", "cdn.example.com"]
  }
}
```

If assets are moved to a CDN host, include that host in `allowed_hosts` and make
sure the signed manifest's asset URLs resolve there.

## Private channels

Static hosts can require a bearer token or basic auth:

```json
"source": {
  "type": "static",
  "manifest_url": "https://updates.example.com/private/stable/manifest.json",
  "headers": {
    "Authorization": "Bearer ${UPDATE_TOKEN}"
  }
}
```

`${UPDATE_TOKEN}` must exist at runtime. The updater errors rather than sending a
silently empty token. Headers are sent to the manifest and same-host assets only.
If your assets are on a different host, use signed URLs generated by the manifest
tooling or a CDN that does not require the secret.

Do not embed long-lived credentials in a distributed client if the channel is
public. Prefer a short-lived token issued by your backend, supplied through the
environment.

## Mirrors and failover

A single static host is a single point of failure. Deploy the same manifest and
assets to multiple hosts and configure either:

- `source.manifest_urls` with ordered URLs for the same static channel;
- a `source.type: chain` containing GitHub, static mirrors, jsDelivr, ghproxy,
  Gitee and your own site;
- `asset_mirrors` URL templates for artifact hosts.

Fallback downloads must match the originally checked SHA-256. Mirrors cannot
change the version or bytes. See
[configuration](configuration.md#chain-automatic-failover).

## Multiple channels

Use separate directories, manifests and client channel values:

```text
stable/manifest.json
beta/manifest.json
nightly/manifest.json
```

The client enforces that the manifest's `channel` matches the configured
channel. GitHub channels are derived from prerelease flags instead.

## Rollback at the service level

Clients roll back with `wml-update rollback` using the local backup. To roll
back a release for all clients, publish the previous version as a **new higher
version** rather than republishing an old version number. SemVer comparison
means clients already on the broken version will not downgrade to an older
version number.

For an emergency, ship `1.5.1` containing the previous code.

## Signing in CI

```yaml
- name: Sign and publish
  env:
    WML_SIGNING_KEY: ${{ secrets.WML_SIGNING_KEY }}
  run: |
    umask 077
    printf '%s' "$WML_SIGNING_KEY" > "$RUNNER_TEMP/signing-key.pem"
    wml-update publish \
      --spec publish.json \
      --out dist/stable \
      --key "$RUNNER_TEMP/signing-key.pem"
    rm -f "$RUNNER_TEMP/signing-key.pem"
```

Upload the output with your cloud provider's CLI. Keep the public key in the
repository or client config, never the private key.

## Verification checklist

- `manifest.json.sig` validates with the shipped public key.
- Every asset's `sha256` and `size` match what was built.
- Asset URLs are reachable by clients and are in `allowed_hosts`.
- `channel` matches.
- The install target is writable by the updating user.
- The archive's top-level layout matches `strip_components`.
- macOS bundle signatures remain valid after update.
- A test update succeeds on all three platforms, including rollback.
