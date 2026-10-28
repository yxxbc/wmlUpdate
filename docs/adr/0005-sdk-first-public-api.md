# ADR 0005: Make the Go SDK the primary integration surface

- Status: accepted
- Date: 2026-10-28

## Context

The first implementation exposed the updater primarily as a standalone binary
with an NDJSON/HTTP protocol. That works for every language, but Go applications
had to ship and spawn a process and model updater lifecycle themselves. The
product goal is a lightweight dependency with minimal intrusion: expose check
and apply, not a framework.

## Decision

- Publish a Go package at the module root with a deliberately small API:
  `New`, `Check`, `Apply`, `Close`.
- `Check` returns an opaque checked snapshot. `Apply` installs that exact
  snapshot, without re-fetching mutable source metadata; artifact bytes are
  still downloaded and verified.
- Keep the host in control of lifecycle, UI, prompts and restart. The library
  never exits or restarts the host process.
- Keep the standalone CLI and HTTP API as **optional adapters** for non-Go
  languages, process isolation and operational tooling.
- Keep release packaging/signing in the CLI (`publish`, `keygen`, `sign`,
  `verify`) so release automation does not need a language binding.
- Avoid daemon requirements, global registration, framework hooks and required
  application directory conventions.

## Consequences

- Go applications integrate with a few lines and no process boundary.
- Other languages retain a supported path through the sidecar protocol.
- There are two public contracts to keep compatible: the Go API and the NDJSON/
  HTTP event protocol. Both are tested from the same engine.
- `Update` values are intentionally opaque and not serializable; check and apply
  must happen in one process.
- A future native binding for another language can layer on the engine or the
  sidecar without changing the Go API.
