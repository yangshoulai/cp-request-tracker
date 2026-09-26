# 开发与发布

## 工具链

- Go 1.26 与目标平台 CGO 编译器
- CPA v7.3.17 SDK
- `modernc.org/sqlite` SQLite 驱动
- Linux 发布包在 manylinux2014 容器中构建，保持 glibc 2.17 兼容基线

## 开发构建

```bash
cd plugin
go mod tidy
go build -buildmode=c-shared -o cpa-request-tracker.so .
```

插件实现使用 CPA 标准动态库 ABI。修改 UsageRecord、Host Auth List 或 Management Route 结构时，应先对照 CPA 锁定 SDK 版本的类型定义。

## GitHub 安装源准备

当前仓库地址为 `https://github.com/yangshoulai/cp-request-tracker`。如果以后迁移仓库，需要同步更新：

- `plugin/go.mod` 的 module 路径（可根据仓库布局调整）
- `plugin/main.go` 中插件 GitHub repository metadata
- `registry.json` 的 `author`、`repository` 和 `homepage`
- README 中自定义 registry URL

CPA 插件管理器通过自定义插件商店索引读取本项目 registry；若要进入官方默认商店，需要向 CLIProxyAPI-Plugins-Store 另提 PR。

## 版本发布

1. 更新 `pluginVersion` 默认版本，并提交变更。
2. 推送与版本对应的 `vX.Y.Z` 标签会构建并发布 GitHub Release。产物按 CPA 规定的 `<plugin-id>_<version>_<goos>_<goarch>.zip` 命名，并包含 `checksums.txt`。当前目标为 Linux amd64/arm64、macOS amd64/arm64 和 Windows amd64。
3. 发布 tag 会覆盖二进制中的插件 metadata 版本，并生成插件商店可安装的正式 Release。

工作流需要仓库的 `GITHUB_TOKEN` 默认 contents 写权限，不需要配置额外凭据。
