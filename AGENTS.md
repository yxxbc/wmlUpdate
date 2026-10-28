# Notes for coding agents

This file is for engineers and coding agents changing WML Update. Read it before
editing. It records the product constraints, invariants and verification steps
that are easy to break while making an otherwise reasonable change.

## Product shape

WML Update is a **small Go update library first**:

- package `github.com/yxxbc/wmlUpdate`
- public entry points: `New`, `(*Updater).Check`, `(*Updater).Apply`,
  `(*Updater).Close`
- `Check` is read-only; `Apply` installs the exact checked snapshot
- the library never owns the host's UI, event loop, process lifecycle or restart
  policy

The command `cmd/wml-update` is optional. It provides:

- the release toolchain (`publish`, `keygen`, `sign`, `verify`);
- the sidecar adapter for non-Go languages (`--json`, `serve`).

Do not turn the SDK into a framework. Do not add a required daemon, global
singleton, application base class or mandatory directory convention.

## Current compatibility line

- `v1.0.0` is the first stable public release.
- `main` may contain unreleased work; release notes and `CHANGELOG.md` decide what
  ships next.
- Never move, delete or reuse a published tag. Go module proxies and downstream
  checksums may already have cached it.
- Read [docs/versioning.md](docs/versioning.md) before changing exported API,
  configuration fields or the NDJSON/HTTP contracts.

Changes that maintain compatibility may add exported fields, sources, options and
commands. Breaking an exported identifier or documented protocol semantics needs
a major version.

## Source map

```text
wmlupdate.go / sdk.go / config.go / events.go   public SDK
internal/engine                                 orchestration and checked snapshots
internal/source                                 github, static, chain providers
internal/httpx                                  hardened HTTP, proxy, download, retry
internal/manifest                               manifest model and Ed25519 signature
internal/checksum                               SHA-256 and checksum files
internal/install                                staging, transactions, rollback, helper
internal/archive                                safe archive extraction and creation
internal/state / internal/lock                  durable state and app lock
internal/sign                                   keys and detached signatures
internal/api                                    optional loopback HTTP adapter
internal/cli / internal/publish                 CLI and release tooling
```

Read [docs/architecture.md](docs/architecture.md) and the ADRs before changing a
trust boundary or install strategy.

## Invariants

1. **No artifact without a hash.** The only exception is the explicit
   `allow_unverified_assets` opt-in. Never make an unverified path the default.
2. **Signatures are over exact bytes.** Self-hosted manifests verify a detached
   Ed25519 signature before parsing. GitHub checksum signatures cover the exact
   checksum file. Do not canonicalize or re-encode before verification.
3. **Checked snapshots are exact.** `Check` captures a release/asset. `Apply`
   must not silently re-fetch and install a different version. Fallback artifacts
   must have the same version and SHA-256.
4. **Mirrors are untrusted for bytes.** A source chain may change availability,
   never the selected version or hash. Never send GitHub tokens or configured
   source headers to a different mirror host.
5. **Archives are untrusted.** Reject absolute/volume paths, `..`, escaping
   symlinks, hardlinks, special files, symlink-parent writes and excessive
   expansion. Do not weaken these checks for convenience.
6. **No dangerous install targets.** Reject filesystem roots, the user home
   directory itself, symlinked targets and paths overlapping the state directory.
7. **Live changes are journaled.** Directory/file replacement is staged, backed
   up, journaled and recoverable. A new install strategy without crash recovery
   and rollback must be documented as an exception.
8. **Host lifecycle stays with the host.** `Apply` does not call `os.Exit` or
   restart the process. Windows deferred apply additionally waits for requested
   PIDs and uses a copied helper.
9. **macOS signing continuity.** For a signed installed `.app`, require the staged
   app to pass `codesign --verify --deep --strict` and keep the same
   `TeamIdentifier` unless `allow_signer_change` is explicitly set. Ad-hoc signing
   intentionally bypasses continuity and destroys notarization.
