# 部署与运维

## 配置分类

- 普通配置：`data_dir`、Matrix homeserver/user/policy、同步时序、凭证 key 环境变量
  名称、健康监听地址、新请求限流窗口和可选逻辑 `job_pool` 的环境元数据/
  `desired_slots`。`configs/example.json` 不包含任何 Secret 值。
- Secret 配置：Matrix access token、凭证加密主密钥（只由环境/Secret manager 注入）。
- 运行数据：数据库、浏览器 Profile、审计日志、待发送事件和用户同意后的诊断队列。

普通配置示例位于 `configs/example.json`，包含数据目录、凭证环境变量名称、本地
健康监听地址和默认请求限流值；Matrix 网络配置按需添加。数据库路径固定由存储层派生为
`<data_dir>/chuzi.db`，备份目录固定为 `<data_dir>/backups/`；请求和账号
输入不能覆盖这些路径。`cmd/service` 普通启动默认加载该文件，也可通过 `-config`
指定路径；Store、Profile 和队列都从同一 `data_dir` 派生。Secret 不进入 Git。
生产环境至少限制服务账户、数据库和 Profile 目录的文件权限。

启用逻辑执行槽位池时，配置只填写 pool ID、environment manifest 摘要、能力和
`desired_slots`，例如：

```json
{
  "job_pool": {
    "pool_id": "windows-cloudgame",
    "environment_id": "chuzi-environment/v1",
    "environment_version": "1.0.0",
    "desired_slots": 2,
    "capabilities": ["windows-desktop", "cdp"],
    "require_trusted": true
  }
}
```

环境 manifest 摘要只是选择约束，不是信任凭证。启用 Windows slot lifecycle 前，
必须通过环境包管理边界安装并验证对应版本，并将验证后的 Record 通过 Store API
持久化为同一 bbolt 数据目录中的 `environment_packages` 记录；只有
`trusted`、`enabled`、`healthy` 和 `ready` 全部成立时才可使用。服务启动时
若找不到完全匹配的 ready 记录会 fail closed，slot 保持 unprovisioned/provisioning，不会仅凭配置
或调用方 requirement 运行。

服务提供显式环境维护入口：`-environment-install`/`-environment-upgrade`
安装签名包，随后使用 `-environment-id`、`-environment-version` 配合
`-environment-trust`、`-environment-enable`、`-environment-health` 和
`-environment-promote` 完成生命周期门；`-environment-disable` 和
`-environment-rollback` 用于停用及回滚。部署 signer 公钥放在
`<data_dir>/.chuzi/environment-trust.json`，服务不从普通配置或命令行读取
私钥、runtime executable 或任意路径。

Windows 主机若启用 OS 用户池，还需在同一配置中声明与 `job_pool` 容量和环境版本
一致的 `windows_job_pool`：

```json
{
  "windows_job_pool": {
    "enabled": true,
    "desired_slots": 2,
    "user_prefix": "ChuziJob",
    "rdp_enabled": false,
    "session_idle_timeout_seconds": 300,
    "agent_heartbeat_seconds": 10,
    "provision_timeout_seconds": 120,
    "cleanup_timeout_seconds": 60,
    "environment_id": "chuzi-environment/v1",
    "environment_version": "1.0.0"
  }
}
```

服务启动时先 reconcile 逻辑 slot 记录。Windows 构建在 `windows_job_pool.enabled`
时继续运行受控 provisioner：它创建/修复带 CHUZI 标记的普通本地用户、受限目录 ACL、
session-aware `chuzi-user-agent.exe` 和 per-slot Job Object；没有 active session 或
agent heartbeat 时 slot 保持 provisioning/quarantined，不会参与调度。非 Windows 构建
返回 `slotwindows.ErrUnsupported`，只运行逻辑 slot。

