# 架构与目录规划

## 目标

系统负责接收请求、调度账号、运行隔离的浏览器会话、保存必要状态，并通过 Matrix 向授权请求者发送结果。浏览器运行时应当使用每账号独立 Profile；“指纹”在本项目中首先表示会话隔离和实例标识，不作为规避第三方安全检测的功能目标。

## 逻辑组件

```text
Matrix Adapter ──> Request Service ──> Queue/Scheduler ──> Session Runner
       ^                  │                    │                 │
       └──── Status Notifier <──── State Store ┴──── Credential Store
```

- **Matrix Adapter**：验证房间/用户权限，解析固定命令，生成安全回复并调用
  Request Service。
- **Request Service**：创建请求、幂等检查、结果查询和取消；房间/用户授权由
  外部适配器执行。
- **Queue/Scheduler**：按全局并发上限和账号级租约分配工作，处理超时与重试；
  服务级限流尚未实现。
- **Session Runner**：管理浏览器 Worker 生命周期，绑定账号 Profile，报告运行结果。
- **Browser Worker**：运行在独立进程中，负责浏览器运行时和自动化适配边界；不能直接决定账号业务状态。
  当前 Node.js Worker 提供 deferred 生命周期替身和 headless-CDP runtime adapter；Rust helper 已提供同一协议的独立
  Wry/deferred 实现。原生 CI 对 Windows/macOS 执行 Wry 编译与打包检查，对 Linux
  amd64/arm64 另执行 WebKitGTK 的 X11/Wayland smoke；`cmd/service` 默认使用 Node
  backend，Rust helper 通过显式 `-browser-backend rust` 选择。
- **State Store**：持久化账号、请求、状态转换、租约、队列索引、审计、凭证
  密文和 Matrix 通知 outbox。
- **Credential Store**：提供加密凭证的读写，不向业务层暴露不必要的明文。
- **Status Notifier**：将领域事件转换为 Matrix 可读消息；当前通过注入的
  Sender 交付，不包含 Matrix 网络客户端。

## 建议目录树

以下是目标目录。`internal/account`、`internal/protocol`、`internal/browser`、
`internal/request`、`internal/queue`、`internal/credential`、`internal/matrix`、
`internal/observability`、`internal/store`、`internal/config` 和 `migrations/`
均已有实现与测试；`tests/`、`deploy/` 当前尚不存在，仍是规划目录。`cmd/service`
已把配置、Store、Request Service、Session Runner 和 Queue Scheduler 组装成持久化
调度入口；Matrix 网络客户端、凭证入口、通知 worker 和部署编排仍未接入。

```text
.
├── cmd/                         # 可执行程序入口
│   └── service/
├── browser-worker/              # Node.js Worker 协议、deferred 与 headless-CDP 适配器
├── browser-runtime/             # Rust helper；deferred/Wry 桌面 WebView
├── cmd/launcher/                # UI-neutral 最小启动器入口
├── internal/
│   ├── protocol/                # 控制服务与 Worker 的版本化协议
│   ├── account/                 # 已实现：账号实体与状态机
│   ├── browser/                 # Profile 生命周期与会话运行器
│   ├── request/                 # 已实现：请求创建、查询和取消服务
│   ├── credential/              # 凭证加密、轮换和访问接口
│   ├── queue/                   # 排队、租约、超时和重试
│   ├── matrix/                  # Matrix 适配器与事件格式化
│   ├── store/                   # 已实现：数据库与事务封装
│   ├── config/                  # 已实现：配置加载与路径派生
│   ├── observability/           # 已实现：脱敏观测事件边界；日志/指标接入规划中
│   └── launcher/                # release manifest、校验和组件/插件管理接口
├── migrations/                  # 已实现：bbolt schema 迁移
├── tests/                       # 规划中：集成测试与端到端测试（当前不存在）
├── configs/                     # 脱敏示例配置
├── deploy/                      # 规划中：容器、服务编排和运行时配置（当前不存在）
├── docs/                        # 项目文档与 ADR
├── scripts/                     # 开发和治理脚本
├── cr/                          # UGS 变更记录
├── .githooks/                   # UGS Git hooks
└── .ugs/                        # UGS 版本与策略清单
```

## 跨平台构建边界

GitHub Actions 是唯一的发布构建入口。当前支持四个目标：

