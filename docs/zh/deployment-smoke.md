# 部署 Smoke Checklist

[English](../deployment-smoke.md)

在把 runnerd 部署视为可以承载真实 GitHub Actions 流量前，使用这份 checklist 做生产风格验证。

在线切库默认关闭，生产使用仍需独立验收。按[迁移方案](sqlite-postgres-online-migration.md)的阶段门禁执行；
普通启动、健康检查和重启恢复不能证明数据库迁移无感。

## 前置条件

- 一个可通过 HTTPS 接收 GitHub webhooks 和 console 登录的 runnerd 部署。
- `runnerd.yaml` 已配置 `database`、`auth`、`github` 和 `worker` sections。
- 目标 repository 或 organization 已安装 GitHub.com App。
- GitHub App 已配置当前部署所用 runner 模式需要的[仓库级和组织级权限](../../README.zh.md#所需权限)。
- GitHub App OAuth callback URL 指向 runnerd origin 下的 `/auth/github/callback`。
- GitHub App webhook 或 repository webhook 将 `workflow_job` events 发送到 `POST /webhooks/github`。
- 目标 account/organization Preferences 已配置 Sandbox service API URL 和 API key，或 `/admin/sandbox_service` 已启用 admin fallback。
- 已按[公共 Runner 模板](default-runner-templates.md)完成 4 个标准公共 Qiniu 模板的
  双区域构建、发布、catalog 检查和 smoke 验证。4 个 large 模板也必须通过相同门禁
  （包括运行时根磁盘的 `disk_size_mb` 容量下限）后才能使用其 development specs；适用的
  模板门禁通过前，不要部署默认启用的 managed specs。
- 已通过 `runnerd --bootstrap-admin github:<github-user-id>` 引导 admin account（该命令设置 admin 后直接退出，需在启动服务前执行）。

不要在本文档中写入真实 secret，也不要提交部署本地文件，例如 `runnerd.local.yaml`、`.smee-url`、sqlite databases、private keys 或 cookie jars。

## 1. Service Health

确认服务可访问：

```bash
curl -fsS https://<runnerd-host>/healthz
```

预期结果：HTTP 200，响应包含 `status: ok`。

从 `index.html` 找到当前带哈希的 JavaScript 或 CSS 路径，验证生产 UI 的缓存和压缩响应头：

```bash
curl -sS -D - -o /dev/null https://<runnerd-host>/
curl -sS --compressed -D - -o /dev/null https://<runnerd-host>/assets/<current-hashed-asset>.js
```

预期结果：

- HTML shell 返回 `Cache-Control: no-store`。
- `/assets/` 下带内容哈希的文件返回 `Cache-Control: public, max-age=31536000, immutable`。
- 请求接受 gzip 时，大型 JavaScript 和 CSS 响应返回 `Content-Encoding: gzip`，同时包含 `Vary: Accept-Encoding`。
- 未版本化的静态文件使用短期浏览器缓存，而不是 immutable 策略。

在已安装 UI 依赖的 checkout 中运行生产浏览器 canary：

```bash
RUNNERD_UI_SMOKE_BASE_URL=https://<runnerd-host> task ui-production-smoke
```

预期结果：Chromium 能渲染公开首页 heading，并且没有 page error、console
error、script/stylesheet 加载失败或空的 React root。与前面的 HTTP 检查不同，
该步骤会实际执行已部署的 JavaScript chunks；通过后才能把部署标记为 ready
或承载全部流量。

通过 admin console 登录：

```text
https://<runnerd-host>/admin/
```

预期结果：GitHub OAuth 完成，signed session 具有 `role: admin`。

使用在某个已安装组织中具有 active owner membership（`role: admin`）的用户登录，然后打开：

```text
https://<runnerd-host>/account/preferences
```

预期结果：该组织出现在 Settings scope 列表中，并能打开
`/organizations/<login>/preferences`。这可以验证 GitHub App 的
`Members: Read-only` 已由该 installation 批准。再分别使用普通组织成员和仅有
repository 权限的 outside collaborator 重复验证：已授权仓库仍可出现在
`/repositories`，但组织不得出现在 Settings 中；Preferences 只能返回脱敏后的
readiness，scoped mutation、catalog read 和自定义 Runner Specs 必须被拒绝。

准备至少一个次要 account，并打开 Accounts 页面：

```text
https://<runnerd-host>/admin/accounts
```

检查：

- 搜索、角色筛选、每页条数和翻页只改变账户列表，全局统计总数保持不变。
- 关联 GitHub identity 会按 login 加载头像；头像不可用时回退到账户首字母。
- 当前管理员的角色控件处于禁用状态。
- `role: user` session 调用账户列表和角色修改 API 都会被拒绝；管理员直接 PATCH 自身 role 会返回 conflict。
- 把次要 account 从 `user` 改为 `admin` 后立即生效，并生成 `account.role.update` 审计事件。
- 准备两名管理员和两个已登录 session，并发执行相互降级时不能同时成功；至少保留一名管理员。
- 完成全部角色检查后，如有需要，先由存活的管理员恢复原管理员，再由预期管理员恢复次要 account 的 role。

准备至少一个有效 installation，并打开 GitHub App 账号页面：

```text
https://<runnerd-host>/admin/github_accounts
```

检查：

- 列表与 GitHub 为此 App 展示的有效 installations 一致，包括账号 login、类型、稳定账号 ID 和 installation ID。
- 选择账号后打开 `/admin/github_accounts/<installation-id>`，并展示该 installation 当前授权的仓库。
- 账号改名后刷新页面，原数字 installation-ID 深链仍然有效。
- 未登录和 `role: user` session 调用两条只读 API 都会被拒绝，响应不会暴露 token、permissions 或凭据。

## 2. Diagnostics

打开 admin console 的 diagnostics 页面，或调用：

```bash
curl -fsS -b "$COOKIE_JAR" https://<runnerd-host>/diagnostics/pprof | jq
curl -fsS -b "$COOKIE_JAR" https://<runnerd-host>/diagnostics/vars | jq
test "$(curl -sS -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" https://<runnerd-host>/runner_groups)" = 404
test "$(curl -sS -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" https://<runnerd-host>/runner_policies)" = 404
test "$(curl -sS -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" https://<runnerd-host>/diagnostics/catalog-migration-readiness)" = 404
```

检查：

- 推荐部署路径下 `github.auth_mode` 是 `app`。
- `state.database` 指向预期的 sqlite、Postgres 或 MySQL 数据库。
- 当 local pprof service 可用时，可以看到 pprof discovery files 和 dump scripts。
- 对失败或可疑请求，`/admin/runner_requests/{id}` 会显示能够解释当前结果的诊断结论和按时间排序的事件时间线；GitHub 查询失败时，本地证据仍然可用。
- 已退役的 Runner Group、Policy 及临时 catalog migration readiness API 返回 `404`；`/admin/runner_groups` 与 `/admin/runner_policies` 仍会安全重定向到 Runner Specs。
- 持久化为 `failed` 且 `failure_stage=admission`、`failure_reason=profile_labels_not_matched` 的 Runner 请求显示为**未匹配**，不计入失败指标，可单独筛选，也不显示重试操作；真正的失败请求仍显示为**失败**，并在适用时允许重试。
- 现有 workflow `runs-on` labels 和已启用 Runner Spec 的匹配行为保持不变。Release C 部署不得同时修改 Catalog 或 Sandbox 配置。
- 验证 `/runner-specs`、`/account/runner-specs` 与一个可管理的 `/organizations/{login}/runner-specs` 路由。确认顶层页面是不带作用域选择的只读平台目录，可复制工作流标签且没有启用／并发控制，而 Settings 只显示自有自定义规格；确认 `/user/runner-specs` 拒绝 repository-only 作用域，只为当前作用域自己的 `scoped_custom` 条目返回 `template_id`，并省略平台自定义规格的 template ID。

## 3. Runner Catalog

创建 runner specs 前先验证 Sandbox credential precedence：

- 没有 scoped credentials 的 account 仅在 admin default 已启用且完整时可以列出 templates。
- `all` 模式下，个人 repository owner 与 organization owner 都能使用完整 default。
- `selected` 模式下，stable-ID audience list 中的 owner 可以使用 default；未选择的 owner 和空 audience 都不能使用。
- 添加一个从未登录或同步的 GitHub login，确认 admin response 显示 GitHub 返回的 canonical login、stable ID 和 account type。
- 启用 GitHub App auth 后，确认 selected owner 的第一个 workflow 能解析并缓存原本未知的 installation owner；后续请求不应再次查询 owner。
- 保存 account 或 organization scoped credentials 后，effective source 不再是 `admin_default`。
- 移除 audience entry 会阻止新的 fallback resolution，但不会改变已 snapshot 的 runner request。
- 禁用 admin default 后，原本未配置的 account 会得到 `sandbox service not configured`。

升级后检查数据库目录，确认旧公共名称规格保留标签、容量、发布、Runner 准备方式、Docker 和赞助策略。新安装目录为空，须先由管理员创建规格。详见[平台 Runner 规格管理](platform-runner-specs.md)。

```bash
curl -fsS -b "$COOKIE_JAR" https://<runnerd-host>/runner_specs |
  jq '[.[] | {name, template_source, published, required_labels, default_template_name, enabled}]'
```

已有标准目录的 5 个逻辑名称包括 `qiniu-ubuntu-slim`、`qiniu-ubuntu-22.04`、`qiniu-ubuntu-24.04`、`qiniu-ubuntu-26.04` 和 `qiniu-ubuntu-latest`；这是迁移示例，不是启动时要求的固定数量。每个已配置区域运行 `task template-defaults-check` 并保存 8 个物理模板 ID。公共名称通过该 scope 的 endpoint 解析，不能写回某一区域的 ID。large 规格只有配置经过校验的公共名称并明确发布后才进入公共目录。

运行正向和负向 match tests：

```bash
curl -fsS -X POST https://<runnerd-host>/runner_specs/match \
  -b "$COOKIE_JAR" \
  -H 'content-type: application/json' \
  -d '{"repository_full_name":"<owner>/<repo>","labels":["qiniu","ubuntu-24.04"]}' | jq

curl -fsS -X POST https://<runnerd-host>/runner_specs/match \
  -b "$COOKIE_JAR" \
  -H 'content-type: application/json' \
  -d '{"repository_full_name":"<owner>/<repo>","labels":["ubuntu-24.04"]}' | jq

curl -fsS -X POST https://<runnerd-host>/runner_specs/match \
  -b "$COOKIE_JAR" \
  -H 'content-type: application/json' \
  -d '{"repository_full_name":"<owner>/<repo>","labels":["qiniu"]}' | jq
```

预期结果：只有第 1 个请求选择 `qiniu-ubuntu-24.04`。

先在 `/admin/sandbox_service` 配置后台 Sandbox endpoint 和凭据，再创建带显式
模板 ID 和 operator labels 的自定义回归 spec。保存时应使用后台凭据查询模板
访问权限及可用默认构建；实际运行仍使用仓库 owner 的有效 Sandbox 配置和已保存的
模板 ID。在不影响现有任务的情况下确认：

- 不存在的模板返回 `400 template_not_found`，不修改 spec 和审计记录。
- 没有可用默认构建时返回 `400 template_not_ready`。
- 缺少后台凭据时返回 `409 sandbox_service_not_configured`；不改变模板的非发布操作仍可修改。
- 上游权限错误、服务故障和超时均拒绝保存并给出可操作提示；修正后重试可成功。
- 新构建仍在进行时，已有可用默认构建的模板仍能保存。
- 自定义 spec 仍可编辑和删除，并单独验证真实任务。

成功创建示例：

```bash
curl -fsS -X POST https://<runnerd-host>/runner_specs \
  -b "$COOKIE_JAR" \
  -H 'content-type: application/json' \
  -d '{"name":"deployment-custom","labels":["self-hosted","deployment-custom"],"required_labels":["deployment-custom"],"template_id":"<private-template-id>","max_concurrency":1,"enabled":true}' | jq
```

使用组织组规格前，以具有有效成员身份的组织所有者身份打开组织 Runner Specs，开启组注册，选择或刷新真实组，核对注册目标及复制的 `group` + `labels` YAML。在允许的仓库运行匹配 Workflow，验证接单与清理；另行验证无权使用组的仓库在 `github_runner_group` 阶段失败且未创建 Sandbox，非所有者不能读取 `/user/github-runner-groups`。Workflow 限制由 GitHub 执行。此真实门禁独立于 fixture-backed 浏览器验证。

## 4. Webhook Delivery

在 GitHub App 或目标 repository 的 webhook 设置中重发最近一次 delivery，或触发一个新 workflow。

预期结果：

- Delivery 使用 `application/json`。
- Delivery 包含有效的 `X-Hub-Signature-256`。
- runnerd 对支持的 `workflow_job` actions 返回 2xx JSON response。
- Unsupported events 会被有意 ignored，而不是作为 runner failures。

## 5. Workflow Pickup

为每个 managed 逻辑 label 触发 1 个 job：

```yaml
name: runnerd-smoke

on:
  workflow_dispatch:

jobs:
  slim:
    runs-on: [qiniu, ubuntu-slim]
    steps:
      - run: uname -a
  ubuntu_22:
    runs-on: [qiniu, ubuntu-22.04]
    steps:
      - run: uname -a
  ubuntu_24:
    runs-on: [qiniu, ubuntu-24.04]
    steps:
      - run: uname -a
  ubuntu_26_preview:
    runs-on: [qiniu, ubuntu-26.04]
    steps:
      - run: uname -a
  latest:
    runs-on: [qiniu, ubuntu-latest]
    steps:
      - run: |
          uname -a
          whoami
          pwd
```

手动触发。

预期结果：

- Runner request 依次显示为 `queued`、`creating`、`running`。
- 5 个 GitHub Actions jobs 都离开 queued 状态，并运行在临时 managed runners
  上。两个 24.04 逻辑 labels 都通过 scoped region catalog 解析到 24.04 物理
  模板。
- 每个 job 都启动请求的操作系统；26.04 job 记录为预览验收。
- Job 的 `Set up runner` log 包含 Qiniu sandbox id、runner request id 和 runner name。
- 规范的 Admin Runner Request 详情页展示所选 Sandbox 区域、解析后的物理模板
  ID，以及可取得时的镜像模板版本、预装 GitHub Actions Runner 版本和固定 hook 通过
  一次性通道在 Job 开始前冻结、匹配 Runner 进程退出后持久化的实际运行版本。由于启用了官方自更新，
  实际运行版本可能更新。刷新详情页后这些值保持一致；升级前的历史请求或从未开始 Job 的请求对
  不可用证据显示 `-`，浏览器控制台没有报错。
- 任一请求仍为 `running` 时，在详情页执行 Ubuntu 软件源网络诊断，或调用：

  ```bash
  curl -fsS -b "$COOKIE_JAR" \
    -H 'Content-Type: application/json' \
    -d '{"target":"ubuntu_archive"}' \
    https://<runnerd-host>/runner_requests/<running-request-id>/network-diagnostics | jq
  ```

  确认响应只包含固定 target/host、DNS 与实际连接 IP 证据、分阶段耗时、HTTP
  状态、退出码、稳定错误类别和观察时间，不包含原始 stderr。立即再次调用应返回带 `Retry-After` 的
  `429`；`https://example.invalid` 等 target 应在不调用 provider 的情况下返回
  `400`；非管理员 session 调用相同接口应返回 `401`。运行记录新增一条
  `control · network_diagnostic` 事件，同时请求和 GitHub Job 独立保持运行。若
  attempt 在探测期间结束，API 应返回 `409` 且不附加过期结果。
- Job 结束后，runner request 变为 `completed`。

参考证据：2026-08-04（CST），
[run 30858489153](https://github.com/miclle/qiniu-ci-runner-test/actions/runs/30858489153)
接收了带有效签名的 GitHub `workflow_run` 和 `workflow_job` deliveries，并让
5 个 jobs 全部完成。5 条 requests 均进入 `completed`，Runner processes 均正常
退出，Sandbox 均完成清理，repository 最终没有残留 self-hosted Runners。该参考
run 不能替代对实际部署的 webhook secret 与 GitHub webhook 配置是否一致的验证。

## 6. Restart Recovery

在 workflow 中增加一个持续时间足够长的步骤，并在 job 运行期间重启 runnerd。只重启 runnerd 服务，不要直接停止沙箱。

预期结果：

- 启动恢复期间 `/healthz` 仍可访问，其他 HTTP 路由在恢复完成前返回 `503`。
- 活跃请求通过有界并发恢复，不再串行等待所有更早的请求；每个 worker 开始恢复时，根据剩余启动恢复总预算和剩余 worker 波次数确定单请求超时，父 context 的 deadline 仍是硬上限。
- runnerd 完成启动恢复后才启动 worker loops 并处理新的排队任务。
- `running` request 保持 `running`，sandbox ID 和 runner PID 不变，并记录成功重连事件；可恢复的 `creating` request 可能会发现并补写重启前尚未持久化的 sandbox ID 和 runner PID。
- 恢复流程只能为同一 Sandbox/PID execution attempt 补齐缺失的模板版本或预装
  Runner 版本。它会保留已持久化的实际运行 Runner 版本，但不会在重启后从 Workflow
  输出重建缺失的 Job 前标记证据；空探测结果不能覆盖已有证据，也不能附加来自旧
  attempt 的元数据。
- GitHub Actions job 持续运行，不会重新排队或丢失 runner。
- 由旧进程持有 lease 的 `queued` 请求能够被新 worker 继续处理。
- GitHub 或沙箱状态查询暂时失败时只记录错误，不会停止已有沙箱。

## 7. Cleanup

Workflow 完成后确认：

- Qiniu sandbox 已停止或不再 active。
- GitHub self-hosted runner registration 已移除，或已 offline 并被 runnerd 清理。
- Job 详情默认展示 Runner 日志，没有 GitHub 日志标签。确认 Jobs 与后台请求详情显示相同的混合事件、时间戳、阶段和日志流筛选；检查更早分页、活动期间增量刷新和手动刷新，不丢失历史或强制滚动到底部。普通用户事件接口必须拒绝匿名会话、缺失/被拒绝的 OAuth Token，以及不匹配的安装/仓库组合。Jobs 面板通过任务名称在新标签页打开 GitHub Job，通过工作流名称打开 Run，没有重复按钮。独立详情页的按钮仅在已知仓库和 Run 时回退到 Run，两者都未知时不显示按钮。打开详情不会请求 `/github-log`。
- Runner request 的 control/stdout/stderr logs 可以通过 admin UI 或 `/runner_requests/{id}/logs/{name}` 查看。
- `/diagnostics/vars` 显示更新后的 workflow job、runner registration、cleanup 和 duration counters。

## 8. Failure Drill

部署仍在观察期时，执行受控路由与 operator-control 检查：

- 触发 `[ubuntu-24.04]` 和 `[qiniu]`；两者都必须保持 unmatched。
- 在 Admin 中禁用 `qiniu-ubuntu-24.04`，重启 runnerd，并确认它仍保持禁用；
  重新启用后确认恢复调度。
- 降低某个 managed spec 的 concurrency，并触发两个 jobs。
- 运行 `deployment-custom` spec，确认使用显式 template ID；清理后删除它。任何活动全局规格的执行参数修改或删除应返回 `409 runner_spec_in_use`；空闲的原托管规格可编辑和删除，重启后修改仍保留且删除条目不会重建。
- 发布经过校验的公共名称规格，检查 `/runner-specs` 和公共模板 API；取消发布后两处均隐藏（公共 API 缓存最多 60 秒）。私有 ID 不得出现，无效公共名称应拒绝且没有审计记录。

预期结果取决于场景：

- unmatched labels 或 disabled specs 会记录为 admission failures；
- 数据库在重启后保留全部管理员修改；
- concurrency pressure 会让后续 requests 保持 queued，而不是被丢弃；
- retryable placement 或 rate-limit failures 会填充 `next_retry_at`，并保持后续可处理。

如果路由或模板健康状态回退，先禁用全部 managed specs 以及已启用的 `-large`
自定义 specs。回滚时不要删除公共模板或自定义 specs。

如果部署说明包含 private hosts、account names、channel URLs、secrets 或 cookie data，请记录在仓库外部。

## 9. Sandbox Deploy 验收

使用真实私有仓库、GitHub App Installation、可用的独立 Deploy 模板，以及账号或
组织 Sandbox 配置执行本节。开始前确认 App 具有仓库 Contents 读取权限。仓库成员可以部署；
管理组织设置与私有模板目录需要 Members 读取权限和 active owner membership。生成的 ID 和时间戳只记录在仓库外部的验收
记录中。

1. 在已知 Commit 写入 `.qiniu/deploy.yaml`，从 branch 发起手动部署，确认 API 保存完整
   SHA、不可变 spec hash、一小时默认 TTL（或限定范围内的请求 TTL），且响应不含命令或凭证。
2. 确认部署进入 `running`，HTTPS endpoint 的 health path 可访问，响应包含
   `endpoint_access: public`；UI 应提示该地址没有应用层鉴权。
   对 Vite preview，确认原仓库无需修改，启动时传入当前 Sandbox 的精确域名，保留 Host 校验，
   不使用通配符白名单。主动启用的公开 worker 验证命令见 [Testing](testing.md#sandbox-deploy-验证)。
3. 确认源 GitHub Job 和 Runner 清理完成后服务仍可访问；Deploy 不得注册 GitHub Runner。
   [Testing](testing.md#sandbox-deploy-验证) 中可选的 CI Run／attempt 验收要求实际观察
   相同 Commit 的并行运行，并由 Provider 确认 Runner 清理。
4. 使用同一个 `Idempotency-Key` 重复提交，并人为制造 provider 创建超时。确认系统按精确的
   `qiniu-deployd` metadata 补偿查询，重试不会创建第二个 Sandbox。
   [Testing](testing.md#sandbox-deploy-验证) 中主动启用的 `create-timeout` worker 子测试
   可重复验证真实资源创建响应丢失及首次查询不可用的恢复。`TestDeploymentLivePublicAPI`
   则针对已运行的本地 runnerd 验证并发认证提交、终态重放、DELETE 与配额释放，命令见同一 Testing 小节。
   补偿清理成功后，部署及版本必须保留原始失败阶段与原因。
5. 服务运行期间重启 runnerd，确认恢复相同 Sandbox ID、runtime process tag、endpoint 和
   到期时间；旧 attempt 回调不能修改当前部署。
6. 分别执行 stop、DELETE、TTL 到期和启动失败，确认 provider 证据最终表明 Sandbox 已释放；
   清理失败保持可见并继续重试。主动启用的 `start-failure` worker 模式以退出码 23 的
   合成启动计划验证真实资源清理。
7. 验证匿名用户与没有精确 Installation＋仓库权限的用户不能读取或修改部署。有相同仓库
   权限的其他成员应能查看历史／日志，以及创建、重新部署、停止和删除。OAuth 缺失或被拒绝
   时失败关闭；热缓存读取复用 Jobs 授权。确认成员只能选择公共模板，私有目录和 Sandbox
   设置仍由 owner 管理；检查 revision 执行凭据与每个用户的终端隔离。
   经 owner 明确批准后临时暂停并恢复测试 Installation：30 秒缓存到期后，仓库列表应
   排除它，详情／日志／操作应拒绝访问，即使 GitHub 仍返回其仓库。恢复后确认可以重新
   访问；将此证据与单仓库移除、OAuth Token 撤销分别记录。
   经账号明确批准后，独立撤销该本地 App 当前 OAuth Token，等待缓存到期，确认 Jobs／
   Deploy 的读取与操作返回 `403 / REAUTH_REQUIRED`；正常浏览器 OAuth 应恢复有效
   Token，回到原详情且保留历史。
   创建足够的活动部署后应收到
   `429 deployment_quota_exceeded`；释放一个后准入恢复。超过保留期后终态部署行／事件应清理，
   审计证据仍保留。

2026-10-14 的公开 API、启动失败与保留期加速验收结果见
[部署配置](deployment.md#实现迭代方案)。保留期仅修改一个已结束测试记录的时间戳，
并非自然等待七天。

终端变更上线前运行 `task ui-terminal-fault-test`，步骤见
[终端计时断流回归](testing.md#终端计时断流回归)。本地 5／15 秒原 Shell 恢复和
25 秒超时／空闲回收检查不覆盖已部署的入口代理与远程 Sandbox，需要单独保留
这些链路的计时生产证据；未计时的手动断网不能证明在 20 秒窗口内恢复。

分别记录本地单元／集成检查、浏览器/UI 检查和真实 Sandbox provider 运行三类证据。Local fake
和 preview fixture 不能替代本节验收。


在真实浏览器中确认 `/deployments` 只展示列表及“创建部署”链接，点击进入
`/deployments/new` 并刷新，确认只展示表单。列表页不读取表单目录，创建页不读取历史。
切换“版本来源”的分支／Tag，确认加载对应的真实引用列表，再选择当前
scope 的可用模板。引用为空或加载失败时不能提交旧版本，并可刷新重试。
创建部署后确认跳转到仓库／PR 上下文中的 `/deployments/{id}` 详情路径，ID 不放在查询参数中。
在执行中和就绪后刷新该地址，必须保留同一部署及阶段、stdout／stderr 日志。验证更早日志
分页和事件读取失败时保留历史。缺少模板或符合资格的 Sandbox 服务时，创建应被拒绝并给出可操作提示。
幂等重放和重试必须保留所选模板 ID。

确认 `/repositories` 显示平台服务已就绪的 scope 无需额外 Deploy 设置即可发现公共模板。
分别使用组织 owner 和仓库成员，确认两者均不能浏览平台私有目录，也不能用平台私有模板 ID 创建或重新部署。
验证平台未启用、配置不完整或 audience 未命中时拒绝新准入／重新部署，scope 自有和继承凭据优先，
配额仍生效；已存在的 Sandbox 在平台被禁用或 audience 改变后仍通过保存的快照重连和清理。
对个人仓库协作者，平台 audience 仅选择仓库 owner 时，验证模板发现、创建和重新部署成功；
仅选择协作者时应拒绝，且版本与审计历史不变。确认协作者自有 scope 凭据仍可独立于
平台 audience 使用，精确仓库授权检查保持有效。

部署版本验证需覆盖首次 #1、新版与审计原子提交及回滚、旧版状态保留、版本内游标分页、旧版迟到输出拒绝、旧 SQLite 重复升级、Postgres/MySQL 迁移及事务、URL 版本恢复与迟到响应隔离。确认窄屏下历史和日志上下排列，读取无需 Provider 请求。

指定公开 fork PR 时，使用 [Testing](testing.md#sandbox-deploy-验证) 的真实 API 验收参数，
指定仓库、来源、Commit 和 HTML 标记。推进 PR head 后部署历史完整 SHA，核对保存的 fork
身份、旧版页面内容及 DELETE 后的 Provider 资源不存在。真实 GitHub 权限撤销、私有仓库
Contents 权限及第二个用户的访问保持独立验收。

对已有 PR 部署，还需核对真实标题及分支／PR／Tag 的 GitHub 链接。
[Testing](testing.md#sandbox-deploy-验证) 中主动启用的
`TestDeploymentLivePullRequestMetadata` 检查认证元数据、匿名拒绝与生命周期保留，
不创建或停止 Sandbox；它可能回填旧记录缺失的标题。API 证据应与已登录 Chrome 的
页面渲染、OAuth 过期及回跳验证分别记录；临时签名会话不证明浏览器登录成功。
