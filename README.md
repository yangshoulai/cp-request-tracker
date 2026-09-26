# CPA Request Tracker

一个 CLIProxyAPI（CPA）原生插件：记录每次模型调用的账号类型、账号邮箱、时间、耗时、模型和思考程度，并从 CPA 下载与调用关联的请求日志。

插件只保存调用元数据，不读取或保存请求体和响应体。日志正文仍由 CPA 按自身 `request-log` 配置记录。

## 功能

- 从 CPA Usage Plugin 接收已完成调用并写入 SQLite。
- 在 CPA 插件菜单中提供带分页、筛选的调用表格。
- 账号类型、账号邮箱、模型和思考程度筛选项从 CPA Management API 动态读取，并按账号类型和模型联动；账号类型按 `Provider`（如 `codex`、`xai`）记录。
- 在每条记录中查看请求头、上游 API 请求头和上游 API 响应头，展示 CPA 日志实际返回的头值。
- 从同一行直接下载 CPA 请求日志。
- 尝试读取浏览器 `localStorage` 的 `cli-proxy-auth`，支持 `enc::v1::` 和 `enc::v2::` 格式；也可手动输入 Management key。
- 默认保留 7 天请求元数据；CPA 插件配置可设为 1 到 3650 天。

## 安装

### 通过 CPA 插件管理器

CPA 插件商店支持自定义 registry。将本仓库推送到 GitHub 后，向 CPA 的 `plugins.store-sources` 添加 registry 地址：

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/yangshoulai/cp-request-tracker/main/registry.json
```

在 CPA 管理面板的插件管理器中刷新自定义来源并安装 `CPA Request Tracker`。推送 `v*` 标签会自动构建 Linux amd64/arm64、macOS amd64/arm64 和 Windows amd64 版本，并创建包含 CPA 可识别 ZIP 和 `checksums.txt` 的 GitHub Release，供插件商店下载。Linux 插件在 manylinux2014 环境中构建，以兼容 glibc 2.17 基线。

若要出现在 CPA 官方默认插件商店，需另行向官方插件商店提交 registry PR；单独发布本仓库不会自动修改官方商店索引。

### 启用 CPA 请求日志

在 CPA 配置中开启请求日志，再安装或启用插件：

```yaml
request-log: true
plugins:
  enabled: true
  configs:
    cpa-request-tracker:
      enabled: true
      priority: 1
      retention_days: 7
```

`retention_days` 默认 7，允许 1–3650。插件收到新用量记录或读取列表时清除超出期限的元数据。CPA 的请求日志文件仍按 CPA 自己的日志清理设置管理。

## 使用

从 CPA 面板打开「调用记录」。插件会尝试从当前面板 origin 的 `cli-proxy-auth` 中读取 Management key；读取失败、密钥未被持久化或无效时，可在页面输入框中手动输入。插件只将密钥保存在当前标签页的 `sessionStorage`，并向承载插件页面的 CPA origin 发出授权请求。

连接成功后，筛选栏会读取 CPA 的账号清单、账号实际可用模型和模型定义。选择账号类型后，模型列表会缩小到该类型；选择模型后，思考程度会显示该模型支持的 `thinking.levels`。CPA 没有返回模型定义时，模型仍可从账号清单中显示，但对应思考程度下拉框会保持为空。

点击记录行的「下载日志」后，插件使用该记录的 CPA `TraceID` 请求：

```text
GET /v0/management/request-log-by-id/:id
```

日志下载需要 CPA 已开启 `request-log` 并且对应日志文件仍在 CPA 日志目录中。

点击「查看头信息」会使用同一个 TraceID 读取 CPA 日志，只展示 `=== HEADERS ===`、`=== API REQUEST n ===` 和 `=== API RESPONSE n ===` 中的头信息，不展示请求体或响应体。CPA 记录重试时会按 `n` 分组显示多个上游请求或响应。插件不会额外修改头值；如果 CPA 写日志时已经将敏感值写成掩码，插件无法恢复原值。

## 数据与密钥

- SQLite 路径：`<系统用户配置目录>/CLIProxyAPI/plugins/cpa-request-tracker/calls.sqlite`。
- 数据库只保存调用 ID、trace ID、账号引用、类型、时间、耗时、模型、思考程度和结果状态，不保存请求或响应正文。
- 邮箱从 CPA 当前账号列表回填并缓存。若某账号在插件首次读取账号信息前已被删除，CPA 就无法再提供其邮箱。
- Management key 为浏览器本地轻度混淆格式，混淆不等于加密。跨源嵌入时浏览器会隔离父面板的 localStorage，此时使用页面内手动输入。

## 本地构建

需要与目标 CPA 平台匹配的 Go/CGo 工具链。以当前系统为例：

```bash
cd plugin
go build -buildmode=c-shared -o cpa-request-tracker.so .
```

标准插件的动态库必须按目标 CPA 平台编译。向 GitHub 推送 `v*` 标签即可触发多平台构建与 GitHub Release 打包。

## 兼容性

当前 SDK 按 CLIProxyAPI v7.3.17 编译。插件依赖 CPA 的 Usage Plugin、Management API、Host Auth List 回调和按 TraceID 下载请求日志功能。

## 许可

MIT，见 [LICENSE](LICENSE)。