每次 lifecycle reconcile 失败都会写入脱敏的 `slot/reconcile` 事件，并递增
`chuzi_slot_reconcile_errors_total`；错误正文、SID、路径和凭证不会写入日志。最近一次
失败会让 health endpoint 的 `slot_lifecycle` 检查返回 503，下一次成功 reconcile 后恢复
ready。这样 OS 边界故障既会继续按固定间隔重试，也会出现在指标、结构化日志和 readiness
投影中。

## Windows job pool 原生验收

阶段 4 的原生 smoke 只能在受控 Windows runner 上运行。runner 必须使用管理员权限、
可创建 disposable 本地用户、可查询 WTS session，并带有 `go.exe`、Node.js 20+ 和
当前 checkout；专用 runner 标签为 `self-hosted`, `windows`, `chuzi-job-pool`。测试脚本不接受
用户名、路径、命令或 executable 参数：

```powershell
scripts/test_windows_job_pool_smoke.ps1
```

脚本在 `RUNNER_TEMP` 下创建一次性 runtime、data 和 Profile 目录，构建固定的
`chuzi-user-agent.exe`，复制 runner 提供的 `node.exe` 与 `browser-worker/src/worker.mjs`，
并在退出时只删除带有 `CHUZI-MANAGED` ownership 标记的测试用户和本次临时目录。输出不得
包含密码、SID、用户名、Profile 路径、pipe 路径或原始 Win32 错误。没有可用的管理员权限或
已登录的受控用户 session 时，smoke 必须失败；不能将交叉编译或逻辑测试当作 Windows
原生通过。

`chuzi-build-windows-job-pool-preflight` 在 GitHub-hosted `windows-2022` 上编译所有
Windows Go 包、运行 `go vet`、检查 native smoke 测试入口并构建固定 user-agent。手动
`test` 通道使用这个 preflight 完成可安装包的 CI 验证，并明确跳过需要受管用户真实 WTS
session 的 native smoke；`nightly`、`stable` 和主分支构建仍要求专用 native runner。
因此 test 包通过不代表 Windows 用户/session/desktop 已完成生产验收，安装后的真机 smoke
仍是后续验收步骤。

原生 smoke 覆盖受管用户创建/复用/删除、Remote Desktop Users 成员和
Administrators 排除、ownership/SID 对账、Profile ACL、reparse/path traversal 拒绝、
agent named pipe/token、session-aware worker 启动、browser worker handshake、worker
停止、active session health、过期 lease fence、service shutdown agent cleanup、未知
ownership 项保护和资源退休。发布 workflow 的
`chuzi-build-windows-job-pool-smoke` job 使用专用 runner；对 nightly/stable 该 job 失败或
排队不可用时，`chuzi-build` 聚合检查失败或保持等待，发布不能继续。

## Windows readiness 和故障处理

Windows job pool 的 readiness 必须按以下分类投影，不能将计数非零直接解释为生产 ready：

| 分类 | 含义 | 处理 |
| --- | --- | --- |
| `slot_pool_disabled` | 未启用逻辑或 Windows pool | 不调度作业 |
| `provisioning` | 正在创建或修复 slot/agent | 等待 reconcile |
| `ready` | trusted environment、session、desktop、agent health 均通过 | 允许调度 |
| `degraded` | reconcile、health 或容量部分失败 | 保留可用 slot，限制新作业并告警 |
| `quarantined` | agent/ACL/session 故障隔离 | 不调度，按 ownership 检查修复 |
| `draining` | 缩容或 generation 升级等待 lease 释放 | 不接新作业 |
| `rdp_unavailable` | RDP 未启用或无受控 authorizer/broker | 仅允许非交互能力 |
| `environment_untrusted` | manifest、digest、signer 或 ready record 不匹配 | fail closed |
| `windows_platform_unavailable` | 非 Windows、权限不足或原生 smoke 未通过 | 不启用 OS 用户池 |

RDP capability 仍由 credential boundary 管理，并只向 Core/UI 返回 opaque token。当前默认
authorizer 是 deny-by-default；在提供受控 RDP broker、Windows API 权限和测试凭证边界之前，
生产交互式 RDP 必须保持 `rdp_unavailable`。