- `windows-amd64`
- `linux-amd64`
- `linux-arm64`
- `darwin-arm64`

Go 控制服务使用 `CGO_ENABLED=0` 构建，Node.js Worker 以锁定的源码包随产物发布，Rust `chuzi-browser-runtime` helper 也随四个目标的 stage 发布。Windows/macOS stage 启用 Wry 桌面 WebView feature；Ubuntu 24.04 Linux stage 也启用 Wry 的 WebKitGTK feature，并在 X11 与 Wayland 图形会话中运行本地测试页。Windows/macOS helper 已由对应原生 CI 构建并打包；Linux amd64/arm64 还通过了 X11/Wayland smoke。`linux-arm64` 使用 GitHub `ubuntu-24.04-arm` 原生 ARM64 runner，Go、Node.js、WebKitGTK 编译和 smoke test 在 ARM64 主机执行；这仍不等同于无显示环境 headless 浏览器。上述 helper 构建和 smoke 测试不改变 `cmd/service` 默认仍使用 Node deferred Worker、且 Rust 只能显式选择的事实。

控制服务与 Worker 通过版本化 JSON Lines 协议通信。Worker 只报告浏览器运行事实；账号状态机、租约、重试和对外状态仍由 Go 控制面负责。

## 组件化发布与启动器边界

Nightly release 的最小安装单元是启动器。发布 stage 同时携带服务、Node
browser-worker 和 Rust desktop runtime，但 package 脚本还为四者生成独立组件归档，
让安装者可以按需安装而不必把所有运行资源放入本地安装。完整包的
`release-manifest.json` 记录每个组件的版本、依赖、入口和资源 SHA-256/大小；组件
包本身不是信任凭证，插件仍须通过未来的签名/权限策略审查。

`internal/launcher` 是 transport-neutral 的后台接口：`UpdateChecker` 负责查询更新，
`ResourceVerifier`/`ResourceRepairer` 负责完整性检查与修复，`ComponentManager` 和
`PluginManager` 负责安装状态，`SettingsStore` 保存启动行为设置。接口不假设 UI
技术、网络协议或插件进程模型；当前 `cmd/launcher` 只实现 manifest 读取和只读校验。

## 真实 WebView 与 headless 边界

Rust helper 继续通过现有 Go Worker 的版本化 JSON Lines 边界运行，不使用
Go/Rust FFI。helper 内部的运行时接口只返回 capability 和脱敏运行事实，业务
状态、租约、重试和 Profile 路径仍由 Go 控制面决定。

桌面 WebView 后端使用 `wry`，由 `tao`/平台事件循环承载：Windows 使用
WebView2，macOS 使用 WKWebView，Linux 使用 WebKitGTK。Wry 统一的是 WebView
创建和页面操作 API，不是一个跨平台 headless 浏览器。隐藏窗口仍需要有效的
用户图形会话、主线程和事件循环。

真正的 headless 后端单独建模，优先控制部署环境已安装的 Chromium/Edge（CDP
或 WebDriver），不由 Wry、WebKitGTK 或 WKWebView 假设提供。当前 Node
headless-CDP worker 已实现外部命令启动、动态 loopback CDP 端口、`/json/version`
发现与 endpoint 校验、服务派生 Profile 传递以及取消/关闭回收。该后端不会打包
完整 Chromium，也不会把桌面隐藏窗口标记为 headless；发现 CDP 后如果没有上层
自动化操作模型，worker 会报告 `configuration/automation_not_configured`，不会
报告业务成功。

首批平台基线如下：

| 后端 | 平台基线 | 首批承诺 | 当前状态 |
| --- | --- | --- | --- |
| Desktop WebView | Windows 10/11 | WebView2 Runtime 检测；visible/hidden 模式；服务派生 WebContext | 已集成；原生编译/打包检查通过（CR-0014-B） |
| Desktop WebView | macOS 11+，Apple Silicon | WKWebView；GUI session/run loop；当前 ephemeral store | 已集成；原生编译/打包检查通过（CR-0014-B） |
| Desktop WebView | Ubuntu 24.04 LTS amd64/arm64 | WebKitGTK 4.1；X11 与 Wayland GUI session | 已集成；原生编译及 X11/Wayland smoke 通过（CR-0014-C） |
| Headless browser | 部署环境提供 Chromium/Edge | Node headless-CDP worker；动态 loopback 端口、独立 Profile、CDP discovery 和有界进程回收 | CR-0017 第一增量已集成；业务自动化适配器仍未实现 |

