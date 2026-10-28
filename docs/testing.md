# Testing

You do not need to create an external package to test WML Update. The repository
contains unit, integration and end-to-end tests for the engine and the public
SDK.

## Automated test suite

```bash
git clone https://github.com/yxxbc/wmlUpdate.git
cd wmlUpdate
make test
```

This runs:

- strict SemVer, config, manifest and signing tests
- archive traversal, symlink, hardlink and expansion defenses
- source discovery for GitHub Releases, static mirrors, ordered chains and
  download failover
- resumable downloads, checksums and HTTP policy
- transactional directory/file installs, backups, recovery and rollback
- the public SDK and local HTTP API
- an external-consumer compile check is not part of the suite, but the SDK tests
  exercise the public API directly

Race detector:

```bash
make race
```

CI runs the suite on Ubuntu, macOS and Windows, plus `go vet`, `govulncheck`,
cross-compilation and the SDK playground. The same playground can be run on
every platform with `make e2e`.

## One-command local end-to-end test

```bash
make e2e
# or
go run ./examples/playground
```

The playground:

1. creates a temporary v1 application tree;
2. builds a v2 zip;
3. computes its SHA-256 and writes a loopback manifest;
4. serves it through an in-process HTTP server;
5. calls the public SDK with `Check`, then `Apply`;
6. verifies the installed file and the post-update `Check`;
7. prints `local SDK end-to-end test passed`.

Use `-keep` to inspect the generated source, target, state, backup and logs:

```bash
go run ./examples/playground -keep
```

The playground uses an **unsigned** loopback manifest to keep the harness small.
Ed25519 manifest and checksum signatures are covered by the automated tests and
are expected in production.

## Test your own configuration

For a real config, use the CLI diagnostics first:

```bash
wml-update --config updater.json doctor --current-version 1.4.2
wml-update --config updater.json --json check --current-version 1.4.2
wml-update --config updater.json update --dry-run --current-version 1.4.2
wml-update --config updater.json fetch --output /tmp/app.zip --current-version 1.4.2
```

`fetch` performs discovery, download and SHA-256 verification without touching
the install tree. Inspect the artifact, then run `update` when ready.

To test a different platform without leaving your machine, use `--os` and
`--arch`; install paths and asset rules are resolved for that target.

## Test a self-hosted release locally

```bash
wml-update keygen --out-dir keys
wml-update publish --spec publish.json --out public/stable --key keys/signing-key.pem
python3 -m http.server 18080 --directory public
```

Then point a temporary config at:

```text
http://127.0.0.1:18080/stable/manifest.json
```

and run `check` or `update`. Loopback HTTP is allowed by the default security
policy. Use a temporary `state_dir` and install target for the test.

## Test a GitHub release

Create a scratch repository or a prerelease in an existing one, attach the
platform archives and `checksums.txt`, then:

```bash
wml-update --config test-updater.json check --current-version 0.0.1
wml-update --config test-updater.json fetch --output /tmp/artifact
```

For private repositories, set the token environment variable named by
`source.token_env`. Do not put the token in the JSON file.

## Test Windows deferred apply

On Windows, with the target process running:

```bash
wml-update --config updater.json update --defer --wait-pid <pid> --json
```

The parent returns `apply_scheduled`; the application should exit. The helper
writes its outcome to:

```text
<state_dir>/plans/<plan_id>.json.result
```

The same behavior is available from the SDK with `ApplyOptions{Defer: true,
WaitPIDs: ...}`.

## Test the release CLI

```bash
wml-update keygen --out-dir /tmp/test-keys
wml-update publish --spec publish.json --out /tmp/test-public --key /tmp/test-keys/signing-key.pem
wml-update verify --manifest \
  --public-key-file /tmp/test-keys/signing-key.pub.pem \
  /tmp/test-public/manifest.json
```

This exercises archive creation, hashing, manifest validation and detached
signature verification without a network service.

## Test the SDK from another module

If you are testing your own integration, a small external module is the most
realistic check:

```bash
mkdir sdk-consumer && cd sdk-consumer
go mod init example.com/sdk-consumer
go get github.com/yxxbc/wmlUpdate@v1.1.0
```

```go
package main

import (
    "context"
    "fmt"

    wmlupdate "github.com/yxxbc/wmlUpdate"
)

func main() {
    u, err := wmlupdate.New(wmlupdate.Options{
        ConfigPath:     "updater.json",
        CurrentVersion: "1.4.2",
    })
    if err != nil { panic(err) }
    defer u.Close()

    update, err := u.Check(context.Background())
    if err != nil { panic(err) }
    fmt.Println(update.Available, update.Version)
}
```

The included `examples/go` is this pattern in runnable form.
