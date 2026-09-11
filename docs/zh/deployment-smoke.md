# 部署 Smoke Checklist

[English](../deployment-smoke.md)

在把 runnerd 部署视为可以承载真实 GitHub Actions 流量前，使用这份 checklist 做生产风格验证。

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
  后才能使用其 development specs；适用的模板门禁通过前，不要部署默认启用的
  managed specs。
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

使用在某个已安装组织中具有 active membership 的用户登录，然后打开：

```text
https://<runnerd-host>/account/preferences
```

预期结果：该组织出现在 Settings scope 列表中，并能打开
`/organizations/<login>/preferences`。这可以验证 GitHub App 的
`Members: Read-only` 已由该 installation 批准。再使用仅有 repository 权限的
outside collaborator 重复验证：已授权仓库仍可出现在 `/repositories`，但组织
不得出现在 Settings 中，其 scoped Sandbox mutation 和 catalog read 必须被拒绝。

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

启动后确认 runnerd 协调了且仅协调了 5 个 managed specs，并且没有自定义名称
冲突：

```bash
curl -fsS -b "$COOKIE_JAR" https://<runnerd-host>/runner_specs |
  jq '[.[] | select(.managed_by == "qiniu/ci-runner") |
      {name, required_labels, default_template_name, enabled}]'
```

预期名称为 `qiniu-ubuntu-slim`、`qiniu-ubuntu-22.04`、
`qiniu-ubuntu-24.04`、`qiniu-ubuntu-26.04` 和 `qiniu-ubuntu-latest`。
确认启动日志中不存在 managed-profile name collision。在每个已配置的 Sandbox
区域运行 `task template-defaults-check` 并保存 8 个物理模板 ID；runnerd 必须通过该
scoped endpoint 解析相同稳定名称，不能保存某一区域的 ID。
另外在 Admin 中分别验证已启用的 5 个 `-large` 对外默认 spec；这些 label 由
operator 配置，不应出现在 managed 条目中。

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
- 缺少后台凭据时返回 `409 sandbox_service_not_configured`；managed 控制项和
  现有自定义 spec 的非模板参数仍可修改。
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
- GitHub Actions job 持续运行，不会重新排队或丢失 runner。
- 由旧进程持有 lease 的 `queued` 请求能够被新 worker 继续处理。
- GitHub 或沙箱状态查询暂时失败时只记录错误，不会停止已有沙箱。

## 7. Cleanup

Workflow 完成后确认：

- Qiniu sandbox 已停止或不再 active。
- GitHub self-hosted runner registration 已移除，或已 offline 并被 runnerd 清理。
- Runner request 的 control/stdout/stderr logs 可以通过 admin UI 或 `/runner_requests/{id}/logs/{name}` 查看。
- `/diagnostics/vars` 显示更新后的 workflow job、runner registration、cleanup 和 duration counters。

## 8. Failure Drill

部署仍在观察期时，执行受控路由与 operator-control 检查：

- 触发 `[ubuntu-24.04]` 和 `[qiniu]`；两者都必须保持 unmatched。
- 在 Admin 中禁用 `qiniu-ubuntu-24.04`，重启 runnerd，并确认它仍保持禁用；
  重新启用后确认恢复调度。
- 降低某个 managed spec 的 concurrency，并触发两个 jobs。
- 运行 `deployment-custom` spec，确认使用显式 template ID；随后删除它，并
  确认删除 managed spec 仍返回 conflict。

预期结果取决于场景：

- unmatched labels 或 disabled specs 会记录为 admission failures；
- reconciliation 会在重启后保留 operator 控制的 disabled 状态；
- concurrency pressure 会让后续 requests 保持 queued，而不是被丢弃；
- retryable placement 或 rate-limit failures 会填充 `next_retry_at`，并保持后续可处理。

如果路由或模板健康状态回退，先禁用全部 managed specs 以及已启用的 `-large`
自定义 specs。回滚时不要删除公共模板或自定义 specs。

如果部署说明包含 private hosts、account names、channel URLs、secrets 或 cookie data，请记录在仓库外部。
