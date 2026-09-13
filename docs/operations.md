# 部署与运维

## 配置分类

- 普通配置：`data_dir`、Matrix homeserver/user/policy、同步时序、凭证 key 环境变量
  名称和健康监听地址。`configs/example.json` 不包含任何 Secret 值。
- Secret 配置：Matrix access token、凭证加密主密钥（只由环境/Secret manager 注入）。
- 运行数据：数据库、浏览器 Profile、审计日志和待发送事件。

普通配置示例位于 `configs/example.json`，包含数据目录、凭证环境变量名称和本地
健康监听地址；Matrix 网络配置按需添加。数据库路径固定由存储层派生为
`<data_dir>/chuzi.db`，备份目录固定为 `<data_dir>/backups/`；请求和账号
输入不能覆盖这些路径。`cmd/service` 普通启动默认加载该文件，也可通过 `-config`
指定路径；Store、Profile 和队列都从同一 `data_dir` 派生。Secret 不进入 Git。
生产环境至少限制服务账户、数据库和 Profile 目录的文件权限。

## 目标部署的最低运行要求

1. 可持久化的状态数据库。
2. 可持久化且受限的凭证/会话存储。
3. Matrix Bot 用户和授权房间。
4. 与运行模式匹配的浏览器运行时及其资源限制。
5. 日志轮转、健康检查和任务租约回收。

`cmd/service` 已运行持久化 Store、Queue 和 Session Runner 调度循环；默认 Node
backend 仍是 deferred/合成 Worker。配置 Matrix 后会启用 HTTP sync/send client、
同步网关和通知 worker；凭证注入通过 `-inject-account` 从显式环境变量加密写入。

## 浏览器运行时前置条件

桌面 WebView 和真正 headless 是两个部署模式。visible 或 hidden 的桌面
WebView 都需要图形会话和平台事件循环；hidden 只是不向用户显示窗口，不等于
无显示环境运行。

| 平台 | Desktop WebView 运行时 | 首批前置条件与范围 |
| --- | --- | --- |
| Windows 10/11 | WebView2 Evergreen 或 Fixed Version Runtime | 启动前检测 Runtime；Windows 10 不能假定系统已有；不依赖普通 Edge 浏览器本体 |
| macOS 11+ Apple Silicon | 系统 WKWebView | 需要 GUI session/run loop；首批只覆盖 Apple Silicon 原生构建 |
| Ubuntu 24.04 LTS amd64/arm64 | WebKitGTK 4.1 | 构建需要 GTK/WebKitGTK 4.1 开发包，运行需要对应运行库和有效 GUI session；首期覆盖 X11 与 Wayland，其他发行版另行验证 |

WebView2 缺失、GTK/WebKitGTK 缺失、图形会话缺失和权限错误必须返回稳定的
分类错误，不能伪装成普通账号失败。Profile 由服务生成并保存在受限数据目录，
运行时不接受请求方提供的文件系统路径。

真正 headless 后端依赖部署环境已安装的 Chromium/Edge，通过独立 Node worker 的
CDP 连接控制；它不复用桌面 WebView 的“隐藏窗口”模式，也不承诺 Safari 或
WKWebView 可 headless。`-browser-backend headless` 选择该 worker，
`-headless-browser-command` 必须是部署方明确配置的单一可执行文件路径/名称，参数
由 worker 固定生成且不经过 shell。worker 使用动态 loopback 端口和服务派生的
`--user-data-dir`，只轮询 `/json/version`；发现成功后仍须由上层自动化适配器执行
页面操作，当前 worker 不报告业务成功。

当前 Rust helper 已在发布 stage 中构建；Linux amd64/arm64 的原生 CI 已在
X11/Wayland 下执行 WebKitGTK smoke，Windows/macOS 则执行 Wry 编译与打包检查，
尚未完成 Windows/macOS 的 GUI 运行时 smoke。服务入口仍未将 helper 设为默认
Worker；部署时不能仅凭 stage 中存在 helper 就推断服务具备真实浏览器自动化能力。

Matrix HTTP client 使用 Client-Server `sync`、`send` 和 `whoami` 接口；access token
由 `matrix.access_token_env` 指定的环境变量注入。通知 outbox 保存在同一 bbolt
数据库，发送 worker 使用稳定 event ID 并在网络失败后保留记录。

阶段二的默认存储拓扑是单节点纯 Go bbolt。一个数据目录只能由一个服务
实例拥有；该文件锁不提供跨主机多实例一致性。服务入口组装 `Store` 后，
`Store.Open` 会运行可重复的 schema 迁移并拒绝未知版本；调度器在每次循环开始
时恢复过期租约并处理截止时间。多实例部署必须在单独的 CR 中选择外部数据库
和并发/迁移策略。

Rust helper 在构建阶段由 stable Rust/Cargo 编译，发布包携带编译后的
`chuzi-browser-runtime`，部署主机不需要安装 Rust。Windows/macOS/Linux 构建启用
Wry；Linux 运行时需要 WebKitGTK 4.1、GTK 和有效的 X11 或 Wayland 图形会话。
Wry、GTK/WebKitGTK 和 headless 浏览器的运行库要求仍按各自平台和 CR 声明。

凭证密钥由部署环境注入。默认环境适配器读取 `CHUZI_CREDENTIAL_KEY_ID` 和
`CHUZI_CREDENTIAL_KEY`；生产环境进行轮换时，Secret 管理器必须在切换期间
同时提供旧 key 和当前 key。密钥缺失时服务应安全失败，不能创建明文回退。

配置/存储库层加载示例（`cmd/service` 普通启动使用同一顺序）：