## 扩缩容、升级和恢复

扩容先增加 `desired_slots`，由 reconcile 创建新的 generation；缩容将 leased slot 标为
`draining`，释放 lease 后才进入 `retiring`。环境升级必须先安装、验证、trust、health 和
promote 新 package；旧 generation 继续服务已有 lease，释放后才切换。升级失败或回滚期间
保持旧的 ready record，不删除未知目录或用户。

服务重启或断电恢复时，agent Job Object 和 lease fence 优先处理；过期 account/slot lease
不得直接恢复为 Ready，残留 agent 必须先停止，generation 不匹配的 agent 不能接收新 request。
quarantine 只能在 ownership、ACL、环境和 agent health 全部重新通过后显式恢复。紧急停用时
先禁用 pool 和 RDP authorizer，再停止服务；恢复按 package promote、reconcile、health、
readiness 顺序执行。

## 目标部署的最低运行要求

1. 可持久化的状态数据库。
2. 可持久化且受限的凭证/会话存储。
3. Matrix Bot 用户和授权房间。
4. 与运行模式匹配的浏览器运行时及其资源限制。
5. 日志轮转、健康检查和任务租约回收。

`cmd/service` 已运行持久化 Store、Queue 和 Session Runner 调度循环；默认 Node
backend 仍是 deferred/合成 Worker。配置 Matrix 后会启用 HTTP sync/send client、
同步网关和通知 worker；凭证注入通过 `-inject-account` 从显式环境变量加密写入，
`-rotate-account` 使用当前部署 key 重加密，`-revoke-account` 在清除密文前执行撤销。

## Core API 本地 IPC

服务普通启动会同时监听由 `data_dir` 派生的 Core endpoint。Unix 为
`<data_dir>/core.sock`，目录权限为 `0700`、socket 权限为 `0600`；Windows 为带数据目录
摘要的 owner-only named pipe。endpoint 不能由请求正文覆盖，活动 socket 也不会被第二个
服务实例删除。launcher 应使用 `internal/coretransport.Connect`，先执行 `chuzi.core/v1`
`hello`，再调用 JSONL Core API；原生客户端应调用 launcher 的稳定命令，不直接打开 named
pipe、bbolt 或读取 Profile。

每个调用带唯一 request ID，客户端可并发发起调用，服务端按 ID 返回响应。取消 context
会发送 transport-level `cancel`，业务上的 `cancel_request` 仍是独立的状态机命令。
响应只含 Core DTO 和稳定错误码，禁止返回 store、凭证、Profile 路径或底层错误文本。
契约测试覆盖版本拒绝、未知方法、握手前访问、敏感字段脱敏、取消、并发多路复用、超大
帧、socket 权限和 endpoint 占用。

首个 Windows Slint 客户端在 `ui/windows`，构建脚本为
`scripts/build_windows_slint.ps1`，Actions 任务 `chuzi-build-windows-slint` 生成并上传
`chuzi-windows-installer-exe`。安装器把自包含 Slint 发布目录安装到
`Program Files\\Chuzi`，并将 Core payload 随安装包放在应用目录中。launcher 通过
`%ProgramData%\\chuzi`（或部署提供的 `CHUZI_DATA_DIR`）派生与 Go 相同的
named pipe 名称，执行 `hello` 后调用提交、查询和取消；Windows 客户端只调用 launcher
并显示稳定错误码。launcher 的 `core-status`、`core-start`、`core-stop` 和 `core-call`
命令是 UI 到 Core 的统一后台入口。

## 浏览器运行时前置条件

当前服务只保留 Node.js Worker。默认 deferred Worker 仅验证协议和生命周期；真实
浏览器流程使用可配置的 headed-CDP 或 headless-CDP，并由部署环境提供 Chromium/Edge。Profile 由
服务生成并保存在受限数据目录，运行时不接受请求方提供的文件系统路径。

