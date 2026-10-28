# Contributing

Thanks for improving WML Update.

## Before you start

- Search existing issues and pull requests.
- For a security issue, follow [SECURITY.md](SECURITY.md); do not open a public
  issue.
- For a new update source or install strategy, open an issue with the failure
  modes and rollback semantics first. These are trust-boundary changes.

## Development

Requires Go 1.24+.

```bash
make test
make check
make build
```

`make check` runs gofmt, tests and `go vet`. The release build is CGO-free and
cross-compiles to Windows, macOS and Linux.

## Expectations

- Keep the JSON event protocol and error codes stable; add fields, do not
  repurpose existing ones.
- Treat archives, manifests, asset URLs and API inputs as untrusted.
- Every install path needs crash recovery and a rollback story, or an explicit
  documented limitation.
- Add tests for traversal, symlinks, truncated downloads, signature failures and
  interrupted transactions where relevant.
- Update docs, schemas and examples when behavior changes.
- Avoid new dependencies unless the standard library cannot do the job safely.

## Commits and pull requests

- Keep changes focused and explain the why in the commit body.
- Include the platform(s) you tested.
- Describe security implications and migration notes.
- Run `make check` before pushing.
- Do not commit signing keys, generated binaries, state directories or local
  update artifacts.

## License

By contributing, you agree your contribution is licensed under the MIT License.
