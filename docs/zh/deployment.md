# Deploy 配置

[English](../deployment.md)

Issue #125 首版支持每次部署启动一个临时 HTTP 服务。服务端会在创建
Sandbox 前，从用户选择的完整 Commit 读取仓库中的 `.qiniu/deploy.yaml`。

首版配置格式保持小而严格：

```yaml
version: 1
workingDirectory: .
installCommand: npm ci
startCommand: npm run start -- --host 0.0.0.0 --port 3000
port: 3000
healthcheck:
  path: /health
  timeoutSeconds: 60
```

`workingDirectory` 相对于检出后的仓库目录，不能越界。`installCommand` 和
`startCommand` 会在目标运行环境中执行受信任的仓库代码，不能在命令中写入
凭证。`healthcheck.path` 必须是相对于服务根地址的路径。解析器会拒绝未知字段、
多个 YAML 文档、不支持的版本、不安全路径、无效端口以及超过 64 KiB 的文件。

上述 camelCase 字段沿用现有仓库约定。也接受 `working_directory`、
`install_command`、`start_command` 和 `healthcheck.timeout_seconds` 下划线别名；
同一字段同时提供两种拼写会被拒绝。

解析器会将仓库配置映射为 deployd 的结构化 `Spec`。deployd 不拥有这套 YAML
格式，只负责校验执行参数，并将参数绑定到已解析的完整 Commit。Deploy 控制器
必须在创建 Sandbox 前持久化计划，不能把命令写入生命周期事件，也不能把
Sandbox 凭证放进计划。

启动应用时，runnerd 将当前 Sandbox 应用地址的精确域名注入
`__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS`。Vite preview 因此可以接受公网健康检查，
仓库无需预先写入动态域名或使用通配符 Host 白名单。该环境变量只在启动时传入，
不属于持久化的执行 Spec，也不会传给安装命令。

启动确认使用本地截止时间，不将它通过 envd `Connect-Timeout-Ms` 变成应用进程的存活期限。
确认 PID 后，取消观察流不会停止服务；应用继续运行，直到手动停止或 Sandbox 到期。
克隆和安装命令仍保留执行超时。

创建部署时从所选账号或组织的可运行公共／自有 Sandbox 目录中选择模板。
`runnerd.yaml` 可以为未传 `template_id` 的 API 客户端配置可选默认值：

```yaml
deploy:
  template_id: your-deploy-sandbox-template
  work_root: /var/lib/runnerd/deployments
  max_concurrent_per_scope: 3
  retention_hours: 168
  allow_admin_fallback: false
```

`template_id` 与 Runner Spec 完全分离。`work_root` 是控制器拥有的绝对路径，
位于 Sandbox 内，只用于每次 attempt 的临时工作目录。worker 会创建带独立元数据的
Sandbox，检出固定 Commit，持久化 deployd checkpoint，并在服务就绪后保持该
Sandbox 运行。系统记录 Sandbox ID、attempt、runtime ID 和进程 tag，重启后只会
重新连接同一个 attempt。命令输出在交给可选事件接收器前会限制大小并脱敏。每个部署
最多保留 2,000 条事件和 8 MiB 消息内容，时间线有界且优先保留最新证据。worker 不依赖默认模板配置。准入使用有效作用域凭据验证所选物理模板 ID，缺少模板、
模板不可用或凭据未配置时会在入队前拒绝。已有缺少模板的 queued 记录会明确失败，
应选择模板重新创建。创建／重试在事务中保存排队事件，并立即唤醒 worker；定期协调仍作为兜底。

区域来自该 scope 的有效 Sandbox endpoint。准入时保存区域标识、endpoint、加密凭据
和独立 Deploy 模板绑定，后续全局配置变更不会移动已准入部署的区域或模板。重试会重新解析
当前 scope 的凭据，并按重新部署方式刷新分支源码或复用不可变源码与模板绑定。Deploy 默认不使用平台 CI 兜底；
仅 `allow_admin_fallback: true` 时允许，且仍遵守已有的启用状态与 audience 限制。
Deploy 不使用 Fork sponsorship 或 Cache S3 凭据。

单个 Deploy worker 会定期连接 running attempt，并检查保存的 runtime/PID/tag。
健康服务在控制器重启后继续运行；临时 Provider 故障和身份不匹配会保留资源，等待下次检查。
Provider 查询后会再次检查到期时间，避免查询跨过截止时间时将正常到期误记为进程失败。
中断的 `creating` attempt 只按精确操作元数据查找并补偿，不重放用户命令或创建替代资源。
创建结果未知时保持非终态，直到找到并清理资源，或到期后的成功查询确认资源已不存在。
停止尚未确定的创建返回 `202`，等待补偿完成。确认 Sandbox 不存在才视为释放；重连使用
保存的剩余时长，初次创建时保存 Provider 返回的实际到期时间。此处只支持单进程恢复，
多实例 Deploy worker 尚未支持或验证。

每个账户或组织 scope 最多允许 `max_concurrent_per_scope` 个排队／运行中的部署
attempt。准入在状态事务中串行化，超过限制的并发请求返回
`429 deployment_quota_exceeded`，不会额外创建 Sandbox。Sandbox 已释放的终态记录
会按 `retention_hours` 保留，之后删除部署行和有界事件；审计事件继续作为长期控制面证据保留。

首版将 Provider 生成的 HTTPS 地址定义为**公开且不带应用层鉴权**的测试地址。部署就绪后
响应包含 `endpoint_access: "public"`。URL 本身不是授权机制；需要保护数据的应用必须自行
实现鉴权，不能把凭证写入仓库命令或输出。带鉴权的反向代理或签名访问契约另行设计。

