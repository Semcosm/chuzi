# Genshin / BetterGI 适配器设计记录

状态：调研和未来设计，未实现。

这份记录描述 chuzi 将来如何在一个受管 Windows 执行槽内启动原神（实体
客户端或云游戏）、启动 BetterGI、执行预设任务，并保留同一 Windows 会话
的 RDP 观察和人工接管窗口。它不是 BetterGI 的安装说明，也不把当前的
chuzi.adapter/v1 协议扩展伪装成已经完成的实现。

## 结论

首个生产目标应当是 Windows-only 的 Genshin session supervisor：chuzi
拥有执行槽、WTS 会话、进程树、租约、RDP capability 和回收；BetterGI
继续作为视觉识别和模拟输入引擎。适配器只向 BetterGI 的受支持公开接口发出
受控操作，并把脱敏的运行事实交回 Core。

~~~text
Core / Queue / account state machine
             | request + slot lease
             v
Genshin session supervisor (Windows slot user + Job Object)
       |                              |
       | native executable            | BetterGI WebView2 instance
       v                              v
  Genshin game window             cloud Genshin game
       |                              |
       +---------- BetterGI task engine ----------+
                          |
             same WTS session / same desktop
                          |
                 observation RDP workspace
                 (explicit operator lease for input)
~~~

一次运行只能绑定一个 account lease、一个 slot lease、一个服务派生的 Profile
和一个 WTS session。RDP 是该会话的第二个观察/控制面，不是另起一套游戏或
浏览器。

## 调研事实和边界

以下事实来自 BetterGI README、运行时设计、API 页面和源码，研究记录日期为
2026-10-06：

- BetterGI 的目标运行环境是 Windows/.NET 8；它以视觉识别和模拟输入完成
  自动化，推荐 16:9、1920x1080 一类的稳定显示条件。适配器必须把分辨率、
  DPI、窗口可见性和桌面初始化当作运行时前置条件。
- 当前源码的命令行选项包括 start、startOneDragon、--startGroups、
  --TaskProgress 以及 WebView 实例相关参数。这些是版本相关的启动细节，
  不能在没有版本锁定和回归测试时当作长期协议。
- BetterGI 的 API 文档目前没有可供本项目直接依赖的稳定官方 API/SDK。
  现阶段不能假定任意本地 named pipe、UI 控件或私有类是公共集成接口。
- 云游戏路径由 BetterGI 自己的 WebView2 runtime 承载，包含独立的 WebView
  数据目录和云游戏页面；当前实现使用版本相关的固定云游戏 URL（含
  autobegin=1 一类启动参数）并以 RTC/game 状态判断 ready。它不应与现有
  Node CDP worker 争用同一窗口或 Profile；CDP worker 的浏览器边界仍只负责
  浏览器 runtime。
- BetterGI 默认把用户配置放在安装根的 User/config.json，多实例共享该根
  会造成账号、任务和窗口设置串扰。因此每个 slot 必须有独立的 BetterGI
  安装/配置根，或在已验证的版本中证明等效的实例隔离。
- BetterGI 使用 GPLv3。第一阶段应支持部署方提供并确认的安装包，记录版本、
  digest 和许可证结果；是否随 chuzi 分发二进制必须另做许可证审查。

相关参考：

- https://github.com/babalae/better-genshin-impact#readme
- https://github.com/babalae/better-genshin-impact/blob/main/Docs/design/game-runtime.md
- https://github.com/babalae/better-genshin-impact/blob/main/Docs/api/README.md
- BetterGenshinImpact/Helpers/CommandLineOptions.cs
- BetterGenshinImpact/Service/Instance/WebViewInstanceLauncher.cs
- BetterGenshinImpact/Service/Instance/WebViewInstanceStore.cs
- BetterGenshinImpact/GameTask/Runtime/Win32/Win32RuntimeProvider.cs
- BetterGenshinImpact/Core/Config/GenshinStartConfig.cs

这些资料只证明当前版本的行为和集成风险，不构成 chuzi 对第三方服务、
游戏账号或 BetterGI 未来版本的兼容承诺。自动化必须只用于用户获授权的账号
和服务；本设计不包含凭据窃取、风控绕过、反检测或访问控制规避。

## 适配器职责

适配器由 session supervisor 分成以下有界动作。每个动作有独立超时、取消
和脱敏错误码；适配器报告运行事实，账号状态机仍是业务状态唯一来源。

