# 本地测试与 GitHub 配置

[English](../testing.md)

这份文档说明如何用本地 Qiniu sandbox 环境测试服务，以及如何在 GitHub 仓库中配置 self-hosted runner 自动拉起。

## 1. 本地配置文件

服务现在默认读取 `./runnerd.yaml`；也可以通过 `--config` 指向别的路径：

```bash
cp runnerd.yaml.example runnerd.yaml
mkdir -p ./secrets
```

相对 sqlite `database.dsn` 和 `github.app.private_key_file` 都相对 `runnerd.yaml` 所在目录解析。旧版 `database.url` 在 `database.dsn` 为空时仍作为 deprecated alias 兼容读取。当前只支持 GitHub.com，不支持 GitHub Enterprise Server。GitHub 鉴权可以使用 GitHub App、PAT token 或 basic auth，但只能三选一。

最小可用配置示例：

```yaml
server:
  http_addr: ":25500"

database:
  backend: sqlite
  dsn: ./var/runnerd.db

auth:
  session_secret: <random session signing secret>
  encryption_key: <separate random encryption key>
  session_ttl_hours: 12

sandbox:
  regions:
    - id: us-south-1
      label: "United States · Dallas 1"
      sandbox_api_url: https://us-south-1-sandbox.qiniuapi.com
      # 可选：同时配置以下两个字段后，该区域支持 Cache S3。
      s3_region: us-north-1
      s3_endpoint: https://internal-s3-las-us-north-1-dal.qiniucs.com

github:
  webhook_secret: <random webhook secret>
  app:
    id: <github app id>
    # 可选。不填时会按 webhook 里的 repository 动态解析 installation。
    # installation_id: <installation id>
    private_key_file: ./secrets/github-app.pem
  oauth:
    client_id: <github app client id>
    client_secret: <github app client secret>
    redirect_url: http://127.0.0.1:25500/auth/github/callback
  # 可选。不填表示允许所有已安装 App 且能通过 runner policy/spec 匹配的仓库。
  # allowed_repositories:
  #   - <repo owner>/*
  #   - <repo owner>/<repo name>

worker:
  max_concurrent_runners: 100
  recovery_timeout_seconds: 120
  lease_ttl_seconds: 300
  retry_base_delay_seconds: 15
  retry_max_delay_seconds: 300
  retry_max_attempts: 5
```

如果需要避免敏感值被直接展示，先构建 runnerd，再将每个原值通过 stdin 传给 `./bin/runnerd --obfuscate-config-value`，然后把生成的 `RUNNERD_ENC(v1:...)` 填入 YAML。明文配置仍保持兼容。支持的字段包括 `database.dsn`/`database.url`、`auth.session_secret`、`auth.encryption_key`、`github.webhook_secret`、`github.token`、`github.basic_auth.password` 和 `github.oauth.client_secret`。运行时包装类型还会在意外的文本格式、结构化日志、JSON 和 YAML 输出中显示掩码。该能力仅用于混淆：解码 key 位于 runnerd 内，能够检查或执行二进制的主机用户仍可恢复原值。

Sandbox service API URL 和 API Key 不在 `runnerd.yaml` 中配置。登录后，`/repositories` 会展示所选账户或组织的有效 Sandbox 来源，但不再内嵌 credential editor。配置缺失时，可管理的 scope 会链接到 `/account/preferences` 或 `/organizations/{login}/preferences`，Settings 是唯一写入入口。组织 installation 可因用户仅拥有仓库权限而可见，但 Sandbox 管理权要求用户是 GitHub 组织的 active member；Settings 只列出当前账户和可管理组织。Outside collaborator 只能查看 readiness，不能修改组织 scope，也不能读取该组织的 Sandbox templates 与 runner-created instances。默认关闭的平台兜底仍由管理员在 `/admin/sandbox_service` 管理。fallback audience 为 `all` 或 `selected`；selected entries 按仓库 owner 的稳定 GitHub account ID 和 type 匹配。API Key 使用 `auth.encryption_key` 加密保存。解析顺序为 runner request 已保存快照、installation custom/inherited 配置、符合条件的个人账户配置、已启用且 audience eligible 的 admin default，最后才是未配置错误。

首次使用产品引导只会在现有账户级 `account_preferences` 表的 `onboarding/product-tour` 下保存版本号、状态和 `tour_seen` 标记，不会保存 Sandbox API Key。记录缺失或版本过旧时返回 `pending` 且 `tour_seen=false`。走完引导浮层后写入 `pending` 且 `tour_seen=true`，因此不会再次自动弹出，但必需的设置仍会保留。当前登录账户能解析到 custom、inherited 或符合条件的 admin default 任一有效 Sandbox 来源后即写入 `completed`。首次引导中显式跳过会写入 `skipped` 并关闭浮层，但不会隐藏必需设置。从账户菜单重播引导不会重置或覆盖已保存状态。

Runner Spec 不是 `runnerd.yaml` 字段。内部 Runner Group 和 Repository Policy 已移除；Spec 上可选的 `runner_group` 仍表示 GitHub Organization Runner Group 注册目标。
runnerd 启动时会协调 5 个 Qiniu Ubuntu managed specs。其 labels、required
labels、稳定公共模板名称、priority 和 default availability 由 runnerd 管理，
operator 控制的 `enabled`、`max_concurrency` 和 `min_idle` 会被保留。自定义
spec 仍通过 Admin API/UI 管理，需要显式 `template_id`、advertised labels 和
可选 required labels。新建自定义 spec 或更换模板 ID 前，需要在
`/admin/sandbox_service` 配置后台 Sandbox endpoint 和 API Key。校验只使用这套
后台配置，不读取当前登录用户或组织的凭据；运行时默认服务的启用开关和 audience
不限制管理员校验。
`GetTemplate` 确认模板存在且可访问，再从所属团队目录或公共默认目录读取实际已上传的
默认构建 ID。最新重建失败或仍在进行时，旧的可用默认构建仍可通过检查。详情中的
构建历史不能直接代表可用状态：它有分页、包含其他 tag，且对非所有者隐藏。
不在公共默认目录中的第三方公共模板无法通过该 API 确认构建状态，会返回
`template_state_unavailable`。

