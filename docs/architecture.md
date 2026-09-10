# 架构与目录规划

## 目标

系统负责接收请求、调度账号、运行隔离的浏览器会话、保存必要状态，并通过 Matrix 向授权请求者发送结果。浏览器运行时应当使用每账号独立 Profile；“指纹”在本项目中首先表示会话隔离和实例标识，不作为规避第三方安全检测的功能目标。

## 逻辑组件

```text
Matrix Adapter ──> Request Service ──> Queue/Scheduler ──> Session Runner
       ^                  │                    │                 │
       └──── Status Notifier <──── State Store ┴──── Credential Store
```

- **Matrix Adapter**：验证房间/用户权限，解析命令，发送状态事件。
- **Request Service**：创建请求、幂等检查、权限校验和结果查询。
- **Queue/Scheduler**：按全局、账号和服务限制分配并发，处理超时与重试。
- **Session Runner**：管理浏览器 Worker 生命周期，绑定账号 Profile，报告运行结果。
- **Browser Worker**：运行在独立 Node.js 进程中，负责浏览器自动化适配；不能直接决定账号业务状态。
- **State Store**：持久化账号、请求、状态转换和审计信息。
- **Credential Store**：提供加密凭证的读写，不向业务层暴露不必要的明文。
- **Status Notifier**：将领域事件转换为 Matrix 可读消息。

## 建议目录树

以下是目标目录。当前已实现 `internal/account` 的纯领域核心、
`internal/protocol` 协议边界，以及阶段二的配置、存储和迁移边界；Matrix
适配和观测边界已在 `internal/matrix`、`internal/observability` 创建，其他
运行时目录随着路线图推进再创建。

```text
.
├── cmd/                         # 可执行程序入口
│   └── service/
├── browser-worker/              # Node.js Worker 协议与浏览器适配边界
├── internal/
│   ├── protocol/                # 控制服务与 Worker 的版本化协议
│   ├── account/                 # 已实现：账号实体与状态机
│   ├── browser/                 # Profile 生命周期与会话运行器
│   ├── credential/              # 凭证加密、轮换和访问接口
│   ├── queue/                   # 排队、租约、超时和重试
│   ├── matrix/                  # Matrix 适配器与事件格式化
│   ├── store/                   # 已实现：数据库与事务封装
│   ├── config/                  # 已实现：配置加载与路径派生
│   └── observability/           # 日志、指标、审计
├── migrations/                  # 已实现：bbolt schema 迁移
├── tests/                       # 集成测试与端到端测试
├── configs/                     # 脱敏示例配置
├── deploy/                      # 容器、服务编排和运行时配置
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

Go 控制服务使用 `CGO_ENABLED=0` 构建，Node.js Worker 以锁定的源码包随产物发布。`linux-arm64` 当前由 Linux runner 交叉编译，暂不宣称原生 ARM64 浏览器运行覆盖。引入 Playwright、Chromium 或其他原生依赖前，必须增加对应架构的运行 smoke test 和变更记录。

控制服务与 Worker 通过版本化 JSON Lines 协议通信。Worker 只报告浏览器运行事实；账号状态机、租约、重试和对外状态仍由 Go 控制面负责。

## 关键边界

浏览器模块不能直接决定对外业务状态；它只能报告运行事实，由账号状态机根据事件和持久化数据完成状态转换。Matrix 模块不能直接操作凭证，只能提交请求和消费脱敏后的领域事件。

## Matrix 适配与通知契约

`internal/matrix` 只接收已抽取的 Matrix event 字段，先检查房间/用户白名单，
再解析固定命令并调用 Request Service。请求创建时绑定通知房间，普通用户的
`status`/`cancel` 只能访问同一房间；管理员跨房间访问必须由策略显式授予。
适配器和 notifier 都只记录分类错误与脱敏标识，不能把命令正文、凭证、房间
原始 ID 或内部堆栈写入观测事件。

状态事件由 Store 在同一事务写入 `matrix_notifications` outbox。Notifier 使用
短期 claim、稳定 event ID 和可注入 Sender 进行发送；网络失败不会删除记录，
重试或服务重启会重新使用同一 event ID。当前边界不实现生产 Matrix 网络客户端。

## Session Runner 生命周期契约

Go Session Runner 为每次 queue claim 生成账号级 Profile 目录，并通过版本化
JSON Lines 协议驱动一个独立 Worker：先 `hello` 握手，再发送
`session_start`，等待 `session_started` 及 `session_succeeded`、
`session_failed` 或 `session_cancelled`。取消、超时、租约心跳失败和父进程
退出都会进入有界的 `session_cancel`/`shutdown` 流程；无法确认的进程退出只
返回脱敏的 transient runtime fact。

Worker 只能报告这些运行事实，不能写入账号状态或审计记录。当前 Node Worker
仅提供协议和 deferred-browser failure/synthetic lifecycle 模式，真实浏览器
运行时必须在后续独立变更中引入并验证。

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