该配置描述源码部署，暂不包含应用 Secrets、持久化数据库、自定义域名、多服务
编排或 CI/PR 自动触发。

## GitHub App 权限

私有仓库 Deploy 要求 App 具备仓库 **Contents: Read-only** 权限，以读取
`.qiniu/deploy.yaml` 和固定 Commit。现有用户 OAuth 流程还必须能够列出该仓库及其
Installation 访问范围。Deploy 与 Jobs 一样按 OAuth/App 的 Installation＋仓库精确交集
授权，不按创建者或组织角色隔离。仓库成员可以查看、创建、重新部署、停止和删除部署。
Sandbox Preferences 与私有模板目录仍由 owner 管理；管理组织设置仍需
**Members: Read-only** 和 active owner membership（`role: admin`）。
Deploy 不会继承 CI Secrets、Cache S3 凭证或 Fork sponsorship 权限。

部署请求与 Jobs 使用相同的登录失效处理。HTTP 401 会重新检查 runnerd 会话，
过期时展示登录页；结构化的 `REAUTH_REQUIRED` 错误会触发一次 GitHub OAuth
跳转，并在 `return_to` 保留当前来源、部署和 revision。并发错误共用单次跳转控制，
之前账号或页面的延迟响应不会触发跳转。普通权限拒绝、限流和 provider 错误仍按
原错误展示。

## 首个控制面切片

当前已提供用于持久化部署计划的登录 API。`installation_id` 是现有 Repositories
和 Preferences API 使用的本地关联 installation 记录 ID；外部 GitHub
Installation ID 由服务端解析：

- `GET /user/deployment-templates?installation_id=<local-id>&repository_full_name=<owner/repo>`
  向作用域 owner 返回可运行的自有／公共模板。其他用户必须通过精确仓库授权，
  只获得公共模板 ID 与名称，不返回私有目录或作用域元数据。非 owner 创建时只能
  选择已验证的公共模板；仓库成员可以重新部署已有的保存版本。Deploy fallback 仍需明确启用。
- `GET /user/deployment-refs?installation_id=<local-id>&repository_full_name=<owner/repo>&source=branch|tag&page=1`
  每页最多返回 100 个引用名称及 `has_more`。读取前检查与创建相同的
  OAuth/App 精确仓库交集；OAuth 缺失或被拒绝时失败关闭。
- `POST /user/deployments` 接收所选物理 `template_id`、可选的模板目录快照 `template_name`、
  `template_cpu`、`template_memory_mb` 和 `template_disk_mb`、已关联的本地 `installation_id`、精确的
  `repository_full_name`、`ref`、可选的 `source`（`branch` 或 `tag`）、可选的 `config_path` 和可选的
  `ttl_seconds`（五分钟到 24 小时，默认一小时）。可以使用 `Idempotency-Key` 请求头安全重放同一请求。
  创建时保存原始规范化准入字段的摘要。Provider 更新到期时间、停止或重新部署后，重放仍
  返回已有资源，不启动新 attempt；执行字段不同则返回 409。没有摘要的历史记录无法安全
  重建原请求，因此拒绝幂等键重放；新工作使用新键。
  部署 ID 使用 26 位小写 Base32 编码，保留 128 位标识空间，可直接用于 URL。
  有幂等键时根据账号和请求键生成固定 ID；无幂等键时生成新的随机 ID。
  客户端应将 ID 作为不透明字符串使用；上线前不提供旧 ID 的重放回退逻辑。
  传入 `source` 时，服务端先将名称限定为 `refs/heads/<name>` 或 `refs/tags/<name>`，
  避免同名分支与 Tag 混淆。未传 `source` 的 API 客户端仍可使用 branch/tag/commit ref。
  已知来源的响应返回来源和用于展示的原名称。
- `GET /user/deployments` 在本地数据库中按已授权的 Installation＋仓库过滤，再计算
  总数和分页；创建者身份不限制可见性。
  可选参数 `status` 接受 `running`、`processing`、`stopped`、`failed` 的逗号分隔组合，
  在分页前筛选部署当前状态；API 省略、留空或传 `all` 表示全部。`processing` 包含排队、规划、创建、停止中；`stopped` 包含
  已停止和已到期。未知值、重复或空的组合成员、`all` 与其他组混用、重复的 status 参数返回 400。响应包含筛选后的 `total` 与
  `status_counts`（`all`、`running`、`processing`、`stopped`、`failed`）。数量统计覆盖
  完整授权数据及仓库／ref／Commit／PR 条件，独立于 status 和分页；匹配历史来源时，
  仍按部署当前状态分组，不按选中历史版本的状态分组。
- `GET /user/deployments/{id}`、已保存事件、版本与生命周期操作复用 Jobs 的仓库授权缓存：
  20 秒后刷新，30 秒硬过期，并合并并发刷新。缓存未命中时可能访问 GitHub；部署内容
  本身不需要 Sandbox I/O。OAuth 缺失或被拒绝时失败关闭，原创建者也遵守该规则。
  仓库授权还要求用户当前可见的 Installation 元数据中 `suspended_at` 为空；
  GitHub 的仓库列表接口在 Installation 暂停期间仍可能返回仓库。
  创建／重试为每个 revision 保存执行账号；worker 在检出来源前重新校验该操作者。
  历史执行账号为零时使用原创建者；创建者身份和幂等键归属保持不变。
