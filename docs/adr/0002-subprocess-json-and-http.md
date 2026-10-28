# ADR 0002: Expose subprocess NDJSON first, loopback HTTP second

- Status: accepted
- Date: 2026-10-28

## Context

"Any language" must not mean maintaining an SDK per language. Desktop UIs also
need progress, cancellation and long-lived state.

## Decision

Make the CLI with `--json` the primary protocol: one JSON object per line,
terminal `result` or `error`, stable error codes and exit codes. Offer an
optional loopback HTTP server for applications that want asynchronous jobs,
polling, cancellation and SSE. Both surfaces share the same engine and result
model.

## Consequences

- A language only needs process execution and a JSON parser.
- Progress can be streamed without a network server.
- The HTTP API adds a security surface, so it requires a bearer token, binds
  loopback by default and limits request sizes.
- API jobs are intentionally in-memory; durable state remains `state.json`.
