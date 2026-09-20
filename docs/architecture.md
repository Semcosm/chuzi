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
  当前 Node.js Worker 提供 deferred 生命周期替身和 headless-CDP runtime adapter；原生
  客户端通过 Core API 工作，不依赖浏览器 WebView runtime。
- **State Store**：持久化账号、请求、状态转换、租约、队列索引、审计、凭证
  密文和 Matrix 通知 outbox。
- **Credential Store**：提供加密凭证的读写，不向业务层暴露不必要的明文。
- **Status Notifier**：将领域事件转换为 Matrix 可读消息；通知 worker 通过持久化
  outbox 和稳定 event ID 重试交付。生产拼装使用 `internal/matrix` 的 HTTP Client，
  仍可注入 Sender 做离线测试。

## 建议目录树

以下是目标目录。`internal/account`、`internal/protocol`、`internal/browser`、
`internal/request`、`internal/queue`、`internal/credential`、`internal/matrix`、
`internal/observability`、`internal/store`、`internal/config`、`internal/coreapi`、
`internal/core` 和 `migrations/` 均已有实现与测试。`cmd/service` 已把配置、Store、凭证服务、
Request Service、Session Runner、Queue Scheduler、可选 Matrix 同步/通知 worker 和
健康端点组装成持久化调度入口；`deploy/` 提供 systemd 与 Secret 边界示例。

```text
.
├── cmd/                         # 可执行程序入口
│   └── service/
├── browser-worker/              # Node.js Worker 协议、deferred 与 headless-CDP 适配器
├── cmd/launcher/                # UI-neutral 启动器 CLI 入口
├── ui/                          # 原生平台客户端
│   ├── windows/                  # 已有 Rust + Slint 首个客户端
│   ├── macos/                    # SwiftUI/AppKit 客户端（后续 CR）
│   └── linux/                    # GTK 客户端（后续 CR）
├── internal/
│   ├── coreapi/                 # chuzi.core/v1 DTO、API 和稳定错误分类
│   ├── core/                    # Core 编排 facade，不拥有状态机或存储
│   ├── coretransport/            # chuzi.core/v1 本地 JSONL IPC（Unix socket/named pipe）
│   ├── coretest/                # Core 跨模块测试替身（不参与生产拼装）
│   ├── protocol/                # 控制服务与 Worker 的版本化协议
│   ├── account/                 # 已实现：账号实体与状态机
│   ├── browser/                 # Profile 生命周期与会话运行器
│   ├── automation/              # 业务自动化适配器契约与 JSONL 协议
│   ├── plugin/                  # 原生/Wine 插件进程启动与回收边界
│   ├── request/                 # 已实现：请求创建、查询和取消服务
│   ├── credential/              # 凭证加密、轮换和访问接口
│   ├── queue/                   # 排队、租约、超时和重试
│   ├── matrix/                  # Matrix 适配器与事件格式化
│   ├── store/                   # 已实现：数据库与事务封装
│   ├── config/                  # 已实现：配置加载与路径派生
│   ├── observability/           # 已实现：结构化脱敏日志、轮转、指标和事件 Sink
│   └── launcher/                # release manifest、校验和组件/插件管理接口
├── migrations/                  # 已实现：bbolt schema 迁移
├── tests/                       # 跨模块集成测试与端到端测试
├── configs/                     # 脱敏示例配置
├── deploy/                      # systemd 与 Secret/恢复运维示例
├── docs/                        # 项目文档与 ADR
├── scripts/                     # 开发和治理脚本
├── cr/                          # UGS 变更记录
├── .githooks/                   # UGS Git hooks
└── .ugs/                        # UGS 版本与策略清单
```

平台 UI 通过 Stable API Boundary 使用 Core 和 `cmd/launcher` 提供的能力。Windows
首阶段采用 Rust + Slint，macOS 采用 SwiftUI（必要时使用 AppKit），Linux 采用 GTK；
三者分别遵循目标平台的默认控件、窗口行为、无障碍和主题机制。客户端只负责视图、
交互和平台生命周期，不读取 bbolt、凭证或 Profile，也不复制下载、校验、锁、插件
信任和回滚策略。Windows 首个客户端已落在 `ui/windows`；macOS/Linux 的实现 CR 仍需
分别明确 API 版本、打包方式和运行时支持范围。

