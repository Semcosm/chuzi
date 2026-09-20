# 云游戏多账号队列服务

这是一个面向云游戏网页端的多账号会话管理与排队服务。项目以“账号、浏览器会话、业务请求、外部通知”作为核心概念，为多个账号提供隔离的登录会话、队列调度和状态同步能力。

当前仓库已通过 GitHub 远端 `Semcosm/UGS` 的 Release `v0.3.27` 初始化，并受 UGS 治理规则约束。项目设计文档位于 [`docs/`](docs/README.md)。

## 当前阶段

目前仓库已完成跨平台基础、领域核心、单节点存储、请求队列、Session
Runner/browser-worker 生命周期边界、加密凭证与安全审计，以及 transport-neutral
的 Matrix 命令与状态通知边界。服务控制面采用 Go，浏览器 Worker 使用 Node.js；
生产 Matrix 传输客户端和部署编排仍按路线图逐步加入；真实浏览器适配目前仅覆盖
云原神的已授权会话检查流程。

`cmd/service` 现在负责加载 `configs/example.json`（也可通过 `-config` 指定），打开
持久化 bbolt Store，并组装 Request Service、Session Runner 和 Queue Scheduler。
普通启动只运行持久化调度循环；没有排队请求时保持 idle，收到中断后关闭 Store。
默认 backend 仍是 Node.js deferred Worker；真实浏览器流程必须显式选择 Node
headless-CDP backend，并由部署环境提供浏览器可执行文件。`-self-test` 继续使用
临时目录运行一次 Node Worker 协议 smoke test，不代表生产服务入口或真实浏览器自动化。

需要运行首个真实业务适配器时，必须显式同时设置
`-browser-backend headless -automation-adapter genshin-cloudgame`。该流程固定访问
`https://ys.mihoyo.com/cloud/#/`，只检查服务派生 Profile 中已有的授权会话，不提交用户名/密码，
不处理验证码或风控，也不接受任意 URL。适配器只返回脱敏页面事实，由 Core evaluator
将未登录或页面结构变化分别映射为凭证失败或未知业务失败。

`cmd/service` 已提供生产运行拼装：凭证服务从部署环境注入密钥，Matrix HTTP
Client/同步网关和通知 outbox worker 可由配置启用，健康检查可绑定受限 HTTP 端点；
`-backup`、`-restore`、`-validate-backup`、`-diagnostics`、`-audit`、
`-inject-account`、`-rotate-account` 和 `-revoke-account` 提供停止服务后的运维操作。服务仍不会
自行创建账号，也不会把 Secret 写入普通配置或日志。

Core API 已有可调用的本地 IPC 形态。服务在数据目录派生固定 endpoint：Unix 使用
`core.sock`（权限 `0600`），Windows 使用 owner-only named pipe。客户端先完成
`chuzi.core/v1` `hello` 协商，再通过 JSONL envelope 调用提交、查询、取消、结果、事件和
通知方法；请求按 ID 多路复用，取消会传播到服务端。原生客户端不得读取 bbolt、凭证或
Profile。Windows 首个 Slint 客户端位于 `ui/windows`，通过独立的 named-pipe 客户端只消费
这一边界；macOS SwiftUI/AppKit 与 Linux GTK 客户端仍待后续 CR。

观测能力通过 `observability` 配置启用：服务写入结构化脱敏 JSONL 日志并有界轮转，
可选的本地 metrics 端点只暴露低基数分类指标；日志、指标、健康和审计输出都不会
包含凭证、Cookie、页面内容或原始账号/房间标识。

浏览器运行时只保留 Node.js Worker：默认 deferred 生命周期替身，以及显式选择的
headless-CDP Worker。Wry/Rust desktop runtime 路径已废弃，不再属于服务 backend、
构建矩阵、发布包或客户端架构。headless 流程只使用部署环境提供的 Chromium/Edge，
返回脱敏运行事实，由 Core evaluator 映射为账号结果。

GitHub Actions 当前构建目标固定为 `windows-amd64`、`linux-amd64`、`linux-arm64` 和 `darwin-arm64`。CI 不使用真实账号、Token 或生产 Matrix 凭证。

当前首个 release 流程是 nightly：GitHub Actions 每日自动构建并上传四个平台的限期
artifact，不创建 Git tag 或 GitHub Release。版本格式为
`nightly-<run-number>-<commit-short-hash>`，完整 commit hash 写入 manifest/index。
每个目标包含 UI-neutral CLI、服务和浏览器 Worker 以及
`release-manifest.json`；同时提供按组件拆分的归档和带大小/SHA-256 的
`release-index.json`，安装者不必安装全部运行资源。启动器后台契约位于
`internal/launcher`，原生平台 UI 通过 Core API 调用 Go 服务，并按需使用 UI-neutral CLI，不复制下载、
校验、锁和回滚策略。首次运行的 `initialize` 状态会驱动组件选择和安装进度页面，
显式的 `-release-index` 才会通过 HTTPS 下载所选组件及依赖。组件启停、插件信任、
原子行为设置、跨进程安装锁和可取消的操作进度继续由同一 CLI/后台接口提供；不会
隐式下载或执行未知插件。Go 的前台服务进程控制边界仍不替代 systemd、launchd
或 Windows 服务管理器。

## 设计原则

- 每个账号使用独立浏览器 Profile，隔离 Cookie、LocalStorage、缓存和会话生命周期。
- 账号状态由统一状态机驱动，避免队列、浏览器和通知模块各自维护状态。
- 凭证默认加密存储，日志和 Matrix 消息不得泄露明文凭证。
- 调度、会话运行和外部通知解耦，支持失败重试、超时回收和服务重启恢复。
- 浏览器 Worker、调度、会话运行和外部通知解耦；headless 只使用显式配置的外部浏览器。
- headless backend 不下载或打包 Chromium/Edge，只使用显式配置的外部可执行文件。
- 只自动化用户有权使用的账号与服务，不实现凭证窃取、访问控制绕过或攻击能力。

## 文档入口

- [项目文档总览](docs/README.md)
- [长期演进路线图](docs/roadmap.md)
- [架构与目录规划](docs/architecture.md)
- [Core API v1](docs/core-api.md)
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
