# ADR 0001: Implement the updater in Go as one static binary

- Status: accepted
- Date: 2026-10-28

## Context

The updater must run on Windows, macOS and Linux, frequently as a subprocess of
applications written in unrelated languages. It needs HTTP, archive handling,
cryptography, filesystem transactions, process control and a CLI.

## Decision

Use Go with `CGO_ENABLED=0` and build one binary per OS/architecture. Keep the
runtime dependency set small: Cobra for the CLI, `gofrs/flock` for cross-platform
file locks, `doublestar` for preserve globs, and `x/sys` for Windows process
checks.

## Consequences

- Distribution is a single archive; no language runtime, package manager or
  shared library is required on the client.
- Cross-compilation is part of CI and release automation.
- The updater is easy to call from any language as a subprocess.
- Dependency review is small and bounded.
- A Go toolchain is required to build, but not to use.
