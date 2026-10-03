# Chuzi Windows 最小基座

状态：设计基线
基线日期：2026-10-03

这份文档从 Chuzi 当前代码和运行边界反推 Windows 主机的最小可用集合。目标是先得到一个能稳定创建用户、建立 RDP/WTS Session、启动受控 agent、运行 Node.js + Chromium/CDP 的基座，再逐项裁剪无关组件。用户级 Winlogon Shell 使用固定、签名的 Windows PowerShell 5.1 supervisor；agent 生命周期仍由服务拥有。

## 先定边界

Chuzi 需要的是一个 **Windows Session runtime**，不是一个完整桌面镜像。运行链路是：

```text
Chuzi service
  -> managed local user
  -> User Profile / HKCU
  -> RDP / WTS session
  -> Win32 desktop
  -> chuzi-user-agent
  -> Node.js worker
  -> Chromium/Edge CDP
  -> account browser profile
```

模板用户只用于验证和派生每个槽位的配置，不应作为所有作业共用的长期登录账号。每个执行槽位仍需要独立 SID、Profile、Session、桌面、工作目录和生命周期。

## 四层最小集合

### 1. 主机级基础设施

这些组件是 Windows Session 和 Chuzi 服务的底座，当前阶段不要禁用：

| 组件 | 用途 | 最小要求 |
| --- | --- | --- |
| Local Security Authority / SAM | 本地用户认证、SID、令牌和组成员关系 | 可创建/验证受管普通用户 |
| RPC、DCOM、Service Control Manager | 服务启动、WTS、用户登录和本机 IPC 的系统依赖 | 可用；不要改成禁用 |
| Winlogon、CSRSS、Session Manager | 建立交互式用户 Session 和登录生命周期 | 系统默认运行 |
| User Profile Service (`ProfSvc`) | 加载 Profile、`NTUSER.DAT`、`UsrClass.dat` 和 HKCU | 不得禁用；首次登录必须能生成 Profile |
| Remote Desktop Services (`TermService`) | RDP 认证、连接和 Session 创建 | headed/RDP 模式下运行 |
| RDP-Tcp listener + 防火墙规则 | 让 `mstsc` 连接到本机 3389 | headed/RDP 模式下启用 |
| 网络栈、DNS、TLS、loopback | Matrix、浏览器页面、CDP 和本机 RDP | 至少允许服务需要的出站访问和 `127.0.0.0/8` loopback |
| DWM / 图形会话 | headed Chromium、GUI 程序和桌面合成 | headed 模式保留；纯 headless 模式可后评估 |
| 文件系统和 NTFS ACL | data dir、Profile、工作目录、日志和 named pipe | 服务、槽位用户、安装目录分别设最小权限 |

### 2. 模板用户

模板用户只做基线验证和配置来源，建议使用固定前缀、独立本地账号，不加入 Administrators：

- 普通本地用户；只加入 `Remote Desktop Users`。
- 已成功创建 Profile，至少存在 `NTUSER.DAT`；完成一次交互式登录后通常还会有 `AppData\Local\Microsoft\Windows\UsrClass.dat`。
- 用户级 Winlogon Shell 固定为 `powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy AllSigned -File "<service-runtime>\session-shell.ps1"`；脚本由安装流程签名，安装目录 ACL 禁止受管用户修改。不把 `explorer.exe` 或 `chuzi-user-agent.exe` 作为本次方案的 Shell。
- 不保存真实账号 Cookie、Matrix token、RDP 密码或业务数据。
- 密码由 provisioner 生成并保存到受保护的凭据边界；不进入脚本、日志、RDP DTO 或普通配置。
- Shell、启动项和用户级策略只写入该用户自己的 hive。

