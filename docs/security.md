# 安全与凭证管理

## 凭证

- 数据库存储密文，密钥通过部署环境的 Secret 管理，不与代码或示例配置一起提交。
- 业务代码通过最小权限接口获取短时使用句柄；默认不返回明文凭证。
- 支持凭证轮换、撤销和审计；撤销时可先调用可选的会话失效边界。账号删除
  流程当前尚未实现。
- 日志、错误堆栈、截图和 Matrix 消息均需脱敏。

Matrix command/notification 边界只允许固定命令和显式白名单房间/用户。账号在
回复和通知中使用稳定 hash 标签；状态消息不包含凭证、Cookie、页面内容、房间
原始 ID、操作者或内部错误文本。观测事件只保存操作、结果、request ID、hash
资源标签和分类错误。

当前 Credential Store 使用 AES-GCM，附加数据绑定 `account_id`、凭证版本和
`key_id`，因此跨账号、跨版本或跨密钥替换会认证失败。bbolt `credentials`
桶只保存密文和元数据，`credential_audits` 桶只保存操作、操作者、版本、key ID
和时间，不保存密码、Token、Cookie 或页面内容。

密钥通过 `Keyring` 接口提供；`EnvKeyring` 默认读取部署环境的
`CHUZI_CREDENTIAL_KEY_ID` 与 `CHUZI_CREDENTIAL_KEY`，后者使用 base64 或十六进制
编码。环境适配器只提供当前 key；生产轮换必须使用能保留历史 key 的 Secret
管理器实现。缺少 key、篡改密文或认证标签错误都会安全失败且不返回明文。

撤销会先要求可选的会话失效边界停止关联会话；失效失败时凭据保持可用并且
不会写入撤销审计。成功后才擦除 nonce 和密文，之后访问和轮换都会被拒绝。
`Use` 回调收到的工作缓冲在返回后清零，调用方不得保留该 slice；审计记录在
授权访问前写入并与相关记录变更使用同一事务。

## 浏览器 Profile

每个账号绑定独立 Profile 目录和互斥租约，避免 Cookie、缓存和 LocalStorage 串号。Profile 路径只能由服务生成，禁止把任意用户输入直接拼接为文件路径。当前 `Profiles.Prepare/Acquire` 会保留目录供后续尝试复用，清理和保留策略仍是显式的后续运维层；持久会话数据应加密或置于受限目录。

本项目支持“每线程独立会话标识”和正常的浏览器配置隔离；不把伪造设备信息、规避风控或绕过验证码作为需求。

## 浏览器运行时边界

Rust runtime helper 通过独立进程和版本化 JSON Lines 与 Go 控制面通信。Go
Profile 边界只向它传入服务派生的路径和受限 session 参数；helper 对路径再做
绝对路径、无父目录跳转的协议校验，不能把请求输入解释为任意文件路径，也不能
直接读取 Credential Store 或写入账号状态。helper 已由原生
CI 构建；Linux amd64/arm64 还通过 X11/Wayland WebKitGTK smoke，Windows/macOS
目前只有 Wry 编译与打包检查，尚未完成 GUI 运行时 smoke。`cmd/service` 默认仍启动
Node deferred Worker；Rust helper 只有通过 `-browser-backend rust` 才会被显式选择。

桌面 WebView 的 visible/hidden 模式都依赖操作系统图形会话；隐藏窗口不是
headless 安全边界。真正 headless 后端必须单独审查已安装 Chromium/Edge 的可执行文件、CDP 端口、
Profile 权限、网络范围和进程隔离。当前 headless worker 只接受服务配置的浏览器
命令，固定使用 `--headless=new`、loopback CDP 地址、动态端口和服务派生
`--user-data-dir`，通过 `/json/version` 严格校验 loopback `ws:` endpoint；命令参数
不经过 shell，浏览器 stdout/stderr 不进入协议。worker 只返回 `session_handle`、
主机/端口元数据和分类运行事实，不接触 Credential Store。

首个真实 WebView vertical slice 只允许本地测试页、显式导航、有限 JS 执行和
固定结果读取。不得在 CI 或 smoke test 中注入真实凭证、Cookie、生产 URL、
截图或下载内容。页面内容、脚本错误、Cookie、请求头和运行时堆栈不得进入日志、
Matrix 消息或 metadata-only 审计。

当前 Windows backend 将服务派生的 `profile_dir` 交给独立 WebContext；Linux
WebKitGTK backend 也使用服务派生的 `profile_dir` 和独立 WebContext；macOS
11+ backend 使用 WKWebView ephemeral store，因此当前 helper slice 不宣称
macOS 持久 Profile 或凭证会话已经可用。Linux X11/Wayland backend 仍需要 GUI
session，不创建真正 headless 浏览器。

headless 浏览器缺失、CDP endpoint 超时/非法、Profile 路径不合法和进程崩溃必须
fail closed，并映射为分类 runtime/configuration fact。平台运行时缺失（WebView2、GTK/WebKitGTK、图形会话）也必须 fail closed，并映射
为分类 runtime/configuration fact；不能自动下载未知浏览器、回退到系统任意
可执行文件，或借助 CAPTCHA、风控和反检测技术改变第三方服务行为。

## 权限与审计

Matrix 用户/房间采用白名单或角色授权。目标部署中的管理命令（添加账号、读取
状态、取消任务、轮换凭证）必须记录审计事件。当前适配器只实现 `request`、
`status`、`cancel` 和 `help`，并通过注入的脱敏 `observability.Sink` 记录操作；
账号管理、凭证轮换命令及生产审计接入尚未组装。默认拒绝跨账号查询，服务端
校验请求者权限而不是信任客户端字段。
