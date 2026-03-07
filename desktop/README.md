# Any2API Desktop

单一 Tauri 桌面端，用来复用统一的 `to-api` 管理后台。

## 设计目标

- 只做一个桌面端，不按 Go / Python / Rust 分开
- 默认连接 `http://127.0.0.1:8099/admin`
- 通过嵌入 `/admin` 页面直接复用现有 Web 管理台
- 未来其它语言版本只要实现同样的 `/admin` 与 `/admin/api/*` 协议即可接入

## 本地开发

1. 先启动后端，例如 `any2api/go`（Any2API Go 后端）
2. 进入当前目录执行 `npm install`
3. 运行 `npm run tauri dev`

首次打开后，可以在桌面端左侧修改后端地址。