- `GET /user/deployments/{id}/pull-request` 为旧 PR 部署补充可选标题元数据。
  它使用相同的精确仓库授权和 Jobs 的 PR 查询缓存及并发合并，只回填相同 PR ref
  中为空的标题，保留已有版本标题与生命周期时间。界面在本地缺少标题时独立请求，
  GitHub 缓慢或不可用不会阻塞详情和日志。页面顶部“刷新”会通过同一缓存重试缺失标题；
  同一来源／版本后返回的空标题快照不会清除已补全的标题，新版本独立解析标题。
  新 PR 创建与最新版本重部署已经保存
  `pull_request_title`。详情标题可跳转到目标仓库的 PR、分支或 Tag 源码树；
  Tag 无需创建 GitHub Release，fork Commit 链接仍使用保存的来源仓库。
- `GET /user/deployments/{id}/events` 返回有界的生命周期／输出事件，并支持
  互斥的 exclusive `before_id`／`after_id` 游标。初次返回最新事件并按时间正序展示；
  `after_id` 返回后续正序页，使活动轮询能够连续补齐日志。
- `POST /user/deployments/{id}/stop`（或 `DELETE /user/deployments/{id}`）停止带
  tag 的进程并删除部署 Sandbox。清理失败会保留为 `failed`，worker 会继续重试。
- `POST /user/deployments/{id}/retry` 只有在上一轮 Sandbox 证据已清理后才能重新入队。
  JSON 请求体可使用 `{"mode":"latest"}` 重新解析分支并保存新的不可变计划，
  或使用 `{"mode":"revision","revision_number":N}` 复用保留的部署版本；省略请求体时复用当前版本。

创建流程会将 ref 固定为完整 Commit SHA，从该 Commit 读取配置，生成 deployd
不可变计划，加密保存规范化后的执行 Spec，并在同一事务中写入部署记录和审计事件。
这些 API 不返回命令内容。启用 worker 后，部署会依次进入 `planning`、`creating` 和
`running`；到期时重新连接保存的 Sandbox/PID attempt，停止带 tag 的进程并删除
Sandbox，然后记录 `expired`。创建或清理失败时只记录 `failed`，不会假设远端资源
已经释放。

`GET /user/deployment-repositories?limit=100&offset=0` 返回仓库汇总：
`repository_full_name`、保留的部署总数 `total`、`active_count` 与 `active_deployments`，
以及 `has_more`、`limit`、`offset`。每页默认 100 个仓库，上限 500；可用
`repository_full_name` 精确读取尚未加载的当前仓库。查询先按 Installation＋仓库精确授权，
再聚合和分页；活动部署不受历史第一页限制，只返回 ID、仓库、完整 ref、Commit、状态和更新时间，
不返回运行时或凭据。内容来自本地数据库，不访问 Sandbox 或 GitHub 目录；授权复用 Jobs
的 OAuth/App 交集缓存，响应使用 `Cache-Control: no-store`。

`/deployments` 与部署详情共用 Jobs 风格的仓库侧边栏。“全部部署”展示授权历史；
点击仓库进入不带来源筛选的 `/github/{owner}/{repo}/deployments`。
仓库项采用紧凑两行布局：第一行显示仓库名及聚合状态标签，第二行弱化显示活动部署数量。
长名称省略显示，悬停可查看完整名称。全部排队时显示“排队中”；只有规划、创建或运行
其中一种状态时显示“部署中”；混合状态或停止中显示“有活动部署”；没有活动部署时不显示标签。
独立的展开按钮展示排队、规划中、创建中、运行中和停止中的部署，通过分支／Tag／PR
及短部署 ID 区分。正在查看的部署变为终态后仍保留导航入口；只有终态历史的仓库也保留。
切换时默认展开当前仓库；从详情返回仓库部署列表，默认仅选“运行中”，Jobs 的来源筛选链接仍可使用。
部署列表与仓库导航独立分页，刷新保留已加载历史。桌面侧边栏与主体独立滚动，窄屏上下排列，
侧边栏滚动高度受限。

列表使用共享 Toggle Group 展示“运行中／处理中／已停止／失败”单选分段筛选，
数量来自服务端完整统计。默认“运行中”，再次点击当前项仍保持选中。组件采用紧凑浅色容器，
选中项以浮起背景区分。选择“运行中”时 URL 省略 status，其他状态保存其值，保留分支／Tag／PR／Commit 条件；
刷新及浏览器前进／后退恢复选择。API 请求始终明确传入所选状态。
切换状态会重置历史分页并忽略旧分页响应，不筛选左侧导航；空结果仍保留筛选控件。详情继续使用独立的“部署日志／终端／部署历史”标签页。

“创建部署”链接进入 `/deployments/new`，从仓库工作区进入时预选仓库。
列表与详情不加载 GitHub App 或表单目录；创建页只显示表单，不读取部署列表或日志。

部署链接使用应用内导航，涵盖列表、创建、详情页面之间的跳转，以及 Jobs 中的部署入口。
跳转保留当前会话，不重新加载整个页面或重复显示初始登录验证。浏览器前进／后退及
新标签页打开仍可使用；手动刷新仍检查登录状态，每次 API 请求仍由服务端校验权限。

创建表单在明确选择仓库后提供“版本来源”（分支／Tag），再从真实引用列表中
“选择分支”或“选择 Tag”。切换仓库、作用域或版本来源后，旧发现请求不会覆盖当前列表。
空列表禁止提交；加载失败可重试，更多版本分页加载并保留当前选择。
App 无法读取版本时，表单提示 Installation owner 批准 Contents 读取权限后重试；
仓库可见不代表 App 已获准读取内容。

