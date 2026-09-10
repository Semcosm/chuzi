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
4. 浏览器运行时及其资源限制。
5. 日志轮转、健康检查和任务租约回收。

Matrix 适配器当前只提供可注入的 transport-neutral Sender 边界；生产部署还
需要在后续阶段选择 Matrix SDK、access token Secret 和连接/同步策略。通知
outbox 保存在同一 bbolt 数据库，发送 worker 必须使用稳定 event ID 并在网络
失败后保留记录。

阶段二的默认存储拓扑是单节点纯 Go bbolt。一个数据目录只能由一个服务
实例拥有；该文件锁不提供跨主机多实例一致性。服务启动时会运行可重复的
schema 迁移并拒绝未知版本。多实例部署必须在单独的 CR 中选择外部数据
库和并发/迁移策略。

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

GitHub Actions 负责远端构建，不要求开发者在本地安装完整的发布工具链。构建使用 Go 控制服务和 Node.js Worker 两套锁定的工具链，目标矩阵为：

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

当前 Worker 只提供协议和生命周期验证；Linux ARM64 的 Go、Node.js 和协议 smoke test 已在原生 ARM64 runner 执行，但真实浏览器/WebView 仍未接入。WebView、Playwright/Chromium 适配必须在增加依赖、浏览器下载或原生模块前单独提交 CR，并为四个发布目标记录运行覆盖范围。

## 运维检查

- 部署前运行 `scripts/validate_policy_manifest.sh`。
- 定期检查悬挂任务、过期凭证、Profile 磁盘占用和 Matrix 发送失败数。
- 定期检查 Matrix outbox 的待发送数量、过期 claim 和按分类统计的发送失败。
- 备份状态数据库前确认凭证密文与密钥分离保存；撤销后的凭据密文已擦除且不可恢复。
- 发布遵循 UGS 的版本和变更记录规则。
