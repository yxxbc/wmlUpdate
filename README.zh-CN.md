# WML Update

一个面向 **Windows、macOS、Linux** 的小型、安全、语言无关的通用更新器。

任何语言的应用都不需要引入 SDK：把单个静态二进制作为子进程调用，读取稳定的
JSON 行协议，即可获得版本发现、校验下载、事务化安装、回滚、Windows 延迟替换
和可选的本地 HTTP API。

[English](README.md)

## 为什么

自己实现更新器要同时处理三个操作系统、断点续传、被篡改的安装包、zip-slip、
符号链接逃逸、Windows 运行中文件锁定、回滚，以及如何向任意语言的 UI 汇报进度。
WML Update 把这些能力收敛到一个可审计的 Go 二进制中：

- **单二进制、无运行时依赖**，覆盖 Windows / macOS / Linux，`amd64` / `arm64`。
- **GitHub Release 与自托管静态清单**两种更新源。
- **任意语言可集成**：CLI + NDJSON，或本地 HTTP API。
- **每个安装包强制 SHA-256**，可选 **Ed25519 分离签名**，TLS 强制与域名白名单。
- **目录/单文件事务化替换**，保留备份、崩溃恢复与 `rollback`。
- **安全解压**：拒绝路径穿越、逃逸符号链接、硬链接、特殊文件、超大解压和条目轰炸。
- **断点续传**、有界重试和 `.part` 文件。
- **延迟应用助手**，解决 Windows 应用运行中无法替换的问题。
- **自托管发布工具**：打包、哈希、签名、清单与校验文件生成。

## 安装

