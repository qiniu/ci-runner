# 本地测试与 GitHub 配置

[English](../testing.md)

这份文档说明如何用本地 Qiniu sandbox 环境测试服务，以及如何在 GitHub 仓库中配置 self-hosted runner 自动拉起。

本地质量检查使用 `task lint`；其中的 `staticcheck` 使用 `go.mod` 声明的 Go 版本，与 CI 保持一致。

## 1. 本地配置文件

[SQLite 到 PostgreSQL 在线迁移方案](sqlite-postgres-online-migration.md)包含运维步骤与验收边界。
`task state-online-migration-test` 执行带 race 的 state 专项；设置 `RUNNERD_CATALOG_BACKEND_TESTS=1`
和 `RUNNERD_POSTGRES_TEST_DSN` 后还覆盖真实 PG 捕获、复制、切库、回退子进程退出、在线锚点刷新及启动/收尾。
专用数据库名须以 `_test` 结尾，测试表会被重置。同环境执行
`go test -race ./internal/server -run TestOnlineMigrationServerSQLBackends -count=1`
可验证同一 Server/worker/SSE/假 PTY 的往返切换，使用独立 schema。
默认使用临时 SQLite 并跳过 PG 检查。故障覆盖行/位点/连接失败、journal 缺失/损坏、基线中断、身份漂移及 authority 不确定。
生产副本、真实 Runner/Deployment/原终端仍需独立验收。UI 构建会替换嵌入资源，应与 Go 编译串行执行。
忽略的 `var/` 临时 Go 文件若存在混合包会阻塞 `go test ./...`；项目 `task test` 检查 `./cmd/... ./internal/...`，无需改动这些文件。

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

如果需要避免敏感值被直接展示，先构建 runnerd，再将每个原值通过 stdin 传给 `./bin/runnerd --obfuscate-config-value`，然后把生成的 `RUNNERD_ENC(v1:...)` 填入 YAML。明文配置仍保持兼容。支持的字段包括 `database.dsn`/`database.url`、`database.online_migration.postgres_dsn`、`auth.session_secret`、`auth.encryption_key`、`github.webhook_secret`、`github.token`、`github.basic_auth.password` 和 `github.oauth.client_secret`。运行时包装类型还会在意外的文本格式、结构化日志、JSON 和 YAML 输出中显示掩码。该能力仅用于混淆：解码 key 位于 runnerd 内，能够检查或执行二进制的主机用户仍可恢复原值。

Sandbox service API URL 和 API Key 不在 `runnerd.yaml` 中配置。登录后，`/repositories` 会展示所选账户或组织的有效 Sandbox 来源，但不再内嵌 credential editor。配置缺失时，可管理的 scope 会链接到 `/account/preferences` 或 `/organizations/{login}/preferences`，Settings 是唯一写入入口。组织 installation 可因用户仅拥有仓库权限而可见，但 Sandbox 管理权要求用户具有 GitHub 组织的 active owner membership（`role == admin`）；Settings 只列出当前账户和所有者可管理的组织。普通组织成员、outside collaborator、空或未知 role 只能读取脱敏 readiness（`manageable: false` 和有效来源），不能读取 API URL、密钥状态、Cache S3 字段、继承来源账号元数据、templates、runner-created instances 或 scoped custom Runner Specs。membership 查询失败时，请求会被拒绝，且不会返回组织配置。本地 runnerd admin role 不能绕过 GitHub 组织所有者检查。默认关闭的平台兜底仍由管理员在 `/admin/sandbox_service` 管理。fallback audience 为 `all` 或 `selected`；selected entries 按仓库 owner 的稳定 GitHub account ID 和 type 匹配。API Key 使用 `auth.encryption_key` 加密保存。对于公共名称绑定的规格，解析顺序为 runner request 已保存快照、installation custom/inherited 配置、符合条件的个人账户配置、符合条件的组织赞助、已启用且 audience eligible 的 admin default，最后才是未配置错误。赞助会在创建 Sandbox 前重新校验稳定的 Fork/source/owner 身份，以及所选审批、权限或成员模式；它不会提供 Cache S3 或自定义 Spec。测试必须覆盖三种模式、公共名称绑定边界、容量延后、撤销、fresh retry 清空快照、owner-only 审计 API，以及账户／Organization UI 路由隔离。

首次使用产品引导只会在现有账户级 `account_preferences` 表的 `onboarding/product-tour` 下保存版本号、状态和 `tour_seen` 标记，不会保存 Sandbox API Key。记录缺失或版本过旧时返回 `pending` 且 `tour_seen=false`。走完引导浮层后写入 `pending` 且 `tour_seen=true`，因此不会再次自动弹出，但必需的设置仍会保留。当前登录账户能解析到 custom、inherited 或符合条件的 admin default 任一有效 Sandbox 来源后即写入 `completed`。首次引导中显式跳过会写入 `skipped` 并关闭浮层，但不会隐藏必需设置。从账户菜单重播引导不会重置或覆盖已保存状态。

Runner Spec 不是 `runnerd.yaml` 字段。内部 Runner Group 和 Repository Policy 已移除；Spec 上可选的 `runner_group` 仍表示 GitHub Organization Runner Group 注册目标。
所有平台 Runner 规格由数据库保存并在 `/admin/runner_specs` 维护。启动只迁移旧策略一次，不再协调固定目录或补建规格，新数据库的目录为空。详见[平台 Runner 规格管理](platform-runner-specs.md)。

分别验证公共名称和固定 ID 引用；ID 也可能指向公共模板。公共名称须通过配置的 Admin Sandbox 唯一解析为可运行的公共模板。创建、更换绑定及重新启用已展示规格时要求有效默认构建；已有规格只开启展示时仅验证公开性。这些操作均远程校验；绑定不变的策略修改和取消发布无需访问 Provider。校验只使用 Admin 凭据，不受运行时 fallback 的 enabled/audience 限制。固定 ID 保留 GetTemplate 和有效默认构建校验，开启展示还要求模板实际为 public。覆盖公共／非公共 ID、无法确认状态、表单 pending／过期响应及拒绝保存无审计变更。标准和 large 公共模板可保留现有 ID 开启展示，且不得改变运行行为或泄露 ID。本次无需新迁移，不自动开启展示。

整个远程检查限时 5 秒。未配置后台服务返回 `409 sandbox_service_not_configured`；
模板不存在或没有可用默认构建分别返回 `400 template_not_found`、
`400 template_not_ready`。上游 401/403 返回 `502 sandbox_template_access_denied`，
其他上游故障返回 `502 template_validation_unavailable`，超时或取消返回
`504 template_validation_timeout`。应修正配置、模板或重试；检查失败不会静默放行，
也不会将上游响应正文暴露给客户端。拒绝保存时，spec 和审计记录均保持不变。若校验期间其他操作已修改或删除该 spec，条件写入返回 `409 runner_spec_conflict`；应刷新后重试，不会覆盖较新的状态。

PATCH 比较去除首尾空白后的绑定，以及发布／启用转换。验证拒绝或过期保存不修改规格和审计，活动执行配置修改／删除返回 `409 runner_spec_in_use`，而启用、容量和取消发布仍可修改。迁移测试须保留时间戳、策略和索引，审计失败时回滚，并证明重启不会覆盖新策略或恢复已删除行。后台校验不证明镜像内容或其他 scope 的访问权，仍需区域和真实 workflow smoke。

`database.backend` 支持 `sqlite`、`postgres` 和 `mysql`。本地开发优先使用 sqlite；共享数据库的多实例部署需要先用两个 runnerd 进程验证 lease 行为，再作为正式运行方式记录。