```go
cfg, err := config.Load("configs/example.json")
store, err := store.Open(cfg)
```

备份由存储服务写入配置派生的 `backups/` 目录；`-restore` 只接受该目录内的
非符号链接、0600 文件，并验证当前 schema 后以临时文件和原子替换恢复数据库。
恢复必须在服务停止时执行；Profile 目录仍由部署系统独立备份。

## 构建与发布目标

### Nightly release（当前首个 release 流程）

`.github/workflows/chuzi-build.yml` 每天 `02:17 UTC` 自动运行，也支持手动
触发。Nightly 不创建 Git tag 或 GitHub Release，而是为四个平台上传保留 14 天
的 Actions artifact，并使用 `nightly-<run-number>` 版本号。每个平台同时生成完整
包和按 `launcher`、`service`、`browser-worker`、`desktop-runtime` 拆分的组件包。

完整包内的 `release-manifest.json` 是启动器与未来 UI 的稳定输入，声明目标平台、
版本、组件资源 SHA-256/大小、插件描述和更新 channel。`cmd/launcher` 提供 manifest
展示、校验、基于本地 manifest 的更新检查、资源修复、组件启停和插件信任/启停 CLI。
CR-0022 的后台实现不下载未知资源：更新检查通过注入的 source，修复和组件安装只
使用显式本地 source root，插件归档先校验摘要再安全解包到临时目录并原子替换。
Nightly 的 `plugins` 列表默认为空，不能将组件包误认为已实现插件生态。

GitHub Actions 负责远端构建，不要求开发者在本地安装完整的发布工具链。构建使用 Go 控制服务和 Node.js Worker 两套锁定的工具链；Rust helper 的格式和单元测试也在每个目标 runner 上执行，目标矩阵为：

| Target | Output | Build mode |
| --- | --- | --- |
| `windows-amd64` | `.zip` | Windows native runner |
| `linux-amd64` | `.tar.gz` | Linux native runner |
| `linux-arm64` | `.tar.gz` | Linux native ARM64 runner (`ubuntu-24.04-arm`); WebKitGTK desktop helper with X11/Wayland smoke |
| `darwin-arm64` | `.tar.gz` | Apple Silicon macOS runner |

构建命令由以下脚本定义：

```bash
scripts/build.sh <target> [version]
scripts/package.sh <target> <version>
```

Windows runner 使用对应的 `*.ps1` 脚本。构建产物必须包含 Go 服务、Worker 文件、
`chuzi-browser-runtime` 和 `build-manifest.json`，并生成 SHA256 校验文件。
发布脚本生成的 manifest 将 `browserRuntime` 标为 `wry-desktop`；源码中的
无 feature Rust helper 和 Node Worker 才使用 `deferred`，Node 包同时携带
`headless-cdp` 适配器。CI smoke test 只使用
本地 Worker、内嵌测试页和测试协议，不使用真实云游戏账号或生产凭证。

当前 Node Worker 继续提供 deferred 协议和生命周期替身，并提供 headless-CDP
进程边界；四个目标的 Rust/Wry helper
已接入构建，原生编译由对应 runner 验证。Linux amd64/arm64 在原生 runner 上
安装 WebKitGTK 4.1，并使用 Xvfb 与 Weston headless compositor 分别覆盖 X11
和 Wayland 的本地测试页 smoke test；Windows/macOS 没有对应的 GUI 运行时 smoke
步骤。CDP 的业务操作适配器、WebDriver、浏览器下载或其他原生模块必须在单独
CR 中增加，并为四个发布目标分别记录构建、运行库、图形会话和 smoke test 覆盖范围。

CR-0014-B 合并后，Windows/macOS 发布包携带 Wry helper；CR-0014-C 使 Linux
发布包也携带 WebKitGTK helper。Windows/macOS/Linux 的运行仍要求对应平台
WebView2/WKWebView/WebKitGTK 和 GUI session；Wayland smoke 使用 Weston headless
compositor，不能被误解为真正 headless 浏览器。helper 不会自动替代默认 Node
backend，只有 `-browser-backend rust` 才会显式组装；headless 则通过
`-browser-backend headless` 显式选择，业务自动化仍待后续 CR。

业务自动化插件是独立于浏览器 Worker 的进程边界。Windows 可直接启动原生插件
进程；macOS/Linux 如使用 Wine，必须为每个插件派生独立的 Wine prefix，并验证
Wine 可执行文件、Windows 运行库、图形会话和目标插件版本。Wine 启动不是当前四
平台 release 的既定能力，尤其不能从 WebKitGTK 或 Rust helper 的构建结果推断
Linux ARM64/macOS arm64 可运行 BetterGI。插件协议使用 `chuzi.adapter/v1`，只传递
服务派生的 session/request 标识和脱敏运行事实，凭证不得进入 JSONL payload。

CR-0021 的首个适配器通过显式 Node 入口运行，不改变服务默认的 deferred backend：
`node browser-worker/src/headless-adapter.mjs --browser-command <installed-browser>`。
测试和 smoke 只把仓库内 fake CDP fixture 作为 `--browser-command`，并使用
`local.test_page_probe` 与假账号；部署不得把该测试入口解释为生产账号自动化能力。

## 运维检查

- 部署前运行 `scripts/validate_policy_manifest.sh`。
- 定期检查悬挂任务、过期凭证、Profile 磁盘占用和 Matrix 发送失败数。
- 定期检查 Matrix outbox 的待发送数量、过期 claim 和按分类统计的发送失败。
- 备份状态数据库前确认凭证密文与密钥分离保存；撤销后的凭据密文已擦除且不可恢复。
- 发布遵循 UGS 的版本和变更记录规则。
