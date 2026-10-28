# Architecture

## Goals

1. One codebase and one binary covering Windows, macOS and Linux.
2. A stable integration contract usable from any language.
3. Authenticated metadata and verified artifacts.
4. Safe, recoverable installation.
5. Useful diagnostics, not silent failure.

## Non-goals

- Delta updates. A full artifact is downloaded; content-addressed caching avoids
  re-downloading the same bytes.
- A background service or system-wide scheduler. The host application decides
  when to check.
- Arbitrary package-manager integration. The `command` strategy can invoke one,
  but rollback is owned by that installer.
- Code signing. The updater preserves bytes; macOS notarization and Windows
  Authenticode signing remain part of your release pipeline.
- A hosted update service. Any static web server is sufficient.

## Component map

```text
cmd/wml-update
  └─ internal/cli ──────────────── commands, JSON event output, exit codes
       ├─ internal/engine ──────── orchestration, current-version policy, results
       │    ├─ internal/source ──┬─ github.go   Releases API + checksums
       │    │                   └─ static.go   signed manifest + sidecar
       │    ├─ internal/httpx ─── hardened client, retry, resumable download
       │    ├─ internal/manifest  manifest model, validation, signature
       │    ├─ internal/checksum  SHA-256 and sha256sum parsing
       │    ├─ internal/install ─┬─ Prepare    staging / extract / preserve
       │    │                   └─ Apply      journal, backup, swap, rollback
       │    ├─ internal/archive   safe zip/tar extraction and creation
       │    ├─ internal/state     atomic JSON state and install journal
       │    ├─ internal/lock      per-app advisory lock
       │    └─ internal/sign      Ed25519 keys and detached signatures
       └─ internal/api ─────────── optional loopback HTTP API, jobs, SSE
```

Supporting packages: `internal/version` (strict SemVer), `internal/platform`
(normalization and asset heuristics), `internal/event` (NDJSON contract),
`internal/config` (strict JSON loading, validation, defaults),
`internal/publish` (release tooling), `internal/buildinfo` (ldflags).

## Update flow

```text
check/update
   │
   ├─ load strict config, resolve target platform
   ├─ determine current version (flag > config > state)
   ├─ acquire per-app lock (mutations only)
   ├─ recover interrupted transaction (if any)
   ├─ fetch release metadata
   │    ├─ GitHub: Releases API, SemVer selection, checksums/signatures
   │    └─ static: manifest bytes → signature → parse → channel/app checks
   ├─ select platform asset (rules → metadata → heuristic)
   ├─ compare SemVer
   ├─ download to state/downloads with Range resume and retry
   ├─ verify size + SHA-256 (and authenticated hash source)
   ├─ install strategy:
   │    ├─ directory: extract → preserve → journal → swap → backup
   │    ├─ file:      stage → journal → backup → rename
   │    ├─ command:   run verified installer
   │    └─ external:  return artifact path
   └─ record state, optional relaunch, emit result
```

## Trust model

The source URL and configuration are trusted; everything fetched over the
network is untrusted.

1. **Metadata authenticity**
   - Self-hosted manifests are signed with Ed25519 when a public key is
     configured. The signature covers the exact raw bytes, so whitespace and
     key ordering are not ambiguous.
   - GitHub metadata is authenticated by TLS and the GitHub API. Hash strength
     is improved by requiring the API's asset `digest` or a `checksums.txt`
     asset. An optional signed checksums file provides an independent trust
     anchor.
2. **Artifact integrity**
   - Every download requires a SHA-256. Downloading is refused otherwise unless
     the dangerous `allow_unverified_assets` flag is set.
   - The hash is taken from a signed manifest, a signed checksums file, or
     GitHub's API digest.
3. **Network boundaries**
   - HTTPS is required except loopback or explicit opt-in.
   - Redirects are re-evaluated against scheme, host allowlist and count.
   - Authorization headers are not forwarded to a different host by Go's
     redirect policy; self-hosted headers are only attached to same-host
     assets at the model layer.
4. **Filesystem safety**
   - Archive extraction rejects absolute paths, Windows volume paths, `..`
     traversal, escaping symlinks, hardlinks, device/FIFO entries, duplicate
     symlink-parent writes, excessive entries and excessive expansion.
   - Install targets are validated against filesystem root, the home directory,
     symlink targets and state-directory overlap.
   - Staging and backups are siblings of the target, keeping renames on one
     filesystem.
5. **Local API**
   - Bearer token required, constant-time comparison.
   - Loopback bind by default; remote binding requires an explicit flag.
   - No CORS policy is added, so browser origins cannot call it cross-site
     without the token.

## Installation transaction

State is kept in `<state_dir>/state.json`. Before any live rename, a pending
journal records target, stage, backup, versions and phase.

### Directory / file replace

1. `prepared`: artifact verified, new tree staged; live target untouched.
2. Rename target → backup (`backed_up` saved).
3. Rename stage → target (`committed` saved).
4. Append backup metadata, write installed version, clear journal.
5. Prune backups beyond `keep_backups`.

On failure, the backup is restored. On the next mutating command, recovery
inspects the journal:

- `prepared` with backup present and target missing: restore backup.
- `backed_up` with target present and stage gone: the swap completed; finalize.
- Otherwise: restore the backup and remove partial staged data.
- Command installers are not recoverable; the journal is cleared with a warning
  telling the operator to verify the application manually.

Backups are retained as siblings of the target so rollback requires only
renames. `rollback` moves the current installation aside and restores the latest
backup.

### Why a detached helper

On Windows, a running executable and loaded DLLs cannot be replaced, and the
updater itself may live inside the application directory. With `--defer`:

1. The parent updater downloads, verifies, extracts and writes a plan.
2. It copies its own executable into the state directory.
3. It starts the copied helper detached with `--wait-pid` values, then exits.
4. The helper waits for the application to exit, acquires the lock, runs the
   same journaled apply path, optionally relaunches, and writes a result file.

This avoids depending on a helper inside the tree being replaced.

## State and concurrency

- One advisory lock per `app_id` serializes mutating operations across processes.
- API jobs reject a second mutating job; the CLI additionally uses the file lock.
- `state.json` writes are atomic (temp file, fsync, rename, directory fsync).
- Source metadata uses ETag/Last-Modified caches stored under the state directory.
- Downloads are named by expected SHA-256, so concurrent or repeated checks do
  not trust stale content.

## Result and event contract

`--json` writes one JSON object per line:

- `log`, `warning`, `progress`: progress is `download` with `bytes`, `total`,
  `percent`.
- `result`: exactly one terminal successful object.
- `error`: terminal failure with a stable code.

The same shapes are returned by the HTTP job API. See
[integration](integration.md).

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | Internal error / diagnostics failure |
| 2 | Usage or configuration error |
| 3 | Source, network, no release or no matching asset |
| 4 | Integrity / signature / checksum failure |
| 5 | Installation or rollback failure |
| 6 | Lock, state or I/O failure |
| 130 | Cancelled |

## Extension points

- New source: implement `source.Provider` and normalize to `release.Release`.
- New installer: add a strategy to config validation, `Prepare` and `Apply`,
  with a journal phase and rollback semantics.
- New API: the engine is independent of Cobra and HTTP.

## Decisions

- [ADR 0001: Go static binary](adr/0001-go-static-binary.md)
- [ADR 0002: subprocess NDJSON plus local HTTP](adr/0002-subprocess-json-and-http.md)
- [ADR 0003: transactional replace with backups](adr/0003-transactional-install.md)
- [ADR 0004: supply-chain verification](adr/0004-supply-chain-verification.md)
