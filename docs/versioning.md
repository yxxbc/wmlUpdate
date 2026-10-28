# Versioning and compatibility

WML Update follows [Semantic Versioning 2.0.0](https://semver.org/).

## Current status

- **`v1.0.0`** is the first stable release and freezes the public Go API and
  package import path.
- `v0.1.0` was the initial public preview. It remains permanently available for
  reproducibility and must not be retagged or moved.
- `main` may contain changes that are not in the latest release. The documentation
  site publishes `dev` separately from released versions.
- The nightly workflow publishes prerelease builds such as
  `v1.1.0-nightly.20261028.42.1`. They are valid Go module tags but are never
  `latest` and are not selected by a stable client.

## One ordering version, several metadata axes

A single SemVer is still the right **identity and ordering key** for the Go
module, the CLI and the update protocol. It is not the only metadata a frequent
release process needs.

Use orthogonal axes:

| Axis | Example | Purpose |
| --- | --- | --- |
| SemVer release | `1.0.3`, `1.1.0-beta.2` | update identity and ordering |
| channel | `stable`, `beta`, `nightly` | who receives the build |
| build/commit | `20261028.3`, git SHA | display, support and provenance |
| published time | RFC 3339 | feed ordering and release notes |

Rules for frequent builds:

- Every build that clients can install gets a **unique SemVer**. Prefer a
  monotonic patch counter (`v1.0.1341`) for stable builds or a pre-release
  (`v1.1.0-nightly.20261028.3`) for nightly builds.
- Never reuse a version for different bytes.
- Never use `+build` metadata as the only difference: SemVer ordering ignores it,
  so the updater would treat the builds as equal.
- Publish stable, beta and nightly through separate manifest URLs/channels. A
  nightly tag must not be promoted to stable clients by accident.
- Keep one tag for the Go module and CLI release of the same commit. If the CLI
  and SDK ever need independent release cadences, split the CLI into its own Go
  module and tag path rather than inventing two versions for one module.
- Versioned documentation follows tags with `mike`; `dev` is not a release.

This is why a single number is not insufficient by itself: it remains the
ordering key. The missing pieces for frequent updates are unique version bumps,
channels and provenance/build metadata, not a second competing version number.

See [frequent releases](frequent-releases.md) for the feed/staging model.

## Compatibility promise from v1.0.0

The stable public surface is:

- package `github.com/yxxbc/wmlUpdate`
- `New`, `(*Updater).Check`, `(*Updater).Apply`, `(*Updater).Close`
- `Options`, `Config`, `Update`, `ApplyOptions`, `Result`, `Event`
- documented JSON configuration and NDJSON/HTTP protocols

Within a major version:

- **minor** releases add fields, sources, options and commands;
- **patch** releases fix defects without changing documented behavior;
- **major** releases may change or remove exported identifiers, method
  signatures or documented protocol semantics;
- new configuration fields are additive; unknown fields continue to be rejected
  so typos fail loudly.

Go guarantees:

- minimum Go version is declared in `go.mod` and may only increase in a minor
  release;
- the module path, import path and package name are stable for the `v1` line;
- pre-built CLI archives are provided for Windows, macOS and Linux (`amd64`,
  `arm64`).

## Tags are immutable

Published tags are treated as immutable. Do not move, delete or reuse a version
tag: Go module proxies, checksums and downstream lock files may already have
cached it. If a release must be withdrawn, publish a new version and use the
Go `retract` directive if appropriate; never rewrite history behind an existing
tag.

Pre-releases use SemVer identifiers, for example `v1.1.0-rc.1` or
`v1.1.0-nightly.20261028.42.1`. They are not selected by default and should be
treated as unstable. Nightly tags are immutable too; do not reuse a nightly
version or delete it to "clean up" after clients may have cached it.

## Documentation versions

The documentation site publishes:

- released versions, e.g. `1.0.0`;
- `latest`, an alias for the newest stable release;
- `dev`, built from `main`.

The version selector is provided by `mike`. Users on a tagged release should
read the matching documentation version, not `dev`.

## Release checklist

1. `make check`, `make race`, and the SDK playground pass on the host.
2. CI passes on Ubuntu, macOS and Windows.
3. A real consumer builds against the release candidate.
4. Update `CHANGELOG.md` and version references.
5. Create an annotated tag, push it, and let the release workflow build, sign
   `checksums.txt` and publish assets.
6. Verify the detached signature with `signing/release-signing-key.pub.pem`.
7. Deploy the matching documentation version with `mike`.