创建成功后进入仓库或 PR 上下文的部署详情地址，按路径中的 ID 直接读取资源，
不依赖列表第一页。刷新和浏览器前进／后退保留选择。详情逐行展示阶段、stdout／stderr，
轮询活动 attempt 并合并已有历史，支持更早日志和手动刷新；读取失败时保留已加载日志并显示重试提示。

## 部署版本与日志

失败后的补偿清理成功时，部署及版本仍保留原始失败阶段与原因；清理过程中断后恢复也遵循此规则。

Deployment 保持稳定的资源 ID。首次创建以事务保存版本 #1；
`POST /user/deployments/{id}/retry` 创建下一个版本。使用分支最新代码时会重新解析
已移动的分支，复用版本时复制所选固定 Commit 和加密计划。新版本、排队日志及审计在同一事务中提交。
只有当前版本可推进，旧版的来源、状态、地址、已确认 Sandbox 标识与日志独立保留。
执行 attempt 继续用于 Provider fencing，与用户可见的版本编号分开。

本地详情返回 `revision_number`、最新 100 条 `revisions`、`revisions_has_more` 和
最近确认成功的版本编号。更早版本通过
`GET /user/deployments/{id}/revisions?before_number=N` 获取；版本日志通过
`GET /user/deployments/{id}/revisions/{number}/events` 获取，支持相同的排他
`before_id`／`after_id` 游标。原 `/events` 接口读取当前版本。
这些读取通过 Jobs 缓存校验精确仓库权限，使用 `Cache-Control: no-store`，不请求 Provider，
也不返回加密计划或服务凭据。

详情左侧展示部署导航，右侧突出当前部署的状态、代码来源、访问地址与资源配置。
桌面端两栏独立滚动；窄屏上下排列。
侧栏独立加载本地部署列表，不阻塞详情展示。默认打开“部署日志”，其后依次为“终端”和
“部署历史”。历史行可查看版本日志，或在生命周期允许时基于该版本重新部署。
日志采用 Runner 时间线布局，展示毫秒时间戳、输出类型／阶段及保留正文，继续使用
JetBrains Mono 字体。切换版本后重置游标，拒绝前一版本的迟到响应。
历史日志明确显示版本提示并提供“返回当前版本”；顶部概览和资源始终描述当前部署。
`revision=N` 在刷新及浏览器历史导航后保留日志选择，即使所选旧版不在最新一页，
也能直接读取。展示页面无需实时探测 Sandbox。

升级以新增字段和索引方式兼容旧数据库，为缺少版本的部署补为 #1，保留原来源、
凭据、时间戳、attempt 证据及日志。旧记录的更早执行日志一起保留在导入的 #1 中，
不重建已经丢失的历史状态。日志数量与字节预算按版本分别计算。
终态保留期结束后，在同一事务中删除 Deployment、所有版本及日志，审计仍保留。

## 实现迭代方案

2026-10-16 的收尾核对区分实现、浏览器夹具与真实验收。以下证据还不足以关闭
Issue #125。授权边界采用已确认的“仓库成员可部署”；Sandbox 设置和私有目录仍仅由 owner 管理。

最终分阶段提交前，`task lint` 和 16 项部署生产构建浏览器夹具通过，覆盖 PR 标题／
来源链接、仓库导航、可刷新详情、五类 OAuth 失效入口及延迟响应隔离。独立暂存快照
还通过 GitHub／server race 套件、state 套件和 37 项部署 UI 交互测试。这些检查补充
此前已记录的 `task test`（332 项 UI 测试及 Go race／coverage），浏览器夹具继续与
真实 Provider 和 OAuth 证据分别记录。

| Issue 验收项 | 证据与剩余边界 |
| --- | --- |
| 授权来源、固定 Commit、启动与 HTTP 访问 | 公开 base、fork、历史 PR Commit，以及指定私有来源的 Contents、固定源码、构建及 HTTP 访问均通过。 |
| CI 清理独立性与 Deploy 不注册 Runner | 已记录同 Commit 的 CI 并行、Runner 释放后服务 HTTP 200 及 Runner 列表检查通过。 |
| 详情、日志、失败、到期及刷新恢复 | 已记录真实历史／刷新、认证标题 API、PR／分支／Tag 来源页面、本地会话失效登录回跳，以及真实 OAuth Token 撤销后的内置浏览器登录恢复通过。 |
| 仓库及作用域授权 | 精确权限对拒绝及成员共享自动化测试通过；真实匿名读取返回 401。Installation 真实暂停和 OAuth Token 撤销后缓存到期即拒绝访问，恢复后可重新访问。第二账号共享和单仓库移除仍需单独验收。 |
| 确认停止／删除／到期／失败清理 | 公开 API／worker 和私有来源失败／停止验收均有 Provider 确认资源不存在；本地记录保留终态历史且 Sandbox 引用为空。 |
| 重复请求、未知创建结果、重启与 attempt 隔离 | 已记录真实 API 幂等准入、传输故障恢复和完整本地进程重启通过；worker 测试覆盖旧 attempt 隔离。 |
| 配额、日志预算、Secrets 隔离及 CI 回归 | 已记录配额／重新准入及加速保留期验证通过；state／server／worker 测试约束日志预算、脱敏与隔离。 |
| 真实项目及完整清理证据 | 下文保留公开分支／PR／Tag、私有来源及 Installation 真实暂停／恢复的证据。共享账号和其他撤权方式仍需单独验收。 |
| 中英文用户／配置／App 权限文档 | 中英文配置、测试、smoke 和 README 说明 Contents／Members 授权及当前边界。 |

