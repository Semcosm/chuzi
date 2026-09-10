# 部署与运维

## 配置分类

- 普通配置：服务地址、并发上限、超时、重试策略。
- Secret 配置：数据库密码、Matrix access token、凭证加密主密钥（不进入普通配置文件）。
- 运行数据：数据库、浏览器 Profile、审计日志和待发送事件。

普通配置提供 `configs/example.*` 示例，当前阶段使用
`configs/example.json` 的 `data_dir` 字段。数据库路径固定由服务派生为
`<data_dir>/chuzi.db`，备份目录固定为 `<data_dir>/backups/`；请求和账号
输入不能覆盖这些路径。Secret 不进入 Git。生产环境至少限制服务账户、
数据库和 Profile 目录的文件权限。

## 最低运行要求

1. 可持久化的状态数据库。
2. 可持久化且受限的凭证/会话存储。
3. Matrix Bot 用户和授权房间。
4. 与运行模式匹配的浏览器运行时及其资源限制。
5. 日志轮转、健康检查和任务租约回收。

## 浏览器运行时前置条件

桌面 WebView 和真正 headless 是两个部署模式。visible 或 hidden 的桌面
WebView 都需要图形会话和平台事件循环；hidden 只是不向用户显示窗口，不等于
无显示环境运行。

| 平台 | Desktop WebView 运行时 | 首批前置条件与范围 |
| --- | --- | --- |
| Windows 10/11 | WebView2 Evergreen 或 Fixed Version Runtime | 启动前检测 Runtime；Windows 10 不能假定系统已有；不依赖普通 Edge 浏览器本体 |
| macOS 11+ Apple Silicon | 系统 WKWebView | 需要 GUI session/run loop；首批只覆盖 Apple Silicon 原生构建 |
| Ubuntu 24.04 LTS amd64/arm64 | WebKitGTK 4.1 | 需要 GTK/WebKitGTK 4.1 开发与运行库；首期只承诺 X11，Wayland 另行验证 |

WebView2 缺失、GTK/WebKitGTK 缺失、图形会话缺失和权限错误必须返回稳定的
分类错误，不能伪装成普通账号失败。Profile 由服务生成并保存在受限数据目录，
运行时不接受请求方提供的文件系统路径。

真正 headless 后端另行依赖部署环境已安装的 Chromium/Edge，通过 CDP 或
WebDriver 控制；它不复用桌面 WebView 的“隐藏窗口”模式，也不承诺 Safari 或
WKWebView 可 headless。

Matrix 适配器当前只提供可注入的 transport-neutral Sender 边界；生产部署还
需要在后续阶段选择 Matrix SDK、access token Secret 和连接/同步策略。通知
outbox 保存在同一 bbolt 数据库，发送 worker 必须使用稳定 event ID 并在网络
失败后保留记录。

阶段二的默认存储拓扑是单节点纯 Go bbolt。一个数据目录只能由一个服务
实例拥有；该文件锁不提供跨主机多实例一致性。服务启动时会运行可重复的
schema 迁移并拒绝未知版本。多实例部署必须在单独的 CR 中选择外部数据
库和并发/迁移策略。

Rust helper 的协议检查需要 stable Rust/Cargo；当前它只参与格式检查和单元测试，
不进入发布包，也不要求部署主机安装 Rust。Wry、GTK/WebKitGTK 和 headless
浏览器的运行库要求由各自后续 CR 单独声明。

凭证密钥由部署环境注入。默认环境适配器读取 `CHUZI_CREDENTIAL_KEY_ID` 和
`CHUZI_CREDENTIAL_KEY`；生产环境进行轮换时，Secret 管理器必须在切换期间
同时提供旧 key 和当前 key。密钥缺失时服务应安全失败，不能创建明文回退。

配置加载示例：

```go
cfg, err := config.Load("configs/example.json")
store, err := store.Open(cfg)
```

备份由存储服务写入配置派生的 `backups/` 目录；恢复前应验证备份文件、
权限和 schema 版本，不能用未验证的任意路径覆盖运行数据库。

## 构建与发布目标

GitHub Actions 负责远端构建，不要求开发者在本地安装完整的发布工具链。构建使用 Go 控制服务和 Node.js Worker 两套锁定的工具链；Rust helper 的格式和单元测试也在每个目标 runner 上执行，目标矩阵为：

| Target | Output | Build mode |
| --- | --- | --- |
| `windows-amd64` | `.zip` | Windows native runner |
| `linux-amd64` | `.tar.gz` | Linux native runner |
| `linux-arm64` | `.tar.gz` | Linux native ARM64 runner (`ubuntu-24.04-arm`); WebView runtime coverage pending |
| `darwin-arm64` | `.tar.gz` | Apple Silicon macOS runner |

构建命令由以下脚本定义：

```bash
scripts/build.sh <target> [version]
scripts/package.sh <target> <version>
```

Windows runner 使用对应的 `*.ps1` 脚本。构建产物必须包含 Go 服务、Worker 文件和 `build-manifest.json`，并生成 SHA256 校验文件。CI smoke test 只使用本地 Worker 和测试协议，不使用真实云游戏账号或生产凭证。

当前 Node Worker 和 Rust helper 只提供协议和生命周期验证；Linux ARM64 的
Go、Node.js 和协议 smoke test 已在原生 ARM64 runner 执行，但真实浏览器/WebView
仍未接入。Rust/Wry、CDP/WebDriver、浏览器下载或原生模块必须在单独 CR 中增加，
并为四个发布目标分别记录构建、运行库、图形会话和 smoke test 覆盖范围。

在 CR-0014-B/C 合并前，发布包不携带 Wry 原生运行时。Linux ARM64 runner 只能
证明原生 ARM64 构建和无 GUI 协议测试；它不能替代真实 ARM64 图形环境的
WebKitGTK smoke test。

## 运维检查

- 部署前运行 `scripts/validate_policy_manifest.sh`。
- 定期检查悬挂任务、过期凭证、Profile 磁盘占用和 Matrix 发送失败数。
- 定期检查 Matrix outbox 的待发送数量、过期 claim 和按分类统计的发送失败。
- 备份状态数据库前确认凭证密文与密钥分离保存；撤销后的凭据密文已擦除且不可恢复。
- 发布遵循 UGS 的版本和变更记录规则。
