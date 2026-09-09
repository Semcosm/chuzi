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

## Schema v2

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
并建立可重建的 `queue` 索引。打开旧 v1 数据库时，服务先执行可重复迁移
并根据请求投影重建该索引；索引不是业务状态来源，启动时会重新生成。

账号投影不是第二套业务状态来源。读取账号时，存储层会由审计记录重建
`account.Snapshot` 并执行其完整性校验；状态机仍负责判断转换是否合法。

## 事务边界与幂等

一个新状态事件在一个 bbolt write transaction 内同时写入账号投影、审计
记录、event ID 索引和请求状态投影。任意一步失败都不会留下部分转换。
重复提交完全相同的 event ID 和内容返回原审计结果，不增加 revision；同
一 event ID 内容冲突会被拒绝。

请求使用调用方提供的 `request_id` 和 `idempotency_key`。相同幂等键重复
创建同一请求返回已有请求；相同幂等键映射到不同请求会明确报错。请求和
账号的关联在创建和状态转换时都由存储层校验。

## 重启、租约与备份

服务重启后打开同一个 `data_dir`，迁移先校验 schema，再从持久化审计和租
约数据恢复。租约判断继续使用调用方提供的时间；`now >= expires_at` 的
租约可以被新 owner 回收，未过期租约保持占用。

备份只能生成到配置派生的 `backups/` 目录，并使用调用方提供的 UTC 时间
命名。备份使用 bbolt 一致性快照；恢复前必须确认文件权限和 schema 版本。
