# 服务级请求限流

Request Service 对新建请求执行可配置的滑动窗口限流。当前范围如下：

- 全局提交、调用方（Matrix 用户或 Core `actor`）、Matrix 房间和账号是四个独立维度；每个维度的 `*_limit` 为 0 时关闭。
- 只限制 `submit_request` 和 Matrix `!ugs request`。`get_request`、列表/状态查询和 `cancel_request` 不受限流影响。
- 相同幂等键的重试不会消耗额度，也不会因为额度已满而被拒绝；持久化事务仍负责判定请求冲突。
- 每个窗口使用调用方注入的时钟，窗口边界和过期行为可在确定性测试中复现。并发提交在限流器锁内预留额度，持久化失败或幂等重试会回滚预留。

## 配置

部署配置的 `rate_limit` 节点使用请求数和秒数：

```json
{
  "rate_limit": {
    "global_limit": 30,
    "global_window_seconds": 60,
    "actor_limit": 10,
    "actor_window_seconds": 60,
    "room_limit": 20,
    "room_window_seconds": 60,
    "account_limit": 0,
    "account_window_seconds": 0
  }
}
```

示例配置启用全局、调用方和房间限制；账号维度默认关闭，因为账号状态机本身已经拒绝同一账号的并发活动请求。窗口最大为 24 小时，非零上限必须提供正窗口。

## 错误和指标

Request Service 返回稳定的 `request.ErrRateLimited`。Core API 和本地 JSONL 协议将其映射为 `rate_limited`，消息为 `request rate limit exceeded`。Matrix 适配器记录 `rate_limited` 分类，并继续隐藏内部存储错误。

拒绝提交会记录 `chuzi_rate_limited_requests_total`，同时写入普通事件指标的 `component=request`, `operation=submit`, `error_class=rate_limited` 维度。
