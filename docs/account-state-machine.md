# 账号状态机

## 对外业务状态

| 状态 | 含义 |
| --- | --- |
| `NO_REQUEST` | 账号可用，当前没有待处理请求 |
| `QUEUED` | 请求已接受，等待调度 |
| `STARTING` | 正在准备浏览器 Profile 和会话 |
| `LOGGING_IN` | 正在执行用户授权的登录流程 |
| `LOGIN_SUCCEEDED` | 登录完成并获得有效会话 |
| `LOGIN_FAILED` | 登录流程结束但未成功 |
| `EXPIRED` | 已知凭证或会话失效，需要重新授权 |
| `CANCELLED` | 请求被授权操作者取消 |
| `BLOCKED` | 因配置、权限或人工策略暂不可用 |

## 合法转换

```text
NO_REQUEST -> QUEUED -> STARTING -> LOGGING_IN -> LOGIN_SUCCEEDED
                                      └──────────> LOGIN_FAILED
LOGIN_FAILED -> QUEUED
QUEUED/STARTING/LOGGING_IN -> CANCELLED
LOGIN_SUCCEEDED -> EXPIRED -> QUEUED
任意非终态 -> BLOCKED
```

每次转换必须记录 `account_id`、`request_id`、旧状态、新状态、原因、操作者/组件和时间戳。重复事件必须幂等，服务重启后可以依据租约和最后心跳回收悬挂任务。

## 实现级契约

状态机以纯函数方式处理一个可持久化的账号快照和一个状态事件。它不
读取系统时钟、不访问数据库、不启动浏览器，也不发送 Matrix 消息。

状态事件至少包含以下字段：

| 字段 | 约束 |
| --- | --- |
| `event_id` | 非空；用于幂等去重 |
| `account_id` | 必须匹配快照中的账号 |
| `request_id` | 非空；用于关联请求和审计，即使是配置导致的阻断也必须提供关联 ID |
| `from` | 必须等于快照当前状态，防止过期写入 |
| `expected_revision` | 必须等于快照当前 `revision`，防止并发快照覆盖；初始快照使用 `0` |
| `to` | 必须是状态图中声明的合法目标 |
| `reason` | 非空的分类原因；不得放入密码、Token、Cookie 或完整页面内容 |
| `actor` | 非空的组件、操作者或系统身份 |
| `occurred_at` | 由调用方提供的非零时间；测试不得依赖当前真实时间 |

成功应用事件时，快照的 `revision` 加一，并生成一条包含上述字段、修订
号和事件 ID 的审计记录。事件 ID 第一次出现时才会产生状态变化；相同
事件 ID 与完全相同的事件内容重复提交必须返回原结果且不增加修订号，
相同事件 ID 但内容不同必须拒绝为冲突。`from` 或 `expected_revision` 不
匹配也必须拒绝，调用方需要重新读取快照后再决定是否提交。

`NO_REQUEST -> QUEUED` 会写入新的 `request_id`；其余当前图中的请求流转
必须沿用快照中的关联请求。`CANCELLED` 和 `BLOCKED` 是当前契约中的终态，
不会被隐式重置。`LOGIN_FAILED` 表示本次登录尝试的失败结果；当重试策略
允许时，阶段三的 Request Service/Queue 可以使用同一 `request_id` 提交
`LOGIN_FAILED -> QUEUED`，开始下一次尝试。状态机只验证这条业务转换，
不负责创建请求或选择何时调度。

## 状态事件和租约恢复

租约是独立于业务状态的可持久化运行事实，包含 `lease_id`、`owner`、获取
时间、最后心跳时间和过期时间。租约在 `now >= expires_at` 时过期。只有
没有活动租约或已有租约已过期时才能获取；心跳必须匹配租约 ID 和 owner，
且不能延长已经过期的租约。服务重启后直接用持久化的过期时间与注入的
当前时间比较，过期租约可被回收，未过期租约保持占用。

重试决策使用总尝试次数和失败分类。只有 `transient` 错误自动重试；凭证、
权限、配置和未知错误默认不重试。第 `n` 次尝试失败后的退避为
`min(base_delay * 2^(n-1), max_delay)`，达到最大尝试次数后不再重试。所有
时间和次数均由策略参数显式提供，避免隐藏的无限重试。

## 失败策略

- 可重试错误使用指数退避并设置上限。
- 凭证错误、明确的权限错误不自动无限重试。
- 浏览器崩溃先回收 Profile 锁和进程，再由调度器决定是否重试。
- 对外消息只发送必要的错误分类，不发送密码、Token、Cookie 或完整页面内容。
