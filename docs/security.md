# Security model

## Scope

WML Update authenticates update metadata, verifies artifact bytes, checks network
boundaries, extracts archives safely and replaces installations transactionally.
It does not sandbox the application being updated, sign the new application, or
protect a host that is already compromised.

## Trust boundaries

| Input | Trust |
| --- | --- |
| `updater.json` and CLI flags | Trusted local configuration |
| Environment values referenced by config | Trusted local secrets |
| Update source (HTTPS / GitHub API) | Untrusted until authenticated |
| Manifest contents | Untrusted before signature verification |
| Archive entries | Untrusted |
| Download bytes | Untrusted until SHA-256 verification |
| Local API caller | Untrusted until bearer token check |

## Supply chain

### Metadata

**Self-hosted.** With `security.public_key_file` (or inline `public_key`), the
client downloads `<manifest_url>.sig` and verifies an Ed25519 signature over the
exact raw manifest bytes before parsing. Missing or invalid signature is fatal.
Without a configured key, metadata relies on TLS and is explicitly reported in
documentation as less strong.

**GitHub.** Release metadata comes from the GitHub API over TLS with an optional
access token. For artifact hashes the client uses, in order:

1. A signed `checksums.txt` when a public key is configured (required).
2. A `checksums.txt` asset when present.
3. The GitHub API asset `digest` field.
4. Nothing → the update is refused unless `allow_unverified_assets` is set.

### Artifacts

Every download requires a 64-hex SHA-256. The final file is not exposed until:

- the expected byte size (when supplied) matches,
- the SHA-256 matches,
- the file is fsynced and atomically renamed from `.part`.

Hash mismatch deletes the partial file and retries within the configured budget.
A checksum failure is reported as an `integrity` error.

### Signing keys

`keygen` writes a PKCS#8 Ed25519 private key with mode `0600` and a PKIX public
key. The public key can be distributed inline as base64 raw bytes. The private
key is a release credential:

- keep it in a secret manager or protected CI secret,
- never commit it,
- never serve it from the update host,
- rotate by shipping a client that trusts the new key before switching the
  channel key,
- use a dedicated key per update channel if channels have different trust.

The signing format is detached Ed25519 over raw file bytes, signature encoded as
standard base64 (raw 64-byte signatures are also accepted). This is easy to
reproduce with OpenSSL or a small script and avoids canonical-JSON pitfalls.

## Network protections

- HTTPS only, except loopback or explicit `security.allow_insecure_http`.
- Redirects are checked against scheme, allowlist and a 10-hop limit.
- `security.allowed_hosts` applies to metadata, signatures, assets and every
  redirect target.
- Authorization headers are not forwarded by Go's HTTP client across host
  changes. Self-hosted config headers are attached to assets only when the asset
  host exactly matches the manifest host.
- Source-chain fallback URLs and asset mirrors never receive the winning
  source's credentials. A fallback download must match the checked SHA-256.
- Response metadata bodies are size-limited (4 MiB manifests, 32 MiB GitHub
  response, 16 MiB checksums).
- Downloads are bounded by `download.max_bytes` and verified sizes.
- Retries are bounded and honor `Retry-After`.
- Proxy settings follow Go's standard `HTTP_PROXY` / `HTTPS_PROXY` environment
  unless `download.proxy` explicitly overrides them. A trusted proxy can observe
  destination hosts and URLs; it cannot substitute bytes because SHA-256 is still
  enforced.

## Filesystem protections

### Archive extraction

- absolute POSIX paths and Windows volume/UNC paths rejected,
- `..` traversal rejected after normalization and after `strip_components`,
- entries with NUL bytes rejected,
- symlink targets must remain inside the extraction root,
- writing through a symlinked parent is rejected (prevents symlink-first attacks),
- hardlinks, devices, FIFOs and other special files rejected,
- entry-count and expanded-size limits,
- setuid/setgid bits are not applied,
- files are created before restrictive directory modes are applied.

Symlinks are supported because macOS `.app` bundles require them. They are
validated, not blindly trusted.

### Install target

Directory and file installs reject:

- filesystem roots,
- the user's home directory itself,
- targets that are symlinks,
- targets inside the state directory,
- state directories inside the target,
- staging/backup paths outside the target's parent.

This is defense in depth against a typo or malicious config. Configuration is
trusted, but an updater with root privileges is a dangerous tool.

### Transactional behavior

The old installation is renamed to a backup before the staged tree is activated.
The journal is written before each rename. Recovery restores the previous tree
when a transaction was interrupted before the new tree was active.

`command` installers are intentionally outside this guarantee. They run with the
updater's privileges and their rollback behavior belongs to the installer.

### macOS quarantine and ad-hoc signing

`install.macos.remove_quarantine` runs `xattr -dr com.apple.quarantine` on the
installed target. This bypasses Gatekeeper's first-launch evaluation and weakens
a user security boundary; enable it only when the update channel, signature and
hashes are under your control.

`install.macos.ad_hoc_sign` runs `codesign --force --deep --sign -` on a `.app`.
Ad-hoc signing can make an unsigned bundle launch, but it **invalidates Developer
ID signatures and notarization**. Never enable it for a notarized distribution.
Both options default off and only run when the updater runs on macOS.

For a signed installed `.app`, the updater verifies the staged bundle with
`codesign --verify --deep --strict` and requires the same `TeamIdentifier`.
`allow_signer_change` exists for an intentional certificate/team migration; do
not use it to bypass an unexpected signer change.

### Windows helper

`--defer` copies the updater executable to the state directory, not the target.
The helper waits for selected PIDs, acquires the per-app lock, applies from the
journal and writes a result file. This avoids executing a binary from inside the
tree being replaced.

## Local HTTP API

- Bearer token required on every endpoint except `/v1/health`.
- Constant-time token comparison.
- Loopback bind by default; non-loopback binds require an explicit flag.
- Body size limited to 1 MiB, server read/header timeouts set.
- Unknown JSON fields rejected.
- No CORS headers are added.
- No token is logged. A generated token is printed once on the ready line and is
  not stored by the server.
- Jobs keep only the last 512 events.
- Remote deployment should use a TLS reverse proxy and an additional
  authentication layer.

The API can install software and write files as the updater user. Treat the
token as equivalent to that user's update authority.

## Privileges

Do not run the updater as root/system unless the install target requires it.
If elevated installation is necessary, keep the updater and its state owned by
the privileged account, protect the config and signing public key from
unprivileged modification, and ensure the install target is not user-writable.
A user who can modify `updater.json`, the public key, or the install target can
redirect or replace updates.

## Residual risks

- A compromised signing key allows malicious releases until the key is rotated.
- TLS-only self-hosted channels without a public key are vulnerable to a
  compromised host or CA.
- `allow_unverified_assets` removes artifact authentication.
- `allow_insecure_http` exposes metadata and artifacts to network attackers.
- A malicious local user with write access to the install target or state
  directory can subvert the updater.
- Replacing an application directory intentionally deletes files not preserved.
- macOS code signing and Windows Authenticode are not performed; archives must be
  signed before packaging.
- Command installers can do anything the updater user can do.
- The local API has no per-endpoint authorization or rate limiting beyond the
  loopback default.
- Source mirrors can deny service or serve different bytes; they cannot bypass
  the selected version or SHA-256 requirement. A mirror that serves a valid but
  malicious manifest still requires the configured Ed25519 key.
- A configured proxy is a trusted network intermediary for availability and
  metadata; it does not replace artifact hashes.
- Removing quarantine reduces Gatekeeper enforcement; ad-hoc signing destroys
  notarization.

## Verification checklist for integrators

- Pin/embed the Ed25519 public key and configure `security.public_key_file`.
- Leave `allow_unverified_assets` and `allow_insecure_http` off.
- Set `allowed_hosts` to the exact update infrastructure.
- Install into an application-managed directory with least privilege.
- Preserve only data your application owns.
- Ship a rollback-tested installer.
- Test the deferred flow on Windows.
- Verify macOS bundles remain signed after packaging.
- Protect API tokens and signing keys.

## Reporting

Please report vulnerabilities privately via GitHub Security Advisories for
`yxxbc/wmlUpdate`. Do not open a public issue for a working exploit.