`internal/coreapi` 定义 `chuzi.core/v1` 的 transport-neutral DTO、命令接口和稳定
错误代码。`internal/core` 将 Request Service、Store 的只读投影和通知/审计查询
编排成该接口；它不返回 `store.Request`、bbolt 对象、凭证、Profile 路径或 UI 类型。
其中 `PipelineRunner` 把 Session Runner、Credential.Use 和
`automation.Adapter.Execute` 组合为一个 `queue.Runner`，只把脱敏运行事实交还
Queue。`internal/coretest` 提供确定性时钟/ID、凭证、Worker、自动化和 Matrix
Sender 替身，供跨模块测试复用；这些替身不参与生产拼装。
`tests/core` 使用临时 Store 验证请求提交、账号/请求查询、取消、脱敏结果、领域事件、
完整假实现链路和通知状态；`internal/coretransport` 和原生客户端只能依赖这层契约。

`internal/coretransport` 是原生客户端的进程边界。请求和响应使用 JSONL envelope，首个
请求必须是 `hello` 并协商 `chuzi.core/v1`；方法覆盖 `submit_request`、请求/账号查询、
`cancel_request`、结果、事件和通知查询。服务端按连接隔离请求上下文，支持按请求 ID 的
并发响应和传输取消，错误只返回稳定 `coreapi.Code`。Unix endpoint 从服务 `data_dir`
派生并设为 `0600`，启动时不会覆盖仍在使用的 socket；Windows 使用 `go-winio` named
pipe 和 owner-only SDDL。客户端不接受任意 endpoint 作为业务参数，UI 只能使用部署派生的
本地地址。

## 业务自动化适配器与插件

`internal/browser` 只管理浏览器 Worker 的生命周期；业务自动化操作必须通过
`internal/automation.Adapter` 执行。该接口描述能力、服务派生的会话标识、操作、
取消、关闭和稳定错误分类，适配器只能返回脱敏运行事实，不能直接写账号状态、
队列或审计记录。

需要独立进程时，`internal/plugin` 通过版本化 JSONL 协议承载同一接口。原生进程和
Wine 进程都使用参数数组启动，不经过 shell；Wine prefix 必须是服务派生的绝对
路径。平台差异只存在于进程启动后端，不能扩散到 BetterGI 等业务协议中。

`plugins/bettergi` 是 BetterGI 通信插件的预留目录。它不包含 BetterGI 自动化本体
或二进制，只负责将 `chuzi.adapter/v1` 映射到 BetterGI 的公开集成接口。BetterGI
具体协议、版本兼容性和 Wine 运行要求必须在独立实现 CR 中以证据确认；当前不能
把该目录解释为已经支持 BetterGI 自动化。

## 跨平台构建边界

GitHub Actions 是唯一的发布构建入口。当前支持四个目标：

- `windows-amd64`
- `linux-amd64`
- `linux-arm64`
- `darwin-arm64`

Go 控制服务使用 `CGO_ENABLED=0` 构建，Node.js Worker 以锁定的源码包随四个目标的 stage 发布。
`linux-arm64` 使用 GitHub `ubuntu-24.04-arm` 原生 ARM64 runner；headless 浏览器始终由部署环境提供，
不由发布包下载或打包。

控制服务与 Worker 通过版本化 JSON Lines 协议通信。Worker 只报告浏览器运行事实；账号状态机、租约、重试和对外状态仍由 Go 控制面负责。

## 组件化发布与启动器边界

Nightly release 的最小安装单元是启动器。发布 stage 同时携带服务和 Node
browser-worker，package 脚本还为三者生成独立组件归档，
让安装者可以按需安装而不必把所有运行资源放入本地安装。完整包的
`release-manifest.json` 记录每个组件的版本、依赖、入口和资源 SHA-256/大小；组件
包本身不是信任凭证，插件仍须通过未来的签名/权限策略审查。

