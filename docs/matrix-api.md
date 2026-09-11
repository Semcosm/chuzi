# Matrix 服务接口

## 房间命令（初版约定）

命令格式以 `!ugs` 开头，具体前缀可配置：

```text
!ugs status <request-id>
!ugs request <account-id>
!ugs cancel <request-id>
!ugs help
```

管理命令必须经过房间和用户授权。`status` 和 `cancel` 使用 request ID，
`request` 使用 account ID；命令处理应返回一个可追踪的 `request_id`，避免把
登录凭证放入命令参数。

## 状态事件

目标通知契约至少包含：账号脱敏标识、请求 ID、业务状态、发生时间、耗时（如有）
和脱敏原因。状态更新应具备幂等事件 ID，并允许请求者通过 `status` 命令重新
查询最终状态。当前实现只渲染账号 hash、request ID、状态、时间和可选的失败
分类；耗时和原始 reason 尚未暴露。

示例：

```text
[chuzi] account=id_7f31a2c4d5e6 request=req_91c2 status=LOGIN_SUCCEEDED time=2026-09-06T04:00:00Z
```

Matrix 连接断开时，事件写入待发送队列；恢复后按事件 ID 去重发送。敏感信息只通过受控的管理界面或安全人工流程处理，不通过公共房间广播。

## 当前实现边界

`internal/matrix` 当前是 transport-neutral 适配器。它通过显式房间/用户白名单
调用 Request Service，并使用事件 ID 派生稳定的 request/reply ID；普通用户只能
访问请求创建时绑定的房间，管理员角色可按策略跨房间查询。`request`、`status`
和 `cancel` 的成功回复只包含脱敏账号标识、request ID、状态和可选的失败分类；
`status` 查询的参数是 request ID，不是 account ID。

状态通知由 bbolt 的 `matrix_notifications` outbox 驱动。请求配置通知房间时，
每个 domain event 在状态事务内至多生成一条记录；Notifier claim 后调用注入的
`Sender`，发送参数包含稳定 event ID；失败会按退避重试，重启可回收过期 claim。
仓库不包含生产 Matrix SDK、access token 或网络连接实现，fake Sender 集成测试
不访问外部服务。
