# ADR 0006: Ordered source chains with hash-bound download failover

- Status: accepted
- Date: 2026-10-28

## Context

A single source URL makes updates unavailable when a host, API, or asset CDN is
blocked or down. Applications in restricted networks commonly need mirrors
(jsDelivr, ghproxy, Gitee, a website) and automatic fallback. Naively switching
to another source can install a different version or unauthenticated bytes.

## Decision

- Add `source.type: chain` with an ordered `sources` array. Metadata is fetched
  from each source until one returns a valid, authenticated release.
- Add `manifest_urls` for static mirror metadata and `asset_mirrors` URL
  templates for artifact hosts.
- On artifact download failure, try mirrors and later chain sources only when
  they offer the exact selected version and a SHA-256 equal to the checked
  artifact.
- Never forward source credentials or GitHub tokens to a different mirror host.
- Keep signatures and hashes mandatory; mirrors affect availability only.
- Document first-success semantics: a reachable but stale first mirror wins over
  a newer fallback. Operators should keep mirrors synchronized.

## Consequences

- A blocked GitHub API or asset host no longer makes updates impossible.
- Fallback downloads require an extra metadata fetch only after a failure.
- Mirrors cannot cause downgrade or byte substitution without breaking the hash.
- Configuration is more verbose, but chain and mirror templates are optional and
  backward compatible.
- Nested chains are rejected to keep semantics and loop risk bounded.