CR-0014-A 建立 Rust helper、capability/error 契约和本地协议测试页路径；
CR-0014-B 已在 Windows/macOS target-gated 接入真实 Wry WebView，CR-0014-C 已在
Ubuntu 24.04 target-gated 接入 WebKitGTK，并在 X11/Wayland GUI session 中只加载
内嵌本地测试页。CR-0014-B/C 的证据覆盖 helper 的原生构建、打包，以及 Linux
X11/Wayland smoke 路径；Windows/macOS 的构建证据不表示 GUI 运行时 smoke 已完成，
也不表示 Go 服务默认选择该 helper；服务入口只有显式选择 Rust backend 时才会
启动它；headless-CDP worker 已在 CR-0017 第一增量中接入，但业务自动化适配器
和生产浏览器操作仍未接入。

## 关键边界

浏览器模块不能直接决定对外业务状态；它只能报告运行事实，由账号状态机根据事件和持久化数据完成状态转换。Matrix 模块不能直接操作凭证，只能提交请求和消费脱敏后的领域事件。

## Matrix 适配与通知契约

`internal/matrix` 只接收已抽取的 Matrix event 字段，先检查房间/用户白名单，
再解析固定命令并调用 Request Service。请求创建时绑定通知房间，普通用户的
`status`/`cancel` 只能访问同一房间；管理员跨房间访问必须由策略显式授予。
适配器和 notifier 都只记录分类错误与脱敏标识，不能把命令正文、凭证、房间
原始 ID 或内部堆栈写入观测事件。

状态事件在请求带有通知房间时由 Store 在同一事务至多写入一条
`matrix_notifications` outbox 记录。Notifier 使用短期 claim、稳定 event ID
和可注入 Sender 进行发送；网络失败不会删除记录，重试或服务重启会重新使用
同一 event ID。当前边界不实现生产 Matrix 网络客户端。

## Session Runner 生命周期契约

Go Session Runner 为每次 queue claim 派生并独占账号级 Profile 目录（必要时创建），并通过版本化
JSON Lines 协议驱动一个独立 Worker：先 `hello` 握手，再发送
`session_start`，等待 `session_started` 及 `session_succeeded`、
`session_failed` 或 `session_cancelled`。取消、超时、租约心跳失败和父进程
退出都会进入有界的 `session_cancel`/`shutdown` 流程；无法确认的进程退出只
返回脱敏的 transient runtime fact。

Worker 只能报告这些运行事实，不能写入账号状态或审计记录。当前 Node Worker
继续提供协议和 deferred-browser failure/synthetic lifecycle 模式，并提供独立的
headless-CDP 进程边界；Rust helper
提供同一协议的独立进程边界；Windows/macOS/Linux 的 Wry feature 已显式启用，
Linux WebKitGTK 同时支持 X11 与 Wayland GUI session。`ProcessFactory` 会启动
调用方指定的可执行文件和脚本，或启动不带脚本参数的 helper；`cmd/service` 默认
仍指定 Node deferred Worker，`-browser-backend rust` 选择 Rust helper，
`-browser-backend headless` 选择 Node headless-CDP worker，并通过
`-headless-browser-command` 指定部署环境已安装的 Chromium/Edge 可执行文件。
headless worker 的 session handle 只代表已验证的运行时连接，不代表业务操作已完成。

## Credential Store 生命周期契约

`internal/credential` 使用部署环境提供的 Keyring，以 AES-GCM 加密每个账号的
凭据 payload。bbolt 只保存密文、nonce、key ID、版本和时间戳；密钥不进入
数据库、配置示例或 Git。调用方只能通过 `Use`/`Access` 回调获得一次性工作缓冲，
回调返回后缓冲会被清零，业务层不能通过元数据读取长期明文。

凭据写入、访问、密钥轮换和撤销都与 metadata-only audit 在同一存储事务中
提交。撤销会先请求可选的 SessionInvalidator 停止账号会话，确认失败则不写入
撤销状态；成功后清除 nonce 和密文并保持不可恢复状态。轮换需要 Keyring 同时
保留旧 key ID 和当前 key。Credential Store 不决定账号业务状态，也不直接与
Matrix 或浏览器 Worker 通信。