CDP 后端依赖部署环境已安装的 Chromium/Edge，通过独立 Node worker 的 CDP 连接控制。
`-browser-backend headless` 选择无头模式，`-browser-backend headed` 选择有头模式；
`-browser-command` 必须是部署方明确配置的单一可执行文件路径/名称，参数
由 worker 固定生成且不经过 shell。worker 使用动态 loopback 端口和服务派生的
`--user-data-dir`，只轮询 `/json/version`；发现成功后仍须由上层自动化适配器执行
页面操作，当前 worker 不报告业务成功。

首个真实流程通过 `-browser-backend headed -automation-adapter genshin-cloudgame`
显式启用，固定检查 `https://ys.mihoyo.com/cloud/#/` 的已授权会话。它复用 worker 已启动的
Chromium/CDP 会话，不启动第二个浏览器。

Windows 客户端 Sessions 页面可通过 Core 的 `get_browser_view` 请求活动会话的一次
JPEG 快照。该请求只在用户点击查看时产生截图和 IPC 传输；未查看时 headless
浏览器不会持续编码或发送帧，因此不会引入常驻视频流开销。快照是只读的，当前
不提供鼠标或键盘控制；请求必须命中仍在运行的 request，否则返回 unavailable。

Matrix HTTP client 使用 Client-Server `sync`、`send` 和 `whoami` 接口；access token
由 `matrix.access_token_env` 指定的环境变量注入。通知 outbox 保存在同一 bbolt
数据库，发送 worker 使用稳定 event ID 并在网络失败后保留记录。

阶段二的默认存储拓扑是单节点纯 Go bbolt。一个数据目录只能由一个服务
实例拥有；该文件锁不提供跨主机多实例一致性。服务入口组装 `Store` 后，
`Store.Open` 会运行可重复的 schema 迁移并拒绝未知版本；调度器在每次循环开始
时恢复过期租约并处理截止时间。多实例部署必须在单独的 CR 中选择外部数据库
和并发/迁移策略。

凭证密钥由部署环境注入。默认环境适配器读取 `CHUZI_CREDENTIAL_KEY_ID` 和
`CHUZI_CREDENTIAL_KEY`；生产环境进行轮换时，Secret 管理器必须在切换期间
同时提供当前 key 和 `CHUZI_CREDENTIAL_KEYS` JSON map 中的旧 key。密钥缺失
或历史 map 无法解析时服务应安全失败，不能创建明文回退。轮换窗口内对所有
账号执行 `-rotate-account`，确认审计和恢复演练完成后再移除旧 key。

配置/存储库层加载示例（`cmd/service` 普通启动使用同一顺序）：

```go
cfg, err := config.Load("configs/example.json")
store, err := store.Open(cfg)
```

备份由存储服务写入配置派生的 `backups/` 目录；`-restore` 只接受该目录内的
非符号链接、0600 文件，并验证当前 schema 后以临时文件和原子替换恢复数据库。
恢复必须在服务停止时执行；Profile 目录仍由部署系统独立备份。

## 构建与发布目标

### CI DAG and pull request checks

Pull request 更新由 `.github/workflows/chuzi-pr.yml` 执行并行的 contract、Go 和 Node
源码测试，最后由同名 `chuzi-build` 聚合 job 作为 required check。同一个 PR 的旧运行会在
新 push 后取消，避免分支迭代时堆积过时检查。PR 不执行 Go/Launcher 发布编译、Worker
产物构建或 release package。

`.github/workflows/chuzi-build.yml` 是 `main` 合并、正式 tag、nightly 计划任务和手动
dispatch 使用的完整构建 DAG。它不监听普通开发分支的 push；分支 PR 只执行上面的快速
源码检查，合并到 `main` 后才启动完整目标构建。公共 contract/Go/Node 校验各自只
运行一次，并与以下独立构建并行：