5 个 `-large` workflow labels 有意作为 operator 配置的对外默认 spec，而不是
managed catalog 条目。需要在 Admin 中单独创建并启用对应 spec，然后在自定义 spec 检查中验证显式
template ID 和标签契约；它们不应出现在 managed spec reconciliation 列表或公共
managed-template API 中。

整个远程检查限时 5 秒。未配置后台服务返回 `409 sandbox_service_not_configured`；
模板不存在或没有可用默认构建分别返回 `400 template_not_found`、
`400 template_not_ready`。上游 401/403 返回 `502 sandbox_template_access_denied`，
其他上游故障返回 `502 template_validation_unavailable`，超时或取消返回
`504 template_validation_timeout`。应修正配置、模板或重试；检查失败不会静默放行，
也不会将上游响应正文暴露给客户端。拒绝保存时，spec 和审计记录均保持不变。若校验期间其他操作已修改或删除该 spec，条件写入返回 `409 runner_spec_conflict`；应刷新后重试，不会覆盖较新的状态。

PATCH 按去除首尾空白后的模板 ID 判断是否变更。只修改标签、容量或启用状态时不访问
Sandbox；managed spec 的控制项也不校验模板，继续保持运行时名称解析。已有 spec
不会被自动重新验证或禁用。实际任务仍使用所属账户或组织的 Sandbox 配置；后台检查
通过不代表其他作用域拥有访问权限，也不证明镜像包含 Runner 程序，仍需验证真实 workflow。

`database.backend` 支持 `sqlite`、`postgres` 和 `mysql`。本地开发优先使用 sqlite；共享数据库的多实例部署需要先用两个 runnerd 进程验证 lease 行为，再作为正式运行方式记录。

新建的已接受和已拒绝 runner request 在内存中从 webhook 解析 workflow 上下文、分支、SHA、Job URL 和 PR 编号，并将 `github_payload_json` 留空。Installation ID 和 Job ID 来自 `RunnerRequest` 字段；这些字段、仓库、标签及解析后的 webhook 上下文仍保存到结构化字段，供运行和展示使用。State 测试覆盖重新打开数据库后的 `workflow_job`、`workflow_run` 元信息、拒绝请求的持久化，以及重复迁移和 installation ID 修复时保留历史原文。保留历史兼容列、已有值和回填逻辑；清理历史原文与回收磁盘空间需要单独安排维护操作。请求与日志的保留行为不变。

状态表结构主要由 `internal/state/records.go` 里的 GORM tag 定义。服务启动时，已有 SQLite `runner_requests` 和 `runner_profiles` 表只通过创建全部缺失的 model columns 和 indexes 做 additive migration；它们会跳过通用 SQLite `AutoMigrate` 表重建，从而保留历史上通过 ALTER 添加的 runner-request 字段，以及旧 runner-profile rows 和自定义 indexes，并补齐 managed-catalog 字段。Admin newest-first 列表依赖 `(queued_at DESC, id ASC)` 上的 `idx_runner_requests_queued_id`。Repository-authorized 列表通过 `(github_installation_id, queued_at DESC, id ASC)` 上的 `idx_runner_requests_github_installation_queued_id` 分别查询每个 installation，再合并有界结果。创建缺失索引不会重写 rows，但应先在 disposable production-sized copy 上测量启动 I/O 和锁等待。未来如需对任一 additive-only 表做 non-additive 变更，必须增加窄范围显式 migration 和数据保全回归 fixture。其他表会先针对旧 columns、obsolete OAuth constraints 和不兼容的 legacy scope tables 执行窄范围 compatibility pass，再运行 GORM `AutoMigrate`。缺少 `scope_type`/`scope_id` 的旧 `account_preferences` 和 `account_secrets` 表会被删除并重建，而不是迁移原数据。升级后必须重新配置其中保存的 Sandbox Preferences 和 API keys；已保存的 GitHub OAuth tokens 也会被清除，相关用户需重新使用 GitHub 登录后才能同步 installations。修改 state record、索引或迁移 helper 时，至少先跑：

```bash
go test ./internal/state -count=1
```

不要只用全新 sqlite 文件验证迁移；旧 schema 升级路径也需要覆盖，尤其是新增 `NOT NULL` 列、唯一索引或关系约束时。

确认 SQLite 可以直接通过索引满足 newest-page 排序，不需要临时排序：

```bash
sqlite3 ./var/runnerd.db \
  "EXPLAIN QUERY PLAN SELECT id, queued_at FROM runner_requests ORDER BY queued_at DESC, id ASC LIMIT 100;"

sqlite3 ./var/runnerd.db \
  "EXPLAIN QUERY PLAN SELECT id, queued_at FROM runner_requests WHERE github_installation_id = 123 AND LOWER(repository_full_name) IN ('owner/repo') ORDER BY queued_at DESC, id ASC LIMIT 100;"
```

预期结果：第一条计划使用 `idx_runner_requests_queued_id`，第二条使用 `idx_runner_requests_github_installation_queued_id`，并且都不包含 `USE TEMP B-TREE FOR ORDER BY`。需要分别验证每个 installation predicate；用 `OR` 合并多个 installation 可能重新触发临时排序。

验证生产 SQLite snapshot 时，应在 disposable copy 启动前后记录数据完整性计数：

```bash
sqlite3 runnerd-export.db \
  "SELECT COUNT(*), SUM(CASE WHEN github_installation_id > 0 THEN 1 ELSE 0 END), SUM(CASE WHEN sandbox_api_url <> '' THEN 1 ELSE 0 END), SUM(CASE WHEN sandbox_api_key_encrypted <> '' THEN 1 ELSE 0 END), SUM(CASE WHEN sandbox_config_source <> '' THEN 1 ELSE 0 END) FROM runner_requests;"
```