Profile policy/bootstrap 只对目标 SID 的用户 hive 生效。首次初始化使用
`CreateProcessWithLogonW(LOGON_WITH_PROFILE)` 创建该用户自己的 Profile，不复制其他用户的
`NTUSER.DAT` 或 `UsrClass.dat`。Shell policy 幂等地更新同一个 `Shell` 值，不添加启动项；
不修改 HKLM，也不接受调用方传入 executable、脚本、命令或 Profile 路径。

不要直接复制一个正在使用的模板用户目录覆盖其他用户。Windows 首次登录会从默认 Profile 派生用户文件；`NTUSER.DAT`/`UsrClass.dat` 必须由目标用户拥有并可写，否则 Profile 可能加载失败。

### 3. 每个执行槽位

槽位是服务资源，不是账号输入。每个槽位必须有：

- 受管用户名、SID 和 ownership marker。
- 独立 Profile、`NTUSER.DAT`、工作目录、临时目录和日志目录。
- 独立 WTS Session；只接受 SID 匹配且状态为 active 的 Session。
- 服务派生的 Win32 desktop，例如 `winsta0\ChuziSlot<token>`。
- 仅授予该槽位访问自己工作树和必要 Chuzi Profile 的 ACL。
- `chuzi-user-agent` 的 named pipe、短期 token、heartbeat 和 lease fence。
- Job Object；agent 退出或服务重启时能回收整个进程树。
- 结束时先停止 agent/浏览器，再释放 slot lease，最后回收临时目录和用户。

“用户存在”不等于“槽位可运行”，必须把 Profile、Session、desktop、ACL 和 agent 健康一起作为 ready 条件。

### 4. Chuzi 运行时

基座只预装运行时，不预装业务账号数据：

- `chuzi` Go service / launcher。
- Node.js 20+，用于 browser worker 和显式适配器进程。
- 外部提供的 Chromium 或 Edge；Chuzi 不自动下载浏览器。
- `chuzi-user-agent.exe`，用于 named pipe、作业进程、heartbeat 和回收。
- 固定的 `session-shell.ps1`，只报告用户/session readiness 并等待退出，不读取业务凭证、Cookie、
  Matrix token 或账号 Profile，也不启动 agent、worker、浏览器或任意命令。
- 服务派生 data dir：bbolt、profiles、plugins、logs、diagnostics、backups。
- 受保护的凭据密钥来源；Windows RDP 自动登录使用 `TERMSRV/<host>` 的 Credential Manager 项，但密码不回显。
- 如果使用 headed/CDP：DWM、图形驱动和目标浏览器所需的显示能力。
- 如果使用 headless/CDP：可以不依赖可见桌面，但仍保留服务、Profile、网络、Node 和浏览器进程边界。

## 可禁用项

先做用户级和启动项级裁剪，再做主机级服务裁剪。每禁用一项都要重新跑基座校验和一次真实 job smoke。

### 第一阶段：默认可关闭或按用户禁用

这些组件不在 Chuzi 当前代码路径中，优先按用户或启动项关闭：

- Explorer shell（由 user-agent/指定 Shell 接管）。
- OneDrive、Widgets、Xbox、Consumer UWP 后台任务。
- Windows Search 索引和搜索入口。
- Clipboard、音频、驱动器、打印机等 RDP 重定向。
- 用户级通知、自动启动的消费类应用和不需要的启动项。

### 第二阶段：经过探针确认后再做主机级裁剪

以下服务可能与其他 Windows 软件或运维工具共享依赖，不能仅凭名称直接禁用：

- Print Spooler。只有目标作业和系统运维都不需要打印时才评估。
- Bluetooth Support、Windows Search、Xbox 相关服务。
- OneDrive/consumer sync 相关组件。
- 其他第三方驻留服务。

本基座明确禁止把 RPC、Winlogon、CSRSS、User Profile Service、TermService、网络栈或 DWM（headed 模式）列入“可随手关闭”清单。

## 两种运行模式

### Headed + 本地 RDP

这是当前 Windows job-pool smoke 和人工运维的模式：