新建的已接受和已拒绝 runner request 在内存中从 webhook 解析 workflow 上下文、分支、SHA、Job URL 和 PR 编号，并将 `github_payload_json` 留空。Installation ID 和 Job ID 来自 `RunnerRequest` 字段；这些字段、仓库、标签及解析后的 webhook 上下文仍保存到结构化字段，供运行和展示使用。另一组结构化快照字段只保留原始 Workflow Job 的终态名称、状态、结论、Runner 名称和本地观察时间；它不会恢复 raw webhook 留存，也不会回填历史记录。State 测试覆盖重新打开数据库后的 `workflow_job`、`workflow_run` 元信息、终态结果 round trip、拒绝请求的持久化，以及重复迁移和 installation ID 修复时保留历史原文。保留历史兼容列、已有值和回填逻辑；清理历史原文与回收磁盘空间需要单独安排维护操作。请求与日志的保留行为不变。

状态表结构主要由 `internal/state/records.go` 里的 GORM tag 定义。服务启动时，已有 SQLite `runner_requests` 和 `runner_profiles` 表只通过创建全部缺失的 model columns 和 indexes 做 additive migration；它们会跳过通用 SQLite `AutoMigrate` 表重建，从而保留历史上通过 ALTER 添加的 runner-request 字段，以及旧 runner-profile rows 和自定义 indexes，并补齐 managed-catalog 字段。Admin newest-first 列表依赖 `(queued_at DESC, id ASC)` 上的 `idx_runner_requests_queued_id`。管理员 `/runner_requests` 集合接口返回包含 `items`、`next_cursor` 和 `has_more` 的 envelope；游标对 `(queued_at, id)` 使用独占语义，不能与其他筛选条件混用。`/runner_request_metrics` 独立返回全局的排队／创建／运行／停止中／已完成／失败／未匹配和 Runner Spec 数量，不受列表筛选影响。Repository-authorized 列表通过 `(github_installation_id, queued_at DESC, id ASC)` 上的 `idx_runner_requests_github_installation_queued_id` 分别查询每个 installation，再合并有界结果。创建缺失索引不会重写 rows，但应先在 disposable production-sized copy 上测量启动 I/O 和锁等待。未来如需对任一 additive-only 表做 non-additive 变更，必须增加窄范围显式 migration 和数据保全回归 fixture。其他表会先针对旧 columns、obsolete OAuth constraints 和不兼容的 legacy scope tables 执行窄范围 compatibility pass，再运行 GORM `AutoMigrate`。缺少 `scope_type`/`scope_id` 的旧 `account_preferences` 和 `account_secrets` 表会被删除并重建，而不是迁移原数据。升级后必须重新配置其中保存的 Sandbox Preferences 和 API keys；已保存的 GitHub OAuth tokens 也会被清除，相关用户需重新使用 GitHub 登录后才能同步 installations。修改 state record、索引或迁移 helper 时，至少先跑：

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
与 audit 的原子提交和回滚。规格策略迁移测试还验证旧私有绑定保留物理 ID、清空不参与运行的公共名称、保持可编辑，并保留历史时间戳。

```bash
RUNNERD_CATALOG_BACKEND_TESTS=1 \
RUNNERD_POSTGRES_TEST_DSN='host=127.0.0.1 user=runnerd password=runnerd dbname=runnerd_test port=5432 sslmode=disable' \
RUNNERD_MYSQL_TEST_DSN='runnerd:runnerd@tcp(127.0.0.1:3306)/runnerd_test' \
  go test ./internal/state -run 'Test(ApplyMutationWithAudit|FreshSchema|ScopedRunnerCatalogFreshSchema|RunnerProfilePolicy)SQLBackends' -count=1 -v
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
   - Deploy 需要批准新增的 `Contents: Read-only` 和 `Pull requests: Read-only`；批准后重试版本发现，已保存用户授权被拒绝时重新通过 GitHub 登录
   - 如果给已有 App 新增了 `Members: Read-only`，每个 installation owner 都必须先批准权限更新，组织 Settings 才会变为可管理
7. 记录这些值：
   - App ID
   - App slug（App URL 里的短名称，例如 `https://github.com/apps/<slug>`）
   - Installation ID（可选；不配置时 runnerd 会按仓库动态解析）
   - private key 文件路径

Installation owner 批准 `Members: Read-only` 后，重新加载 `/account/preferences`。当前登录用户具有 active owner membership（`role: admin`）的组织应出现在 Settings scope 列表中，并能打开 `/organizations/{login}/preferences`。普通组织成员和仅有 repository 权限的 outside collaborator 仍可在 `/repositories` 看到已授权仓库，但对应组织必须继续从 Settings 隐藏。如果批准后符合条件的组织仍未出现，先退出登录，再重新完成 GitHub OAuth 后重试。

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

`<your-github-user-id>` 是 GitHub `/user` 返回的稳定 numeric `id`，不是可修改的 login。role 属于本地 account，OAuth identity 只保存 provider、stable subject 和 login 展示信息，因此后续可以把其他 provider identity 绑定到同一个 account。普通用户登录后，`/repositories` 是统一 readiness 入口：安装或同步 GitHub App、加载授权仓库交集、展示本地 job activity，并解析所选账户或组织的有效 Sandbox 来源。如果不存在 custom、inherited 或符合条件的 admin-default 来源，可管理的 scope 会显示 **Configure Sandbox**，链接到精确的账户或组织 Preferences 路由；credential 表单保留在 Settings 中。Sandbox Templates 和 Sandbox Instances 只保留给当前账户及所有者可管理的组织。GitHub 带 `installation_id` 回调后，runnerd 会记录该 account 绑定的 GitHub App installation。普通用户能看到的 job 按精确的 `(installation_id, repository_full_name)` 过滤；runnerd 使用已保存的 GitHub App user access token，获取用户仓库权限与每个已绑定 App installation 仓库范围的交集，并统一保护列表、详情、分组、日志和终端操作。runnerd 不会把完整的仓库授权列表复制到本地状态；成功结果在内存中的硬过期时间仍为 30 秒。缓存满 20 秒后的首次请求会立即使用仍有效的交集，并只触发一次后台刷新。共享刷新使用 server 级 timeout，不受首个调用者取消影响；installation 或 OAuth 变更会推进 account cache epoch，旧 epoch 的刷新不能回填缓存，也不能服务后续请求。GitHub 拒绝 token 时会立即清除缓存；瞬时刷新错误可以重试，但不会把授权延长到原 30 秒期限之后。readiness 页面按需加载相同的交集。用户 token 缺失或被 GitHub 拒绝时会默认拒绝访问，并要求用户重新使用 GitHub 登录。GitHub 返回无权访问的已绑定 installation 时，runnerd 会跳过该 installation，既不会暴露其 job，也不会阻止其他可访问 installation 正常加载。目录接口要求普通用户 session；installation scope 还必须通过与 credential mutation 相同的 active-owner-role 检查。接口使用 account 或选中 installation 的加密凭据，把支持的 region id 映射到服务端维护的 endpoint，且不会暴露凭据。管理员登录后，浏览器会保存 signed HttpOnly session cookie，并自动带上该 cookie 访问 `/runner_requests` 等管理接口。需要用 `curl` 调管理 API 时，可以从浏览器或 OAuth 调试流程导出 cookie 到 `COOKIE_JAR`，后续示例统一使用：

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

独立的 GitHub App 账号页面是只读管理面。它使用 App authentication 查询 GitHub 当前 installation 目录，不读取本地 OAuth account 表：

