<div class="hero" markdown>
![WML Update](assets/logo.png){ width="120" }

# Update infrastructure, not a framework.

A lightweight Go update **library** with a release CLI and an optional sidecar
for any language. Embed it, call `Check`, then `Apply`. Your application keeps
its UI, lifecycle and restart behavior.
</div>

[Get started with the Go SDK :material-arrow-right:](sdk.md){ .md-button .md-button--primary target="_self" }
[View on GitHub :fontawesome-brands-github:](https://github.com/yxxbc/wmlUpdate){ .md-button }
[Go API reference :material-file-document-outline:](https://pkg.go.dev/github.com/yxxbc/wmlUpdate){ .md-button }

```go
u, err := wmlupdate.New(wmlupdate.Options{
    ConfigPath:     "updater.json",
    CurrentVersion: appVersion,
})
defer u.Close()

update, err := u.Check(ctx)
if update.Available {
    result, err := u.Apply(ctx, update, wmlupdate.ApplyOptions{})
}
```

## Why WML Update

<div class="grid cards" markdown>

- :material-package-variant-closed: **Small public API**

    `New`, `Check`, `Apply`, `Close`. No daemon, service locator, global state or
    required application architecture.

- :material-shield-lock: **Verified artifacts**

    Every download requires SHA-256. Optional Ed25519 manifests and checksums,
    HTTPS enforcement and host allowlists.

- :material-swap-horizontal: **Transactional installs**

    Staged directory/file swaps, crash recovery, backups and rollback data.

- :material-microsoft-windows: **Windows-aware**

    Deferred detached helper waits for the application to exit, then replaces
    and relaunches it.

- :material-cloud-download: **Two sources**

    GitHub Releases and self-hosted, signed static manifests.

- :material-code-braces: **Any language**

    Non-Go applications use the same engine through NDJSON or a local HTTP API.

</div>

## Quick start

```bash
go get github.com/yxxbc/wmlUpdate@v1.1.0
```

```go
import wmlupdate "github.com/yxxbc/wmlUpdate"

u, err := wmlupdate.New(wmlupdate.Options{
    ConfigPath:     "updater.json",
    CurrentVersion: "1.4.2",
})
if err != nil { return err }
defer u.Close()

update, err := u.Check(ctx)
if err != nil || !update.Available { return err }

_, err = u.Apply(ctx, update, wmlupdate.ApplyOptions{})
```

Continue with the [Go SDK guide](sdk.md), [configuration](configuration.md), or
[take the sidecar path](integration.md) for Python, Node.js, C#, Java or shell.

## Release tooling

```bash
wml-update keygen --out-dir keys
wml-update publish --spec publish.json --out public/stable --key keys/signing-key.pem
```

Produces `manifest.json`, its Ed25519 signature, `checksums.txt` and a detached
checksum signature. See [self-hosting](self-hosting.md) and
[GitHub Releases](github-releases.md).
