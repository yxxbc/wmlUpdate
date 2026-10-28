# Versioning and compatibility

WML Update follows [Semantic Versioning 2.0.0](https://semver.org/).

## Current status

- **`v1.0.0`** is the first stable release and freezes the public Go API and
  package import path.
- `v0.1.0` was the initial public preview. It remains permanently available for
  reproducibility and must not be retagged or moved.
- `main` may contain changes that are not in the latest release. The documentation
  site publishes `dev` separately from released versions.

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

Pre-releases use SemVer identifiers, for example `v1.1.0-rc.1`. They are not
selected by default and should be treated as unstable.

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