10. **Local API is local.** Token required, loopback default, no CORS opt-in,
    body limits. Do not log tokens.
11. **Key material never enters git.** The release private key lives in the
    GitHub Actions secret `WML_SIGNING_KEY` and the maintainer's password manager
    / macOS Keychain. Only the public key is committed.

## Testing

Before claiming a change works:

```bash
gofmt -w $(git ls-files '*.go')
go vet ./...
go test ./... -count=1
go test -race ./... -count=1
GOTOOLCHAIN=go1.24.0 go test ./... -count=1   # module minimum
go run ./examples/playground                  # real SDK update
```

Run these when relevant:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...
GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 go build ./...
mkdocs build --strict
actionlint .github/workflows/*.yml
goreleaser check
```

CI covers Ubuntu, macOS and Windows. New behavior needs a focused test; security
behavior needs both a rejection test and a success test. Do not rely on the
playground alone.

See [docs/testing.md](docs/testing.md).

## Frequent releases

Frequent builds are supported, not simulated by a heavy scheduler:

- a dedicated static feed/manifest, cached with ETag;
- unique monotonic SemVer versions;
- `Check` on the host's schedule;
- deferred apply to stage while running and install on exit;
- mirrors, proxy and hash-bound fallback;
- Windows file-sharing retries and relaunch retries;
- retention of previous versions.

Read [docs/frequent-releases.md](docs/frequent-releases.md) before changing update
scheduling, staging or restart behavior.

## Code standards

- Match the surrounding Go style; keep comments about *why*, not restatements.
- Use `context.Context` on every network, install and long-running operation.
- Wrap errors with `%w`; preserve machine-readable codes through
  `engine.CodeOf` / the public `Error`.
- Prefer the standard library. A new dependency needs a security and maintenance
  justification.
- Keep platform-specific code behind build tags (`*_windows.go`, `*_unix.go`).
- Use atomic state writes and the per-app lock for mutations.
- Avoid stdout in library code and in the JSON event path. JSON stdout is a
  protocol; diagnostics go to stderr or events.
- Add tests for every failure mode you touch: cancellation, lock contention,
  signature/hash mismatch, truncated download, interrupted swap, read-only target.

## Documentation and release

When behavior, config or protocol changes, update the relevant files together:

- `README.md` / `README.zh-CN.md` when positioning or quick start changes
- `docs/*.md` for behavior and integration
- `schema/config.schema.json` / `schema/manifest.schema.json` for JSON changes
- `examples/config/*.json` for examples
- `CHANGELOG.md` for user-visible changes
- `docs/versioning.md` for compatibility policy

Docs are built with MkDocs Material and published to GitHub Pages with `mike`:

- `dev` from `main`
- release tag versions from tags
- `latest` alias for the newest stable release

Release is a deliberate operation, not part of every change:

1. Run the full test matrix above.
2. Verify a real consumer can `go get` the release candidate.
3. Update the changelog and version references.
4. Tag `vX.Y.Z` and push the tag; the Release workflow builds, signs checksums
   and publishes assets.
5. Verify the release signature with
   `signing/release-signing-key.pub.pem`.
6. Confirm the docs deployment for the tag.

See [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md).

## Known traps

- Do not use build metadata (`+sha`) as the only distinction between versions;
  SemVer ordering ignores it. Unique patch/pre-release versions remain the
  ordering key; channels and build provenance are separate metadata axes.
- Do not invent a second competing version number for the SDK and CLI of the
  same Go module. If independent release cadences are ever required, split the
  CLI into its own module and tag path.
- Do not inject headers into fallback URLs; compare hosts explicitly.
- Do not remove `.part` recovery or hash verification while simplifying the
  downloader.
- Do not perform `xattr`/`codesign` on non-macOS platforms or before verifying
  the staged bundle.
- Do not add a fallback that installs a different version just because it is
  "newer"; the checked version is the contract.
- Do not add a background updater to the library; expose primitives and let the
  host schedule.