2026-10-13 的公开执行验收使用公共 `base` 模板，以及 `qiniu/deployment` 的完整 Commit
`fe64a78977093d7c7fd7fb9a159975c5f73f8ff6`，仓库配置保持不变。真实 worker 返回 HTTP 200，
控制器对象重连保留原 attempt，手动停止进入 completed，三分钟 TTL 后进入 expired。
两次清理均由 Provider 404 确认资源释放。

同日的已登录本地浏览器验收使用 `qiniu/deployment` 的 `example/vite` 分支与 `base`：
创建返回 201，跳转到详情，展示安装／启动／就绪日志，刷新后保持同一详情和日志。
公网 HTML 返回 HTTP 200，浏览器无 console/page 错误。停止 API 返回 200，记录进入
`completed` 并清空 Sandbox 引用。中文表单的分支／Tag 字段和空 Tag 禁止提交也已验证。
当时私有仓库权限和已登录的 retry/delete 流程尚未验收；后续结果记录在下文。

2026-10-14，用户在 Web 终端实现后确认真实 Sandbox 验收已通过。此处记录用户完成的
验收，未附加命令记录或逐项场景结果；上述 Issue #125 的独立验收项仍保持待完成。

2026-10-14 的另一轮验收使用普通 Google Chrome、相同的公开仓库 Commit 与 `base`。
创建和刷新保留短部署 ID 与日志。完整重启 runnerd 后，PID 从 99226 变为 4048，部署 ID、
版本 #1、attempt #1、Sandbox ID、runtime ID、进程 ID／tag、endpoint、到期时间及 Commit
均保持不变；重启前后公网 HTML 均返回 HTTP 200。随后通过已登录浏览器停止，记录进入
`completed` 并清空 Sandbox 引用。本轮未单独查询停止后的 Provider 404，也未同时运行 CI Job。

2026-10-14 的真实 Provider 故障注入通过主动启用的 `create-timeout` worker 子测试完成：
丢弃一次真实 Sandbox 创建成功的响应，并让首次元数据查询超时。记录保持 `creating`、
attempt #1，未记录尚未确认的 Sandbox ID。新的 worker 按精确操作恢复资源并补偿清理，
保留 `failed`／`recovery`，清理后通过 Provider 404 确认释放；传输层仅接到一次创建请求。
传输故障由测试注入，创建、元数据查询与释放使用真实 Provider。这是 worker 层证据，
不代表重复的已登录 API 提交或真实 Provider 服务中断已验收。

2026-10-14 在 `09c3a81` 上通过普通 Chrome 继续验收：使用版本 #1 的原计划创建 #2，
再选择最新分支代码创建 #3，两次公网访问均返回 HTTP 200。刷新后保留 #1 与旧日志，
历史版本终端不可连接。两个新版本均已停止，最终记录为 completed、Sandbox 引用为空，
三个版本均保留。分支期间没有前进，因此不能证明移动分支行为；这两次停止未单独验证
Provider 404。本次记录早于仓库共享授权调整，不代表跨账号真实验收；私有仓库和 fork PR
真实执行按当前安排暂缓。

2026-10-14 的已认证 API 验收使用运行中的本地 runnerd、相同 Commit 的
`qiniu/deployment`／`example/vite` 与公共 `base`。同一幂等键并发提交两次只创建一个
部署和 attempt；作用域上限为 3，超额请求返回 429 且不落库，释放后准入恢复。
公网 HTML 返回 200；DELETE 后进入 completed、清空 Sandbox 引用，并通过 Provider
查询确认资源不存在。清理后重放返回同一终态记录，20 条事件仍保留；本轮准入的四个
测试资源均已停止。测试为已有本地账号签发仅保存在内存中的短期 session，覆盖真实授权
与 Provider 执行，不代表 OAuth 登录或第二个用户已验收。本轮发现 Provider 到期时间
精度导致幂等重放冲突，现已改为比较不可变的创建请求摘要，不依赖可变的到期时间或版本字段。

独立的真实 worker `start-failure` 验收在固定公开计划中注入 `install: true` 与
`start: exit 23`：仅创建一个 Sandbox，部署和版本均保留 `failed`／`probing`，
清理后由 Provider 查询确认资源不存在。这属于合成计划的真实执行证据，不代表仓库配置准入验收。

运行中的本地 worker 也通过了保留期加速验收：配置为 168 小时，仅将上述已确认
Sandbox 释放的 API 测试记录的 `updated_at` 回拨 169 小时。正常清理循环删除了该部署、
一个版本及 20 条事件，两个审计事件保持不变。这证明实际进程在时间戳注入下的清理行为，
不代表已自然等待七天。

2026-10-14 在 `84abfa8` 上使用普通 Chrome 验收真实本地 runnerd 的窄屏详情，
部署有三个版本，选中历史版本 #1。375 × 667 视口下，document 滚动宽度仍为 375；
历史和日志容器均宽 343，x=16。历史底部 y=1290.5，日志顶部 y=1524.5，确认上下排列。
20 条已保存日志均存在，DevTools 无 console 错误。该证据使用真实 API 数据，
独立于生产 fixture，观察对象为已停止的历史部署。