* `go_build` matrix 在 Ubuntu 上交叉编译四个目标的 Go service/launcher。
* `node_checks` 只构建一次 Node Worker，并通过 `ci-worker` artifact 交给所有目标。
* `windows_slint` 在一个 Windows runner 上完成自包含 Slint 安装器 EXE 构建和
  named-pipe 契约测试；它不需要 MSIX 测试证书或单独的 Runtime MSIX。

每个 `assemble_target` job 从 `ci-go-*`、`ci-worker` 和 `ci-runtime-*` artifact 组装一个
目标包，job 的显示名称仍为 `chuzi-build-<target>`，以保持验收和 release retry 的
审计契约。test/nightly/stable 产物再由 `artifact_integration` 在单独 runner
汇聚，验证 commit、版本、release index、归档内容和 SHA-256，最后才允许
`chuzi-build` 聚合 job 通过。Runner 之间不共享本地文件系统，只通过 Actions artifact
传递构建结果。

### 三条发布通道

三条通道由同一 workflow 构建，但 channel 和 version 由 `release_context` 一次解析，
后续 job 不再自行推导。Actions artifact、catalog 目录和 prerelease 标记按通道隔离：

| Channel | Entry point | Version | Artifact | Catalog |
| --- | --- | --- | --- | --- |
| `test` | 手动 dispatch，任意分支 | `test-<run>-<sha12>` | `chuzi-test-<target>` | `test/`，`prerelease: true` |
| `nightly` | `main` 的计划任务或手动 dispatch | `nightly-<run>-<sha12>` | `chuzi-nightly-<target>` | `nightly/`，`prerelease: true` |
| `stable` | 人工确认后创建签名 annotated `vX.Y.Z` tag | `vX.Y.Z` | `chuzi-stable-<target>` | `stable/`，`prerelease: false` |

Test 允许新代码带有未知问题，只能发布 test catalog。Nightly 只接受 `main`，必须通过
完整测试和构建后才发布 nightly catalog。Stable 不从 test/nightly 自动晋级；人工确认后创建
签名 annotated tag，tag workflow 才会生成 GitHub Release、attestation 和 stable catalog。

### Nightly release

`.github/workflows/chuzi-build.yml` 每天 `02:17 UTC` 自动运行，也支持在 `main` 手动
触发。Nightly 不创建 Git tag 或 GitHub Release，而是为四个平台上传保留 14 天
的 Actions artifact，并使用 `nightly-<run-number>-<commit-short-hash>` 版本号。每个平台
同时生成完整包、按 `launcher`、`service`、`browser-worker` 拆分的
组件包，以及记录归档大小/SHA-256 的 `release-index.json`；不创建 tag 或 GitHub Release。

