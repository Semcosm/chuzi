# 长期演进路线图

## 目标与使用方式

本路线图描述 chuzi 从跨平台基础实现到可运维服务的演进顺序。它用于
约束依赖和验收门槛，不替代 `docs/architecture.md`、
`docs/account-state-machine.md`、`docs/security.md` 和 `docs/operations.md`
中的具体契约。

每个非平凡阶段都必须通过独立的 UGS Change Request、签名提交、Pull
Request、required checks 和集成记录完成。路线图只在对应代码、测试和
运行证据已经合并后更新状态，不预先宣称未来能力已经实现。

## 已完成基线

| 领域 | 状态 | 证据 |
| --- | --- | --- |
| 仓库治理 | 已完成 | CR-0001：UGS standard profile、签名和保护分支 |
| 跨平台构建 | 已完成 | CR-0002：Windows amd64、Linux amd64、Linux arm64、Darwin arm64 |
| CI 运行时治理 | 已完成 | CR-0003：Node.js 24-compatible Actions 和 Go cache warning 清理 |
| Go 控制面与 Node Worker | 基础边界已完成（持久化调度入口已组装） | 版本化 JSON Lines 协议、Worker 生命周期 smoke test、配置/Store/Queue/Session Runner 入口组装；默认仍使用 Node deferred Worker |
| 账号领域核心 | 已完成 | CR-0005：纯 Go 状态机、幂等事件、审计、重试策略和租约原语 |
| 业务运行时 | 边界已完成（持久化调度已组装） | 状态存储、队列、凭证和 Matrix transport-neutral 边界已接入并有测试；入口可恢复/调度请求，真实账号浏览器、生产传输和凭证入口仍未接入 |

当前四个平台的构建通过不代表四个平台都具备真实账号浏览器自动化覆盖。
`linux-arm64` 的 Go 和 Node.js 构建在 GitHub `ubuntu-24.04-arm` 原生 ARM64 runner
执行；真正 headless 仍需部署环境提供 Chromium/Edge 并单独验证。

### 当前 release 基线

Nightly release 是首次可交付流程：GitHub Actions 定时构建四个目标，不打 tag、不创建
GitHub Release，只上传限期 Actions artifacts。版本包含 run number 和短 commit hash；
产物已拆分为 launcher、服务和浏览器 Worker 组件，并携带资源校验
manifest/index。当前交付的是 UI-neutral `chuzi-launcher` CLI、服务、浏览器 Worker
；平台 UI 尚未随 nightly 发布。后续 Windows、macOS、Linux 客户端分别
通过 Stable API Boundary 复用同一启动器/Core 能力，Linux 首阶段采用 GTK，平台
默认样式和无障碍行为由各自原生框架负责。CR-0027 的网络下载仍不包含 Chromium、
真实账号或生产凭证。

## 演进阶段

### 阶段一：领域状态机与可执行契约

状态：已完成（CR-0005）。

`internal/account` 已实现，并补充了账号状态机的实现级定义：事件 ID、
重复事件幂等规则、非法转换错误、重试次数和退避、租约超时、服务重启
恢复、操作者与组件身份以及可审计事件格式。状态机保持纯 Go 领域边界，
不依赖持久化、浏览器、Matrix 或真实凭证。

完成标准：

- 所有文档声明的合法转换和失败路径都有表格驱动单元测试。
- 相同事件重复提交不会重复改变状态；冲突事件会被明确拒绝。
- 事件使用调用方提供的时间和 expected revision；重试和租约判断可注入测试，不依赖真实时间或外部服务。
- 状态机不依赖浏览器、Matrix、数据库或真实凭证。

### 阶段二：配置、状态存储与恢复

状态：已完成（CR-0006，main 集成结果已记录），已确定单节点 bbolt 拓扑。

实现 `internal/config`、`internal/store` 和 `migrations`，持久化账号、
请求、状态转换、租约和审计记录。存储边界支持原子状态转换、迁移、备份
和服务重启后的悬挂任务恢复；`cmd/service` 已加载配置、打开 Store 并让 Scheduler
在循环中执行恢复。