```text
http://127.0.0.1:25500/admin/github_accounts
```

选择账号后会打开 `/admin/github_accounts/<installation-id>`，并加载该 installation 当前授权的仓库范围。路由使用数字 installation ID 作为标识，因此 GitHub login 改名不会让深链失效。这些 API 只返回账号展示身份和仓库全名，不返回 installation token、permissions、凭据或本地观测到的 repository rows。

```bash
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/admin/api/github-app/installations | jq
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/admin/api/github-app/installations/<installation-id>/repositories | jq
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

页面源码在 `ui/`，使用 React、Vite、Tailwind CSS、shadcn 风格组件和仓库内主题 CSS。`task build` 会先执行 `task ui-build`，把前端产物写入 `internal/server/ui/` 后再编译 `runnerd`。`/` 始终显示公开产品首页，提供文档和 Jobs 入口，并且不会加载受保护的用户资源；受保护的普通用户 Jobs 首页位于 `/jobs`。未登录访问 `/jobs`、Job 分组深链、账户设置或 Admin 路由时，会显示独立登录页，其 OAuth 链接通过 `return_to` 保留完整的同源目标地址。未知路由显示 404；已登录但没有管理员角色的用户访问 Admin 路由时，会看到明确的无权限页面。`/repositories` 是普通用户统一的 readiness 页面，负责 GitHub App 安装/同步、用户与 App 的授权仓库交集、本地 job activity，以及有效 Sandbox service 状态。`/account/repositories` 和 `/organizations/{login}/repositories` 作为兼容的 scoped deep links，仍由同一页面渲染。账户与组织 Preferences 是普通用户唯一的 Sandbox credential 编辑器；readiness 只在可管理 scope 缺少配置时链接过去。Settings 只为当前账户和所有者可管理的组织保留 Sandbox Service、Sandbox Templates 和 Sandbox Instances 资源管理。首次进入页面时只加载当前路由实际使用的资源。已登录的用户路由会读取一次 `GET /user/onboarding/product-tour` 获取账户级引导状态；该增强请求失败时会被忽略，不会阻断核心 workspace 数据，也不会参与轮询。只有状态为 `pending`、尚未看过引导且精确进入 `/jobs` 首页的账户会自动开始六步引导，深链不会被打断。最后几步会导航到 `/repositories`；有效来源无需操作，缺少且可管理的来源会引导用户进入 Settings。已看过、已完成或跳过的账户都可从账户菜单重播，且不修改已保存状态。Jobs 首页加载第一页 `GET /user/runner_requests?limit=100&offset=0` 并每 5 秒轮询该页，同时保留已经加载的历史；稳定 job-group 路由和 Load older jobs 操作可以加载受限的 500 行历史窗口。API 会拒绝 `limit + offset` 超过 500 的请求，也不会返回不可用的 next link。GitHub App metadata、Preferences 和 onboarding state 都不进入轮询。Admin 路由只加载当前 section 所需的 request/spec/audit 依赖。Overview 与 Runner 请求集合会轮询请求列表；活动中的请求资源每 5 秒轮询自身诊断信息，并在路由切换后忽略过期响应。已发布公共目录 使用无需登录的 `GET /api/public/runner-templates`，并且只能暴露 已发布的稳定名称和 workflow labels。Provider catalog 使用 `GET /user/sandbox/templates?region=<id>` 和 `GET /user/sandbox/instances?region=<id>&template_id=<id>`；实例接口只列出 runner 创建的 sandboxes，并使用统一的 scoped/default credential resolver。Installation scope 的目录读取必须属于所有者可管理的组织。测试必须证明未登录与已登录的公共响应完全相同、不包含 provider/scoped metadata，并保证公共与 provider catalog 的加载、重试、失败恢复和 stale response 处理彼此独立。管理面包含 Overview、`/admin/accounts` 的账户列表与角色控制、带 retry/stop 操作及统一事件时间线的 Runner Requests、Runner Specs、`/admin/sandbox_service` 的平台回退、audit、label match test 和运行时 diagnostics，不包含已退役的内部 Runner Group/Policy 管理或 provider resource catalogs。

公共目录缓存测试覆盖并发读取合并、到期刷新、数据库错误后恢复、Admin 成功创建／修改／删除后的失效，以及拒绝修改后的缓存保留。

受保护的 `/runner-specs` 是不带作用域选择的普通用户只读平台目录，只显示已启用且已发布的公共规格及可复制的工作流标签；来源／状态 Badge、停用规格、账户／Organization 选择以及启用或并发控制都不在该页面展示。Settings 下的 `/account/runner-specs` 与 `/organizations/{login}/runner-specs` 只显示该作用域自有的自定义规格。三个页面都使用 `/user/runner-specs`；该 API 只接受当前账户或可管理 Organization 作用域，作用域自定义模板使用该作用域 Sandbox 凭据验证，不使用 Admin 兜底凭据。

Runner Spec 回归须区分已展示公共 `platform_public` 或 `platform_custom` 条目与 scope 自有的 `scoped_custom` 条目。未开启展示的平台规格不出现在 User 响应中，物理 ID 和 Runner Group 只随所选 scope 自有条目返回。

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

本地管理后台 fixtures 还覆盖 Runner 规格卡片、完整标签、模板独立加载和重试、相同模板请求复用、刷新、窄屏换行、编辑及删除确认。这些 fixtures 不证明真实 Sandbox 服务行为。

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

### 终端计时断流回归

```bash
task ui-terminal-fault-test
```

需要 Linux 或 macOS、Go、Bun，以及包含标准库 `pty` 模块的 `python3`。
Task 会安装匹配的 Chromium 并重新构建内嵌生产 UI。直接运行时，先执行
`task ui-deps` 并进入 `ui/`，再运行 `bunx playwright install chromium` 和
`bun run test:terminal-fault`。Linux 缺少浏览器系统库时使用
`bunx playwright install --with-deps chromium`。无需 runnerd 配置、GitHub 登录、
Sandbox 凭据或生产地址。首次 Go／浏览器安装完成后约需两分钟；测试串行运行，
不自动重试。

独立 Playwright suite 会通过 `terminalfault` build tag 和 race detector 启动
仅监听 loopback 的 Go 测试服务。它提供构建后的 UI，使用真实终端 SSE、回放与
空闲清理代码，并通过 Python 在本地真实 PTY 中启动交互式 `/bin/sh`。
认证、部署元数据、Provider 创建和 PTY resize 使用 fixture。loopback HTTP
代理会断开现有连接，并按计时拒绝新的浏览器请求；使用浏览器原生 EventSource
与真实时钟：

| 中断时长 | 预期行为 |
| --- | --- |
| 5 秒 | 20 秒恢复窗口内恢复，session/PID 和 `Last-Event-ID` 保持一致；60 条编号输出按顺序且仅到达一次，恢复后输入正常。 |
| 15 秒 | 在接近恢复边界时验证相同断言，不创建替代 Shell，也不提前清理。 |
| 25 秒 | UI 约 20 秒关闭，并在断网期间尝试 DELETE。因 DELETE 无法到达 runnerd，网络恢复时 Shell 仍存活，随后由真实的 30 秒空闲清理回收；不自动创建替代 Shell 或重连。 |

恢复窗口从首个 EventSource error 开始计算，而非代理中断瞬间。断言保留有界
调度余量和实际时长，不加速浏览器时钟。即使计时器提前触发，代理也会重新核对
单调时钟截止时间后才恢复流量。5 秒场景会在早期游标观察后等待额外一帧，
并以首个 error 之前最后一个数据／心跳事件的游标核对回放，避免在途输出导致
断言使用过期游标。测试结束后回收 fixture 和子进程。
该命令独立于 `task test` 与 `task ui-production-smoke`，Check 中由
`terminal-fault` job 运行；`RUNNERD_UI_SMOKE_BASE_URL` 不会将它切到线上。

查看 `ui/test-results/` 中的 `terminal-harness-*.log`、每个测试的 `timings.json`、
原生 `events.json` 和服务端 `metrics.json`、恢复／超时截图，以及失败时保留的 Playwright trace。
CI 无论成功或失败都会上传这些产物。失败时先核对时长、游标、序号和进程退出
证据，再决定如何处理，不要直接放宽断言或增加重试。该测试不能证明生产代理、
GitHub 授权、远程 Sandbox、TTL 或权限撤销验收通过，仍须执行
[部署 smoke 门槛](deployment-smoke.md#9-sandbox-deploy-验收)。

确认已配置的公共规格（迁移保留旧行，启动不创建规格）：

```bash
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_specs |
  jq '.[] | select(.template_source == "public") |
      {name, required_labels, default_template_name, enabled}'
