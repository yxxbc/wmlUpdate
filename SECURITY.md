# Security policy

## Supported versions

Security fixes are made on the latest release line. Please upgrade before
reporting an issue that may already be fixed.

## Reporting a vulnerability

Report vulnerabilities privately through GitHub Security Advisories for this
repository:

https://github.com/yxxbc/wmlUpdate/security/advisories/new

Do not open a public issue for a working exploit. Include:

- affected version and platform,
- a minimal reproduction or proof of concept,
- the impact you believe is possible,
- whether the issue requires a malicious update source, local access, or a
  compromised signing key.

We aim to acknowledge reports within 72 hours and to coordinate disclosure after
a fix is available.

## Scope

In scope:

- signature/checksum verification bypass,
- archive extraction escapes,
- path traversal or unsafe install target handling,
- token or secret disclosure,
- local API authentication bypass,
- update source URL/redirect handling flaws,
- privilege or rollback integrity issues.

Out of scope:

- attacks requiring a compromised Ed25519 signing key,
- a malicious local administrator/user who can modify the updater binary,
  configuration, public key or install target,
- expected deletion of files in a directory that is intentionally managed as a
  whole,
- lack of macOS notarization or Windows Authenticode on the application itself.

## Safe defaults

Do not enable `allow_unverified_assets` or `allow_insecure_http` in production.
Configure an Ed25519 public key, an explicit host allowlist, least-privilege
install locations and a protected API token. See `docs/security.md`.
