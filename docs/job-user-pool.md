# 作业专用 Windows 用户池

## 逻辑资源模型

Job pool 的运维配置由 Core/Launcher 持久化，包括 `desired_slots`、
`max_concurrency`、environment ID/version、manifest digest、signer、capabilities、
`require_trusted`、`desired_state`、`config_revision` 和更新时间。配置变更先创建
operation，再由 reconcile 驱动 slot 生命周期；不会直接覆盖运行中的 leased slot。

阶段 1 引入逻辑 `Execution Slot` / `SlotPool`，把业务账号、队列并发和
执行资源解耦。slot 是可以复用的执行资源，不代表一个固定 account，也不保存
业务账号凭据。逻辑层只持久化槽位和环境元数据；Windows-only provisioner 负责
受控创建、检查和回收真实用户、目录、session 与 agent。

Windows provisioner 对应 `internal/slot.EnvironmentProvisioner` 接口，通过
`Provision`、`Inspect` 和 `Retire` 实现资源生命周期；它接收已验证的
pool/slot/environment 元数据，返回受控的 agent handle 和健康事实。该接口不能
接受任意路径、任意命令、PowerShell 文本、密码、Cookie、Profile 路径或真实账号
数据作为 Core/API 输入。

## 资源模型

每个 slot 保存稳定 `slot_id`、`ordinal`、`pool_id`、`environment_id`、
`environment_generation`、capabilities、manifest digest、signer/trust 结果、
最近健康时间、失败计数和状态。状态为：

`unprovisioned`、`provisioning`、`ready`、`leased`、`quarantined`、
`draining`、`retiring`、`deleted`。

pool 的 `desired_slots` 表示目标数量。启动时 reconcile 只补齐逻辑
`unprovisioned` 记录；缩容时 leased slot 进入 `draining`，其他超出目标的 slot
进入 `retiring`，不会删除未知资源。只有状态为 `ready`、且环境要求匹配的 slot
才能被调度。环境匹配使用 environment ID、版本、capability、manifest digest、
签名者和可选 trust 要求；这些字段沿用 adapter/plugin manifest 的验证语义，
而非执行任意插件脚本。
环境版本升级也会先 drain 仍持有 lease 的旧 slot；lease 释放后才按新的 generation
重新 provision，旧环境不会接收新作业。

## 租约和恢复

slot lease 绑定 `request_id`、`account_id`、`owner`、`lease_id`、环境 generation
和 `expires_at`。Acquire、Heartbeat、Release、Quarantine 都在存储层执行。
同一 slot 同时最多有一个 lease；重复 release 在 lease 已不存在时是幂等的，
stale owner 不能释放新 lease。过期 lease 可以由新 owner 回收，回收按 slot key
和当前 lease 校验，不能覆盖之后写入的新 lease。agent 错误或超时会将对应
slot quarantine 并增加失败计数；明确的用户取消释放 slot，不把资源故障伪装成
凭据失败。服务重启后会恢复持久化状态，下一次调度会回收过期 lease；状态读取保持无副作用。

账号 lease 仍然由 account/store/queue 负责业务账号并发，slot lease 只表达
执行资源占用。启用 pool 时，账号 claim 和 slot claim 在一个 bbolt 事务中提交；
没有 ready slot 时请求保持 queued/可重试，不转换为 credential failure。session
runner 只接收内部的 slot ID、环境 generation 和可选 agent handle。Core、日志、
诊断和 Matrix 投影只暴露脱敏的 pool 数量和业务状态。
存储诊断会交叉检查 slot lease、account lease、request 和 account snapshot：
两类 lease 必须属于同一 owner、请求和生命周期时间窗，request/account 状态必须仍处于
`STARTING` 或 `LOGGING_IN`，并且一个 account/request 不能绑定多个 slot。发现不一致时
`ValidateDatabase` 和备份校验会拒绝数据库，避免把恢复后的半个 claim 当作可运行状态。

## 容量和观测

`desired` 是配置目标，`ready` 是当前可调度数量，`leased`、`quarantined` 和
`draining` 是运行状态计数。有效槽位容量为 `min(max_concurrency, ready)`。
Core 的 `get_job_pool_status`、运维快照和 `chuzi_slots_*` metrics 只包含这些
计数；不返回 Windows 用户名、SID、密码、Profile 路径、RDP endpoint 或命令行。
`list_job_pools` 和 `get_job_pool` 还返回 environment readiness、reconcile state、
operation ID、last failure code 和 last successful reconcile time。扩缩容、排空、恢复和
环境目标变化都要求乐观 revision 校验；同一 idempotency key 重放不会重复创建 operation。

这一阶段刻意不声明多节点协调，也不改变现有 launcher、browser、credential、
Core 或生产 RDP 边界。Windows provisioner 和 agent 只在本机受控边界内运行，
跨主机资源回收不属于当前拓扑。

当前服务实例仍在启动时绑定部署配置中的一个 `pool_id`。Core/Launcher 可以
持久化和查询多个逻辑 pool，但运行中的 Scheduler 和 Windows provisioner 只会
接管该启动 pool；运行中新增 pool 的自动 reconciler/OS 资源接管不属于本阶段
完成条件。

## Windows 阶段 2

