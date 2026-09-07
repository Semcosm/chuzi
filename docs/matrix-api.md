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