当前实现方向是先支持单节点部署，并保持 `CGO_ENABLED=0` 的跨平台构建。
数据库路径由 `data_dir` 派生为固定文件名，备份路径由服务派生；若目标
变为多实例部署，应另行明确外部数据库、并发一致性和迁移策略，不能把单
机文件数据库的假设带入集群运行。

完成标准：

- 迁移可重复执行并有版本记录。
- 状态、租约和审计事件的写入具有明确事务边界。
- 集成测试覆盖重启恢复、过期租约和重复请求。
- 配置示例不包含任何 Secret，路径由服务配置生成而不是来自任意用户输入。

### 阶段三：请求服务与队列调度

状态：已完成（CR-0008）。

实现 `internal/queue` 和 Request Service，负责请求幂等、账号级租约和全局并发
上限、取消、超时、重试和租约分配。服务级限流尚未实现。调度器只能提交状态机
定义的事件，不能自行维护第二套业务状态。

完成标准：

- 同一幂等键不会创建重复任务。
- 并发限制、取消、超时和重试在测试时具有确定结果。
- 浏览器崩溃、进程退出和服务重启都能释放或恢复租约。
- 对外错误只暴露分类和 request ID，不暴露内部堆栈或凭证内容。

### 阶段四：Session Runner 与 Worker 生命周期

状态：已完成（CR-0009）。

扩展现有 Worker 协议和 `internal/browser`，实现每账号独立 Profile、路径
生成、互斥租约、Worker 启停、心跳、超时、取消和崩溃回收。先使用 fake Worker
完成 Go 控制面集成，再通过同一协议接入当前无真实浏览器运行时的 Node Worker；
Playwright/Chromium 下载不属于本阶段。

完成标准：

- Profile 路径只由服务生成，不能接受任意用户路径。
- 同一账号不会并发占用同一个 Profile。
- Worker 只报告运行事实，业务状态转换仍由 Go 状态机完成。
- fake Worker 集成测试覆盖正常退出、崩溃、超时、取消、租约过期和恢复。

### 阶段五：加密凭证与安全审计

状态：已完成（CR-0010）。

实现 `internal/credential`，提供 AES-GCM 加密存储、最小权限回调访问、轮换、
撤销和 metadata-only 审计。密钥只能来自部署环境的 Secret 管理，不能写入
仓库、配置示例、日志、截图或 Matrix 消息。实现不接入真实浏览器或生产 Matrix。

完成标准：

- 加密数据与密钥分离保存，密钥缺失或轮换失败会安全失败。
- 业务调用方无法默认读取长期明文凭证。
- 日志、错误、审计和通知都有脱敏测试。
- 撤销和会话失效的顺序有明确测试覆盖；账号删除流程尚未实现。

### 阶段六：Matrix 适配器与状态通知

状态：已完成（CR-0011），本阶段完成 transport-neutral 边界；生产 Matrix 客户端仍待阶段八部署接入。

实现 `internal/matrix` 和 `internal/observability` 的基础能力，处理房间/用户
授权、`status`、`request`、`cancel`、`help` 命令，以及脱敏状态通知和断线
后的持久化 outbox。请求状态转换在 bbolt 事务内生成 event-ID 去重的通知记录；
Notifier 通过可注入 Sender 进行 claim、重试和恢复。Matrix 层不能直接访问凭证
或决定账号业务状态，当前不包含生产网络客户端。

完成标准：

- 未授权房间和用户默认拒绝。
- 每个请求和通知都有可去重的事件 ID。
- Matrix 断线、重复投递和恢复发送有 mock 服务集成测试。
- 消息只包含账号脱敏标识、request ID、业务状态和必要的错误分类。

### Core 假实现垂直切片

状态：已完成（Core API 第二阶段）。