```

已有标准目录迁移后可包含 `qiniu-ubuntu-slim`、`qiniu-ubuntu-22.04`、`qiniu-ubuntu-24.04`、`qiniu-ubuntu-26.04` 和 `qiniu-ubuntu-latest`；后台修改或删除会改变列表。
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
curl -fsS -b "$COOKIE_JAR" http://127.0.0.1:25500/runner_request_metrics | jq
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
  具有非零有效默认 BuildID 的模板；覆盖最新重建失败但默认构建仍可用的情况。
- sandbox 创建失败：确认账户/组织 Preferences 或已启用的 admin default 具有与 template 和本地环境匹配的完整 Sandbox service 配置；Runner detail 会显示实际选择的来源。
- `template-smoke` 报告运行时根磁盘容量不足：检查新建 Sandbox 的根磁盘容量是否
  达到所选 TOML 中的 `disk_size_mb`。确认 provider 团队的 `DiskMb` 后，按
  [同名原地重建步骤](default-runner-templates.md)处理并重新执行 smoke。
- 模板 conformance 或 release smoke 的 `stdout.raw`、临时目录提前消失：确认只由主 shell
  执行清理。Bash watchdog 在启动时被终止，可能调用继承的 EXIT trap；子 shell 不应删除
  主 shell 的文件或关闭其 Sandbox。运行 `go test ./internal/sandboxrunner
  -run 'TestSandboxConformance|TestTemplateReleaseSmoke' -count=1`，验证清理、超时及发布证据。
- registration token 失败：检查 [GitHub App 权限表](../../README.zh.md#所需权限)。未配置 `runner_group` 的 spec 需要 repository `Administration`；配置了 `runner_group` 的 spec 需要 organization `Self-hosted runners`。组查询也需要该组织权限。`runner_group_not_supported` 表示作用域不是组织。`runner_group_not_found` 表示组不存在或属于企业继承组；`runner_group_repository_denied` 表示目标仓库无权使用该组。`runner_group_permission_denied`、`runner_group_rate_limited`、`runner_group_timeout` 和 `runner_group_unavailable` 分别表示查询权限、频率、超时和不可用错误，不包含上游正文。表单、YAML 和校验行为见 [GitHub Runner 组](platform-runner-specs.md#github-runner-组)。

### 等待中的 Workflow Job

queued webhook 是状态快照，启动前 REST 查询仍可能返回 `waiting`。
runnerd 会保留本地 `queued`，释放创建容量和 lease，按照
`worker.retry_base_delay_seconds` 与 `worker.retry_max_delay_seconds`
进行有上限的指数退避。已识别的 `waiting`、`pending`、`requested` 和
`in_progress` 持续复查，直到 GitHub 返回 queued 或 completed；审批和
并发等待的超时由 GitHub 控制。`in_progress` 会在服务日志中记录接单的
Runner 名称，不重复创建 Runner。后续 queued 投递只可清除已知等待请求的
下次检查时间，不重开终态或活动执行。重启保留等待记录及延迟。
未知状态或查询失败使用 `worker.retry_max_attempts` 的有限重试次数，
耗尽后在 `github_job_status` 阶段明确失败。HTTP 429 和 GitHub secondary rate limit
则持续延迟排队：重试计数达到上限后保持不变，继续按有上限的退避复查，
释放 lease 和创建容量，脱敏原因码为 `github_job_status_rate_limited`。
限流解除后，无需新 webhook 即可通过轮询恢复启动。状态检查的终态失败在
失败状态成功持久化后计入既有 Runner 操作和 Workflow Job 失败计数；
等待退避不计为终态失败。等待产生的指标变化合并到既有五秒协调周期，
避免每次轮询都扫描全部活动请求；指标读取失败保留待刷新标记，
终态失败仍立即刷新。GitHub Job 已完成但本地终态读写失败时，
错误进入正常启动失败处理，释放 lease 和创建容量；临时存储错误可重试，
不会启动 Runner。控制事件记录决策、原因和
下次复查时间；服务日志关联 webhook action 与 Delivery ID，不持久化原始 payload。

对于 Issue #134 的历史误标记录，管理员可使用现有管理 API 会话，显式调用
`POST /runner_requests/<request_id>/retry` 定向恢复。这是 API 恢复入口，
并未给所有 completed 记录添加重试按钮。接口要求控制日志以旧版精确的
waiting 跳过信息结束，不存在 Sandbox、进程、运行、清理或真实终态结果证据，
GitHub 当前仍为 `queued` 且未分配 Runner，仓库准入及当前 Runner Spec 均有效。
并发状态变化、控制证据缺失或过期均返回 409；不要直接修改数据库或批量重置终态。
恢复成功返回 202，随后经过正常的启动检查、容量、凭证和注册流程。
真实完成记录保持保护，启动时不会自动改写历史终态。

回归验证：

```bash
go test ./internal/server -run 'Test(WorkflowJobWaiting|WorkflowJobStartup|QueuedDeliveryDoesNot|HistoricalWaiting)' -race -count=1
```

全新 checkout 先运行 `task ui-build` 生成嵌入资源。上述测试使用本地
GitHub/Sandbox fixture；生产验收仍需真实带 environment 的 workflow 接单与清理。

## 9. GitHub Actions 日志怎么看

在 Jobs 面板中，点击任务名称打开 GitHub Job，点击工作流名称打开对应的 Run。独立 Job 详情页提供 **在 GitHub 查看 Job**；仅已知 Run 时，使用 **在 GitHub 查看 Workflow Run**。默认的 **Runner 日志** 标签与后台请求详情共用事件时间线，显示 Runner 生命周期和进程 stdout/stderr，并保留时间戳与阶段。首次加载最近 200 条混合事件，筛选仅作用于已加载记录。普通用户的 `GET /user/runner_requests/{id}/events` 接口校验精确安装/仓库权限，接受互斥的排他游标 `before_id` 或 `after_id`，返回 `{events, has_more}`。更早分页与活动期间每五秒增量刷新合并记录，不丢弃已加载历史；支持手动刷新和主动滚动到底部。工作流步骤输出仍在 GitHub 查看。Jobs 面板和独立 Job 详情回退页均使用这种跳转，不再请求 GitHub 日志归档。已退役的 `GET /user/runner_requests/{id}/github-log` 接口返回 `404`。

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

`/runner_requests/{id}/diagnostics` 仅供管理员按需调用，并使用规范的内部 Request ID。它会汇总请求状态、最新 200 条 `control_log` 生命周期事件和 GitHub Job 证据。Sandbox 就绪后、Runner 启动前，runnerd 会记录所选 Sandbox 区域和解析后的物理模板 ID，再执行一个尽力而为、最多 5 秒的探测，从 `IMAGE_VERSION` 或 `ImageVersion` 取得镜像版本，并通过 `/opt/actions-runner/bin/Runner.Listener --version` 取得预装 GitHub Actions Runner 版本。探测只解码这两个白名单值，每项最多 256 字节，绝不持久化或记录完整环境、代理值、token、key 或 cache credential。Runner 注册会保留 GitHub 官方自更新能力；Job 开始前，固定的 runnerd hook 会验证工作目录中 Runner 副本的版本，并通过受权限保护的一次性 FIFO，把一个有界标记和 `RUNNERD_JOB_STARTED` 发送给外层启动进程；官方 hook stdout 只进入 GitHub Job 日志，Sandbox 命令回调不会收到它。Sandbox 服务只接受外层进程转发的首个合法 Job 前标记，将值冻结在内存中，忽略之后所有 Workflow 输出中的版本证据，并且只通过匹配 Sandbox/PID attempt 的退出回调写入 `effective_runner_version`。hook 会在 Workflow step 开始前删除 FIFO，外层进程也会在清理时删除或停止未使用的通道。即使 provider 清理导致没有命令结果，冻结值仍可保留；如果 runnerd 在捕获后重启，因为恢复流程不会信任 Job 开始后的 Workflow 输出，这个尽力而为字段可能保持为空。探测或捕获失败不会阻塞 Runner 启动、完成或清理；已知区域和模板标识仍会保存，无法取得的值保持为空。重新重试或因 Job 错配重新排队时会清除上一次环境快照。区域物理模板 ID 只保存在 Runner Request，不会写回公共名称绑定的 Runner Spec。现有 SQLite 表只会增量添加可空列，因此历史记录继续有效。

私有 ID 规格会经过强制注册前更新门禁。runnerd 在申请注册令牌前，从 GitHub 的仓库级或 Organization 级接口取得官方应用描述，校验 GitHub-owned URL、文件名、架构、版本与 SHA-256，且不会把 API 凭证传入 Sandbox。过期的可写 Runner 需要 `curl`、`tar`、`sha256sum`、`mktemp` 和 `timeout`；包含全部重试在内的下载过程采用五分钟硬截止和 512 MiB 上限，并在 `config.sh` 前完成归档及候选版本校验和工作目录替换。候选目录和旧目录备份会放在同一个随机同级更新根目录中，直至切换完成；旧目录移走后若收到可捕获的 HUP、INT 或 TERM，会恢复旧目录；如果恢复本身失败，则保留并报告备份路径。已安装版本与目标一致或更新时，会跳过这些工具和网络要求，且绝不会降级。查询或 Sandbox 预检失败都会阻止注册；服务端查询失败阶段记为 `github_runner_downloads`，归档版本不匹配时会同时报告实际版本和目标版本。由于模板不可变，在用户重建模板前，每个新 Sandbox 都会按预期重复下载。公共名称规格不走该门禁，继续依赖已测试的固定版本。对刻意使用旧版自定义模板的生产验收，应确认完成 Job 的详情页显示 `runner_version != effective_runner_version`，证明当前任务由刷新后的 Runner 执行。

`POST /runner_requests/{id}/network-diagnostics` 是独立的管理员专用操作，只能用于仍为 `running` 且已持久化 Sandbox ID 和 Runner PID 的请求。JSON body 的 `target` 仅接受 `github_api`、`ubuntu_archive` 或 `llvm_apt`；服务端不接受浏览器传入的 URL、host、command、header 或 credential，也不跟随重定向。固定 Sandbox 命令最多记录 8 个 DNS 地址、实际连接 IP、DNS/TCP/TLS/首字节/总耗时、HTTP 状态、退出码，以及最多 512 字节的稳定探测错误类别；原始命令 stderr 和 provider 细节会被丢弃。完整操作上限为 10 秒，连接上限 3 秒、传输上限 8 秒，range 和最大下载量均限制为 65,536 字节。命令 stdout/stderr 合计输出另行限制为 65,536 字节，超过预算会取消命令。同一个 Request ID/Sandbox ID/PID attempt 同时只能运行一个探测，完成后有 30 秒冷却时间。handler 在 provider I/O 期间不持有 Runner 生命周期锁，provider 调用后会重新加载请求；状态或 attempt identity 变化时丢弃结果。安全的结构化结果或通用 provider 失败结果会写入 `control.log` 的 `network_diagnostic` stage；绝不改变请求状态、重试字段、终止归属、清理流程或 GitHub workflow timeout。历史及终态请求仍可查看，但不能执行实时探测。

当完成 webhook、启动前检查、恢复、reconciler 或 Runner 退出后的有界补偿查询观察到原始 Workflow Job 的终态时，新请求会保留 Job 名称、状态、结论、Runner 名称和本地采集时间。退出后的查询只会在 Sandbox 清理完成或已进入清理重试状态后执行，因此 GitHub 可用性不会延迟该清理。Job ID 为零、与原始 Job 不一致或仍为非终态的观察不会写入，因此分配到的其他 Job 不能覆盖原始结果，活动请求也会继续保持实时行为。重复观察到相同终态时保留首次采集时间；错配完成请求重新排队时，则会随生命周期及终止字段一起清除旧快照。Diagnostics 使用 `lookup_status` 和 `source: retained` 返回快照；在本次 schema 增量之前创建且没有快照的历史记录继续使用有界实时查询，并标记 `source: live`。成功的实时查询会缓存 30 秒，同一 Job 的并发查询会被合并。事件会先过滤再应用数量上限，避免高频 stdout/stderr 输出挤掉生命周期证据。新的生命周期写入会把首次取得终止流程所有权的来源保存为 `process_exit`、`workflow_job_webhook`、`manual_stop`、`recovery_cleanup` 或 `failure_cleanup`；观察到进程结果时还会保存可空退出码。只有结构化状态、稳定的 `runner_exit`／`runner_hook`／`runner_cleanup` control stage 和历史消息回退都没有提供证据时，才返回“Runner 终止过程未被 runnerd 观察到”。因此，GitHub 成功且已记录完整清理的请求不会被误判为异常终止未观察。GitHub 查询失败不会丢弃本地证据，接口也不会返回已保存的 Sandbox 凭证或原始 webhook payload。集合页通过 `GET /runner_requests_lookup/{identifier}` 执行精确查找；当 Runner Name 与内部 Request ID 命名空间出现同值时，优先匹配 Runner Name，再跳转到规范的 ID 路由。`/diagnostics/runner-requests/{identifier}` 继续作为兼容别名，并采用相同的查找优先级。Runner Hook、退出和清理控制事件使用稳定的 `runner_hook`、`runner_exit`、`sandbox_cleanup`、`github_cleanup` 与 `runner_cleanup` 阶段，同时保持旧 `control.log` 读取兼容。当前 Sandbox command result 不提供实际退出信号，因此 runnerd 不会根据退出码或已请求的停止信号进行猜测。

Runner 请求页面支持用用户可见的 Runner Name 或内部 Request ID 精确查找，先解析该标识，再跳转到规范的 `/admin/runner_requests/{id}` 资源页面。表格会让每个请求字段保持单行展示；当视口窄于完整数据宽度时允许横向滚动，纵向滚动仍由页面承载而不是嵌套在表格中。请求资源页面聚合请求状态、诊断结论、带明确来源／新鲜度的 GitHub Job 终态快照或历史实时回退，以及一条按时间排序的“运行记录”。页面展示持久化的 Started、Stopping、Completed 与 Failed 时间，Sandbox 区域、解析后的模板 ID、模板版本、预装 Runner 版本与实际运行 Runner 版本，经过本地化的终止来源及其稳定标识，并且只在真实观察到进程结果时展示退出码。历史记录或部分采集的环境字段显示为 `-`。请求存在存活的 running attempt 时，同一页面会渐进展示固定目标网络诊断控件，并直接呈现解析地址、实际连接 IP、分阶段耗时、HTTP 状态、退出码和稳定错误类别；不存在自由输入目标，成功或失败后刷新现有时间线，不再建立第二套诊断记录页面或导出格式。Cleanup Duration 只使用 `stopping_at` 到当前状态所选择终态时间之间的区间计算；未创建 Sandbox 就完成的请求会保持 `stopping_at` 为空。缺失、无效、倒置或 Go 零时间会显示为不可用，绝不把 `updated_at` 当作清理终点。每条持久化的 control/stdout/stderr 事件都保留为独立时间线行，消息和可选阶段标识直接进入页面正常流，不再使用嵌套输出卡片或折叠交互。页面先加载最新 200 条混合事件，再通过 `GET /runner_requests/{id}/events?before_id=<event-id>` 使用独占游标读取更早记录。活动请求每 5 秒通过一页或多页独占 `after_id` 请求追平新增事件，同时保持历史游标独立，因此不会遗漏事件或丢弃管理员已经加载的历史。独立的 `/admin/diagnostics` 页面只负责 runnerd 运行时检查，展示脱敏摘要和 pprof discovery，只有管理员主动加载或刷新时才请求并渲染可能体积较大的 expvar 快照。旧的 `/admin/diagnostics?runner=...` 链接会先解析标识，再重定向到规范的请求资源。

空闲 Runner sweeper 使用额外的 `idle_cleanup` 终止来源，避免把从未接受 Job 的 Runner 成功清理误标成失败清理。
Runner 进程在接受任何 Job 之前结束时（例如 GitHub 终止了 listener，或 Sandbox 输出流中断且 GitHub 显示 Runner 空闲），只要 Sandbox 和 GitHub Runner 注册都清理成功，请求就不记为完成或失败，而是按有限次数安排 `runner_exited_without_job` 重试；下次启动会先复查原 Job 再创建 Sandbox，重试耗尽后明确失败。

每个已接受的进程退出 watcher 都绑定到本次执行的 Sandbox ID 和 PID。若匹配的进程结果在 Webhook、管理员操作或恢复路径已经完成清理或安排清理重试后到达，runnerd 只补写此前缺失的退出码和 `runner_exit` 事件，不改变首次终止来源，也不重复清理。旧尝试的延迟回调只要有任一身份值不再匹配就会被忽略。

Release C 在 matcher 切换完成后移除了临时 catalog migration readiness API 与界面。已退役的 Runner Group 和 Policy API 返回 `404`，当前 state、server 和 UI 行为都不依赖这些已移除模型。

### Python/pipx 模板回归

修改 Python/pipx 安装逻辑后，运行 `task template-python-test`。该检查需要
Docker 和网络，在固定的 Ubuntu 26.04 根文件系统中执行固定的上游安装器，验证
apt 管理的 `packaging` 保持不变、pipx 工具目录兼容、普通用户可执行 pipx，
以及安装失败仍会阻止构建。该检查不能替代区域 Sandbox 构建与 smoke 门禁。

## 11. 官方参考

- GitHub self-hosted runner workflow labels: https://docs.github.com/en/actions/hosting-your-own-runners/managing-self-hosted-runners/using-self-hosted-runners-in-a-workflow
- GitHub self-hosted runner autoscaling: https://docs.github.com/en/actions/hosting-your-own-runners/autoscaling-with-self-hosted-runners
- GitHub webhook `workflow_job` event: https://docs.github.com/en/webhooks/webhook-events-and-payloads

## Sandbox Deploy 验证

部署详情将 ID 放在仓库或 PR 上下文路径中，保留可读的 Branch／Tag 与 revision 查询参数，拒绝旧 query-ID／来源链接。验证这些路径的未登录返回地址、直接刷新、版本选择及浏览器前进／后退。

先运行 `go test ./internal/state -count=1`，再运行
`go test ./internal/deployment/... ./internal/server -count=1` 与 `task test`。
状态测试覆盖旧 SQLite 部署目标列升级、乐观锁生命周期隔离、保留期限和有界事件。
CI 的独立 PostgreSQL/MySQL gate 包含 `TestDeploymentScopeQuotaSQLBackends`；
它验证两个账号在同一组织下并发准入时的共享配额、事务此前已读取后的可见性，以及
拒绝写入时部署与审计记录保持不变。
`TestDeploymentRevisionsSQLBackends` 验证不可变版本历史；
`TestDeploymentQuerySQLBackends` 验证历史来源列表的查询次数保持固定，以及事务内 UTF-8 字节计数和并发追加。
回滚再升级回归模拟旧版追加和删除日志、重复迁移，确认历史与时间戳保留，并在 SQLite、
PostgreSQL 和 MySQL 上验证启动后每个版本只在首次写入时重算一次。
`TestStartupRecoveryDefersQueuedDeploymentToIndependentWorker` 确认启动恢复不执行排队计划，
且取消启动 context 不影响随后启动的独立 worker；原有 Runner 恢复与 HTTP 门禁测试仍需保留。

恢复测试区分健康 running attempt、临时 Provider 故障、进程身份不匹配、中断创建、
创建结果未知和确认资源不存在。Provider HTTP fixture 验证只查找元数据而不创建资源、
Sandbox 不存在判定和重连时的剩余生命周期。这些检查不能证明公网访问、真实 Contents
权限或远端资源释放；真实验收见 [Deployment Smoke](deployment-smoke.md)。
Deploy 复用 scope／继承／平台服务资格规则，不需要额外开启开关。
`TestDeploymentSandboxServiceMatchesJobEligibility` 覆盖平台启用状态、配置完整性、
selected audience 的身份／类型、scope 与继承优先级、损坏配置拒绝及无快照旧记录。
`TestDeploymentPlatformServiceTemplateAccess` 覆盖 owner／普通成员的公共模板发现、
创建和重新部署，并确认不读取平台私有目录；
`TestRepositoryMemberCanDeployWithoutPrivateCatalogAccess` 保留 scope 私有绑定重放回归。
`TestDeploymentRuntimePreservesSavedService` 验证平台未启用时仍能用保存的凭据重连，并拒绝损坏快照。
`TestLegacyDeploymentRejectsPlatformPrivateTemplate` 验证无快照的旧排队记录在模板验证／创建前拒绝私有或未构建模板。
`TestPersonalRepositoryCollaboratorDeploymentAudience` 覆盖个人仓库协作者的模板发现、创建、
重新部署和无快照解析：选择 owner 后允许平台使用，仅选择协作者或账号类型错误时拒绝，
拒绝不创建新版本／审计事件；协作者自身 scope 凭据仍优先。平台结果与 Jobs 对照。
`TestPlatformDeploymentRetryDoesNotRequireOrganizationOwnership` 验证组织 membership
接口拒绝／不可用不阻止平台公共模板重试；仍保留精确仓库授权、audience 检查和私有模板拒绝，
拒绝重试时不改变版本／审计状态。
仅验证执行的真实 worker 探针直接选择操作者凭据；仓库 owner audience 准入由真实 API gate 验证。
这些本地夹具不代表真实 Provider 部署验收通过。

worker 测试还覆盖仅启动时注入 Vite 精确 Host、保留其他环境变量，以及无法解析应用地址时
拒绝启动。使用公开的 `qiniu/deployment` Vite fixture 和公共 `base` 模板验证真实 worker：

```bash
RUNNERD_DEPLOY_LIVE=1 \
RUNNERD_DEPLOY_LIVE_CONFIG="$PWD/runnerd.local.yaml" \
RUNNERD_DEPLOY_LIVE_ACCOUNT_ID="<已配置的账号 ID>" \
RUNNERD_DEPLOY_LIVE_TEMPLATE=base \
go test ./internal/server -run '^TestDeploymentLivePublicWorker$' -count=1 -v -timeout 20m
```

该测试需主动启用，会读取指定账号的 scope Sandbox 配置，在隔离的临时部署数据库中执行，
并创建真实计费 Sandbox。它验证公网 HTML、相同 attempt 的控制器重连、手动停止、加速到期
和 Provider 确认资源释放；不代表 GitHub 准入授权或完整 runnerd 进程重启验收。
可将 `RUNNERD_DEPLOY_LIVE_EVIDENCE` 设置为未跟踪文件的绝对路径，保存不含凭据的结果。
测试未内置本地账号或物理模板 ID，未显式启用时会跳过。

`create-timeout` 子测试转发一次真实创建，丢弃成功响应，并让首次元数据查询超时。
它要求记录保持未确认的 `creating` attempt，随后恢复传输并使用新的 worker 找回精确资源，
不重复创建，补偿清理后保留原始失败原因，并确认 Provider 404。使用相同环境变量，
将筛选改为 `-run '^TestDeploymentLivePublicWorker$/^create-timeout$'` 可单独执行此项。
故障在本地传输层注入，Provider 操作仍为真实调用；不覆盖重复的已登录 API 请求。
worker 回归测试还验证失败清理过程中断后，部署及其版本仍保留失败状态和原因。

`start-failure` worker 子测试使用同一固定公开来源，在本地注入 `exit 23` 启动计划。
它必须在 starting／probing 阶段失败，将失败证据保留在版本中，只创建一个真实 Sandbox，
并在清理后确认 Provider 404。这是执行／清理证据，不代表 GitHub 配置准入已经验收。

对已启动的**本地** runnerd 验证真实登录态 API 准入：

```bash
RUNNERD_DEPLOY_LIVE_API=1 \
RUNNERD_DEPLOY_LIVE_BASE_URL=http://127.0.0.1:25500 \
RUNNERD_DEPLOY_LIVE_CONFIG="$PWD/runnerd.local.yaml" \
RUNNERD_DEPLOY_LIVE_ACCOUNT_ID="<configured-account-id>" \
RUNNERD_DEPLOY_LIVE_EVIDENCE=/tmp/deployment-api-evidence.json \
go test ./internal/server -run '^TestDeploymentLivePublicAPI$' -count=1 -v -timeout 12m
```

默认测试使用 `qiniu/deployment` 的 `example/vite` 分支。需要运行中服务的配置、
已关联目标仓库 owner 的 Installation、所选作用域没有其他活动部署、
可运行的公共 `base` 模板，以及 1–3 的作用域并发上限。测试为指定的已有账号在内存中签发
短期 session，仅发送到 loopback 地址，不验证 OAuth 登录。真实 GitHub 授权、API 准入、
worker 与 Sandbox 调用均在已有服务进程中执行。同键并发请求只能生成一个资源／attempt；
配额拒绝不留记录，DELETE 释放 Sandbox 并保留历史，终态重放保持幂等，容量释放后准入恢复。
最多创建“作用域上限＋1”个新部署，只停止本次提交，保留终态记录供检查。不证明第二个账号的
权限；默认来源也不证明私有仓库 Contents 权限或 fork PR 执行。不要发布 cookie 或配置。

使用指定的公开 fork／PR fixture 时，可设置：

- `RUNNERD_DEPLOY_LIVE_REPOSITORY`：默认 `qiniu/deployment`。
- `RUNNERD_DEPLOY_LIVE_SOURCE`：`branch`、`tag` 或 `pull_request`，默认 `branch`。
- `RUNNERD_DEPLOY_LIVE_REF`：分支／Tag 名称或正数 PR 编号，默认 `example/vite`。
- `RUNNERD_DEPLOY_LIVE_COMMIT_SHA`：可选，固定部署的完整 SHA。
- `RUNNERD_DEPLOY_LIVE_SOURCE_REPOSITORY`：可选，要求 PR head 的来源仓库匹配。
- `RUNNERD_DEPLOY_LIVE_HTML_MARKER`：可选，要求公网 HTML 包含该标记。

配额验收使用所选个人账号或 Organization 作用域。验收 fork PR 的历史 Commit 时，
先推进 fixture 分支，再固定旧 SHA 和页面标记，核对保存的 fork 身份和实际旧版内容。
该公开验收不会撤销 GitHub 权限，也不能证明私有仓库 Contents 权限。

如需同时验收 CI 生命周期独立性，在上述 API 验收环境中增加
`RUNNERD_DEPLOY_LIVE_CI_RUN_ID=<单 Job 的 Run ID>` 与
`RUNNERD_DEPLOY_LIVE_CI_RUN_ATTEMPT=<attempt>`。CI 必须来自 `qiniu/deployment`，
并与部署使用相同 Commit。测试等待精确的 attempt，实际观察两个 Sandbox 同时运行，
要求保留 CI 成功结果并由 Provider 确认 Runner Sandbox 不存在，然后立即及 20 秒后
检查部署 attempt 不变、公网仍返回 200。错过并行阶段会失败；测试不触发／重跑 CI，
也不修改 Runner request。准备真实工作流，等待测试输出 `waiting for concurrent CI`，
再单独触发选定的 attempt。为减少启动延迟，可先执行
`go test -c -o /tmp/deployment-live.test ./internal/server`，然后使用相同环境执行该二进制，
传入 `-test.run '^TestDeploymentLivePublicAPI$' -test.v -test.timeout 12m`。
私有仓库和第二个用户的权限验收保持独立。

SDK 协议 fixture 验证应用启动不发送远端 `Connect-Timeout-Ms` 截止时间，响应头等待与 PID
确认仍受本地超时限制。worker 回归测试覆盖 Provider 查询跨过到期时间，以及旧到期观察
不能停止替代 attempt 的边界。


部署列表、详情及事件测试应证明跨账号仓库共享、计数／分页前的 Installation＋仓库
精确隔离、Jobs 授权缓存复用，以及 OAuth 缺失或被拒绝时失败关闭。覆盖非 owner 创建、
仅公共模板选择、owner 私有目录／设置边界、revision 执行账号持久化与 worker 重新授权，
以及每个用户的 PTY 隔离。部署内容仍从本地读取；授权缓存未命中时可能访问 GitHub。UI 测试应验证 `/deployments` 只加载本地历史与导航，并通过链接进入
`/deployments/new`；创建页只加载表单依赖，两条路由均支持直接刷新。
部署 UI 验证还覆盖分支／Tag 发现、空列表与失败重试、版本分页、延迟来源／scope 响应、
模板选择、创建后进入详情、直接刷新
`/deployments/{id}`，以及 before／after 日志分页和错误时保留历史。执行部署交互的
聚焦 Bun 测试及 `task ui-production-smoke`；其中部署 fixture 验证 production bundle，
已登录的真实 Sandbox 验收仍单独记录。
导航测试还覆盖超过 100 条历史、只有终态部署的仓库、仓库计数／分页前的精确权限隔离、无凭据活动摘要、列表与侧边栏独立分页、选中终态入口保留、切换仓库／账号后的迟到响应隔离、应用内前进／后退及桌面／窄屏独立滚动。fixture 浏览器检查不代表真实 Sandbox 验收。
单选状态筛选验证覆盖完整授权及来源范围的跨页计数、八种生命周期状态、历史来源按当前状态分组、
非法／重复／空的 API 组合成员、各 UI 状态、默认“全部”、互斥选择、重复点击保持选中、空结果、
保留来源的 URL 选择及刷新、前进／后退、键盘焦点及空格切换、窄屏溢出、
分页重置及切换状态后的迟到分页隔离。运行 `go test ./internal/state -run TestDeploymentStatus -count=1`
及 `filters deployment status` production 浏览器夹具。
API 回归还覆盖完整分支／Tag 引用解析、同名来源的幂等冲突、包含来源前缀的长度限制，
以及未授权时禁止读取上游引用。

部署版本验证需覆盖首次 #1、新版与审计原子提交及回滚、旧版状态保留、版本内游标分页、旧版迟到输出拒绝、旧 SQLite 重复升级、Postgres/MySQL 迁移及事务、URL 版本恢复与迟到响应隔离。确认窄屏下历史和日志上下排列，读取无需 Provider 请求。

### 部署终端验证

重连回归还覆盖持续输出超过一分钟时的 10 秒代理读取空闲限制、HTTP/1.1 与 HTTP/2 的读写期限、按字节偏移只补发缺失后缀、
无效或已移出缓冲区的游标、已关闭会话不再重放输出，以及旧空闲定时器的竞态。
运行 `cd ui && bun run test:production --grep 'resumes deployment terminal'`，
使用 Chromium 的真实 EventSource 验证断流后恢复同一个 PTY、权限拒绝、服务端显式关闭、反复空响应、空闲心跳恢复及 20 秒恢复上限。
这些是本地传输夹具，不证明生产入口会一直保持输出连接。生产验收仍需分别验证持续输出及空闲超过一分钟，
确认传输恢复后 PID 不变，再验证停止、到期和权限撤销仍会关闭终端。
定位首次断流时应保留 runnerd 和入口代理日志；仅凭心跳及本地期限测试，无法确认代理是否存在整次请求时长限制。


运行 `go test ./internal/server ./internal/sandboxrunner -run 'TestDeploymentTerminal|TestDeploymentsShareJobs|TestDeploymentReadsAndMutations|TestUser.*Terminal|TestTerminalHub' -count=1`，验证 PTY 输入、缩放、关闭，仓库授权与用户终端归属，revision／attempt 绑定，停止／到期拒绝，以及 Connect 使用剩余有效期。运行 `cd ui && bun run test:production --grep 'separates deployment'`，通过浏览器夹具验证标签切换、手动连接、键盘输入和断开；夹具不证明真实 Sandbox PTY 可用。真实验收时打开运行中的版本，连接后执行 `pwd`、查看应用日志文件、缩放、断开并重连，再停止部署并确认终端不可用。历史版本不得打开当前 Sandbox。

部署上下文验证：执行 `cd ui && bun test src/github-context-routes.test.js src/app-load-policy.test.js src/components/user-deployments-section.test.js` 及部署 production 浏览器夹具，覆盖旧链接跳转、直接刷新／OAuth 返回、分支名编码、不可变版本选择、返回列表与分页前的本地历史筛选。状态测试包括 `TestDeploymentContextListsMatchHistoricalRevisionsBeforePagination`。

PR 来源验证：执行 `go test ./internal/server ./internal/github -run 'TestPullRequestDeployment|TestDeploymentSource' -count=1`，覆盖固定 head／历史提交、fork 身份、base 不匹配、来源删除、head 竞争、幂等重放、权限撤销及最新／历史版本重部署。新增来源快照字段还需执行状态升级与专用 PostgreSQL/MySQL 版本门禁。PR production 夹具验证表单和路由，真实 fork PR 执行仍需单独验收。

生产浏览器 fixture 覆盖仓库在前的 PR Jobs → 历史 Job → 固定提交创建 → 部署详情／刷新。聚焦 UI 测试另覆盖 Branch／Tag 名称编码、旧链接、延迟目录与切换来源时清除 SHA。运行 `task ui-production-smoke`；fixture 不代表真实 fork PR Sandbox 执行已通过。

路由迁移另覆盖可读斜杠查询、字面百分号转义与保留字符、仓库／分支 Jobs 筛选（包括空结果）、仓库级部署／创建入口、旧来源路径、OAuth 返回、刷新、前进后退与历史资源选择。在 `ui/` 运行 `bun test src/url-query.test.js src/github-context-routes.test.js src/components/auth-pages.test.js`，并运行 `task ui-production-smoke`。

部署 URL 检查还覆盖不携带 SHA 的 Branch／Tag／PR 详情、未隐式限定 Commit 的来源历史、显式 Commit 列表筛选、Job 固定提交创建，以及所选不可变版本的 GitHub 链接和完整 SHA 复制。

PR 标题／链接回归：`TestFillDeploymentPullRequestTitlePreservesSnapshotsAndLifecycle`
与 `TestPullRequestDeploymentPinsSourceAndRejectsInvalidContext` 验证按精确 ref
仅回填空标题、保留时间戳、仓库授权，以及详情先读取本地数据、独立补充 GitHub
元数据。部署 UI 测试覆盖延迟返回的旧记录标题、当前与历史标题的区分，以及
Branch／PR／Tag 链接；PR 生产构建夹具验证长标题、焦点和窄屏布局。

部署认证夹具覆盖列表、详情、日志、Web 终端和创建表单返回的 GitHub
`REAUTH_REQUIRED`，以及 runnerd 会话过期后会话检查成功／失败、普通权限拒绝与限流。
验证 OAuth 仅跳转一次并保留完整来源、资源和 revision 返回地址；普通错误不误触发
重新登录，离开部署页面后也不受之前请求的延迟登录错误影响。

验证运行中本地 API 的已有 PR 部署，且不创建或停止 Sandbox，可使用：

```bash
RUNNERD_DEPLOY_LIVE_METADATA=1 \
RUNNERD_DEPLOY_LIVE_BASE_URL=http://127.0.0.1:25500 \
RUNNERD_DEPLOY_LIVE_CONFIG="$PWD/runnerd.local.yaml" \
RUNNERD_DEPLOY_LIVE_ACCOUNT_ID="<configured-account-id>" \
RUNNERD_DEPLOY_LIVE_DETAIL_ID="<existing-pr-deployment-id>" \
RUNNERD_DEPLOY_LIVE_EVIDENCE=/tmp/deployment-metadata-evidence.json \
go test ./internal/server -run '^TestDeploymentLivePullRequestMetadata$' -count=1 -v -timeout 2m
```

测试使用指定本地配置，在内存中签发短期会话，检查匿名拒绝、认证详情、PR 标题接口、
保留版本、标题持久化以及生命周期字段不变。旧记录的空标题可通过正常元数据接口补齐。
证据明确记录本轮是否触发回填，并分别记录初次和后续请求耗时。本测试不验证浏览器
OAuth、布局、私有 Contents 权限、第二账号授权或真实资源释放。
