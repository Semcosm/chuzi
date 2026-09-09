# 云游戏多账号队列服务

这是一个面向云游戏网页端的多账号会话管理与排队服务。项目以“账号、浏览器会话、业务请求、外部通知”作为核心概念，为多个账号提供隔离的登录会话、队列调度和状态同步能力。

当前仓库已通过 GitHub 远端 `Semcosm/UGS` 的 Release `v0.3.27` 初始化，并受 UGS 治理规则约束。项目设计文档位于 [`docs/`](docs/README.md)。

## 当前阶段

目前仓库已完成跨平台基础、领域核心、单节点存储和请求队列边界，正在实现 Session Runner 与 browser-worker 生命周期契约。服务控制面采用 Go，浏览器 Worker 使用 Node.js；凭证、真实浏览器自动化和 Matrix 业务实现仍按路线图逐步加入。

GitHub Actions 当前构建目标固定为 `windows-amd64`、`linux-amd64`、`linux-arm64` 和 `darwin-arm64`。CI 不使用真实账号、Token 或生产 Matrix 凭证。

## 设计原则

- 每个账号使用独立浏览器 Profile，隔离 Cookie、LocalStorage、缓存和会话生命周期。
- 账号状态由统一状态机驱动，避免队列、浏览器和通知模块各自维护状态。
- 凭证默认加密存储，日志和 Matrix 消息不得泄露明文凭证。
- 调度、会话运行和外部通知解耦，支持失败重试、超时回收和服务重启恢复。
- 只自动化用户有权使用的账号与服务，不实现凭证窃取、访问控制绕过或攻击能力。

## 文档入口

- [项目文档总览](docs/README.md)
- [长期演进路线图](docs/roadmap.md)
- [架构与目录规划](docs/architecture.md)
- [账号状态机](docs/account-state-machine.md)
- [状态存储与恢复](docs/storage.md)
- [安全与凭证管理](docs/security.md)
- [Matrix 服务接口](docs/matrix-api.md)
- [部署与运维](docs/operations.md)

## 仓库治理

运行以下命令校验 UGS 策略清单：

```bash
./scripts/validate_policy_manifest.sh
```

具体提交、评审和分支规则见 [`REPOSITORY_POLICY.md`](REPOSITORY_POLICY.md)。