迁移需要连续运行两次。两次启动后的总行数和各字段非空计数都必须保持稳定；唯一允许增加的是可从 `github_payload_json.installation.id` 恢复的 `github_installation_id`。

仓库提供了一个 opt-in 的 state-only snapshot test。它会先复制源数据库，不会启动 runner recovery：

```bash
RUNNERD_SQLITE_SNAPSHOT=/path/to/runnerd-export.db \
  go test ./internal/state -run TestMigrateSQLiteRunnerRequestSnapshot -count=1 -v
```

State migration 和带审计的 catalog mutation 还提供 opt-in 的真实方言兼容性
门禁。两个 DSN 必须指向名称以 `_test` 结尾的专用、
可丢弃数据库；测试会拒绝其他数据库名称，然后删除并重建 runnerd state tables。
覆盖范围包括 fresh schema 不创建已退役 catalog tables、重复迁移，以及 mutation
与 audit 的原子提交和回滚。

```bash
RUNNERD_CATALOG_BACKEND_TESTS=1 \
RUNNERD_POSTGRES_TEST_DSN='host=127.0.0.1 user=runnerd password=runnerd dbname=runnerd_test port=5432 sslmode=disable' \
RUNNERD_MYSQL_TEST_DSN='runnerd:runnerd@tcp(127.0.0.1:3306)/runnerd_test' \
  go test ./internal/state -run 'Test(ApplyMutationWithAudit|FreshSchema|ScopedRunnerCatalogFreshSchema)SQLBackends' -count=1 -v
```

服务重启恢复有一组不依赖真实 Sandbox 的定向测试：

```bash
go test -tags development ./cmd/runnerd -run TestRecoveryGateAllowsOnlyHealthUntilReady -count=1
go test -tags development ./internal/server -run TestRecover -count=1
go test ./internal/sandboxrunner -count=1
```

启动门禁测试必须确认恢复完成前只有 `/healthz` 可访问。`TestRecover*` 用例必须确认最多恢复四个请求，每个 worker 会根据剩余启动恢复总预算和剩余 worker 波次确定单请求超时，父 context 预算耗尽时不再投递，取消会报告每个被跳过的请求，queued 请求会清理旧 lease，creating/running 请求会重连且不停止沙箱，并发 state version 变化优先于成功或失败的旧重连结果，已超时沙箱不重连而是停止，创建过程中未找到沙箱时会重新排队，而 GitHub job 已进入 `in_progress` 的缺失创建会显式失败，GitHub 任务已完成时仍会进入清理流程，并且单个请求重连失败不会阻断其他请求恢复。

## 2. 配置 GitHub 鉴权

推荐使用 GitHub App。PAT token 和 basic auth 也支持，主要用于本地验证或已有凭据场景。

