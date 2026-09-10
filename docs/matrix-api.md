# Matrix 服务接口

## 房间命令（初版约定）

命令格式以 `!ugs` 开头，具体前缀可配置：

```text
!ugs status <account-id>
!ugs request <account-id>
!ugs cancel <request-id>
!ugs help
```

管理命令必须经过房间和用户授权。命令处理应返回一个可追踪的 `request_id`，避免把登录凭证放入命令参数。

## 状态事件

通知消息至少包含：账号脱敏标识、请求 ID、业务状态、发生时间、耗时（如有）和脱敏原因。状态更新应具备幂等事件 ID，并允许请求者通过 `status` 命令重新查询最终状态。

示例：

```text
[UGS] account=acc_7f31 request=req_91c2 status=LOGIN_SUCCEEDED
time=2026-09-06T12:00:00+08:00
```

Matrix 连接断开时，事件写入待发送队列；恢复后按事件 ID 去重发送。敏感信息只通过受控的管理界面或安全人工流程处理，不通过公共房间广播。

## 当前实现边界

`internal/matrix` 当前是 transport-neutral 适配器。它通过显式房间/用户白名单
调用 Request Service，并使用事件 ID 派生稳定的 request/reply ID；普通用户只能
访问请求创建时绑定的房间，管理员角色可按策略跨房间查询。`request`、`status`
和 `cancel` 的回复只包含脱敏账号标识、request ID、状态和错误分类。

状态通知由 bbolt 的 `matrix_notifications` outbox 驱动。每个 domain event 在
状态事务内只生成一条记录，Notifier claim 后调用注入的 `Sender`，发送参数包含
稳定 event ID；失败会按退避重试，重启可回收过期 claim。仓库不包含生产 Matrix
SDK、access token 或网络连接实现，fake Sender 集成测试不访问外部服务。
