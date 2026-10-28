# ADR 0004: Require hashes, support Ed25519 detached signatures

- Status: accepted
- Date: 2026-10-28

## Context

HTTPS protects transit, not a compromised release account or host. JSON signing
schemes often require canonicalization, which is easy to implement incorrectly
across languages. Users need a simple, reproducible trust anchor.

## Decision

- Require SHA-256 for every artifact. Refuse downloads without one unless the
  operator explicitly sets `allow_unverified_assets`.
- Use detached Ed25519 signatures over exact file bytes, encoded as standard
  base64. Sign self-hosted manifests and GitHub checksum files.
- When a public key is configured, signatures are mandatory and a missing
  signature is fatal.
- Prefer signed checksum files over per-artifact signatures: one signature
  authenticates all hashes and works for any number of assets.
- Enforce HTTPS and optional host allowlists on every request and redirect.

## Consequences

- There is no canonical-JSON ambiguity: bytes in, bytes verified.
- Signing is trivial from Go, OpenSSL, Python or CI.
- Key management becomes a release responsibility; docs cover storage and
  rotation.
- Existing GitHub releases without a digest or checksums fail unless the
  operator opts into unverified downloads or rebuilds their release process.