`internal/launcher` 是 transport-neutral 的后台接口：`UpdateChecker` 负责查询更新，
`ResourceVerifier`/`ResourceRepairer` 负责完整性检查与修复，`ComponentManager` 和
`PluginManager` 负责安装状态，`SettingsStore` 保存启动行为设置，文件锁和
`ServiceController` 约束本地操作并管理由调用方持有的前台服务进程。接口不假设 UI
技术、网络协议或平台服务管理器。CR-0022 与 CR-0026 提供了本地 manifest source、
原子资源修复、组件依赖安装、插件归档安全解包、显式 signer 信任、原子设置持久化、
跨进程锁和可取消进度事件；CR-0027 增加了 `ReleaseIndex`、HTTPS 同源归档下载、
临时文件原子落盘和首次运行初始化状态。`cmd/launcher` 只在显式提供
`-release-index` 时联网。原生平台客户端通过 Stable API Boundary 调用该
CLI/Core 能力，接收 UI 请求结果、脱敏进度和分类错误；更新候选、组件管理和插件
信任操作仍由 Go 后台校验并执行，文件、下载、校验、锁、插件信任和回滚策略不复制
到任何平台 UI。

行为设置文件缺失时使用关闭自动变更的默认值，写入使用 0600 临时文件和原子替换。
修改安装目录或设置前应先持有 `.chuzi/launcher.lock`；锁不会自动打破，发现遗留锁时
必须先确认记录中的进程已退出。`ProcessServiceController` 只负责当前调用方生命周期
内的 shell-free 子进程，生产部署仍由 systemd、launchd 或 Windows 服务管理器负责。

## Headless 浏览器边界

真正的 headless 后端由部署环境提供 Chromium/Edge，当前仅保留 Node headless-CDP
worker。它使用动态 loopback CDP 端口、服务派生 Profile、`/json/version` endpoint
校验以及有界取消/关闭回收；不会打包完整 Chromium，也不会把桌面隐藏窗口当作
headless。首个真实适配器固定检查云原神已授权会话，并只返回脱敏页面事实，由 Core
evaluator 映射为账号结果；endpoint discovery 不代表业务成功。

## 关键边界

浏览器模块不能直接决定对外业务状态；它只能报告运行事实，由账号状态机根据事件和持久化数据完成状态转换。Matrix 模块不能直接操作凭证，只能提交请求和消费脱敏后的领域事件。

## Matrix 适配与通知契约

`internal/matrix` 提供 transport-neutral 适配器、Matrix HTTP Client 和同步网关。
适配器只接收已抽取的 Matrix event 字段，先检查房间/用户白名单，
再解析固定命令并调用 Request Service。请求创建时绑定通知房间，普通用户的
`status`/`cancel` 只能访问同一房间；管理员跨房间访问必须由策略显式授予。
适配器和 notifier 都只记录分类错误与脱敏标识，不能把命令正文、凭证、房间
原始 ID 或内部堆栈写入观测事件。

状态事件在请求带有通知房间时由 Store 在同一事务至多写入一条
`matrix_notifications` outbox 记录。Notifier 使用短期 claim、稳定 event ID
和可注入 Sender 进行发送；网络失败不会删除记录，重试或服务重启会重新使用
同一 event ID。HTTP Client 只实现必要的 Client-Server API 调用；access token 由
部署环境注入，不进入配置、日志或错误文本。

## Session Runner 生命周期契约

Go Session Runner 为每次 queue claim 派生并独占账号级 Profile 目录（必要时创建），并通过版本化
JSON Lines 协议驱动一个独立 Worker：先 `hello` 握手，再发送
`session_start`，等待 `session_started` 及 `session_succeeded`、
`session_failed` 或 `session_cancelled`。取消、超时、租约心跳失败和父进程
退出都会进入有界的 `session_cancel`/`shutdown` 流程；无法确认的进程退出只
返回脱敏的 transient runtime fact。

Worker 只能报告这些运行事实，不能写入账号状态或审计记录。当前 Node Worker
继续提供协议和 deferred-browser failure/synthetic lifecycle 模式，并提供独立的
headless-CDP 进程边界。`ProcessFactory` 会启动
调用方指定的可执行文件和脚本；`cmd/service` 默认仍指定 Node deferred Worker，
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