继续前，请先配置[必要的 GitHub App 权限](../../README.zh.md#所需权限)。以下步骤只说明本地设置细节。

建议流程：

1. 进入 GitHub `Settings -> Developer settings -> GitHub Apps -> New GitHub App`。
2. 基础信息：
   - GitHub App name：例如 `runnerd-local`
   - Homepage URL：先填仓库地址或本地项目文档地址
   - Setup URL：填 runnerd 的 `/github-app/setup` 地址，例如 `http://127.0.0.1:25500/github-app/setup`
   - Webhook：如果 runnerd 自己收 webhook，可以先不开 App webhook，这里和 `workflow_job` webhook 不是一回事
3. 在 `Permissions` 中应用[必要权限表](../../README.zh.md#所需权限)中的设置。
4. Where can this GitHub App be installed：
   - 本地验证一般选 `Only on this account`
5. 创建后，在 App 页面生成 private key，下载 `.pem` 文件，保存到本地，例如 `./secrets/github-app.pem`
6. 安装 App 到目标仓库或组织：
   - 点 `Install App`
   - 选择目标 owner
   - 选择要授权的仓库
   - 如果给已有 App 新增了 `Members: Read-only`，每个 installation owner 都必须先批准权限更新，组织 Settings 才会变为可管理
7. 记录这些值：
   - App ID
   - App slug（App URL 里的短名称，例如 `https://github.com/apps/<slug>`）
   - Installation ID（可选；不配置时 runnerd 会按仓库动态解析）
   - private key 文件路径

Installation owner 批准 `Members: Read-only` 后，重新加载 `/account/preferences`。当前登录用户具有 active membership 的组织应出现在 Settings scope 列表中，并能打开 `/organizations/{login}/preferences`。仅有 repository 权限的 outside collaborator 仍可在 `/repositories` 看到已授权仓库，但对应组织必须继续从 Settings 隐藏。如果批准后符合条件的组织仍未出现，先退出登录，再重新完成 GitHub OAuth 后重试。

对应填入 `runnerd.yaml`：

```yaml
github:
  app:
    id: <app id>
    slug: <app slug>
    # installation_id: <installation id>
    private_key_file: ./secrets/github-app.pem
```

PAT 示例：

```yaml
github:
  webhook_secret: <random webhook secret>
  token: <github token>
```

Basic auth 示例：

```yaml
github:
  webhook_secret: <random webhook secret>
  basic_auth:
    username: <github username>
    password: <token or password>
```

不需要固定全局 repo/org 模式；webhook 会使用 payload 里的 `repository.full_name`。默认创建 repository runner；如果匹配到的 runner spec 设置了 GitHub `runner_group`，runnerd 会按该仓库 owner 创建 organization runner，并把 `runner_group` 作为 GitHub runner registration 的 `--runnergroup` 传入。通过仓库 allowlist 检查后，admission 会从所有已启用 Runner Spec 中按标签选择；已移除的内部 Runner Group 和 Repository Policy 不影响匹配。

## 3. 启动服务

开发 UI 或后端时，优先使用开发模式：

```bash
task deps
task ui-deps
cp runnerd.yaml.example runnerd.local.yaml
task dev
```

`task dev` 默认读取 `runnerd.local.yaml`，从 `127.0.0.1:5173` 开始选择第一个可用端口启动 Vite dev server，并用 `development` build tag 启动 Go 服务。浏览器仍然访问 runnerd 的地址。

公开产品首页：

```text
http://127.0.0.1:25500/
```

受保护的普通用户 Jobs 首页：

```text
http://127.0.0.1:25500/jobs
```

普通用户 repository access 与 Runner readiness 页面：

```text
http://127.0.0.1:25500/repositories
```

兼容的个人 repository 深链，由同一 readiness 页面渲染：

```text
http://127.0.0.1:25500/account/repositories
```

普通用户 personal Preferences 页面：

```text
http://127.0.0.1:25500/account/preferences
```

普通用户个人 Sandbox 目录：

```text
http://127.0.0.1:25500/account/sandbox-templates
http://127.0.0.1:25500/account/sandbox-instances
```

管理员界面：

```text
http://127.0.0.1:25500/admin/
```

如需换配置文件：

```bash
RUNNERD_CONFIG=./runnerd.yaml task dev
```

如需固定 Vite 端口：

```bash
RUNNERD_VITE_PORT=5173 task dev
```

生产模式或验证嵌入式前端资源时，先重新构建 UI 和 binary，再启动 runnerd：

```bash
task build
./bin/runnerd --config ./runnerd.yaml
```

健康检查：

```bash
curl -fsS http://127.0.0.1:25500/healthz
```

受保护的普通用户 Jobs 页面：

```text
http://127.0.0.1:25500/jobs
```

页面会显示 GitHub OAuth 登录入口，并在认证后返回 `/jobs`。首次登录会在数据库中创建 `role=user` 的本地 account，并把 GitHub OAuth identity 绑定到该 account；首个管理员需要在启动服务之前单独执行一次 bootstrap 命令；该命令会设置管理员角色后直接退出，不会启动 runnerd：

```bash
go run ./cmd/runnerd --config ./runnerd.yaml --bootstrap-admin github:<your-github-user-id>
```

`<your-github-user-id>` 是 GitHub `/user` 返回的稳定 numeric `id`，不是可修改的 login。role 属于本地 account，OAuth identity 只保存 provider、stable subject 和 login 展示信息，因此后续可以把其他 provider identity 绑定到同一个 account。普通用户登录后，`/repositories` 是统一 readiness 入口：安装或同步 GitHub App、加载授权仓库交集、展示本地 job activity，并解析所选账户或组织的有效 Sandbox 来源。如果不存在 custom、inherited 或符合条件的 admin-default 来源，可管理的 scope 会显示 **Configure Sandbox**，链接到精确的账户或组织 Preferences 路由；credential 表单保留在 Settings 中。Sandbox Templates 和 Sandbox Instances 只保留给当前账户及可管理组织。GitHub 带 `installation_id` 回调后，runnerd 会记录该 account 绑定的 GitHub App installation。普通用户能看到的 job 按精确的 `(installation_id, repository_full_name)` 过滤；runnerd 使用已保存的 GitHub App user access token，获取用户仓库权限与每个已绑定 App installation 仓库范围的交集，并统一保护列表、详情、分组、日志和终端操作。runnerd 不会把完整的仓库授权列表复制到本地状态；成功结果在内存中的硬过期时间仍为 30 秒。缓存满 20 秒后的首次请求会立即使用仍有效的交集，并只触发一次后台刷新。共享刷新使用 server 级 timeout，不受首个调用者取消影响；installation 或 OAuth 变更会推进 account cache epoch，旧 epoch 的刷新不能回填缓存，也不能服务后续请求。GitHub 拒绝 token 时会立即清除缓存；瞬时刷新错误可以重试，但不会把授权延长到原 30 秒期限之后。readiness 页面按需加载相同的交集。用户 token 缺失或被 GitHub 拒绝时会默认拒绝访问，并要求用户重新使用 GitHub 登录。GitHub 返回无权访问的已绑定 installation 时，runnerd 会跳过该 installation，既不会暴露其 job，也不会阻止其他可访问 installation 正常加载。目录接口要求普通用户 session；installation scope 还必须通过与 credential mutation 相同的 active-organization-membership 检查。接口使用 account 或选中 installation 的加密凭据，把支持的 region id 映射到服务端维护的 endpoint，且不会暴露凭据。管理员登录后，浏览器会保存 signed HttpOnly session cookie，并自动带上该 cookie 访问 `/runner_requests` 等管理接口。需要用 `curl` 调管理 API 时，可以从浏览器或 OAuth 调试流程导出 cookie 到 `COOKIE_JAR`，后续示例统一使用：

```bash
export COOKIE_JAR=./runnerd.cookies
```

管理员账户页面：

```text
http://127.0.0.1:25500/admin/accounts
```

顶部统计卡展示账户总数、管理员、普通用户和已绑定 OAuth identity 数量；这些统计是全局口径，不受搜索、角色筛选或分页影响。账户列表可搜索关联 OAuth identity 的 login、provider 和 stable subject；`role` 可筛选 `admin` 或 `user`，`limit` 和 `offset` 用于分页，默认每页 20 条、最多 100 条。关联 GitHub identity 会按 login 加载约定的 GitHub 头像 URL；头像不可用时回退到账户首字母。页面只能把其他 account 的 role 在 `admin` 和 `user` 之间切换；account 仍由 OAuth/bootstrap 创建，页面不能创建或删除 account，也不能绑定或解绑 identity。角色修改会立即生效，并写入 `account.role.update` 审计事件。系统会拒绝修改自身角色，以及可能导致没有管理员的变更，包括相互竞争的并发降级操作。

```bash
curl -fsS -b "$COOKIE_JAR" \
  'http://127.0.0.1:25500/admin/api/accounts?q=octo&role=admin&limit=20&offset=0' | jq
curl -fsS -X PATCH -b "$COOKIE_JAR" -H 'content-type: application/json' \
  http://127.0.0.1:25500/admin/api/accounts/<account-id>/role \
  -d '{"role":"admin"}' | jq
```

管理员通过显式的 role-gated API 管理平台回退。省略 `api_key` 会保留已有密文，省略 `audience_mode` 会保留当前模式，响应永远不会返回 API Key。`selected` 模式没有 audience entries 时不会匹配任何 account。添加 audience 时可提交 `login` 或 `@login`；runnerd 会先向 GitHub 查询 canonical login、stable numeric ID 和 user/organization type，再保存稳定身份。已同步或已缓存的 owner 只作为可选建议，不是添加前提。selected owner 的第一个 workflow 如果没有本地 installation row，runnerd 会通过 GitHub App auth 查询 installation owner 并缓存该稳定身份。

```bash
curl -fsS -b "$COOKIE_JAR" http://127.0.0.1:25500/admin/api/sandbox-service-default | jq
curl -fsS -X PUT -b "$COOKIE_JAR" -H 'content-type: application/json' \
  http://127.0.0.1:25500/admin/api/sandbox-service-default \
  -d '{"enabled":true,"audience_mode":"selected","api_url":"https://us-south-1-sandbox.qiniuapi.com","api_key":"<sandbox-api-key>"}' | jq
curl -fsS -X POST -b "$COOKIE_JAR" -H 'content-type: application/json' \
  http://127.0.0.1:25500/admin/api/sandbox-service-default/audiences \
  -d '{"account_login":"octo-org"}' | jq
curl -fsS -X DELETE -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/admin/api/sandbox-service-default/audiences/<audience-id> | jq
curl -fsS -X DELETE -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/admin/api/sandbox-service-default/api-key | jq
```

页面源码在 `ui/`，使用 React、Vite、Tailwind CSS、shadcn 风格组件和仓库内主题 CSS。`task build` 会先执行 `task ui-build`，把前端产物写入 `internal/server/ui/` 后再编译 `runnerd`。`/` 始终显示公开产品首页，提供文档和 Jobs 入口，并且不会加载受保护的用户资源；受保护的普通用户 Jobs 首页位于 `/jobs`。未登录访问 `/jobs`、Job 分组深链、账户设置或 Admin 路由时，会显示独立登录页，其 OAuth 链接通过 `return_to` 保留完整的同源目标地址。未知路由显示 404；已登录但没有管理员角色的用户访问 Admin 路由时，会看到明确的无权限页面。`/repositories` 是普通用户统一的 readiness 页面，负责 GitHub App 安装/同步、用户与 App 的授权仓库交集、本地 job activity，以及有效 Sandbox service 状态。`/account/repositories` 和 `/organizations/{login}/repositories` 作为兼容的 scoped deep links，仍由同一页面渲染。账户与组织 Preferences 是普通用户唯一的 Sandbox credential 编辑器；readiness 只在可管理 scope 缺少配置时链接过去。Settings 只为当前账户和可管理组织保留 Sandbox Service、Sandbox Templates 和 Sandbox Instances 资源管理。首次进入页面时只加载当前路由实际使用的资源。已登录的用户路由会读取一次 `GET /user/onboarding/product-tour` 获取账户级引导状态；该增强请求失败时会被忽略，不会阻断核心 workspace 数据，也不会参与轮询。只有状态为 `pending`、尚未看过引导且精确进入 `/jobs` 首页的账户会自动开始六步引导，深链不会被打断。最后几步会导航到 `/repositories`；有效来源无需操作，缺少且可管理的来源会引导用户进入 Settings。已看过、已完成或跳过的账户都可从账户菜单重播，且不修改已保存状态。Jobs 首页加载第一页 `GET /user/runner_requests?limit=100&offset=0` 并每 5 秒轮询该页，同时保留已经加载的历史；稳定 job-group 路由和 Load older jobs 操作可以加载受限的 500 行历史窗口。API 会拒绝 `limit + offset` 超过 500 的请求，也不会返回不可用的 next link。GitHub App metadata、Preferences 和 onboarding state 都不进入轮询。Admin 路由只加载当前 section 所需的 request/spec/audit 依赖。Overview 与 Runner 请求集合会轮询请求列表；活动中的请求资源每 5 秒轮询自身诊断信息，并在路由切换后忽略过期响应。公共 managed catalog 使用无需登录的 `GET /api/public/runner-templates`，并且只能暴露 runnerd-owned 稳定名称和 workflow labels。Provider catalog 使用 `GET /user/sandbox/templates?region=<id>` 和 `GET /user/sandbox/instances?region=<id>&template_id=<id>`；实例接口只列出 runner 创建的 sandboxes，并使用统一的 scoped/default credential resolver。Installation scope 的目录读取必须属于可管理组织。测试必须证明未登录与已登录的公共响应完全相同、不包含 provider/scoped metadata，并保证公共与 provider catalog 的加载、重试、失败恢复和 stale response 处理彼此独立。管理面包含 Overview、`/admin/accounts` 的账户列表与角色控制、带 retry/stop 操作及统一事件时间线的 Runner Requests、Runner Specs、`/admin/sandbox_service` 的平台回退、audit、label match test 和运行时 diagnostics，不包含已退役的内部 Runner Group/Policy 管理或 provider resource catalogs。

受保护的 `/runner-specs` 是不带作用域选择的普通用户只读平台目录，只显示已启用的平台规格及可复制的工作流标签；来源／状态 Badge、停用规格、账户／Organization 选择以及启用或并发控制都不在该页面展示。Settings 下的 `/account/runner-specs` 与 `/organizations/{login}/runner-specs` 只显示该作用域自有的自定义规格。三个页面都使用 `/user/runner-specs`；该 API 只接受当前账户或可管理 Organization 作用域，作用域自定义模板使用该作用域 Sandbox 凭据验证，不使用 Admin 兜底凭据。

Runner Spec 回归必须区分三种目录来源：`managed` 条目返回稳定公共模板名，并以只读
方式返回全局策略；`platform_custom` 条目只读且省略其私有 `template_id`，
`scoped_custom` 条目只返回当前作用域自己的 `template_id`，并可包含仅供
Organization 使用的 `runner_group`。精确规范化后的作用域标签会屏蔽同标签集合的
全局规格，即使作用域规格已停用也不能回退。新建和更换模板的验证发生在带审计事务
之前；不安全名称和其他无效本地字段必须在任何 provider 调用前拒绝。重复规范化
标签、过期 revision 和使用中的修改返回稳定 `409`，且不留下部分审计状态。
Lifecycle 测试还必须在准入后、启动前分别停用一个作用域自定义规格和全局规格，
并证明两者都不会启动 Sandbox。UI 测试必须让旧的手动刷新或 mutation
在切换 Organization 后才返回，并证明它不能覆盖新作用域数据或向新作用域提交。

只运行 UI unit tests 时使用：

```bash
cd ui && bun run test
```

修改固定 UI 文案、locale 资源、翻译 key 构造或 locale 格式化逻辑后，运行：

```bash
task ui-i18n-check
```

该检查会验证英文和中文资源树的类型与数组长度一致、值不为空，并使用相同的
插值变量；同时通过 TypeScript 校验 i18next key，并扫描 JSX 文本、部分用户可见
属性和直接 toast 调用中的未翻译固定字面量。运行时日志、仓库名、ID 和原始服务端
错误不属于翻译范围；确实需要在两种语言中保持一致的技术字面量可以显式加入精确
allowlist。GitHub Actions 会在独立的 `i18n` job 中运行同一检查；只有在仓库的
branch protection 或 ruleset 中将它设为 required check 后，它才会阻止合并。

修改 UI 依赖、Vite/Rollup 配置、manual chunk、生产静态资源加载逻辑或 Jobs
视口/滚动布局后，要在 Chromium 中执行构建产物：

```bash
task ui-production-smoke
```

该任务会安装匹配的 Chromium runtime，构建 `internal/server/ui/`，并在 `4173`
端口启动 Vite preview。Playwright 会打开 `/`，检查 JavaScript 异常、console
error、script/stylesheet 加载失败、空 `#root` 和缺失的公开首页 heading。本地
preview 还会使用限定范围的已登录 API fixtures 打开 `/jobs`，验证桌面端长 Jobs
列表滚动时 document 和 Web 控制台保持不动，同时确认窄屏仍保留正常的页面流。
端口 `4173` 被占用时可设置 `RUNNERD_UI_SMOKE_PORT=<free-port>`。本地 preview
不会启动 `runnerd`，因此这些响应均为测试 fixtures。设置
`RUNNERD_UI_SMOKE_BASE_URL=https://<runnerd-host>` 后，会使用部署环境的真实 auth
endpoint 执行公开页面 canary，并跳过仅供本地使用的 Jobs fixture 回归。该
production smoke 在 GitHub Actions 中是独立 job，因为 Vite 构建成功并不能证明
生成的 chunks 可以在浏览器中执行。

Vite 构建还会把 Rollup 的 `CIRCULAR_CHUNK` 和
`CYCLIC_CROSS_CHUNK_REEXPORT` warning 提升为 error。不要隐藏或宽泛
allowlist 这两类 warning；它们表示 manual chunk 可能生成浏览器不安全的模块
执行顺序。

`task test` 会重新构建 UI、运行同一套 Bun tests，然后执行带 race detection 和 coverage 的 Go tests。Bun suite 覆盖 helper 和 server-rendered component output；导航、dialog、头像加载/回退，以及角色变更后的权限切换仍需在真实浏览器中验证。修改 onboarding 时还要验证：`/jobs` 自动启动、六个目标、跳转到 `/account/preferences`、遮罩关闭后持续显示设置任务、显式跳过的持久化，以及重播不修改状态。

确认 runnerd 已协调 managed specs：

```bash
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_specs |
  jq '.[] | select(.managed_by == "qiniu/ci-runner") |
      {name, required_labels, default_template_name, enabled}'
```

结果应恰好包含 `qiniu-ubuntu-slim`、`qiniu-ubuntu-22.04`、
`qiniu-ubuntu-24.04`、`qiniu-ubuntu-26.04` 和 `qiniu-ubuntu-latest`。
验证 5 个 `-large` labels 时，应通过分别配置的自定义 spec 验证向后兼容的
显式模板路径：

```bash
curl -fsS -X POST http://127.0.0.1:25500/runner_specs \
  -b "$COOKIE_JAR" \
  -H 'content-type: application/json' \
  -d '{"name":"custom-ubuntu","labels":["self-hosted","custom-ubuntu"],"required_labels":["custom-ubuntu"],"template_id":"<template id>","max_concurrency":1,"enabled":true}' | jq
```

Runner Spec 名称会先去除首尾空白，并作为单路径段标识使用。新名称不得包含
`/`，也不得等于 `.` 或 `..`；被拒绝的创建会返回 `400 Bad Request`，且不会提交
Runner Spec 或对应的 audit event。启动和匹配仍兼容旧库中的此类名称，但 runnerd
不会自动重命名或删除这些记录，也不保证其单资源管理 URL 能通过反向代理的路径
规范化。

手动创建一个 runner：

```bash
curl -fsS -X POST http://127.0.0.1:25500/runner_requests \
  -b "$COOKIE_JAR" \
  -H 'content-type: application/json' \
  -d '{"id":"manual-001","repository_full_name":"<owner>/<repo>","runner_spec_name":"qiniu-ubuntu-24.04"}' | jq
```

查看状态：

```bash
curl -fsS -b "$COOKIE_JAR" http://127.0.0.1:25500/runner_requests | jq
curl -fsS -b "$COOKIE_JAR" http://127.0.0.1:25500/runner_requests/manual-001 | jq
```

停止 runner：

```bash
curl -fsS -X DELETE -b "$COOKIE_JAR" http://127.0.0.1:25500/runner_requests/manual-001 | jq
```

状态库默认会写到：

```text
var/runnerd.db
```

runner 的 control/stdout/stderr 日志存放在 DB-backed event store 里，仍然通过管理 API 读取：

```bash
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/manual-001/logs/control.log
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/manual-001/logs/stdout.log
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/manual-001/logs/stderr.log
```

Admin 请求详情的时间线会从同一事件库读取受限的混合事件页。若要读取更早记录，将当前返回的最早事件 ID 作为独占游标传入：

```bash
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/manual-001/events | jq
curl -fsS -b "$COOKIE_JAR" \
  'http://127.0.0.1:25500/runner_requests/manual-001/events?before_id=<oldest-event-id>' | jq
curl -fsS -b "$COOKIE_JAR" \
  'http://127.0.0.1:25500/runner_requests/manual-001/events?after_id=<newest-event-id>' | jq
```

`before_id` 与 `after_id` 不能同时使用。两者都是独占游标；`has_more` 表示请求方向上是否还有更多记录。

## 4. 启动后第一次自检

建议先确认 runnerd 已经正确读到 GitHub App 配置：

```bash
curl -fsS -b "$COOKIE_JAR" http://127.0.0.1:25500/diagnostics/pprof | jq
```

重点看：

- `github.auth_mode` 是否是 `app`
- 如果配置了静态 installation，`github.installation_id` 是否符合预期；动态 installation 模式下这里可以是 `0`
- `state.database` 是否指向你的 `runnerd.yaml` 里配置的数据库

## 5. 暴露 Webhook 地址

GitHub webhook 必须能访问到本地服务。任选一种方式：

使用 smee：

```bash
open https://smee.io/new
echo 'https://smee.io/<your-channel>' > .smee-url
task dev
```

把同一个 smee URL 填到 GitHub webhook 的 Payload URL。`.smee-url` 存在时，`task dev` 会自动启动 smee forwarder。也可以用 `task smee` 单独启动转发。默认转发到 `http://127.0.0.1:25500/webhooks/github`；如果 `runnerd.yaml` 使用了其他监听地址，可以设置 `SMEE_TARGET`：

```bash
SMEE_TARGET=http://127.0.0.1:25501/webhooks/github task smee
```

也可以使用 ngrok：

```bash
ngrok http 25500
```

或 cloudflared：

```bash
cloudflared tunnel create e2b-local-runner
cloudflared tunnel route dns e2b-local-runner runner.example.com
cloudflared tunnel run --url http://127.0.0.1:25500 e2b-local-runner
```

最终 webhook URL 形如：

```text
https://<public-host>/webhooks/github
```

这里的 `runner.example.com` 换成你自己的域名；不要把临时 quick tunnel 的随机 `trycloudflare.com` 地址写死到 GitHub 配置里。

公网部署时只需要把 `/webhooks/github` 暴露给 GitHub。`/runner_requests` 管理接口也可以在同一个服务上访问，但必须携带有效的 OAuth admin session cookie；生产环境建议放在 HTTPS 反向代理后面，并限制管理接口来源 IP。

## 6. 配置 GitHub Repository Webhook

在目标仓库中进入：

```text
Settings -> Webhooks -> Add webhook
```

填写：

- Payload URL：`https://<public-host>/webhooks/github`
- Content type：`application/json`
- Secret：和 `runnerd.yaml` 里的 `github.webhook_secret` 完全一致。
- Which events：选择 `Workflow jobs`。如果希望开启补偿路径，也可以同时选择 `Workflow runs`。
- Active：勾选。

保存后，GitHub 会发送一次 ping。当前服务处理 `workflow_job.queued` / `workflow_job.in_progress` / `workflow_job.completed` 作为主路径，也处理 `workflow_run.requested` / `workflow_run.in_progress` 作为补偿路径；其他事件会返回 ignored，这是正常的。

## 7. 配置 GitHub Actions Workflow

在目标仓库添加：

```yaml
name: qiniu-runner-smoke

on:
  workflow_dispatch:

jobs:
  smoke:
    runs-on: [qiniu, ubuntu-24.04]
    steps:
      - name: Print runner info
        run: |
          uname -a
          whoami
          pwd
```

触发 `workflow_dispatch` 后预期流程：

1. GitHub 创建一个 `workflow_job.queued` webhook。
2. 本服务校验签名并在状态库里写入一条 `queued` runner request。
3. 服务创建 sandbox，获取 GitHub registration token，并在 sandbox 内启动 ephemeral runner。
4. GitHub job 被 managed `qiniu,ubuntu-24.04` runner 接走执行。
5. runner 进程退出后，服务清理对应 sandbox。

如果同时配置了 `Workflow runs` 事件，`workflow_run.requested` / `workflow_run.in_progress` 只作为补偿信号：runnerd 会查询该 run 下仍处于 `queued` 的 jobs，并为尚未通过 `workflow_job` 入队的 job 补建 runner request。这个补偿动作本身不会让 GitHub Actions UI 立刻显示 job 正在运行；UI 会继续显示 queued / waiting for runner，直到 sandbox 内的 ephemeral runner 注册成功并被 GitHub 分配到该 job 后才会变成 in progress / running。

## 8. 排查顺序

先看服务状态：

```bash
curl -fsS -b "$COOKIE_JAR" http://127.0.0.1:25500/runner_requests | jq
```

再看 request 状态和日志：

```bash
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/<request_id> | jq
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/<request_id>/logs/control.log
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/<request_id>/logs/stdout.log
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/<request_id>/logs/stderr.log
```

常见问题：

- `invalid signature`：GitHub webhook secret 和 `github.webhook_secret` 不一致。
- `runner start deferred because global concurrency is at capacity` 或 `runner start deferred because profile is at capacity`：request 会保持 queued，直到全局或 per-spec 容量可用。
- GitHub job 一直 queued：managed default 必须同时包含 `qiniu` 和准确的操作
  系统 label。`[ubuntu-24.04]`、`[qiniu]` 和不受支持的额外 labels 都不会匹配；
  自定义 spec 则使用它自己的 advertised labels 和 required labels。
- 出现 `template_resolution` admission failure：确认 repository owner 对应的
  scoped Sandbox endpoint 中，managed stable name 恰好对应 1 个公共的
  `ready` 或 `uploaded` 模板。
- sandbox 创建失败：确认账户/组织 Preferences 或已启用的 admin default 具有与 template 和本地环境匹配的完整 Sandbox service 配置；Runner detail 会显示实际选择的来源。
- registration token 失败：检查 [GitHub App 权限表](../../README.zh.md#所需权限)。未配置 `runner_group` 的 spec 需要 repository `Administration`；配置了 `runner_group` 的 spec 需要 organization `Self-hosted runners`。

## 9. GitHub Actions 日志怎么看

runnerd 默认创建 repository 级 self-hosted GitHub Actions runner；如果 spec 配置了 `runner_group`，则会为 repository owner 创建 organization runner。job 被 sandbox 里的 runner 接走后，workflow step 的日志会正常显示在 GitHub Actions 页面里：

```text
Repository -> Actions -> 选择 workflow run -> 选择 job
```

能在 GitHub Actions 里看到：

- workflow step 的 stdout/stderr。
- checkout、build、test 等每个 step 的日志。
- job 成功、失败、取消状态。

不能完整依赖 GitHub Actions 看到：

- sandbox 创建失败日志，因为 runner 还没注册上 GitHub。
- runner 下载、`config.sh` 注册、`run.sh` 启动前的错误。
- webhook 校验失败、GitHub token 申请失败、sandbox API 调用失败。

这些控制面日志看本服务管理 API：

```bash
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/<request_id> | jq
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/<request_id>/logs/control.log
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/<request_id>/logs/stdout.log
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/<request_id>/logs/stderr.log
```

服务自身日志会输出到 runnerd 的 stdout/stderr，由启动终端或 service manager 收集。

## 10. Diagnostics / pprof

服务导入了 `github.com/jimmicro/pprof`，启动后会在 binary 所在目录生成 `.pprof` 地址文件和 dump 脚本。管理 API 可以直接查看 diagnostics：

```bash
curl -fsS -b "$COOKIE_JAR" http://127.0.0.1:25500/diagnostics/pprof | jq
curl -fsS -b "$COOKIE_JAR" http://127.0.0.1:25500/diagnostics/vars | jq
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/e2b-<request_id>/diagnostics | jq
```

`/diagnostics/pprof` 会返回：

- 发现到的 pprof 地址文件
- dump 脚本路径
- database backend 和经过脱敏的 DSN/path
- GitHub 鉴权模式（app、token 或 basic）

`/diagnostics/vars` 会直接返回当前 runnerd 进程的 expvar registry，不再选择发现到的 pprof address file，因此旧进程留下的 stale artifact 不会遮蔽当前指标。当前指标覆盖 profile current/busy/idle/pending/desired、retry/lease、create/stop 次数与耗时、GitHub API 调用、runner 注册/清理，以及 workflow job queued/started/completed、conclusion、failure、queue duration 和 run duration。

`/runner_requests/{id}/diagnostics` 仅供管理员按需调用，并使用规范的内部 Request ID。它会汇总请求状态、最新 200 条 `control_log` 生命周期事件，并在受限时间内查询 GitHub Job 当前结果；成功的 GitHub Job 查询会缓存 30 秒，同一 Job 的并发查询会被合并，避免活动页面每 5 秒刷新时都消耗 provider rate budget。事件会先过滤再应用数量上限，避免高频 stdout/stderr 输出挤掉生命周期证据。返回结果包含机器可读的诊断信号，例如“GitHub Job 已失败，但 runnerd 未观察到 Runner 退出”。GitHub 查询失败不会丢弃本地证据，接口也不会返回已保存的 Sandbox 凭证或原始 webhook payload。集合页通过 `GET /runner_requests_lookup/{identifier}` 执行精确查找；当 Runner Name 与内部 Request ID 命名空间出现同值时，优先匹配 Runner Name，再跳转到规范的 ID 路由。`/diagnostics/runner-requests/{identifier}` 继续作为兼容别名，并采用相同的查找优先级。新的 workflow completion 还会持久化 GitHub conclusion、Sandbox stop 请求／结果和最终清理结果，后续故障通常不必先搜索 service manager 的 stdout 日志。

Runner 请求页面支持用用户可见的 Runner Name 或内部 Request ID 精确查找，先解析该标识，再跳转到规范的 `/admin/runner_requests/{id}` 资源页面。表格会让每个请求字段保持单行展示；当视口窄于完整数据宽度时允许横向滚动，纵向滚动仍由页面承载而不是嵌套在表格中。请求资源页面聚合请求状态、诊断结论、GitHub Job 结果和一条按时间排序的“运行记录”。每条持久化的 control/stdout/stderr 事件都保留为独立时间线行，消息直接进入页面正常流，不再使用嵌套输出卡片或折叠交互。页面先加载最新 200 条混合事件，再通过 `GET /runner_requests/{id}/events?before_id=<event-id>` 使用独占游标读取更早记录。活动请求每 5 秒通过一页或多页独占 `after_id` 请求追平新增事件，同时保持历史游标独立，因此不会遗漏事件或丢弃管理员已经加载的历史。独立的 `/admin/diagnostics` 页面只负责 runnerd 运行时检查，展示脱敏摘要和 pprof discovery，只有管理员主动加载或刷新时才请求并渲染可能体积较大的 expvar 快照。旧的 `/admin/diagnostics?runner=...` 链接会先解析标识，再重定向到规范的请求资源。

Release C 在 matcher 切换完成后移除了临时 catalog migration readiness API 与界面。已退役的 Runner Group 和 Policy API 返回 `404`，当前 state、server 和 UI 行为都不依赖这些已移除模型。

## 11. 官方参考

- GitHub self-hosted runner workflow labels: https://docs.github.com/en/actions/hosting-your-own-runners/managing-self-hosted-runners/using-self-hosted-runners-in-a-workflow
- GitHub self-hosted runner autoscaling: https://docs.github.com/en/actions/hosting-your-own-runners/autoscaling-with-self-hosted-runners
- GitHub webhook `workflow_job` event: https://docs.github.com/en/webhooks/webhook-events-and-payloads