`internal/core.PipelineRunner` 已将队列租约绑定的 Session Runner、一次性
`Credential.Use`、`automation.Adapter` 和 Store 的最终状态事务接成可恢复的
控制面链路。`internal/coretest` 提供不依赖 UI、真实浏览器或外部 Matrix 的
确定性替身。`tests/core/vertical_test.go` 覆盖成功、幂等提交、取消竞态、超时、
重试、凭证失败、Worker 崩溃、服务重启后的过期租约恢复以及通知重复投递。

完成标准：

- 通过 Go Core API 提交请求后，Scheduler 可驱动一次完整的队列、租约、会话、凭证、自动化、状态和 outbox 链路。
- 所有替身都使用显式时钟和 ID，不依赖 sleep、真实浏览器、UI 或外部网络。
- 失败和重启恢复只通过现有 Account/Store 事务改变业务状态，并可从 Core API 查询结果、审计事件和通知状态。

### Core API wire 与本地客户端边界

状态：已完成首个 transport 增量和 Windows WinUI 3 客户端首个实现；macOS/Linux 客户端尚未开始。

`chuzi.core/v1` 已冻结为独立 DTO、稳定错误码和 JSONL request/response envelope。
`internal/coretransport` 提供 Unix domain socket 和 Windows named pipe；服务入口实例化
`core.Service` 后挂载 IPC server。握手、版本拒绝、方法白名单、按 ID 并发响应、业务/传输
取消、owner-only 权限、endpoint 占用保护、超大帧和敏感字段脱敏均有跨进程契约测试。

完成标准：

- 客户端只消费 `coreapi.API`，不能访问 Store、凭证或 Profile。
- `hello` 版本协商和错误码在 wire 层稳定；底层错误文本不跨进程返回。
- 服务重启时 endpoint 可清理 stale socket，但不会删除仍被占用的 endpoint。
- Go Windows named-pipe 源码已通过 `GOOS=windows GOARCH=amd64` 交叉编译；WinUI 3
  原生编译与打包由 `chuzi-build-windows-ui` Windows runner 提供证据，本机 Linux 不宣称
  已完成 Windows native build。

Windows 客户端位于 `ui/windows`，只使用 owner-only named pipe 和 `chuzi.core/v1`，提供
提交、查询和业务取消的最小界面。`scripts/build_windows_ui.ps1` 的 `PackagedMsix` 模式与
GitHub Actions `chuzi-build-windows-ui` 生成自包含 Windows App SDK 的
`chuzi-windows-msix-self-contained`；`UnpackagedZip` 仅用于本地诊断。该客户端不进入 Go
服务/数据库包，也不复制 launcher、Store 或凭证逻辑。Windows 原生构建仍需 Windows
runner，Linux 开发机只能执行仓库契约和静态边界检查。

### 阶段七：headless 浏览器与业务自动化

状态：进行中。早期 CR-0014-A/B/C 规划的 Rust/Wry 桌面运行时已经废弃；相关源代码、
服务 backend、构建矩阵、发布组件和 smoke 测试已清理，不再作为后续路线。原生
Windows/macOS/Linux 客户端通过 Core API 工作，不依赖浏览器 WebView runtime。

#### 7A：真正 headless backend（CR-0017，第一增量）

第一增量已实现 Node headless-CDP worker：它控制部署环境已安装的 Chromium/Edge，
不打包完整 Chromium；使用动态 loopback CDP 端口、服务派生 Profile 和固定安全参数，
轮询 `/json/version` 并校验 endpoint，覆盖启动失败、发现超时、非法 endpoint、取消、
关闭和崩溃回收。它不把桌面隐藏窗口作为 headless，也不假设 Safari/WKWebView
可 headless。CR-0043 已实现首个真实 `chuzi.adapter/v1` 适配器：固定检查云原神
已授权会话，返回标题、应用根节点和登录状态等脱敏页面事实；Core evaluator 才将
事实映射为账号结果，未认证或页面结构变化时 fail closed，endpoint discovery 不被视为
业务成功。仓库内本地测试页仍用于协议
回归。更广泛的业务自动化适配器、WebDriver、浏览器版本策略、资源限制和四平台运行
证据仍需后续独立 CR。

