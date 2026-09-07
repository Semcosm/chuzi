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
QUEUED/STARTING/LOGGING_IN -> CANCELLED
LOGIN_SUCCEEDED -> EXPIRED -> QUEUED
任意非终态 -> BLOCKED
```

每次转换必须记录 `account_id`、`request_id`、旧状态、新状态、原因、操作者/组件和时间戳。重复事件必须幂等，服务重启后可以依据租约和最后心跳回收悬挂任务。

## 失败策略

- 可重试错误使用指数退避并设置上限。
- 凭证错误、明确的权限错误不自动无限重试。
- 浏览器崩溃先回收 Profile 锁和进程，再由调度器决定是否重试。
- 对外消息只发送必要的错误分类，不发送密码、Token、Cookie 或完整页面内容。