1. prepare_session：校验 account/slot/session lease、环境 manifest、BetterGI
   版本和 digest，创建服务派生的 Profile、配置根、WebView 数据根，设置窗口
   尺寸和桌面 ACL。
2. start_game：按受信环境 manifest 选择 native 或 cloud backend，不接受用户
   传入的 executable、shell、脚本或任意路径。
3. wait_game_ready：验证进程树归属、WTS session、窗口/桌面、窗口大小、图像
   帧稳定性和游戏运行事实；进程存在不等于游戏已准备好。
4. start_bettergi：以与游戏匹配的完整性级别启动 BetterGI，绑定该 slot 的
   配置根和任务 preset，确认 BetterGI runtime 已 ready。
5. run_plan：执行服务预先登记的 plan/preset，消费进度和 operator-required
   事件，只返回脱敏事实。
6. pause / resume：优先调用 BetterGI 的受支持暂停能力；没有该能力时只允许
   进入安全停止或人工复核，不能通过模拟随机按键“暂停”。
7. operator_takeover：暂停自动化、清理按键状态、取得短期 operator lease，
   再把同一 WTS session 交给 RDP 输入面。
8. stop：先停 BetterGI，再停游戏/WebView，撤销 capability 和凭证使用权，关闭
   Job Object 内的进程树，清理临时目录并释放两类 lease。
9. quarantine：发生不确定结果、进程逃逸、配置串扰、版本不匹配或清理失败时
   隔离 slot，禁止自动重试，等待人工处理。

适配器不直接写 account 状态、queue、audit 或 Matrix；这些动作由 Core、queue
和 state store 根据运行事实完成。

## 两条运行路径

### 实体客户端

1. supervisor 在受管 slot 用户和指定 desktop 中启动已登记的原神客户端。
2. 检查进程树没有脱离 Job Object，窗口属于目标 WTS session，窗口大小和 DPI
   满足 plan，连续若干帧稳定后才发出 game_ready。
3. 在同一 desktop、同一完整性级别启动 BetterGI，传入该 slot 的配置根和已
   登记 preset。不要假设管理员 BetterGI 可以可靠地控制普通用户游戏。
4. BetterGI ready 后执行 plan。游戏进程退出、窗口丢失、画面冻结或输入目标
   改变都产生稳定错误码并进入受控停止/复核。
5. 结束时使用反向顺序：停止 BetterGI，等待输入线程结束，再停止游戏和其子
   进程，最后释放 RDP 和 slot。

### 云游戏

1. supervisor 启动 BetterGI 自己的 WebView2 instance，使用每个 slot 独立的
   WebView user-data directory 和配置根。
2. 使用已登记的 BetterGI cloud runtime/URL 和 readiness 检查等待 RTC/game
   状态；不能让用户在 plan 中传 URL、CDP endpoint 或 Profile 路径。
3. 云游戏窗口和 BetterGI task engine 属于同一 WebView 实例。不要再启动 Node
   CDP worker 去连接它，也不要把云页面像普通浏览器任务一样交给 get_browser_view。
4. 若需要用户完成一次性登录或授权，先进入 operator_required，由 RDP operator
   lease 完成并显式恢复；适配器不记录 cookie、token 或页面原文。
5. 关闭时等待 WebView 子进程和数据句柄消失，再释放 slot；WebView 状态不可
   确认时进入 quarantine。

两条路径对 Core 暴露相同的 plan/session 语义，差异只存在于 backend 能力和
ready/stop 实现。

## 预设计划

计划是服务侧登记的声明式对象，不是用户可任意拼接的命令。示例：

~~~json
{
  "plan_id": "daily-commission-v1",
  "preset_ref": "bettergi:preset/daily-commission",
  "runtime": "native",
  "action": "start_groups",
  "groups": ["daily_commission", "resin"],
  "timeouts": {
    "game_ready_seconds": 90,
    "bettergi_ready_seconds": 45,
    "task_seconds": 1800,
    "stop_seconds": 60
  },
  "recovery": {
    "on_transient": "manual_review",
    "on_unknown_outcome": "quarantine",
    "max_attempts": 1
  }
}
~~~

允许的 runtime、action、group 名称、超时上下限和 recovery 策略来自受信
manifest/registry。拒绝 executable、shell command、脚本路径、URL、CDP
endpoint、任意 Profile 路径、任意环境变量和用户提供的文件路径。BetterGI 的
start、startOneDragon、startGroups、TaskProgress 等具体参数只由版本化 bridge
翻译，不直接暴露给 Matrix 或普通调用方。