完整包内的 `release-manifest.json` 是启动器 CLI 和原生客户端的稳定输入，声明目标平台、
版本、组件资源 SHA-256/大小、适配器描述和更新 channel。`cmd/launcher` 提供 manifest
展示、校验、`initialize`/`initialize-complete` 首次启动状态、基于本地或 HTTPS index
的更新检查、资源修复、组件启停、适配器安装/更新/信任/启停/移除和 `settings`/`settings-save` CLI。设置中的
`start_core_on_launch` 只允许客户端在应用启动时启动已经安装的 Core，不会隐式安装或修改组件。
修改安装目录或设置前会取得 `.chuzi/launcher.lock`，`-progress` 可将脱敏的阶段事件
写到 stderr，Ctrl-C 会通过 context 取消当前操作。显式 `-release-index` 时，下载器
只接受 HTTPS（本地测试可显式允许 loopback HTTP）、同源归档，并校验目标平台、版本、
大小和 SHA-256；组件及其依赖先安全解包到临时 source，再复用原子资源修复和状态回滚。
已有安装目录在升级时可以继续保留旧版 `release-manifest.json`；只要 target 和 channel
一致，新 release index 的版本和 commit 会作为候选版本使用。索引或归档校验失败时，
旧组件状态和文件保持不变，不能把 endpoint 可达或归档下载完成误报为业务安装成功。
未提供 index 时，修复和组件安装仍只使用显式本地 source root。
锁不会自动清除遗留文件，确认占用进程已退出后才允许人工移除。launcher 组件当前
只包含 UI-neutral `chuzi-launcher` CLI；Windows Slint 客户端另以自包含安装器 EXE 分发，
macOS SwiftUI 和 Linux GTK 客户端待后续 CR。所有平台 UI 都通过 Stable API Boundary
调用同一 CLI/Core 能力，不复制文件、下载、校验、执行适配器、授予 signer 信任或实现回滚策略。诊断输出不得记录
凭证或启动器响应 payload。
Nightly 的适配器清单会区分 Browser Worker 内置 runtime 和独立适配器包。当前
`genshin-cloudgame` 使用 `chuzi-adapter/v1` 独立归档：Windows 为 `.zip`，Linux/macOS
为 `.tar.gz`，索引记录归档大小和 SHA-256。只有声明 `distribution: package` 且带归档
和 SHA-256 的适配器才提供独立安装包，不能将 browser-worker 组件包误认为适配器包。
安装包落在服务管理的 `<data_dir>/plugins/<adapter-id>/`，服务只按 adapter ID 从该目录
建立 Registry；安装、校验、信任、启用和运行状态分开保存。升级会清除旧 trust，归档或
状态提交失败时旧包保持可恢复。

GitHub Actions 负责远端构建，不要求开发者在本地安装完整的发布工具链。构建使用 Go 控制服务和 Node.js Worker 两套锁定的工具链，目标矩阵为：

| Target | Output | Build mode |
| --- | --- | --- |
| `windows-amd64` | `.zip` | Windows native runner |
| `linux-amd64` | `.tar.gz` | Linux native runner |
| `linux-arm64` | `.tar.gz` | Linux native ARM64 runner (`ubuntu-24.04-arm`) |
| `darwin-arm64` | `.tar.gz` | Apple Silicon macOS runner |

构建命令由以下脚本定义：

```bash
scripts/build.sh <target> [version]
scripts/package.sh <target> <version>
scripts/build_go_target.sh <target> <version> <output-dir>
scripts/build_worker.sh <output-dir>
scripts/assemble_target.sh <target> <version> <go-dir> <worker-archive> <dist-root>
```

Windows runner 使用对应的 `*.ps1` 脚本。`build.sh`/`build.ps1` 保留为本地一体化构建入口，
并行 CI 使用组件构建和 `assemble_target` 脚本。Windows 目标会把 Node.js `node.exe`
一并放入安装目录；在 Unix 主机组装 Windows 目标时，必须通过 `CHUZI_NODE_RUNTIME`
指定同架构的 `node.exe`。构建产物必须包含 Go 服务、Worker 文件
和 `build-manifest.json`，并生成 SHA256 校验文件。CI smoke test 只使用
本地 Worker、内嵌测试页和测试协议，不使用真实云游戏账号或生产凭证。

稳定版只能由人工确认后创建的带签名 annotated semver tag 触发。tag 构建会复用四平台构建矩阵，
为每个归档生成并签署 UGS attestation，校验签名者、tag/commit 绑定和归档
SHA-256 后才创建 GitHub Release。发布需要仓库 Secret
`CHUZI_RELEASE_SIGNING_KEY` 与变量 `CHUZI_RELEASE_SIGNER`，私钥只存在于
短生命周期 runner。发布前必须提交 `releases/v<version>.md`；缺失或未信任的
tag 会在发布 job 早期失败。

如果正式 tag 已存在，但该 tag 中的 workflow 定义早于发布修复，可从 `main` 使用
`.github/workflows/release-retry.yml` 重试发布。重试必须显式提供已经成功完成四平台
构建的 Actions Run、正式 tag 和 tag 目标 commit；入口会重新验证签名 tag、Run 的
矩阵作业、归档索引、SHA-256 和 stable manifest，然后才签署 attestation 并创建
GitHub Release：