#### 7B：业务自动化适配器与插件进程边界（CR-0020 第一增量）

CR-0021 完成了本地测试页垂直切片；CR-0043 在同一契约之上接入首个真实云原神
会话检查流程。它通过显式服务选项启用，不改变服务默认的 deferred backend，也不
扩展到其他平台。

先建立 `internal/automation` 的跨平台适配器契约和 `internal/plugin` 的原生/Wine
进程后端。Windows 原生进程是首个正式运行目标；Linux amd64 的 Wine 和
macOS/Linux arm64 的 Wine 只在获得真实运行证据后单独提升支持级别。fake plugin
必须覆盖能力协商、正常结果、稳定错误、取消、超时、崩溃回收和凭证不出现在协议
payload 中。BetterGI 只作为后续通信插件，不在本增量内实现自动化本体或假定其
具体私有协议。

所有 7A-7B 变更都必须记录浏览器版本、下载或安装来源、原生依赖、
资源限制、Profile 保留策略和每个平台的构建与运行覆盖。

完成标准：

- 四个发布目标都有明确的构建与运行结论；交叉编译不能替代原生 smoke test。
- 浏览器二进制和 Node 原生依赖具有可审计版本与校验信息。
- CI 使用假账号或本地测试页，不接收生产凭证。
- 不实现凭证窃取、访问控制绕过、验证码规避或反检测行为。

### 阶段八：部署、观测与发布

状态：进行中（CR-0023 已完成；CR-0025 观测与运维加固进行中；稳定版签名发布已由 CR-0024 完成）。

生产运行拼装已提供：`configs/example.json`、`deploy/`、健康检查、Matrix 网络与
通知 worker、凭证注入、数据库备份恢复和服务入口接线。CR-0025 增加结构化脱敏
日志与有界轮转、低基数指标、全局 metadata-only 审计查询、并行健康探针和恢复前
全量数据库校验；CR-0042 增加了可在 CI 运行的生产运行集成验证，以及显式受控
Matrix homeserver 垂直验证入口、凭证轮换/撤销运维命令和租约/重启/损坏恢复演练；
稳定版签名发布已由 CR-0024 接入。构建/打包脚本仍按既定 nightly 与稳定版工作流运行。

完成标准：

- 部署示例与 Secret 管理边界清晰，默认配置不会暴露敏感信息。
- 健康检查能区分服务、存储、队列、Worker 和 Matrix 依赖状态。
- 备份恢复和租约恢复经过演练或自动化测试。
- `scripts/test_runtime.sh` 的 deterministic runtime suite 通过 fake Matrix
  homeserver、fake 账号、测试凭证和本地 Worker 覆盖上述演练；受控环境可显式
  设置 `CHUZI_RUN_CONTROLLED_MATRIX=1` 再执行真实测试 homeserver 的 sync/send。
- 观测输出不包含凭证、Cookie、页面内容或原始账号/房间标识，指标标签保持低基数。
- 发布只通过 UGS 要求的签名 annotated semver tag 和远端构建流程完成。

## 依赖关系

```text
状态机 -> 状态存储 -> 请求/队列 -> Session Runner -> 真实浏览器
                    \-> Matrix 适配器与通知
凭证存储 ------------------------------------^
配置与观测能力贯穿所有阶段
```

真实浏览器之前必须完成状态机、租约、凭证接口和 Worker 生命周期；否则
浏览器错误会绕过业务状态和安全边界。Matrix 适配器可以使用 fake store 和
fake scheduler 提前测试，但不能在领域契约稳定前固化业务状态判断。

## 持续治理规则

- 路线图状态只由已合并的代码、测试和运行证据驱动。
- 架构或安全约束发生变化时，在对应 CR 中同步更新路线图和受影响文档。
- 每个阶段保留独立 CR、风险、回滚、测试证据和 breaking-change 说明。
- 所有测试不得使用真实账号、生产 Token、Cookie、Matrix access token 或
  未授权的第三方服务。