Windows 构建包含 `chuzi-user-agent.exe`。服务在 Windows-only provisioner 中按
`data_dir/job-slots/<slot-id>/generation-<generation>` 派生目录和本地用户；用户备注
保存 `CHUZI-MANAGED:<slot-id>:<ordinal>` 标记，SID 与该标记一起写入受保护的
`ownership.json`。创建使用每次随机生成的密码，密码只在 `NetUserAdd` 调用期间存在，
不会进入配置、命令行、Core、Matrix 或日志。未知用户、SID 漂移、禁用用户、
Administrators 成员和 reparse point 都会使 provision 失败。

服务只为受管目录授予最小 ACL；每个 slot 的 Win32 desktop 由 `slot_id` 的
SHA-256 前 8 字节派生为固定的 `ChuziSlot` 加 16 位小写十六进制名称，调用方不能
传入 desktop 字符串。账号 Profile 的 ACL 在运行前由
`GrantProfile` 授予，worker 关闭后由 `RevokeProfile` 撤销。回收顺序是 agent/job
进程、RDP session、Profile、受管目录、最后本地用户；所有对象必须先通过 ownership
校验，未知对象不会删除。

Agent 通过服务派生的 named pipe 使用闭合命令枚举（`prepare_slot`、`start_job`、
`cancel_job`、`stop_job`、`health`、`shutdown`）。每个命令带 request、slot、lease
和 environment generation；重复命令、过期 lease、超大 JSONL frame 和未知字段会被
拒绝。worker 参数、executable、desktop、Profile 路径和 shell 文本不在协议中。
`start_job` 只选择固定的 browser-worker 或 adapter 枚举；agent 使用启动时注入的
服务配置派生 Profile，子进程加入该 slot 独立的 Job Object，pipe 断开、取消、超时
或 token 失效时终止整棵进程树。
user agent 本身也由服务持有的独立 Job Object 管理，并启用
`KILL_ON_JOB_CLOSE`；服务退出或重启时句柄关闭会回收残留 agent 及其子进程，新的
reconcile 通过新的 token 和 lease 建立健康连接。

named pipe ACL 包含 SYSTEM、当前 slot 用户和启动服务进程的实际 SID；服务可以由
SYSTEM、LocalService 或专用 Windows 服务账号运行。该 SID 只作为受控启动环境的一部分
传给 agent，worker 环境过滤不会继承它。服务传入的 worker runtime 也经过固定 allowlist：
Windows slot 只运行安装目录下的 `browser-worker/src/worker.mjs` 或
`browser-worker/src/headless.mjs`，命令必须解析为安装目录内随包发布的 `node.exe`，请求协议不能改变这些值。Windows service component 会同时携带该 Node runtime。

请求完成、取消、超时或 slot quarantine 会调用 credential boundary 的 request/slot
lease capability revocation；用户回收前先停止 agent 和 session，再删除受管对象。
服务组装会始终把 capability revocation 接口接入 request、scheduler、Core 和
Windows provisioner。默认 RDP authorizer 是 deny-by-default；部署若要提供真实
RDP 连接材料，必须在 credential boundary 内注入受控的 authorizer，不能把 endpoint、
用户名或密码加入配置、命令行或 Core DTO。

slot 用户对 `<data_dir>/chuzi.db` 和 `backups/` 的 ACL 会显式拒绝访问；slot 目录的 ACL
同时保留服务 SID、SYSTEM 和 Administrators 的管理入口。部署仍必须把安装目录配置为服务可写、
slot 用户只读/执行；provisioner 不会把安装目录写权限授予受管用户。

启用 OS 用户池时，`windows_job_pool` 必须与 `job_pool` 的容量和环境版本一致，
并设置 `provision_timeout_seconds`、`cleanup_timeout_seconds`、`agent_heartbeat_seconds`
和可选的 `rdp_enabled`。非 Windows 构建保留逻辑 slot 行为并返回
`slotwindows.ErrUnsupported`，不会尝试创建操作系统用户。

阶段 2 的生产验收仍需在 Windows runner 完成原生 smoke：本地用户和密码生命周期、
ACL/reparse point、Profile、RDP session/desktop、named pipe ACL、
`CreateProcessAsUser`、Job Object 回收、服务重启/断电恢复和 disposable user 清理。
当前默认 RDP authorizer 仍为 deny-by-default；通用 `chuzi-environment/v1` manifest、
资源 digest/签名 trust store、插件注入、滚动升级和回滚属于阶段 3。

## 阶段 4 session bootstrap 边界

阶段 4 native smoke 在调用 `Provision` 前必须拥有真实的 managed-user WTS
session。`SessionBootstrapper` 是受控注入边界，只接收由 provisioner 从 slot
ownership 派生的 slot、ordinal、generation 和 SID；接口不接收密码、用户名、Profile
路径、RDP endpoint、命令或 executable。provider 建立 session 后，provisioner 仍会重新
调用 `FindSession(managed SID)`，并校验 session ID 和 active 状态；provider 返回值或
`CreateProcessAsUser` 不能替代 WTS 校验。生产默认不配置 bootstrapper，继续依赖外部受控
interactive session，找不到 session 时保持 `session_unavailable`/quarantine 的
fail-closed 行为。

bootstrap 退出时先停止 worker 和 agent，再调用 provider 的幂等 `Stop`，等待
`FindSession` 不再发现该 SID，最后由 provisioner 执行 logoff、Profile/ACL、目录和用户
回收。smoke harness 的 user cleanup 与 root cleanup 分阶段重试并分别报告；未知用户、
ownership marker 不匹配的 root 或未知目录永远不会删除。失败会保留脱敏
`test-output.log`，成功运行必须报告 `RemainingSmokeUsers = 0` 和
`RemainingSmokeRoots = 0`。
