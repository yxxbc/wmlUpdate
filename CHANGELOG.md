# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [1.0.0] - 2026-10-28

### Added

- Ordered multi-source failover (`source.type: chain`), static manifest mirrors,
  asset mirror templates and same-version/SHA-256 download fallback.
- Explicit HTTP/HTTPS/SOCKS proxy configuration for all network requests.
- Optional macOS quarantine removal and ad-hoc app signing for directory
  installs.
- A local SDK playground and public testing guide.

## [0.1.0] - 2026-10-28

### Added

- Go library at the module root with a minimal `Check` and `Apply` API, opaque
  checked snapshots and optional progress events.
- Windows, macOS and Linux updater binary (amd64/arm64) as an optional sidecar
  and release tool.
- GitHub Releases and signed self-hosted static manifest sources.
- Language-agnostic NDJSON CLI protocol and loopback HTTP API with jobs and SSE.
- SHA-256 enforcement, optional Ed25519 detached signatures, HTTPS enforcement
  and host allowlists.
- Transactional directory/file installation, backups, rollback, crash recovery
  and a deferred helper for running Windows applications.
- Safe zip/tar extraction with traversal, symlink, hardlink and expansion
  defenses.
- Resumable downloads with retries and cache-aware source metadata.
- Self-hosting tooling: `keygen`, `publish`, `sign` and `verify`.
- Diagnostics (`doctor`, `status`), examples and production documentation.
- Apache-2.0 licensing, project branding and a release CLI.

[1.0.0]: https://github.com/yxxbc/wmlUpdate/releases/tag/v1.0.0
[0.1.0]: https://github.com/yxxbc/wmlUpdate/releases/tag/v0.1.0
