# 状态存储与恢复

## 拓扑决策

阶段二先采用单节点嵌入式 bbolt。bbolt 是纯 Go 实现，服务保持
`CGO_ENABLED=0`，因此不改变 Windows amd64、Linux amd64、Linux arm64 和
Darwin arm64 的构建契约。

该拓扑只支持一个服务实例拥有一个数据目录。bbolt 的文件锁可以防止同一
文件被多个进程同时写入，但不构成跨主机或多实例协调协议。未来如果需要
多实例部署，必须单独确定外部数据库、事务隔离、锁和迁移策略，不能把本
阶段的本地文件假设直接带入集群。

## 路径与配置

配置只接受部署级 `data_dir`。数据库和备份路径由服务生成：

```text
<data_dir>/chuzi.db
<data_dir>/backups/chuzi-<UTC timestamp>.db
```

调用方不能通过请求、账号 ID 或其他外部字段指定数据库或备份路径。配置
示例只包含普通配置，不包含密码、Token、Cookie、密钥或其他 Secret。

`cmd/service` 通过 `config.Load` 和 `store.Open` 使用同一个配置派生的
`data_dir`，启动时执行迁移并重建队列索引；调度循环退出时关闭 Store。入口不
自行创建账号或请求，因此已有的排队请求可在重启后由 Scheduler 按租约状态恢复。
迁移、事务、恢复和备份契约仍由 `internal/store` 库层及其测试作为事实来源。

## Schema v6

迁移在数据库的 `meta/version` 中记录当前版本，并可重复执行。v1 建立
以下 bbolt bucket：

| Bucket | 内容 |
| --- | --- |
| `accounts` | 账号当前状态、请求关联和 revision 投影 |
| `requests` | 请求、幂等键、创建时间和状态投影 |
| `request_idempotency` | 幂等键到 request ID 的唯一索引 |
| `audits` | 按账号分桶、按 revision 保存的不可变状态事件 |
| `events` | event ID 到审计记录的去重索引 |
| `leases` | 按账号保存的租约、owner、心跳和过期时间 |
| `queue` | 按 `created_at`、请求 ID 建立的确定性待调度索引 |

v2 在请求投影中加入 `attempt`、`not_before`、`deadline` 和最后失败分类，
并建立可重建的 `queue` 索引。`Store.Open` 打开旧 v1 数据库时，会先执行
可重复迁移，再根据请求投影重建该索引；索引不是业务状态来源，每次打开时
都会重新生成。

v3 增加凭据密文和安全审计桶：

| Bucket | 内容 |
| --- | --- |
| `credentials` | 按账号保存 AES-GCM 密文、nonce、key ID、版本和撤销元数据 |
| `credential_audits` | 按账号保存 store/access/rotate/revoke 操作元数据 |

v3 迁移不写入任何密钥或明文凭据，且可重复执行。密钥只由部署环境的
Keyring 提供；数据库备份仍然只包含密文，恢复时必须同时确保兼容的外部
Keyring 可用。

v4 增加 Matrix 状态通知 outbox：

| Bucket | 内容 |
| --- | --- |
| `matrix_notifications` | 目标房间、账号/请求关联、状态、失败分类、事件 ID、发送尝试和 claim/交付元数据 |

请求投影可保存一个经过授权的通知房间。每个状态事件在同一 bbolt 写事务
中生成至多一条通知记录；通知正文不落库，发送时由 Matrix notifier 根据
脱敏投影渲染。事件 ID 是稳定的下游事务 ID，发送失败只释放 claim 并安排
下一次尝试，服务重启后可回收过期 claim。

v5 增加 Matrix `/sync` 游标桶：

| Bucket | 内容 |
| --- | --- |
| `matrix_sync_cursors` | 已完整处理并持久化的 Matrix `next_batch` 游标 |

同步网关在启动时读取该游标，并只在一个批次内的可处理消息完成后写入新的
`next_batch`。发送失败或进程在批次中途退出时，旧游标保持不变，重启后会重放
整批事件；事件 ID 派生的稳定回复 ID、请求幂等键和通知 outbox 负责下游去重。