2026-10-14 的真实 CI 独立性验收使用
[`qiniu/deployment` 的 Commit `bd336bd`](https://github.com/qiniu/deployment/commit/bd336bdc3e93d5aca8fd25a8aa2258d90e644123)
与 [Workflow Run 38533165014 的 attempt 2](https://github.com/qiniu/deployment/actions/runs/38533165014/attempts/2)。
实际观察到 Job `115752065357` 和部署 `pgcwpifs2ovrpw37oayfvbz44y` 同时运行，
使用不同 Sandbox。Job 成功后，runnerd 保留成功结果，Provider 确认 Runner Sandbox
不存在；部署保持 running、attempt #1，清理后立即及 20 秒后公网均返回 HTTP 200。
GitHub 在 CI 运行时仅列出预期的 CI Runner，在部署仍运行时已无仓库 Runner。
API 验收最后停止了自己创建的全部部署，Provider 确认资源释放。第一次 CI attempt
因错过并行阶段未计入验收。本轮证明公开同源部署的清理独立性，不代表私有仓库／fork
执行或多实例恢复已经验收。

2026-10-14 的真实公开 fork 验收使用
[`miclle/qiniu-deployment` 的 `example/vite` 分支](https://github.com/miclle/qiniu-deployment/tree/example/vite)
及 [草稿 PR `qiniu/deployment#1`](https://github.com/qiniu/deployment/pull/1)。
fixture 仅增加 HTML 仓库／版本标记，部署命令及 Vite Host 校验保持不变。
分支部署 `ifad75cgk6eyltqlltfhdbecnu` 固定 Commit `9885680`，返回 `fork-preview-v1`。
将分支及 PR head 推进至 `90a244a`（`fork-preview-v2`）后，PR 部署
`qrshzidd2vu7aupytig7l4yyqu` 仍固定完整 SHA
`98856804498a253e8ae8ca35f19cd951b88ea769`，保留来源仓库
`miclle/qiniu-deployment`，公网 HTTP 200 且页面仍包含 `fork-preview-v1`。
两轮真实 API 验收均通过并发幂等、作用域配额／释放后重新准入、终态重放及 DELETE，
全部测试资源均由 Provider 确认释放。本轮证明公开 fork 及 PR 历史 Commit 的执行；
真实 GitHub 权限撤销、私有仓库 Contents 权限和第二个用户的权限仍需独立验收。

1. **预检与计划（已完成）**：严格解析仓库配置，固定完整 Commit，校验 OAuth/App
   精确仓库交集，加密保存 Spec 快照，并以审计事务持久化部署状态。
2. **Sandbox 执行与恢复（本地完成）**：解析作用域 Sandbox 服务和独立 Deploy 模板，
   创建独立 Sandbox，检出固定 Commit，运行 `deployd.Apply`，持久化 checkpoint，
   使用 Sandbox/PID/attempt fencing，并在重试前按精确的 Provider 元数据补偿结果未知的创建操作。
3. **生命周期 API（本地完成）**：后端已经提供停止／删除／重试、TTL 到期、有界部署事件
   和日志，以及带状态的清理证据；真实创建响应丢失后的恢复与清理已验证。
4. **用户界面（首片已完成）**：`/deployments` 展示本地历史，`/deployments/new` 处理授权
   仓库、分支／Tag、模板选择与不可变部署创建，部署详情展示状态、地址、TTL、模板 CPU／内存／磁盘资源、
   生命周期／输出事件及停止／删除／重试操作。
   本地验证和已登录的公开仓库创建／详情／刷新／停止流程已通过；剩余真实验收见上文。
5. **验收**：完成聚焦测试、本地启动／构建检查，再用真实 GitHub App 和 Sandbox
   验证部署、停止、到期、重启恢复及越权访问证据。

2026-10-16 的本地 API 验收检查了 `qiniu/deployment#1` 的已有部署
`4hvwtaxjssi5oxc4nqwgnx3hiq`。运行中服务的详情和 PR 元数据均返回
`test: verify fork deployment preview`，两个保留版本也保存了标题。匿名读取返回 401，
认证读取返回 200，状态、version、attempt、Sandbox 标识及生命周期更新时间保持不变。
最终一轮初次详情请求耗时 3,559 ms（包含冷缓存 GitHub 授权查询），随后元数据、版本和
重复详情请求为 0–1 ms，精度为毫秒。这是一次本地观察，不是延迟保证。本轮开始前标题
已存在，因此不证明新触发的旧记录回填、浏览器 OAuth 或链接渲染。测试在内存中签发短期
会话，不修改 Sandbox。

Mac 恢复可操作后，普通 Chrome 的已登录页面在详情标题和侧边栏均显示了该 PR 的真实
标题；点击标题打开 GitHub PR #1，目标页面显示相同标题。已有 `example/vite` 部署也
显示了指向 `https://github.com/qiniu/deployment/tree/example/vite` 的分支链接。
解锁后，Chrome 已成功打开 `example/vite` 分支页面。按用户要求，后续验证改用
Codex 内置浏览器：未登录的 PR 深链接显示 GitHub 登录入口，完成 OAuth 后返回完全
相同的部署地址。在另一个本地标签页退出登录，再刷新仍挂载的部署页，页面显示登录
提示并保留原目标；重新登录后，同一部署的 PR 标题和日志恢复，未捕获到浏览器控制台
错误。内置浏览器中也打开并核对了 PR 和分支标题链接对应的 GitHub 来源页面。
本次通过退出登录验证真实会话丢失，不等同于按时间自然过期或 GitHub Token 撤销。
该轮登录检查未创建、重新部署、停止或删除 Sandbox。

随后在 `qiniu/deployment` 创建测试 Tag `test/deploy-vite-20261016`，指向
`example/vite` 的 `bd336bdc3e93d5aca8fd25a8aa2258d90e644123`。内置浏览器的创建
表单成功发现该 Tag，使用 `base`、`.qiniu/deploy.yaml` 和 600 秒 TTL 创建部署
`2c3xxapu6j4filrefhx6aymzwi`。部署进入 `running`，真实 Vite 页面返回 HTTP 200，
刷新后 Tag 路由和日志保留。标题链接指向
`https://github.com/qiniu/deployment/tree/refs/tags/test/deploy-vite-20261016`，
GitHub 目标页面正确识别该 Tag。手动停止后部署达到 `completed` 并清空活动 Sandbox
引用；独立 Provider 探针确认保存的 Sandbox 已不存在，同时精确 Tag SHA、版本和
事件历史保留。未捕获到浏览器控制台错误。测试 Tag 和终态部署记录保留供复查，未创建
Release。

2026-10-16 的私有来源验收使用用户指定的仓库，匿名访问其 GitHub API 返回 404。
Installation 所有者批准 `Contents: Read-only` 后，版本发现、配置读取及固定 Commit
的私有源码拉取成功。1 GB `base` 模板因 Node.js 堆内存不足而构建失败；相同源码使用
Node.js 22.22.0 和 8 GB Ubuntu 24.04 模板完成构建，预览地址返回 HTTP 200，内置
浏览器正常渲染应用且未捕获到控制台错误。详情刷新后来源、SHA 和日志保留，匿名读取
详情返回 401。失败构建和成功后手动停止的部署均保留历史，独立 Provider 探针确认两个
Sandbox 均已不存在。仓库标识、截图等详细证据保留在本地未跟踪文件中。本轮不证明
第二账号共享或真实权限撤销。来源项目的类型检查和生产构建通过；其全量 UI 测试为
396 项通过、3 项失败，原失败项单独复跑全部通过，不记为完整套件全绿或 runnerd
回归验证。

2026-10-16 的真实授权验收临时暂停了本地 App 的组织 Installation，其白名单仅含公开
测试仓库。GitHub 不允许保存空仓库列表，因此没有执行单仓库移除。首次暂停暴露出缺口：
`/user/installations/{id}/repositories` 仍返回 200 和目标仓库，而
`/user/installations` 已包含 `suspended_at`。现已在 Jobs／Deploy 共用的仓库授权
入口同时校验这两项证据。修复后等待 30 秒缓存到期，部署列表排除该仓库，详情、日志、
版本历史、PR 元数据、资源信息、停止、重部署和终端创建均返回 404，内置浏览器
不再显示原部署内容。随后恢复 Installation，缓存到期后 API 和浏览器访问恢复，原有
生命周期和版本历史未变化；没有新建 Sandbox 或删除部署。重部署拒绝测试使用无效 mode，
证明在解析操作参数前拒绝。暂停期间的真实创建探针误将远端 Installation ID 用作本地
关联记录 ID，因此该项 404 不计入授权验收。自动化测试使用保存的本地 ID，并断言准入／
发现返回特定的 Installation 不可访问错误，同时覆盖暂停期间删除和源码检出的拒绝。
下文独立的真实 OAuth Token 验收使用正确的本地 ID 验证了准入／发现。
本轮证明 Installation 暂停／恢复，
不等同于单仓库移除、OAuth Token 撤销或第二账号共享。
暂停校验修复通过 `task lint` 和 `task test`（332 项 UI 测试及 Go race／coverage
套件），其中包含 Jobs／Deploy 共用授权的暂停与恢复回归覆盖。

2026-10-16 的真实 OAuth Token 撤销验收先通过 GitHub 官方接口确认当前本地 App／
账号归属，再使用 `DELETE /applications/{client_id}/token` 仅撤销当前 Token，返回
204；旧 Token 随后访问 GitHub 返回 401。等待 30 秒后，15 项本地 API 检查均返回
`403 / REAUTH_REQUIRED`，覆盖 Jobs 列表／详情／事件、部署导航／列表／详情／事件／
版本／PR 元数据／资源信息、停止／重部署／终端创建，以及部署准入和分支发现。
准入与发现请求使用本地 Installation 关联记录 ID，与 GitHub 远端 Installation ID
明确区分。内置浏览器进入原部署地址后完成现有 OAuth 登录流程，回到完全相同的部署
URL；新保存的 Token 通过 GitHub 归属检查，10 项读取检查恢复 200，真实 PR 标题、
版本 #2 和历史日志恢复显示。原部署生命周期未变化，数据库仍保留 31 个部署和 36 个
版本，没有新建 Sandbox 或删除部署，证据中不保存凭据或 Cookie。本轮在本地会话丢失、
Installation 暂停之外，补齐了 GitHub 凭据被拒绝的独立验证；第二账号共享和单仓库移除
仍未验收。

## 部署 Web 终端

详情页提供“部署日志”“终端”和“部署历史”标签。“终端”始终明确标注并连接当前部署，
即使 URL 正在选择历史日志。只有当前运行中、未到期且具有 Sandbox ID
的版本可手动连接终端；切换标签或版本、离开详情页、停止部署都会关闭交互 shell。
终端可检查进程和应用日志文件，不会自动重放部署日志或附加到服务进程的 stdout；
shell 中的修改仅作用于当前 Sandbox。

终端路由前缀为 `/user/deployments/{id}/revisions/{number}/terminal`：
`POST` 创建 PTY，`GET /{sessionID}/events` 订阅输出，
`POST /{sessionID}/input` 发送输入，`POST /{sessionID}/resize` 调整大小，
`DELETE /{sessionID}` 关闭终端。访问会检查当前用户的仓库权限及 shell 归属；关闭已有终端
只需该终端的创建账号，以便在权限或生命周期变化后仍能清理。
会话绑定打开终端的账号、部署、revision、attempt 和 Sandbox 标识，历史版本不能连接当前 Sandbox。
详情与已保存日志继续从本地读取；终端使用部署保存的 Provider endpoint 和加密凭据，
不向浏览器暴露凭据。连接使用部署剩余有效期，不使用固定 Sandbox 超时。
本地生命周期每 5 秒检查一次，已打开的会话每 30 秒重新检查远程权限；
输出连接断开后沿用 30 秒空闲清理。

## GitHub 上下文路由

Jobs 和部署使用仓库资源集合路由：

- `/github/{owner}/{repo}/jobs` 与 `/github/{owner}/{repo}/deployments`
- `/github/{owner}/{repo}/jobs?branch=example/vite`
- `/github/{owner}/{repo}/deployments?branch=example/vite`
- `/github/{owner}/{repo}/deployments?tag=release/v1`
- `/github/{owner}/{repo}/pull_requests/{number}/jobs` 或 `/deployments`
- `/github/{owner}/{repo}/runs/{number}/jobs` 用于缺少来源上下文的 Jobs

Branch 和 Tag 名称放在查询参数中。浏览器链接保留可读的 `/`，同时继续编码
`&`、`#`、`+`、`%` 等字符。先编码再仅恢复 `%2F`，ref 中字面的 `%2F` 仍保留为 `%252F`。
`branch` 与 `tag` 互斥，空值和重复的来源条件无效。Jobs 尚无可靠的 Tag 证据，Tag 筛选仅用于部署。
`commit={sha}` 可按不可变提交筛选列表，分支 Job 分组链接始终携带它。
Jobs 使用 `job={id}` 选择资源。部署详情将 ID 放在路径中：
`/github/{owner}/{repo}/deployments/{id}?branch=example/vite`（Tag 使用 `?tag=release/v1`），
PR 使用 `/github/{owner}/{repo}/pull_requests/{number}/deployments/{id}`。
`revision=N` 选择部署的不可变版本。
部署详情 URL 不携带 `commit`，实际 SHA 由已保存的部署和版本确定。
详情展示短 SHA，链接到来源仓库的 GitHub Commit，并提供完整 SHA 的复制按钮。
返回历史列表时展示整个版本来源，不自动添加 Commit 筛选；显式筛选 Commit 的列表 URL 仍然有效。

仓库 Jobs 仅显示已有授权历史窗口（最多加载 500 条 Runner 请求）中的匹配分组，
空结果不会退回其他仓库；本次路由调整不新增无限历史归档。仓库部署列表在数据库分页前
筛选已保存版本。集合路由展示当前上下文的部署历史，详情路由读取选中的本地部署。
`/deployments` 保留为账号全局列表，`/deployments/new` 保留为创建表单。
仓库级创建链接预填仓库，再由用户选择版本来源。已有 Jobs 链接继续兼容并转换为
当前 Jobs 地址。Deploy 尚未上线，不兼容旧部署来源路径及 `?deployment={id}` 链接。
既有 API 路径不变。

`/deployments/{id}` 是不携带来源上下文的本地记录入口，读取后替换为保存的来源地址，历史版本使用自己的
ref，Commit SHA 从保存的版本读取。URL 中的仓库与来源不授予权限：详情仍按授权仓库和部署 ID 校验，再修正展示地址。
`GET /user/deployments` 的 `repository_full_name`、`ref`、`commit_sha` 和
`pull_request_number` 筛选在分页前查询已保存版本，`selected_revision` 标识匹配的历史版本。
内容读取不请求 Sandbox；授权仍可能刷新 GitHub 仓库权限。

Jobs 分组及独立详情提供“部署历史”和“部署此提交”，使用当前选中 Job 自己的仓库和
PR／分支。历史列表包含该来源的所有提交；创建时固定所选 Job 的 SHA，包括历史 Job。创建链接使用
`/deployments/new?repository=...&source=...&ref=...&commit_sha=...`，参数仅用于预填。
表单等待已授权仓库目录加载，保留显式指定但已关闭的 PR，并显示固定 SHA。
修改仓库、版本来源或 ref 会清除旧 SHA；“改用版本来源的最新提交”可主动解除固定。
提交时由 GitHub 校验最终来源。缺少完整 SHA 或来源信息的 Job 不展示这些入口。
PR 合成 merge SHA 无法通过 head 提交归属校验，会明确拒绝。

## PR 部署

创建时传入 `source: "pull_request"`，`ref` 为正整数 PR 编号。表单列出开放的 PR；
API 可显式指定已有的关闭 PR。执行固定的 PR head 提交，不部署 GitHub 合成的 merge 结果。
Sandbox 作用域与配额归属于已授权的目标仓库；GitHub 返回的 base 仓库身份必须一致，
来源仓库已删除或不可用时拒绝创建。服务端解析 `refs/pull/{number}/head`，确认与观察到的
head SHA 相同，然后通过目标仓库读取配置、检出该不可变 SHA。fork 提供的 URL 不决定
凭据或检出地址。GitHub App 需要已批准的 `Pull requests: Read-only` 和 `Contents: Read-only`。

可选 `commit_sha` 固定之前选择的提交。PR 提交须属于 GitHub 返回的 PR 提交列表
（最多 250 个）；分支提交须为当前 head 的祖先；Tag 须仍指向相同提交。强推后不可用或
不属于该来源的提交会拒绝，不会静默替换成新 head。每个版本保存 `source_repository_id`、
`source_repository_full_name` 和 `source_ref`；旧记录通过新增字段升级，来源证据保持为空。
PR 编号保存在完整 ref 中，并通过 `pull_request_number` 返回。最新重部署重新解析 PR head，
历史版本重部署复制已保存的来源与计划。历史内容仍从本地读取，并向有仓库权限的成员共享。