1. TermService、RDP listener 和 Remote Desktop firewall rules 已启用。
2. 为目标用户创建 Profile 并加入 Remote Desktop Users。
3. 在 `127.0.0.2` 到 `127.0.0.254` 中扫描未占用的 `TERMSRV/<loopback>` 目标，再通过 `cmdkey /generic` 写入已保存凭据；地址不固定为某一个八位组。
4. `.rdp` 只包含地址、用户名和 `prompt for credentials:i:0`，密码不写入文件。
5. 启动 `mstsc` 后等待 WTS Session 从连接查询变为 active。
6. 由 session broker 校验 SID、Session、desktop、agent heartbeat 和浏览器启动。

### Headless + 无 RDP

适合服务器化部署：

- 不要求 TermService、RDP listener 或 DWM 作为运行时条件。
- 仍要求 Profile、Node、Chromium/CDP、网络、ACL、agent/进程回收和数据目录。
- UI 只能通过 Core 的状态和按需快照查看，不能把 RDP 当作业务成功条件。

## 基座验收顺序

按这个顺序验收，失败时只修当前层：

1. **主机探针**：OS、RPC/ProfSvc、RDP（headed 模式）、网络和 data dir。
2. **用户探针**：普通用户、RDP 组、非 Administrators、SID、Profile 和 `NTUSER.DAT`；交互式模式再观察 `UsrClass.dat`。
3. **Session 探针**：手动或自动 RDP 登录，确认目标用户的 WTS Session active。
4. **桌面探针**：创建服务派生 desktop，确认 agent 能在目标 Session 启动。
5. **运行时探针**：agent heartbeat、Node worker hello、Chromium/CDP `/json/version`。
6. **业务探针**：只使用测试页或已授权测试 Profile，最后才接业务适配器。
7. **裁剪回归**：每关闭一项，重复 1-6；失败就恢复上一项。

验收的最小成功标准是：目标用户的 WTS Session 为 active、PowerShell Shell readiness 已报告、
`whoami` 是目标用户、agent 能通过 named pipe 回复、Node worker 能完成 hello、浏览器能在
服务派生 Profile 上建立 CDP 连接。

Winlogon PowerShell 通常运行在用户默认 desktop；服务通过现有 `ensureAgent` 和
`CreateProcessAsUser` 在服务派生的 `winsta0\ChuziSlot<hash>` desktop 启动
`chuzi-user-agent.exe`。两者有意分离；Shell 不选择 slot、desktop、worker、浏览器 executable
或 Profile 路径，服务不放宽 agent executable、desktop 或环境校验。

仓库提供只读 PowerShell 探针：

```powershell
.\scripts\check_windows_minimal_base.ps1 `
  -Mode Headed `
  -DataDir C:\ProgramData\Chuzi `
  -UserName ChuziJob0001 `
  -BrowserCommand 'C:\Program Files\Google\Chrome\Application\chrome.exe' `
  -AgentPath 'C:\Program Files\Chuzi\chuzi-user-agent.exe'
```

`-Mode Headless` 会跳过 TermService、防火墙和 3389 检查，但仍检查 Profile、Node、浏览器和 data dir。探针不会创建用户、写注册表、写 Credential Manager 或修改服务状态。

## 对 Chuzi 实现的直接结论

当前应优先实现或固定以下三个边界：

1. `WindowsBaseProbe`：只读检查主机、RDP、Profile、运行时和 ACL，不负责盲目禁服务。
2. `SlotProvisioner`：只创建/修复带 CHUZI ownership marker 的用户和槽位，不接收任意用户名、路径或命令。
3. `SessionAgent`：只启动签名的 `chuzi-user-agent.exe`，通过 SID、Session、desktop、lease 和 named pipe 做健康检查。

等这三个边界稳定后，再把 Windows 服务裁剪、模板用户镜像和 UI 配置接进来。这样即使 RDP 或某个可选服务发生变化，也不会破坏账号状态机、队列和凭据边界。