```bash
gh workflow run release-retry.yml --repo Semcosm/chuzi --ref main \
  -f run_id=<successful-build-run> \
  -f release_tag=v<major>.<minor>.<patch> \
  -f commit_sha=<tag-target-commit>
```

当前 Node Worker 提供 deferred 协议和生命周期替身，以及可配置的 headed/headless-CDP
进程边界；Genshin Cloud Game 已作为独立适配器包接入。其他业务适配器、WebDriver、浏览器下载或
其他原生模块必须在单独 CR 中增加，并为四个发布目标记录构建和运行覆盖范围。

业务自动化适配器是独立于浏览器 Worker 的进程边界。Windows 可直接启动原生适配器
进程；macOS/Linux 如使用 Wine，必须为每个插件派生独立的 Wine prefix，并验证
Wine 可执行文件、Windows 运行库、图形会话和目标插件版本。Wine 启动不是当前四
平台 release 的既定能力。适配器协议使用 `chuzi.adapter/v1`，只传递
服务派生的 session/request 标识和脱敏运行事实，凭证不得进入 JSONL payload。

CR-0043 的云原神适配器通过显式服务选项运行，不改变服务默认的 deferred backend：
`chuzi -browser-backend headed -automation-adapter genshin-cloudgame
-browser-command <installed-browser>`。它只检查已授权 Profile，不接收明文
凭据，不处理验证码/风控，不接受任意 URL；适配器返回页面事实，由 Core evaluator
判断认证结果，未认证或页面不匹配时 fail closed。测试和
CI smoke 继续使用 fake CDP fixture、假账号和本地页面，不代表生产账号已登录。

## 运维检查

- 部署前运行 `scripts/validate_policy_manifest.sh`。
- 定期检查悬挂任务、过期凭证、Profile 磁盘占用和 Matrix 发送失败数。
- 定期检查 Matrix outbox 的待发送数量、过期 claim 和按分类统计的发送失败。
- 使用 `-diagnostics` 检查 schema、队列、租约、outbox 和数据库大小；使用 `-audit`
  读取有界的脱敏状态/凭证操作审计，使用 `-validate-backup` 在恢复演练前验证备份。
- 部署变更前运行 `scripts/test_runtime.sh`；它只使用 fake 账号、本地协议测试
  homeserver、测试凭证和本地 Worker。受控环境可在提供 disposable Matrix
  homeserver 的 `CHUZI_MATRIX_TEST_*` 变量后额外设置
  `CHUZI_RUN_CONTROLLED_MATRIX=1`，验证真实 `whoami`、sync、send 和网关/通知链路。
- `rate_limit` 下的 `global_*`、`actor_*`、`room_*` 和 `account_*` 字段分别限制
  全局、调用方、Matrix 房间和账号的新请求提交；每个 `*_limit` 为 0 时关闭该维度，
  非零上限必须配置正的 `*_window_seconds`，窗口最长 24 小时。
- `observability.metrics_listen` 提供本地 Prometheus 文本端点；`log_path` 启用
  0600 JSONL 日志并按 `log_max_bytes`/`log_max_files` 轮转。两个端点都必须限制在
  loopback 或受保护管理网络。
- `diagnostics.enabled` 控制用户同意后的故障报告能力；`diagnostics.endpoint`
  只能使用 HTTPS（本机测试允许 loopback HTTP），不配置 endpoint 时报告仍会以
  0600 文件保存在 data_dir 派生的 `diagnostics/` 队列。服务每 30 秒尝试一次待发送
  报告，网络失败按退避时间重试；队列内容仅包含分类事件、平台/版本和脱敏摘要。
- 备份状态数据库前确认凭证密文与密钥分离保存；撤销后的凭据密文已擦除且不可恢复。
- 发布遵循 UGS 的版本和变更记录规则。
