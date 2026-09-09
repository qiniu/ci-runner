<h1 align="center">Qiniu Sandbox GitHub Runner</h1>

<p align="center">
  <strong>基于 Qiniu Sandbox，按 Job 即时创建、用完即销毁的 GitHub Actions Runner</strong>
</p>

<p align="center">
  <a href="./README.md">English</a> ·
  <a href="https://runner.qiniuinc.com/docs/getting-started/hosted">托管版指南</a> ·
  <a href="#快速开始">快速开始</a> ·
  <a href="https://app-6a6b0d723d3a24e095531129.app.qiniucc.com/">一键部署到七牛云 LAS</a> ·
  <a href="https://runner.qiniuinc.com/docs/getting-started/deploy">部署使用指南</a> ·
  <a href="https://runner.qiniuinc.com/docs">文档</a> ·
  <a href="#许可证">许可证</a> ·
  <a href="#社区与贡献">社区与贡献</a>
</p>

---

Qiniu Sandbox GitHub Runner 为每个 GitHub Actions workflow job 按需创建独立的 [Qiniu Sandbox](https://www.qiniu.com/)，即时注册 [self-hosted runner](https://docs.github.com/en/actions/hosting-your-own-runners/about-self-hosted-runners)，并在 job 结束后自动移除 runner、停止沙箱。团队可以沿用熟悉的 GitHub Actions 工作流，同时让每个 job 都在一次性的隔离环境中运行。

Qiniu CI Runner 控制面是开源方案；Workflow Job 实际运行所依赖的 Qiniu Sandbox，是由七牛提供并运营的云服务。

## 核心能力

- **临时 Runner** — 每个 job 一个沙箱，完成后自动清理
- **GitHub App 鉴权** — 推荐的生产鉴权方式，内置 Web 控制台支持 OAuth 登录
- **多数据库支持** — SQLite（默认）、PostgreSQL 或 MySQL 存储运行时状态
- **并发控制** — 全局 `max_concurrent_runners` 和 per-spec `max_concurrency`，基于队列的背压机制
- **内置 Web UI** — 管理控制台（Runner Request、全局 Runner Spec、账户、平台 Sandbox 兜底、审计、匹配和诊断）；普通用户控制台（Job 分组、日志、仓库就绪状态、平台 Runner 规格目录、作用域 Sandbox 管理和账户／Organization 自定义 Runner 规格）
- **配置混淆** — 敏感值可避免在配置文件中直接暴露明文
- **重试与恢复** — 瞬时故障自动退避重试；服务重启后恢复排队任务和仍在运行的远端 runner

## 工作原理

```
GitHub webhook (workflow_job)
        │
        ▼
   ┌─────────┐     创建沙箱             ┌──────────────────┐
   │ runnerd  │ ──────────────────────►  │  Qiniu Sandbox   │
   │ (服务端) │     注册 runner          │  (临时虚拟机)     │
   │          │ ──────────────────────►  │                  │
   └─────────┘                          │  GitHub Actions  │
        │                               │  self-hosted     │
        │  job 完成 / 超时               │  runner          │
        │◄────────────────────────────── │                  │
        │     停止并清理沙箱             └──────────────────┘
        ▼
   状态数据库 (sqlite / postgres / mysql)
```

1. GitHub 向 runnerd 发送 `workflow_job`（queued）webhook。
2. runnerd 先应用仓库 allowlist，再将 job labels 与已启用的 Runner Spec 匹配。
3. runnerd 创建 Qiniu Sandbox 实例，并在其中注册 self-hosted runner。
4. GitHub Actions 将 job 分派到该 runner，job 在沙箱中执行。
5. job 完成（或超时）后，runnerd 移除 runner 注册并停止沙箱。

## 快速开始

```bash
# 1. 构建
task build

# 2. 从示例创建配置
cp runnerd.yaml.example runnerd.yaml
#    编辑 runnerd.yaml：设置数据库、GitHub App 凭据、沙箱参数

# 3. 初始化首个管理员（一次性命令，执行后直接退出，不会启动服务）
./bin/runnerd --bootstrap-admin github:<github-user-id> --config runnerd.yaml

# 4. 启动 runnerd
./bin/runnerd --config runnerd.yaml
```

5. 打开 `http://<host>:25500/`，使用 GitHub OAuth 登录。公开产品首页提供同域 `/docs` 指南，以及指向 `/jobs` 受保护的 Jobs 控制台入口。用户首次登录访问 `/jobs` 时，会看到介绍 Jobs、Repositories、Settings 和 Sandbox 设置的六步引导；之后可从账户菜单重播。
6. 打开 **Repositories** 查看账户或组织的 **Runner readiness**。有效来源只显示状态，不提供配置控件；缺少 Sandbox 且用户可管理该 scope 时，通过 **Configure Sandbox** 进入精确的账户或组织 **Preferences** 页面并配置 **Sandbox Service** 凭据。Settings 只列出个人账户和用户属于 active member 的组织；outside collaborator 只能看到 readiness 只读提示，不能浏览该组织的 Sandbox 资源目录。管理员可以在 `/admin/sandbox_service` 配置兜底。
7. 在**管理控制台**中确认 5 个内置 Qiniu managed Runner Specs。4 个标准公共模板已通过双区域 release gate。4 个 `-large` 模板是对外可用的 operator 配置默认 Runner Spec，使用 80 GiB 物理模板；其记录通过自定义 spec 路径保存，但已启用供普通 workflow 使用。operator 仍可禁用 managed 或 large 默认 spec，或调整并发与 idle capacity。
8. 配置 GitHub webhook → `POST http://<host>:25500/webhooks/github`。
9. 在 workflow 中配置 `runs-on: [qiniu, ubuntu-24.04]` 使用 managed default，或配置自定义 spec 要求的 labels。

本地开发请使用 `task dev` 配合 `runnerd.local.yaml`。详细的本地环境搭建（包括 GitHub App 创建和 webhook 转发）请参阅 [docs/zh/testing.md](docs/zh/testing.md)。

## 配置

`runnerd` 默认读取 `./runnerd.yaml`，也可通过 `--config` 指定路径。完整注释的参考配置见 [`runnerd.yaml.example`](runnerd.yaml.example)。

| 配置段     | 说明                                                             |
| ---------- | ---------------------------------------------------------------- |
| `server`   | 监听地址、读/写/空闲超时                                         |
| `database` | 后端类型（`sqlite` / `postgres` / `mysql`）和 DSN                |
| `auth`     | Session secret、加密密钥、Session TTL                            |
| `sandbox`  | 沙箱生命周期超时（创建、运行、停止）                             |
| `github`   | Webhook secret、鉴权方式（App / PAT / basic）、OAuth、允许的仓库 |
| `worker`   | Lease、重试和并发设置                                            |

要点：

- 相对路径的 `database.dsn` 和 `github.app.private_key_file` 按配置文件所在目录解析。
- 本地和单节点部署建议使用 SQLite。支持 PostgreSQL 和 MySQL，但多实例共享数据库尚未验证。
- CI 会创建全新的 PostgreSQL 与 MySQL schema，并在已有 catalog 上重复执行迁移，再验证 policy-free matching。本地 opt-in 的真实方言命令见 [docs/zh/testing.md](docs/zh/testing.md)。
- 已有 SQLite `runner_requests` 和 `runner_profiles` 表会在启动时补建缺失的 model columns 和 indexes，不会重建整张表，从而保留历史 runner 字段以及旧 profile rows 和 indexes。创建缺失索引不会重写 rows，但大数据库可能出现短暂的启动 I/O 和锁等待；迁移与查询计划检查见 [docs/zh/testing.md](docs/zh/testing.md)。
- **不支持** GitHub Enterprise Server，请使用 GitHub.com App。
- GitHub 鉴权方式三选一：`github.app`、`github.token` 或 `github.basic_auth`。
- 省略 `github.app.installation_id` 时，runnerd 按仓库动态解析 installation，一个 App 可服务多个账号。

### 配置值混淆

敏感字段支持 `RUNNERD_ENC(v1:...)` 格式，避免配置文件中出现明文：

```bash
read -r -s secret_value
printf '%s' "$secret_value" | ./bin/runnerd --obfuscate-config-value
unset secret_value
```

支持的字段：`database.dsn`、`auth.session_secret`、`auth.encryption_key`、`github.webhook_secret`、`github.token`、`github.basic_auth.password`、`github.oauth.client_secret`。这些值在日志和序列化输出中也会显示为 `******`。

> **注意：** 此功能仅防止直接查看配置时的明文泄漏，解码 key 内置在二进制中，不能抵御主机级别的攻击者。

### Cache 与 S3

用户需要在账户或 GitHub installation 的 Preferences 中配置 Cache S3 Bucket、可选 Prefix、AK 和 SK。默认 Prefix 是 `gh-actions-cache`。S3 region 和 endpoint 由用户选择的 Sandbox service region 决定，并由运维人员在 `runnerd.yaml` 的 `sandbox.regions` 中配置。由于 endpoint 可能只在 Sandbox 内网可访问，runnerd 保存时只校验配置格式；Bucket 的可达性和权限由实际工作流在 Sandbox 中验证。AK/SK 会加密保存在 scoped state，不会返回给浏览器或 Runner。

每次启动沙箱时，runnerd 将 GitHub 仓库解析到对应 installation/account scope，校验 Workflow Run 的可信上下文，并通过 `cache.sts_endpoint`（默认 `https://sts-ov.qiniuapi.com`）签发七牛 IAM 联邦凭证。凭证请求时长为配置的 Sandbox 生命周期加五分钟，用于覆盖任务结束后的缓存保存步骤；目前暂不提供刷新机制。随后在沙箱启动脚本中注入 `AWS_*` / `RUNS_ON_S3_*` 环境变量，使 scoped cache action 可以直接把缓存上传/恢复到用户 bucket，而无需经过 runnerd 转发字节。

缓存对象 Key 按仓库和工作流上下文隔离：

```text
<配置前缀>/<owner>/<repo>/scopes/branch-<sha256(branch)前16字节的十六进制>/...
<配置前缀>/<owner>/<repo>/scopes/pr-<number>/...
```

配合 scoped `qiniu/actions-cache@v5` action，runnerd 会注入有序读取前缀和唯一写入前缀。可信分支依次搜索自身和默认分支 scope，且只能写入自身 scope；PR（包括 Fork PR）按 PR、自身 base branch、默认分支的顺序搜索，并且只能写入自己的 PR scope。这与 GitHub merge-ref cache 隔离语义一致，Fork 无法污染 base branch Cache。`pull_request_target`、`workflow_run`、`issue_comment` 及无法验证的元数据只能只读默认分支 scope。Kodo list 操作通过 `kodo:prefix` Condition 限定到已授权的前缀范围，授权范围外的缓存 Key 名称不可枚举。

#### 运维配置

```yaml
sandbox:
  regions:
    - id: us-south-1
      label: "United States · Dallas 1"
      sandbox_api_url: https://us-south-1-sandbox.qiniuapi.com
      s3_region: us-north-1
      s3_endpoint: https://internal-s3-las-us-north-1-dal.qiniucs.com

cache:
  sts_endpoint: https://sts-ov.qiniuapi.com
```

`sandbox.regions` 定义通过 `GET /sandbox/regions` 暴露给前端的公开 Sandbox 区域目录；S3 映射只保留在服务端，因为 endpoint 可能是内网地址。每个条目必须包含 `id`、`label` 和 `sandbox_api_url`。`s3_region` 与 `s3_endpoint` 是可选字段，但必须同时配置；只有同时配置的区域支持 Cache S3。必须至少配置一个 region。为了更好地隔离缓存，建议每个 GitHub installation 使用独立 Bucket。`cache.sts_endpoint` 是七牛 IAM 联邦凭证端点，用于签发短期凭证。

#### 在 GitHub Actions 工作流中使用缓存

使用 [`qiniu/actions-cache@v5`](https://github.com/qiniu/actions-cache) 替代 `actions/cache`。工作流中无需额外配置，runnerd 会自动注入 S3 凭证、有序读取前缀和唯一写入前缀作为环境变量：

```yaml
steps:
  - uses: qiniu/actions-cache@v5
    with:
      path: |
        ~/.cache/go-build
        ~/go/pkg/mod
      key: ${{ runner.os }}-go-${{ hashFiles('**/go.sum') }}
      restore-keys: |
        ${{ runner.os }}-go-
```

上传/下载并发可通过环境变量调优（以下为默认值）：

```yaml
env:
  UPLOAD_QUEUE_SIZE: "16"    # 并发上传分片数
  UPLOAD_PART_SIZE: "16"     # 分片大小，单位 MiB
  DOWNLOAD_QUEUE_SIZE: "16"  # 并发下载请求数
  DOWNLOAD_PART_SIZE: "16"   # 分片大小，单位 MiB
```

## GitHub App 设置

### 所需权限

| 范围         | 权限                | 访问级别     | 用途                                                             |
| ------------ | ------------------- | ------------ | ---------------------------------------------------------------- |
| Repository   | Actions             | Read-only    | 查询 job/run 状态、列出排队 job、读取日志；接收 webhook 事件所需 |
| Repository   | Administration      | Read & write | 仓库级 runner 注册（spec 未设置 `runner_group` 时）              |
| Repository   | Metadata            | Read-only    | 识别仓库及其所属账户                                             |
| Repository   | Pull requests       | Read-only    | 在 job 分组中显示 PR 标题                                        |
| Organization | Members             | Read-only    | 验证有效组织成员关系，以开放组织 Settings 和 scoped Sandbox 管理 |
| Organization | Self-hosted runners | Read & write | 组织级 runner 注册（spec 设置了 `runner_group` 时）              |

设置 `github.app.slug` 可在用户 UI 中显示"安装 GitHub App"链接。使用 `github.allowed_repositories`（支持 `owner/repo` 或 `owner/*` 模式）限制哪些仓库可以使用此 runnerd 实例。

### OAuth 登录

`github.oauth` 用于启用内置控制台的 GitHub App OAuth 登录：

- 使用 GitHub App 的 **Client ID** 和 **Client Secret**。
- 将 App callback URL 设置为 `http://<host>:<port>/auth/github/callback`。
- 将 `auth.session_secret`（session 签名）和 `auth.encryption_key`（用户 secret 加密）设为不同的随机值。

首次 OAuth 登录会创建 `role: user` 的账户。使用 `--bootstrap-admin <github-user-id>` 将账户提升为管理员。

### Webhook 事件订阅

在 GitHub App 设置页面（**Settings → Developer settings → GitHub Apps → 你的 App → General**）中配置：

1. 将 **Webhook URL** 设置为 `https://<你的runnerd地址>/webhooks/github`。
2. 在 **Subscribe to events** 中勾选：
   - **Workflow jobs**（`workflow_job`）— **必需**，触发 runner 创建。
   - **Workflow runs**（`workflow_run`）— 可选，作为 `workflow_job` 丢失时的补偿信号。
3. 保存更改。

> **⚠️ 常见坑：** 如果没有订阅任何事件，GitHub 不会发送任何 webhook，job 将永远卡在 queued 状态。此配置在 **GitHub App 设置页面**，不是仓库的 webhook 设置。

## Webhook 与 Workflow 配置

1. 确保已按上述 [Webhook 事件订阅](#webhook-事件订阅) 配置好 GitHub App webhook，且 `webhook_secret` 与配置文件中的 `github.webhook_secret` 一致。
2. 使用已验证的 managed label 组合，例如：

```yaml
runs-on: [qiniu, ubuntu-24.04]
```

使用 managed defaults 时必须包含 `qiniu` label。自定义 spec 可以定义自己的
advertised labels 和 required labels。

runnerd 处理 `queued`、`in_progress` 和 `completed` 动作。对于 `workflow_run` 事件，runnerd 会列出该 run 下所有排队 job，并将尚未入队的匹配 job 创建 runner request。

新建 runner request（包括准入拒绝记录）只保存解析后的 GitHub 上下文，不保存 webhook 原文。历史兼容列 `github_payload_json` 及已有值继续保留，用于回填旧记录的元信息；此变更不会清理已有原文、请求历史或日志。

## Runner Spec 与匹配

Runner spec 通过管理 API 和控制台管理，**不在** `runnerd.yaml` 中配置。所有已启用 spec 都可供 `github.allowed_repositories` 放行的仓库按标签匹配。

- **Managed Runner Spec**：runnerd 会协调 `ubuntu-slim`、`ubuntu-22.04`、
  `ubuntu-24.04`、预览版 `ubuntu-26.04` 和 `ubuntu-latest` 这 5 个内置
  specs。catalog labels、required labels、公共模板名称和 priority
  由 runnerd 管理；operator 仍可控制 `enabled`、
  `max_concurrency` 和 `min_idle`。
- **自定义 Runner Spec**：由 operator 管理，保存显式 `template_id`、
  advertised labels、可选 required labels 和 5 个 `-large`
  workflow labels 是对外可用的 operator 配置默认 spec，使用这条路径；它们不属于
  runnerd 内置 managed catalog 或公共 managed-template API。新建或更换模板时，
  使用 `/admin/sandbox_service` 配置的 endpoint 和凭据检查访问权限与可用默认构建，
  即使运行时默认服务已禁用也可检查。未配置凭据时，只能管理内置默认 spec 或修改
  现有 spec 的非模板参数。校验失败不会修改 spec 或审计记录；模板未变更时不访问 Sandbox。
- **GitHub Runner Group**：spec 设置了 `runner_group` 时，runnerd 会在该 GitHub Group 中创建组织级 runner；否则创建仓库级 runner。它不是已退役的内部 Runner Group 模型。

> **⚠️ 个人账号注意：** `runner_group` 需要调用组织级 GitHub API。如果仓库属于个人账号（而非组织），必须将 `runner_group` 留**空**，否则 runner 注册会返回 404 错误。

匹配始终遵守 `required_labels ⊆ job_labels ⊆ labels`。因此，managed Ubuntu
spec 同时要求 `qiniu` 和准确的操作系统 label；`[ubuntu-24.04]` 或 `[qiniu]`
都不能单独匹配。workflow 移除 `qiniu` 后不会选择 managed defaults；operator
也可以在 Admin 中单独禁用某个 managed spec，而不改变 runnerd 协调的 catalog
identity。

内部 Runner Group 和 Repository Policy 已移除。旧管理 API 现在统一返回
`404 Not Found`，旧 Admin 书签会重定向到 Runner Specs。它们不再属于受支持的
配置、匹配或恢复行为；当前代码会忽略任何遗留数据库对象。

Managed spec 保存稳定的公共模板名称。runnerd 会在创建 Runner 前，使用
repository owner 对应的 scoped Sandbox endpoint 解析该名称，因此不同区域可以
返回不同的 template ID。自定义 spec 仍直接使用保存的 `template_id`。当前
catalog revision 中，`ubuntu-latest` 映射到 Ubuntu 24.04；修改映射前必须评审
catalog 更新并取得新的区域 smoke 证据。

支持的 workflow labels、发布状态和区域验证流程见[公共 Runner 模板](docs/zh/default-runner-templates.md)。

`GET /api/public/runner-templates` 无需登录即可返回 runnerd 管理的 4 个标准公共
模板。稳定响应只包含公共模板名称、对应的逻辑 Runner Spec 名称和支持的 workflow
label 组合，不包含 provider template ID、credential、endpoint，也不会暴露 operator
配置的 large 或其他自定义模板。对外 runner-labels 指南仍会列出 large 默认规格及其
资源契约。依赖 credential 的
`GET /user/sandbox/templates?region=<id>` 仍是独立的 scoped resource。

普通用户在 `/runner-specs` 只读浏览平台 Runner 规格及其工作流标签；该页面不提供
个人账号／Organization 选择，也不能修改平台规格的启用状态或并发策略。在
`/account/runner-specs` 或 `/organizations/{login}/runner-specs` 只管理对应作用域自有的
自定义规格。需要登录的 `/user/runner-specs` API 会组合 runnerd 管理的规格、只读的
平台自定义规格，以及当前账户或可管理 Organization 自有的自定义规格。平台规格的
可用性和并发仍由 Admin 全局管理。作用域自定义
规格使用精确规范化后的 workflow labels，可以覆盖相同标签集合的全局规格；新建或
更换 template ID 时，只使用当前作用域显式配置或合法继承的 Sandbox credential
验证。`runner_group` 仅供 Organization 自定义规格使用。响应只会为调用者自己的
作用域自定义规格返回 template ID，绝不会暴露平台自定义规格的 template ID。
排队请求在启动前会重新加载并校验原先持久化的 source 与 scope，因此等待期间被
停用的规格不会启动 Runner。

Admin 创建的自定义 Runner Spec 是平台共享规格，会以只读形式出现在每个可管理的
账户和 Organization 目录中。因此 Admin 创建界面会明确提示平台范围，并且只使用
Admin Sandbox 服务校验模板。只应由单个账户或 Organization 使用的环境应创建为
作用域自定义规格。

对于自定义 spec，`template_id` 应指向包含 GitHub runner 镜像的 Qiniu Sandbox 模板。创建沙箱时会使用 **Repositories → Runner readiness** 中显示的仓库 owner 有效 Sandbox service 检查模板访问权限。

## 管理控制台

内置 Web UI 提供：

| 路由                     | 说明                           |
| ------------------------ | ------------------------------ |
| `/admin/`                | 仪表盘：诊断、指标与最近失败     |
| `/admin/accounts`        | 账户管理：列表、搜索、角色变更 |
| `/admin/runner_requests` | Runner Request 历史、重试／停止操作和持久化日志 |
| `/admin/runner_specs`    | 托管和自定义全局 Runner Spec 管理 |
| `/runner-specs` | 只读的平台 Runner 规格目录与工作流标签 |
| `/account/runner-specs`、`/organizations/{login}/runner-specs` | 管理个人或可管理 Organization 自有的自定义 Runner 规格 |
| `/admin/sandbox_service` | Sandbox 服务配置               |
| `/admin/match`           | 针对当前已启用 Runner Spec 的标签匹配预览 |
| `/admin/audit`           | 审计事件历史 |
| `/admin/diagnostics`     | 脱敏运行时摘要、近期失败、pprof discovery 和 expvar |

`/` 始终是公开的 Qiniu CI Runner 产品首页。`/docs` 及其固定指南路由公开、同域，并提供英文和简体中文。普通用户 Jobs 首页位于 `/jobs`；其他受保护路由包括 `/repositories`、PR job 分组（`/github/pulls/{owner}/{repo}/{number}/jobs`）、账户设置（`/account/preferences`、`/account/sandbox-templates`、`/account/sandbox-instances`），以及对应的 `/organizations/{login}/...` 路由。未登录访问受保护路由时会显示独立的 GitHub 登录页，并在 OAuth 完成后返回原 URL。

Runner request 列表默认返回最新 100 行，单页最多 500 行，并且只读取公开 runner state 所需字段，不加载已保存的 webhook payload 或 Sandbox credentials。Admin 轮询使用 `(queued_at DESC, id ASC)` 索引；经过 repository 授权的普通用户轮询通过 `(github_installation_id, queued_at DESC, id ASC)` 分别查询每个 installation，再合并有界结果，同时保留精确的 installation/repository 授权关系。

## 常见问题排查

| 现象                                                 | 可能原因                                 | 解决方法                                                                           |
| ---------------------------------------------------- | ---------------------------------------- | ---------------------------------------------------------------------------------- |
| Job 一直卡在 **queued**，runnerd 日志无 webhook 记录 | GitHub App 未订阅事件                    | 进入 GitHub App 设置 → 勾选 **Workflow jobs** 事件                                 |
| `github registration token: status 404`              | 设置了 `runner_group` 但仓库属于个人账号 | 清空 runner spec 中的 `runner_group`，改用仓库级注册                               |
| 日志中出现 `invalid signature`                       | Webhook secret 不匹配                    | 确保 `github.webhook_secret` 与 GitHub App/仓库 webhook 设置中的 secret 一致       |
| `runner start deferred ... at capacity`              | 全局或 spec 并发上限已满                 | 等待运行中的 job 完成，或调大 `max_concurrent_runners` / spec 的 `max_concurrency` |
| 沙箱创建失败                                         | 仓库 owner 没有有效的 Sandbox service    | 打开 **Repositories**，选择账户或组织并完成 **Runner readiness**；管理员也可在 `/admin/sandbox_service` 配置适用的兜底 |

更多本地调试步骤请参阅 [docs/zh/testing.md](docs/zh/testing.md)。

## Docker

容器镜像仅使用文件配置。将 `runnerd.yaml` 和引用的密钥文件挂载到容器中：

```bash
docker run --rm -p 25500:25500 \
  -v "$PWD/runnerd.yaml:/etc/runnerd/runnerd.yaml:ro" \
  -v "$PWD/secrets:/etc/runnerd/secrets:ro" \
  ghcr.io/qiniu/ci-runner
```

## 构建与开发

```bash
task deps          # 安装 Go 依赖
task ui-deps       # 安装 UI 依赖
task build         # 构建 runnerd（内嵌生产 UI）
task ui-production-smoke # 在 Chromium 中执行生产 UI bundle
task dev           # 启动本地开发环境（runnerd + Vite + smee）
task lint          # 运行代码检查
task test          # 重建 UI + 运行全部测试（Go race detection + Bun UI tests）
task docker-check  # 验证 Docker 构建
task release-check # 验证发布构建
```

单独运行 UI 测试时使用 `cd ui && bun run test`。修改 UI 依赖、Vite/Rollup 配置或
生产静态资源加载逻辑后，还要运行 `task ui-production-smoke`。

### 沙箱模板

| 模板                                   | 说明                                          |
| -------------------------------------- | --------------------------------------------- |
| `templates/github-runner-ubuntu-slim`  | 维护中的 Ubuntu Slim x64 Runner 模板          |
| `templates/github-runner-ubuntu-22.04` | 维护中的 Ubuntu 22.04 x64 Runner 模板         |
| `templates/github-runner-ubuntu-24.04` | 维护中的 Ubuntu 24.04 x64 Runner 模板         |
| `templates/github-runner-ubuntu-26.04` | 预览版 Ubuntu 26.04 x64 Runner 模板           |
| `templates/github-runner-ubuntu-slim-large` | 使用 80 GiB provider 磁盘的 Ubuntu Slim x64 Runner 模板 |
| `templates/github-runner-ubuntu-22.04-large` | 使用 80 GiB provider 磁盘的 Ubuntu 22.04 x64 Runner 模板 |
| `templates/github-runner-ubuntu-24.04-large` | 使用 80 GiB provider 磁盘的 Ubuntu 24.04 x64 Runner 模板 |
| `templates/github-runner-ubuntu-26.04-large` | 使用 80 GiB provider 磁盘的 Ubuntu 26.04 x64 Runner 模板 |

对外的 `ubuntu-latest-large` Runner Spec 是映射到
`github-runner-ubuntu-24-04-large` 物理模板的逻辑标签，不会新增模板目录或构建目标。

先运行 `task template-check-all`，再通过 8 个
`task template-build-ubuntu-*` targets 执行真实 qshell Sandbox 构建。发布与
远程构建超时后的缓存续跑、发布与 smoke 命令见
[公共 Runner 模板](docs/zh/default-runner-templates.md)。

## 文档

| 文档                                                                                   | 说明                                                    |
| -------------------------------------------------------------------------------------- | ------------------------------------------------------- |
| [站点指南](https://runner.qiniuinc.com/docs)                                           | 托管版快速开始、runnerd 部署、Workflow 示例、自定义模板全流程、故障排查和 managed labels |
| [docs/zh/testing.md](docs/zh/testing.md)                                               | 本地测试、GitHub App/OAuth 设置、webhook 转发、故障排查 |
| [docs/zh/deployment-smoke.md](docs/zh/deployment-smoke.md)                             | 生产环境就绪检查清单                                    |
| [docs/zh/default-runner-templates.md](docs/zh/default-runner-templates.md)             | 公共模板 labels、qshell 发布流程、区域 smoke 和回滚     |
| [docs/zh/runner-architecture-comparison.md](docs/zh/runner-architecture-comparison.md) | 架构图及与 ARC / Fireactions 的对比                     |
| [docs/zh/runner-implementation-review.md](docs/zh/runner-implementation-review.md)     | 实现状态与 schema 迁移说明                              |

## 许可证

Qiniu CI Runner 基于 [Apache License 2.0](LICENSE) 开源。

## 社区与贡献

我们欢迎各种形式的贡献，包括 Bug 报告、功能建议、文档改进和代码提交。

- **发现问题或有新想法？** —— 前往 [GitHub Issues](https://github.com/qiniu/ci-runner/issues) 报告问题或提出建议。
- **想改进代码或文档？** —— 提交 [Pull Request](https://github.com/qiniu/ci-runner/pulls)，与我们一起完善 Qiniu CI Runner。
- **想交流使用经验？** —— 扫描下方二维码，加入交流群。

---

<p align="center">
  <img src="./docs/assets/qrcode.png" width="220" alt="Qiniu CI Runner 交流群二维码" />
</p>
<p align="center">
  <em>扫描上方二维码加入交流群，与维护者和社区用户一起交流使用经验和改进建议。</em>
</p>
