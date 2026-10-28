# Example configurations

- `github.json` — GitHub Releases with signed checksums, per-platform install
  paths and OS allowlist.
- `self-hosted.json` — a signed static manifest with an optional bearer header.
- `publish.json` — input to `wml-update publish`.

These are templates. Replace the app ID, repository, URLs, paths, public key and
asset names. Use absolute config paths in production. `current_version` is a
fallback; pass `--current-version` from the running app.