如果任务已经开始但结果无法确认（例如连接中断、窗口冻结后恢复、BetterGI
进程提前退出），不得自动重放可能有副作用的任务；应记录 outcome_uncertain，
停止输入并交给人工复核。

## 状态机

session supervisor 的运行状态建议为：

~~~text
PROVISIONING
  -> DESKTOP_READY
  -> LAUNCHING_GAME
  -> GAME_READY
  -> LAUNCHING_BETTERGI
  -> BETTERGI_READY
  -> PLAN_RUNNING
  -> STOPPING
  -> RELEASED
~~~

任何活动状态都可以进入 STOPPING；以下状态不能自动重试：

- OPERATOR_PAUSED：任务被安全暂停，等待 resume 或取消。
- OPERATOR_TAKEOVER：RDP operator lease 持有输入，BetterGI 必须暂停。
- MANUAL_REVIEW：任务副作用或最终结果不确定。
- QUARANTINED：slot、配置根、进程树或版本信任失败。
- FAILED：有明确且已分类的失败事实，是否重试由 queue 策略决定。

状态转换事件至少带 session/request/plan 标识、时间、脱敏阶段、稳定 code、
retryable 和 operator-required 标记，不带窗口截图、账号名、路径或原生错误
文本。账号业务状态由 internal/account 根据这些事件决定，不能由 BetterGI 或
supervisor 直接把账号标成成功/失败。

## 现有协议需要的增量

当前 internal/automation.Adapter.Execute 是同步调用，插件 JSONL 只有
started/succeeded/failed/cancelled 和只读 snapshot 消息，无法表达长任务进度、
暂停、人工接管或 operator-required。未来可以保持 chuzi.adapter/v1 主协议
兼容，按能力增量协商：

- bettergi.control@1：受控 pause、resume、stop 和计划启动；只接受服务登记的
  operation，不接收任意 BetterGI 命令。
- bettergi.events@1：progress、ready、operator_required、outcome_uncertain、
  task_finished 等有序事件，并带 sequence/idempotency。
- interactive.desktop@1：声明当前 session 可被 RDP 观察、暂停或接管；capability
  本身仍由 Core/credential boundary 签发。

可增加的 JSONL message type 包括 operation_progress、operator_required、
operation_paused、operation_resumed、takeover_granted 和 operation_finished。
事件必须允许重放、去重和连接中断恢复。没有 bettergi.events@1 时，适配器只能
提供有限的终态 operation，不得声称支持可恢复的长任务进度。

BetterGI 内部 named pipe 只有在上游公开文档、版本兼容策略和回归测试出现后
才能作为 bridge transport；在此之前，bridge 只能使用受支持的启动/控制入口，
不能反射调用私有类型或抓取 UI 文本。

## RDP 观察和人工干预

RDP 必须附着到 supervisor 已经创建的 WTS session 和 desktop，不能为观察另建
一个 session，也不能通过 RDP 登录流程启动第二个 BetterGI/游戏实例。它复用
docs/ui/rdp-workspace.md 中的单一 FreeRDP runtime：Dock、Float、Hide 只是同一
framebuffer/input queue 的宿主切换。

默认模式是 read-only observation：能看到脱敏状态和画面，但不发送键鼠输入。
需要人工操作时：

1. Core 检查请求者和房间授权，签发短期、请求绑定的 RDP capability。
2. supervisor 先调用 BetterGI pause；若无法确认暂停，停止自动化并进入
   MANUAL_REVIEW。
3. 清理 BetterGI 的 held keys/buttons，建立带 expiry 的 operator lease，再开放
   RDP 输入。
4. operator 释放或 lease 到期后，输入立即关闭；只有显式 resume 且游戏 ready
   事实仍有效时才恢复计划。

当前 issue_rdp_capability 已是 opaque、短期、授权绑定的 API，但生产 RDP
authorizer 仍 deny-by-default。因此在 authorizer、WTS ownership、ACL 和 FreeRDP
workspace 完成前，适配器只能提供非交互运行或本地诊断，不得开启生产 RDP 登录。
RDP endpoint、用户名、密码、证书、Profile 路径和像素数据不进入 plugin JSONL、
日志、审计或 Matrix。

## 安全、隔离和供应链

- 每个 account/slot 派生独立的 BetterGI 安装或配置根、WebView 数据根、日志根
  和临时目录；禁止复用安装根的共享 User/config.json。