账号投影不是第二套业务状态来源。读取账号时，存储层加载账号投影并回放关联
审计记录，构造并执行 `account.Snapshot` 的完整性校验；状态机仍负责判断转换
是否合法。

v5 增加账号删除生命周期记录：

| Bucket | 内容 |
| --- | --- |
| `account_deletions` | 按账号保存删除 ID、单调 checkpoint、重试/阻断信息、幂等事件索引和脱敏账号标签 |

删除记录与账号投影在同一个数据库中持久化。创建删除记录后，Store 拒绝该账号的新请求；
记录推进使用期望 revision 和稳定 event ID，重复事件不会增加 revision，冲突事件会失败。
删除记录不会保存凭证、Profile 路径、worker handle、Matrix 原始房间 ID 或命令文本。
当前实现提供状态机、持久化和恢复边界；Session Runner、Matrix/Core 双确认以及最终 tombstone/outbox
编排仍需后续实现 CR 接入。

v6 增加逻辑作业专用执行槽位，不创建或删除 Windows 用户、目录或其他外部资源：

| Bucket | 内容 |
| --- | --- |
| `job_pools` | pool ID、环境 manifest 摘要和 `desired_slots` 目标容量 |
| `execution_slots` | 稳定 slot ID、ordinal、pool、环境 generation/capabilities、状态、健康时间和失败计数 |
| `slot_leases` | 按 slot 保存绑定 request/account/owner/lease ID、心跳和过期时间的资源租约 |
| `environment_summaries` | 用于筛选的 environment ID、版本、generation、capabilities、manifest digest、signer 和 trust 结果 |

`desired_slots` 是目标容量；只有 `ready` 槽位计入可调度容量，有效容量为
`min(max_concurrency, ready_slots)`。`provisioning`、`draining`、
`quarantined` 和 `retiring` 不计入 ready。slot 记录不保存业务账号、凭据、
Windows 用户名、SID、Profile 路径、RDP endpoint 或命令。

账号 claim 与 slot claim 在启用槽位池时由同一个 bbolt write transaction
提交，事务同时写入账号 lease、slot lease、请求/账号状态和 slot 状态；任意
一步失败都不会留下半个 claim。slot 的 acquire、heartbeat、release、
quarantine 也使用单事务更新 slot lease 与 slot 状态。重启时可以继续读取
ready/quarantined 状态；调度或诊断读取时回收 `now >= expires_at` 的旧 slot
lease，并且只清理仍对应的 lease，不覆盖新 owner 的 lease。
`ValidateDatabase` 和 `ValidateBackup` 还会检查 slot lease 对应的 pool、slot、
account lease、request 和 account snapshot；owner、request ID、account ID、lease
时间窗以及 `STARTING`/`LOGGING_IN` 状态必须一致，同一 account/request 不能出现
多个 slot lease。校验失败会返回 `ErrCorruptData`，恢复流程不能把不完整 claim
当作健康资源。

## 事务边界与幂等

一个新状态事件在一个 bbolt write transaction 内同时写入账号投影、审计
记录、event ID 索引和请求状态投影。任意一步失败都不会留下部分转换。
重复提交完全相同的 event ID 和内容返回原审计结果，不增加 revision；同
一 event ID 内容冲突会被拒绝。

请求使用调用方提供的 `request_id` 和 `idempotency_key`。相同幂等键重复
创建同一请求返回已有请求；相同幂等键映射到不同请求会明确报错。请求和
账号的关联在创建和状态转换时都由存储层校验。

## 重启、租约与备份

调用方重启后用同一个 `data_dir` 打开 `Store` 时，迁移先校验 schema，再读取
持久化账号投影、审计和租约数据；账号投影会结合审计回放校验，租约由调度器
按过期时间恢复。
`Store.Open` 会重建可派生的队列索引；租约判断继续使用调用方提供的时间，
`now >= expires_at` 的租约可以被新 owner 回收，未过期租约保持占用。

备份只能生成到配置派生的 `backups/` 目录，并使用调用方提供的 UTC 时间
命名。备份使用 bbolt 一致性快照；恢复前必须确认文件权限和 schema 版本。
