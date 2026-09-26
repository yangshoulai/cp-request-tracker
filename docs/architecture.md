# 架构说明

## 数据流

```text
CPA 请求完成
    └─ Usage Plugin 回调
         └─ 只抽取调用元数据
              └─ SQLite（默认保留 7 天）

CPA 插件菜单
    └─ /v0/resource/plugins/cpa-request-tracker/page
         └─ 浏览器呈现单页
              └─ /v0/management/plugins/cpa-request-tracker/records：分页记录和账号信息
                   ├─ /v0/management/auth-files：读取账号类型
                   ├─ /v0/management/auth-files/models?name=...：读取账号实际可用模型
                   └─ /v0/management/model-definitions/{channel}：读取模型思考级别
              └─ 按 TraceID 调用 CPA 请求日志下载接口
```

## 持久化字段

`calls` 表保留每次插件用量回调中的 `RequestID`、`TraceID`、`AuthID`、`AuthIndex`、账号类型、请求时间、耗时、模型、推理强度、失败状态和 HTTP 错误码。`accounts` 表缓存由 CPA Host Auth List 回调提供的账号邮箱。

请求/响应正文、API key、Management key 均不写入 SQLite。

## Trace ID 与日志文件

`UsageRecord.RequestID` 是一次模型执行 ID；CPA 请求日志的文件名后缀使用入站 HTTP request ID。插件保存 `UsageRecord.TraceID`，并传给 CPA `/v0/management/request-log-by-id/:id`。不能用模型执行 `RequestID` 替代它。

## 账号邮箱

CPA 的 Usage Plugin 记录提供认证账号 ID、索引和类型，但没有邮箱字段；Management 回调可以读取当前认证账号清单。插件在读取表格数据时同步邮箱，并通过稳定账号 ID/索引关联历史调用。若调用记录产生后，账号在插件从 CPA 获取其邮箱前就已删除，则无法补取邮箱。

## Management key 发现

页面按 CPA Manager Plus 的实现解码 `enc::v1::`（salt、当前 host 和 User-Agent）与 `enc::v2::`（salt、版本、当前 host），读取 `cli-proxy-auth` 持久化 state 中的 `managementKey`。该值是可逆混淆而非安全加密。浏览器同源策略不允许 CPA 插件 iframe 读取跨源父面板的 localStorage；跨源场景提供手动输入。

## 发布

`registry.json` 使用 CPA 插件商店的 GitHub Release 安装类型。推送 `v*` 标签时，GitHub Actions 针对 macOS arm64/amd64、Linux arm64/amd64、Windows amd64 构建并上传版本化 ZIP 与 SHA-256 清单。Linux 产物使用 CPA 上游相同的 manylinux2014 glibc 2.17 基线。添加到 CPA 官方默认商店需要官方 registry 仓库单独审核合并。