- 游戏、BetterGI、WebView2 和 supervisor 子进程全部加入该 slot 的 Job Object；
  取消、超时、pipe 断开和 supervisor 退出都回收整棵进程树。
- slot 用户只能访问自己的 Profile/运行根；Core、Matrix 和普通插件调用方不接收
  路径。路径由服务根据 account/slot/lease 派生并做 reparse-point 和 ownership
  检查。
- BetterGI 包、bridge 和 environment manifest 需要签名者信任、版本约束和 SHA-256
  digest。首阶段由部署方提供 BetterGI，安装前验证许可证和包完整性。
- 凭据只能通过 credential boundary 的短生命周期回调使用；cookie、token、页面
  原文、截图和完整窗口帧不得进入适配器事实、错误、Matrix 或普通日志。
- 只允许用户授权的原神账号和云游戏服务。不能加入 CAPTCHA 绕过、风控规避、进程
  注入、内存修改、反检测或第三方访问控制绕过。

## 失败、租约和观测

需要稳定错误类别，例如 environment_unavailable、bettergi_version_mismatch、
config_root_shared、game_process_missing、game_not_ready、bettergi_not_ready、
plan_timeout、operator_required、outcome_uncertain、rdp_unavailable 和
cleanup_incomplete。原生 Win32、.NET、WebView2 和 BetterGI 文本只留在受限本机
诊断，并在跨边界前映射为稳定 code。

租约规则沿用现有 account/slot 设计：请求取消、超时、session 崩溃和服务重启都
必须幂等撤销 session/slot lease；过期 owner 不能释放新 lease。清理失败时先
quarantine，保留最小化的 ownership marker 和诊断索引，禁止下一任务复用可能残留
cookie、窗口或按键状态的 slot。

进度和事件只记录阶段、耗时、计数、版本/digest 摘要、失败 code 和 lease 状态。
不要持续录屏或上传画面；RDP 画面只在已授权的活动会话中呈现。

## 分阶段实施

1. **环境和合同确认**：锁定一个 BetterGI 版本，验证 Windows/.NET 8、原神启动
   参数、配置根隔离、窗口尺寸、Job Object 和包许可证；形成 environment manifest
   和测试夹具。
2. **实体客户端纵向切片**：单 slot 完成 prepare、原神 ready、BetterGI preset、
   反向停止和 quarantine；先使用终态 operation，不承诺长任务恢复。
3. **同会话 RDP**：接入现有 RDP workspace 和真实 authorizer，先 observation，
   再实现 pause/held-input 清理后的显式 takeover。
4. **云游戏 backend**：只通过 BetterGI WebView2 instance，验证每 slot WebView
   数据隔离、RTC/game readiness、登录人工介入和关闭回收。
5. **公开 bridge**：在 BetterGI 有稳定公开接口后实现 bettergi.control@1 和
   bettergi.events@1，补充事件顺序、重放、取消和版本回归。
6. **多账号生产化**：并发、升级/回滚、崩溃恢复、断电恢复、长期运行、digest
   轮换、许可证审查和 Windows 四目标发布验收。

每一阶段都必须有无真实账号的集成测试；只有 Windows disposable slot、受控 RDP
和用户授权的测试环境才能做端到端验证。

## 当前阻塞和待讨论问题

- BetterGI 没有本项目可直接依赖的稳定公共 API；需要上游接口、版本支持矩阵和
  失败语义。
- 共享 User/config.json 使“单安装多账号”默认不安全，必须先证明隔离或每个 slot
  安装独立副本。
- BetterGI、WebView2、原神窗口和 RDP 都是 Windows interactive desktop 依赖，
  不能把现有 Linux/macOS headless/CDP worker 当作替代。
- GPLv3 的打包、更新和用户提供安装包边界需要单独的许可证决定。
- 生产 RDP authorizer、WTS session broker 和输入 takeover 仍未接入；issue_rdp_capability
  的存在不等于可用的生产 RDP。
- 需要决定 preset 是由部署 manifest 管理、由管理员在 Core 登记，还是由 BetterGI
  自己的配置导入；普通 Matrix 请求不应直接上传 preset 文件。
- 需要定义任务副作用的“结果不确定”策略，以及人工复核后如何恢复或标记 account
  state。

在这些问题解决前，plugins/bettergi 只表示通信适配器边界和研究结果，不表示
chuzi 已经可以自动登录、运行或接管原神。