从 [GitHub Releases](https://github.com/yxxbc/wmlUpdate/releases) 下载，校验
`checksums.txt` 后把 `wml-update` / `wml-update.exe` 放入 `PATH`。

```bash
wml-update version
```

从源码构建：

```bash
go install github.com/yxxbc/wmlUpdate/cmd/wml-update@latest
```

## 快速开始：GitHub Release

`updater.json`：

```json
{
  "$schema": "https://raw.githubusercontent.com/yxxbc/wmlUpdate/main/schema/config.schema.json",
  "schema_version": 1,
  "app_id": "com.example.myapp",
  "channel": "stable",
  "current_version": "1.0.0",
  "source": {
    "type": "github",
    "owner": "acme",
    "repo": "myapp",
    "tag_prefix": "v",
    "token_env": "GITHUB_TOKEN"
  },
  "asset_rules": [
    {"os": "darwin", "arch": "arm64", "match": "myapp-{version}-macos-arm64.zip", "format": "zip"},
    {"os": "windows", "arch": "amd64", "match": "myapp-{version}-windows-amd64.zip", "format": "zip"},
    {"os": "linux", "arch": "amd64", "match": "myapp-{version}-linux-amd64.tar.gz", "format": "tar.gz"}
  ],
  "install": {
    "type": "directory",
    "paths": {
      "darwin": "/Applications/MyApp.app",
      "windows": "C:\\Program Files\\MyApp",
      "linux": "/opt/myapp"
    },
    "preserve": ["data/**", "config/**"]
  }
}
```

```bash
wml-update --config updater.json check
wml-update --config updater.json update --current-version 1.0.0
```

GitHub Release 中需上传符合 `asset_rules` 命名的安装包，以及 `checksums.txt`。
GitHub 返回 asset `digest` 时会自动使用。配置
`security.public_key_file` 并上传 `checksums.txt.sig` 后，校验文件必须带有效签名。

## 快速开始：自托管

```bash
wml-update keygen --out-dir keys
wml-update publish --spec publish.json --out public/stable --key keys/signing-key.pem
```

`publish.json`：

```json
{
  "app_id": "com.example.myapp",
  "version": "1.2.0",
  "channel": "stable",
  "notes": "修复与改进",
  "assets": [
    {"dir": "dist/MyApp", "url": "myapp-1.2.0-linux-amd64.tar.gz", "os": "linux", "arch": "amd64", "format": "tar.gz"},
    {"dir": "dist/MyApp", "url": "myapp-1.2.0-windows-amd64.zip", "os": "windows", "arch": "amd64", "format": "zip"}
  ]
}
```

把 `manifest.json`、`manifest.json.sig` 和安装包上传到任意静态站点。配置：

```json
{
  "app_id": "com.example.myapp",
  "channel": "stable",
  "current_version": "1.0.0",
  "source": {
    "type": "static",
    "manifest_url": "https://updates.example.com/stable/manifest.json"
  },
  "install": {
    "type": "directory",
    "path": "/opt/myapp"
  },
  "security": {
    "public_key_file": "keys/signing-key.pub.pem"
  }
}
```

详见 [自托管指南](docs/self-hosting.md)。

## 任意语言集成

以 `--json` 启动子进程。stdout 每行一个 JSON 对象，日志和进度也是数据，
不会混入普通文本；stderr 可用于诊断。

```bash
wml-update --config updater.json --json update --current-version 1.0.0
```

```json
{"type":"progress","phase":"download","bytes":1048576,"total":4194304,"percent":25}
{"type":"result","status":"updated","previous_version":"1.0.0","current_version":"1.1.0"}
```

- `check`：只检查，不下载。
- `fetch`：只下载并校验，返回路径。
- `mark`：应用自己完成安装后，记录新版本。
- `serve`：本地 token 鉴权 HTTP API，支持异步任务与 SSE 进度流。

完整协议与各语言示例：[集成文档](docs/integration.md)。示例代码：
[Python](examples/python)、[Node.js](examples/node)、[Go](examples/go)、
[C#](examples/csharp)、[Shell](examples/shell)。

## 常用命令

| 命令 | 作用 |
| --- | --- |
| `check` | 检查更新，不下载 |
| `update` | 检查、下载、校验并安装 |
| `update --defer --wait-pid <pid>` | 先准备，待应用退出后由独立助手应用 |
| `fetch --output app.zip` | 仅下载并校验 |
| `rollback` | 恢复最近一次备份 |
| `status` | 查看版本、备份和未完成事务 |
| `serve` | 本地 HTTP API |
| `publish` | 生成自托管清单、校验文件和签名 |
| `keygen` / `sign` / `verify` | Ed25519 签名工具 |
| `doctor` | 诊断配置、权限、状态与更新源 |

## 安全默认值

- 没有 SHA-256 的下载会被拒绝；GitHub API digest 或 `checksums.txt` 提供哈希。
- 配置 `security.public_key_file` 后，自托管清单必须带有效签名，GitHub 校验文件
  必须带有效签名。
- 强制 HTTPS；明文 HTTP 只允许回环地址，或显式开启
  `security.allow_insecure_http`。
- 重定向会重新检查协议、域名白名单和跳转次数。
- 解压拒绝绝对路径、路径穿越、逃逸符号链接、硬链接、设备文件和超限膨胀。
- 目录安装采用暂存 + 事务交换；保留上一版本用于回滚，中断后由日志自动恢复。
- HTTP API 必须携带 token，默认只监听回环地址。

请阅读 [安全模型](docs/security.md)。目录安装会管理整个目标目录：新版本中不存在、
且未被 `install.preserve` 匹配的文件会被删除。请指向应用自管目录，不要指向用户主目录。

## 文档

- [架构](docs/architecture.md)
- [配置参考](docs/configuration.md)
- [清单格式](docs/manifest.md)
- [集成指南](docs/integration.md)
- [自托管更新](docs/self-hosting.md)
- [GitHub Releases](docs/github-releases.md)
- [安全模型](docs/security.md)
- [故障排查](docs/troubleshooting.md)
- [架构决策记录](docs/adr)

## 开发

需要 Go 1.24+。

```bash
make test
make check
make build
```

## 许可证

MIT，见 [LICENSE](LICENSE)。
