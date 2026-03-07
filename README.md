# Any2API

Any2API 是一个多 provider 的 `to-api` 网关项目，用统一入口桥接 Cursor、Kiro、Grok、Orchids，并同时提供：

- OpenAI 兼容接口：`/v1/chat/completions`
- Anthropic 兼容接口：`/v1/messages`
- 管理接口：`/api/admin/*` / `/admin/api/*`（不同语言版本略有差异）

## 适合做什么

- 把多个上游模型/平台统一暴露成一套 API
- 在 Go / Python / Rust 之间对比实现策略
- 继续扩展账号池、鉴权、流式转发和管理后台

## 当前内置 provider

- Cursor
- Kiro
- Grok
- Orchids

## 仓库结构

- `any2api/go`：Any2API Go 后端，当前最完整，带内置 Web Admin
- `any2api/python`：Any2API Python 后端，提供兼容 API 与管理接口
- `any2api/rust`：Any2API Rust 后端，提供兼容 API 与管理接口
- `any2api/desktop`：Any2API Desktop，统一 Tauri 管理端

## 当前状态

### Go

- 最完整的后端实现
- 提供 `/admin` Web 管理台
- 对 provider capability / execution interface 的抽象最完整
- Cursor 已有真实上游链路，支持流式与非流式

### Python

- 提供健康检查、模型列表、OpenAI/Anthropic 兼容接口
- 提供管理登录、会话与配置接口
- 适合作为轻量实现和协议对照版本

### Rust

- 提供健康检查、模型列表、OpenAI/Anthropic 兼容接口
- 已补齐 Cursor / Kiro / Grok / Orchids 的真实 provider 主链路
- 当前 `stream=true` 仍以服务端包装 SSE 为主，不是 Go 那种 provider 原生流式透传

## 快速启动

### Go

1. `cd any2api/go`
2. `go run ./cmd/server`
3. 默认地址：`http://127.0.0.1:8099`
4. Admin 页面：`http://127.0.0.1:8099/admin`

### Python

1. `cd any2api/python`
2. `python3 server.py`
3. 默认地址：`http://127.0.0.1:8100`

### Rust

1. `cd any2api/rust`
2. `cargo run`
3. 默认地址：`http://127.0.0.1:8101`

## 通用接口

- `GET /health`
- `GET /v1/models`
- `POST /v1/chat/completions`
- `POST /v1/messages`

## 管理能力

- 默认管理密码：`changeme`
- 运行时配置会持久化到各语言目录下的 `data/admin.json`
- Go 版带内置管理页面
- Python / Rust 当前以管理 API 为主

常见环境变量（为了兼容现有部署，前缀暂时保留为旧名）：

- `NEWPLATFORM2API_API_KEY`
- `NEWPLATFORM2API_ADMIN_PASSWORD`
- `NEWPLATFORM2API_DEFAULT_PROVIDER`
- `NEWPLATFORM2API_DATA_DIR`

## Desktop

`any2api/desktop` 是 Any2API 的统一 Tauri 管理端，目标是复用同一套管理协议，而不是为 Go / Python / Rust 分别维护三套桌面 UI。

更多说明见：`any2api/desktop/README.md`

## 改名说明

项目品牌名已调整为 **Any2API**，但以下兼容项目前仍保留：

- 仓库目录现已调整为 `any2api`
- 现有环境变量前缀仍为 `NEWPLATFORM2API_`
- 现有部分 cookie / 本地存储 key 维持旧值，避免直接破坏现有登录态与本地配置

## 下一步建议

如果你要继续把它往生产可用推进，建议优先处理：

1. 更完整的 provider 原生流式转发
2. 多账号池与故障切换
3. 更细的错误映射与限流
4. 更完整的管理后台能力
5. 真实上游鉴权刷新与可观测性
