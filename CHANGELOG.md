# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Windows, macOS and Linux updater binary (amd64/arm64).
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

[Unreleased]: https://github.com/yxxbc/wmlUpdate/compare/main...HEAD
