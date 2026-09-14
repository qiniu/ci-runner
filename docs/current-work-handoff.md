# llgo Job 超时与 runnerd 可观测性交接

更新时间：2026-09-14，Asia/Shanghai。本文用中文保留本次会话的业务上下文、证据、判断和待办，供另一台电脑直接续接。

## 1. 目标、授权与交付位置

- 仓库：`miclle/qiniu-ci-runner`；上游：`qiniu/ci-runner`。
- 工作目录：仓库 checkout 根目录；不同电脑不要求使用相同的本地绝对路径。
- 当前起始分支：`main`；本增量代码基线：`e2bce66e1e336bbf4b27d0e41aef249a3a551d8b`，`feat(diagnostics): expose runner cleanup lifecycle (#96)`。
- 当前工作分支：`feat/github-job-result-retention`；后续推送目标仍为 `origin`，即 `git@github.com:miclle/qiniu-ci-runner.git`。
- 交付提交依次为 `3472ef3 docs: add llgo diagnostics handoff`、`33070e2 docs: update diagnostics branch resume commands`、`7174373 fix(runner): preserve cleanup start timestamps` 和 `ca0eca4 feat(diagnostics): expose cleanup lifecycle`。
- 接收人：用户在另一台电脑上的后续会话。
- 状态：故障分析和 PR #96 生命周期切片已经完成、合并并由用户确认发布到生产。本增量已在本地完成 GitHub Job 终态结果留存及全量验证，尚未提交／推送；环境快照、typed exit payload 和网络诊断仍未实施。
- 传输载体：PR #96 的历史实现已经进入 `upstream/main=e2bce66`。当前 `feat/github-job-result-retention` 仍有未提交改动，尚未推送，不能视为可跨电脑接收；提交和推送需等待用户明确授权。

用户先要求分析一个 llgo GitHub Actions Job，随后提供 runner_requests 查询结果和 control.log，询问 runnerd 是否有问题、如何向沙箱团队提单、runnerd 能否增加诊断能力。首次交接阶段只要求在新分支记录并推送完整交接，未授权重跑 Job、发布模板、部署线上服务或代发服务团队消息；后续已明确授权在同一分支实现生命周期时间戳、详情页字段和结构化退出／清理事件。

## 2. 故障对象及重要标识

- GitHub Job：<https://github.com/xgo-dev/llgo/actions/runs/34580926740/job/103238551606>
- Workflow：`Baseline benchmarks`；Job：`benchmark (WebAssembly)`。
- Run ID：`34580926740`；Job ID / runner request ID：`103238551606`。
- **本次调查对象为 attempt 3**。当时查询 run 总览显示已到 attempt 4，不能将最新 run 概览或其他重跑的日志混入该 Job。
- llgo 分支：`main`；SHA：`1e8c6818ca3cee7efa19e4cfddf307bba443c478`。
- Runner：`e2b-103238551606`；GitHub runner ID：`5041`；runner group：`Default`。
- Sandbox：`iz3jkq5wlyyb83xvq0l3q`；进程 PID：`1000`。
- Sandbox API：`https://us-south-1-sandbox.qiniuapi.com`；配置来源：`admin_default`。
- requested labels：`["qiniu","ubuntu-24.04-large"]`。
- registered labels：`["self-hosted","linux","x64","qiniu","ubuntu-24.04","ubuntu-24.04-large"]`。
- profile：`qiniu-ubuntu-24-04-large`；`profile_source=global`；`profile_scope_type=github_installation`；scope / installation ID：`158409144`。

用户提供的 control 日志地址为 `https://runner.qiniuinc.com/runner_requests/103238551606/logs/control.log`，需要已登录的管理权限。未登录 curl 返回 401；通过用户已登录浏览器读取成功。接收电脑不会继承浏览器会话。不要复制 cookie 或本地凭证到 Git。

## 3. 已确认的 GitHub 证据

GitHub Job API 返回 `status=completed`、`conclusion=cancelled`，不是普通测试断言失败。`Install dependencies` 被取消，后续 Emscripten、Binaryen、Go 配置和实际 Benchmark 均未执行。

GitHub check annotation 明确报告：

```text
The job has exceeded the maximum execution time of 30m0s
The operation was canceled.
```

对应 llgo SHA 的 `.github/workflows/benchmark.yml` 中，`wasm-benchmark` 使用 `[qiniu, ubuntu-24.04-large]`，设置 `timeout-minutes: 30`，通过 `./.github/actions/setup-deps` 安装 LLVM 22。

### 时间线

所有以下日志时间为 2026-09-11 UTC；北京时间加 8 小时。

| UTC 时间 | 事件 |
| --- | --- |
| 10:58:53 | GitHub Job started_at |
| 10:59:07 | 开始 Install dependencies |
| 10:59:08 | LLVM 签名密钥下载成功；开始 apt-get update |
| 11:01:38 | update 下载 41.2 MB，用时 2 分 28 秒，约 279 kB/s |
| 11:01:40 起 | 安装 LLVM 22 及传递依赖；apt.llvm.org 有连续成功下载记录 |
| 11:02–11:28 | archive.ubuntu.com 多个包重复 Ign，大量相邻记录约间隔 30 秒 |
| 11:29:10 | GitHub 取消操作，执行清理及 RUNNERD_JOB_COMPLETED hook |
| 11:29:13 | GitHub Job completed_at |

实际执行停在第一条 LLVM 安装命令，不是后续 common/optional dependencies 安装：

```bash
sudo apt-get install -y llvm-22-dev clang-22 libclang-22-dev lld-22 libunwind-22-dev libc++-22-dev
```

代表性下载日志：

```text
11:02:11 Ign:2 https://archive.ubuntu.com/ubuntu noble-updates/main amd64 libc6-dev amd64 2.39-0ubuntu8.9
11:02:41 Ign:3 https://archive.ubuntu.com/ubuntu noble-updates/main amd64 libc-dev-bin amd64 2.39-0ubuntu8.9
11:03:11 Ign:4 https://archive.ubuntu.com/ubuntu noble-updates/main amd64 libc6 amd64 2.39-0ubuntu8.9
```

类似重试涉及 libc-bin、python3-pygments、python3-yaml、libgpm2、libncurses6、libz3-4 等，持续二十多分钟，期间少量 Ubuntu 包仍成功下载。日志也显示 `file:/etc/apt/apt-mirrors.txt`，但没有采集到文件实际内容。

### 结论与证据边界

1. 高置信度：安装依赖中的 Ubuntu 包下载长期等待、重复尝试，耗尽工作流 30 分钟限制；Benchmark 本身没有运行。
2. 值得优先调查：Sandbox 到 archive.ubuntu.com 的出网路径、APT 镜像及重试行为。不同域名表现不同，不支持泛化为整个互联网不可用。
3. 未确定：DNS、IPv4/IPv6、TCP、TLS、代理、NAT、限流、丢包、源端响应中哪一项是底层原因。APT 尚未输出最终具体下载错误即被 GitHub 取消。
4. 不能仅凭 Ign 断言某一种网络故障，也未收集足够历史通过记录来判定 flaky。
5. apt-key 弃用警告不是本次终止原因；仅延长 Job 时限不能解决下载阻塞。

## 4. 用户数据库记录与 control 日志

用户是在 SQLite 中按 `runner_name = "e2b-103238551606"` 查询并粘贴结果；本会话未连接生产数据库，也未验证其当前状态。

关键字段：

```text
source = github_webhook
status = completed
failure_stage / failure_reason / error = empty
last_error_code / last_error_message = empty
last_error_retryable = 0
retry_count = 0
assigned_job_id = 103238551606
assigned_job_name = benchmark (WebAssembly)
queued_at = 2026-09-11 10:58:45.096332063+00:00
last_attempt_at = 2026-09-11 10:58:45.09922806+00:00
creating_at = 2026-09-11 10:58:45.116880515+00:00
running_at = 2026-09-11 10:58:45.776544337+00:00
completed_at = 2026-09-11 11:29:13.878760556+00:00
stopping_at = 2026-09-11 11:29:14.147378163+00:00
updated_at = 2026-09-11 11:29:14.147378163+00:00
version = 7
failed_at / next_retry_at / lease_owner / lease_expires_at = empty
github_payload_json = empty
github_context_backfilled = 1
pull_request_number = 0
```

敏感配置字段的值故意不复制；新电脑从自己的授权配置取得凭证。原查询中的其他 GitHub 和 profile 元数据见第 2 节。

实际读取的 control.log 全文：

```text
runner request created
checking workflow job status before sandbox start
creating github registration token
starting sandbox runner
sandbox runner started sandbox_id=iz3jkq5wlyyb83xvq0l3q pid=1000
runner accepted a job
runner process exited cleanly
sandbox cleaned after runner exit
```

此下载文本没有逐行时间戳，不能凭它推断完整内部事件时序。基线已有独立的持久化事件时间线功能，后续应复用和完善，而不是假设项目没有事件时间戳。

## 5. runnerd 代码分析

以下是本地基线代码的静态分析；**未核对线上二进制 SHA 与该基线是否相同**。

### 5.1 生命周期没有表现出导致本次失败的异常

queued 到 running 约 0.68 秒，表示 runnerd 的启动阶段记录，没有把它当作 Runner 已经监听或 Job 开始的精确耗时。assigned_job_id 与请求 Job 一致，没有接错任务证据。

GitHub 先取消，Runner 正常退出，runnerd 随后清理。`sandbox cleaned after runner exit` 表示停止/删除调用返回成功，并不是独立查询供应商库存后的物理资源验收。没有发现创建、进程异常退出或回收错误。

### 5.2 completed 不等于测试成功

`internal/server/server_runner_lifecycle.go` 中，正常进程退出路径记录 `runner process exited cleanly`；Sandbox 清理成功且未标失败时设 `StatusCompleted` 和 `CompletedAt`。`internal/sandboxrunner/runner.go` 的 `StopRunner` 连接 Sandbox、尝试终止进程、调用 Sandbox Kill。

`internal/server/server_webhooks.go` 的 completed webhook 使用 Job conclusion 记录 workflow metrics；`stopRunner` 对已经 completed 的请求可直接返回，必要时补充 assigned job 元数据。因此 GitHub 的 cancelled 和 Runner 请求的 completed 可以同时成立，failure 字段为空也不表示吞掉了编译错误。

后续应保留两个维度：Runner 生命周期/回收结果、GitHub Job conclusion。不能将所有取消或测试失败改成 Runner 基础设施失败，也不应自动重跑用户 Job。

### 5.3 时间戳语义瑕疵，已修复并推送

本例 `stopping_at` 比 `completed_at` 晚约 269 ms。修复前代码能产生这个现象：退出处理先设置 CompletedAt；`internal/state/runner_requests.go` 的 `applyStateTimestamps()` 在写入 StatusCompleted 时发现 StoppingAt 为空，就用更晚的 now 补齐。

本轮改动不再为 completed 状态合成 `StoppingAt`；只有真实进入清理时才在外部操作前持久化 stopping 状态，未创建 Sandbox 就完成的请求保持该时间为空。进程退出、webhook stop 与强制停止路径把同一个清理起始时间保留到终态。stopping 的 CAS/数据库写入是进程退出外部清理的硬前置条件：写入失败时旧回调立即返回，不调用 Sandbox 或 GitHub 清理，由新状态、恢复或 reconciler 接管。回归测试覆盖未进入清理的完成路径、清理中的可观察状态、正常/非零/错误进程退出、stopping 冲突、webhook 完成、cleanup retry 和超时强制停止。没有新增列或迁移，也没有自动改写历史数据库时间戳。

### 5.4 详情页时间字段与结构化事件，本分支已实现

Admin Runner request 详情现在直接展示持久化的 Started、Stopping、Completed、Failed，并以 `stopping_at` 到与当前终态一致的 `completed_at` 或 `failed_at` 计算 Cleanup Duration。仍在 stopping 时显示“进行中”；字段缺失、时间无效或终点早于起点时显示 `-`，不使用 `updated_at` 猜测清理结束时间。

现有 `runner_events.stage` 被复用，不新增表、列或迁移。`AppendStagedLog` 与旧 `AppendLog` 共用写入路径，旧 `control.log` 拼接读取保持兼容；Runner 退出、Sandbox 清理、GitHub Runner 注册清理和整体清理完成分别使用稳定的 `runner_exit`、`sandbox_cleanup`、`github_cleanup`、`runner_cleanup` 阶段标识，详情页逐条时间线会直接显示该标识。日志文本和现有状态转换、重试及 GitHub Job conclusion 边界不变。

### 5.5 GitHub Job 终态结果留存，本增量已在本地实现

`runner_requests` 增加可空的 Job 名称、状态、结论、Runner 名称与本地观察时间字段。只有非零 Job ID 与请求原始 `workflow_job_id` 一致并且观察已为终态时才更新；分配到的其他 Job、queued/in_progress 观察和原始 webhook body 都不会进入快照。Runner 请求的 completed/failed 仍描述 runnerd 生命周期，GitHub conclusion 保持独立。

完成 webhook、启动前状态检查、恢复和 reconciler 都会经过现有 `stopRunner`／`completeWithoutSandbox` 路径捕获终态。Runner 进程退出时会在 Sandbox 清理完成或进入清理重试状态后执行一次有界补偿查询，因此即使完成 webhook 丢失，只要 GitHub 此时已返回原始 Job 终态，也会立即保留结果，而 GitHub 延迟或失败不会阻塞 Sandbox 清理。重复的相同终态观察不会只为刷新采集时间而重写状态；错配完成请求重新排队时会同步清除旧 Job 快照。详情 API 对有快照的记录直接返回 `lookup_status/source: retained` 和观察时间，不再依赖远程查询；升级前没有快照的历史记录继续使用原有有界、30 秒成功缓存和同 Job 并发合并的实时查询，并标记 `source: live`。UI 同时显示结论、来源与采集／查询时间，不把持久化快照表述为当前实时状态。

Schema 继续由 GORM tags 驱动；现有 SQLite `runner_requests` 使用 additive-only 增列，不重建表、不批量改写历史记录。专用 PostgreSQL/MySQL 与生产 SQLite snapshot 验证仍取决于外部测试环境。

### 5.6 模板 APT 设置可能放大故障

`templates/github-runner-ubuntu-24.04/scripts/setup-template.sh` 的 `configure_reliable_apt_sources()` 设置优先镜像：

```text
https://archive.ubuntu.com/ubuntu/ priority:1
https://mirrors.edge.kernel.org/ubuntu/ priority:2
https://mirrors.tuna.tsinghua.edu.cn/ubuntu/ priority:3
```

并写入 `/etc/apt/apt.conf.d/80qiniu-network`：

```text
Acquire::Retries "5";
Acquire::http::Timeout "30";
Acquire::https::Timeout "30";
```

与日志节奏吻合，但没有当时的模板构建 ID 或实际配置快照，不能断言线上 Sandbox 与当前脚本完全相同。多个包的请求级超时/重试累计可能耗尽全 Job 时限。备用镜像是否有效切换尚未验证，不应声称配置镜像列表就已经保证故障切换。

## 6. 给沙箱服务团队的提单内容

尚未代发任何消息。用户可将以下摘要连同完整 Install dependencies 日志发给服务团队：

> 请排查 us-south-1 Sandbox `iz3jkq5wlyyb83xvq0l3q` 在 2026-09-11 10:59–11:29 UTC（北京时间 18:59–19:29）访问 archive.ubuntu.com:443 下载 Ubuntu 软件包持续阻塞的问题。Job ID 为 103238551606，系统 Ubuntu 24.04 x64。LLVM 22 安装的传递依赖下载反复 Ign，多个记录约相隔 30 秒，少量包成功；同期 apt.llvm.org 有连续成功下载。最后 GitHub 因 30 分钟时限取消，尚未编译或运行 Benchmark。Runner 正常退出，清理调用成功。
>
> 请核查该时段 DNS 解析和实际目标 IP、NAT/出口代理/防火墙/限流，以及超时、重传、丢包和重置；同区域同模板复测失败包，分别比较 IPv4/IPv6，记录 DNS、TCP、TLS、首字节及总耗时；核对实际 APT 镜像列表、超时/重试与备用源切换行为。请提供链路证据或同环境复现记录。现有日志尚不能区分沙箱链路和源站故障。

提单不需要 API Key、cookie 或完整数据库导出。完整日志在发送前仍应检查脱敏。

## 7. 优化状态与剩余范围

| 优先级 | 提案 | 目的与边界 |
| --- | --- | --- |
| P0 | 运行环境快照 | 记录实际 Sandbox 区域、解析后的模板 ID、可取得的构建版本、Runner 版本；不能把区域物理 ID 写回 managed spec |
| P0 | GitHub Job 结果留存 | **当前增量已完成：** 独立保留原始 Job 终态和观察时间；详情页区分 retained 与 historical live fallback；不改变 Runner 生命周期含义，也不回填历史结果 |
| P0 | 完善控制事件和时间戳 | **本分支已推进：** 已修复清理起始时间和终态时间倒置；详情页已展示 Started、Stopping、Completed、Failed 和语义受限的 Cleanup Duration；退出及 Sandbox/GitHub/整体清理事件已有稳定 `stage`。退出码仍保留在事件消息和失败原因中，尚未新增独立 payload 字段 |
| P1 | 按需网络诊断 | 存活 Sandbox 内的受限目标 DNS/连接/下载探测，记录连接 IP、阶段耗时、HTTP 状态和退出码 |
| P1 | 脱敏诊断包导出 | 环境快照、请求关联信息、控制事件、Job 结果、探测输出一次导出 |

生命周期切片已经通过 PR #96 进入生产；当前独立增量完成 GitHub Job 结果留存后，再评估运行环境快照，typed exit payload 继续作为后续独立范围。主动网络探测保持 P1。环境采集可由模板脚本完成，runnerd 负责触发和保存。APT 快照只取允许字段：镜像、超时、重试、代理是否存在，避免复制全环境或代理凭证。

时机很关键：GitHub 完成通知可能晚于 Sandbox 清理，不能依赖事后连接已销毁实例来收证。启动时保存轻量快照，存活时允许管理员按需触发；若做自动失败采集，应在模板/Job 失败处理阶段给定严格时限，不无限延迟回收。无输出不等于卡死，不能因此杀任务。

网络探测须限制授权、目标、次数、输出和时间，避免任意 URL 请求及签名地址泄漏。不是为所有任务固定增加大量网络流量，也不自动延长工作流时限。

## 8. 新电脑最先读取与复核

当前增量在 `feat/github-job-result-retention` 维护。新电脑恢复时先同步远程，再核对分支基线和正式行为文档：

```bash
git fetch origin upstream --prune
git switch feat/github-job-result-retention || git switch -c feat/github-job-result-retention --track origin/feat/github-job-result-retention
git status --short --branch
git rev-parse HEAD
git merge-base HEAD upstream/main
sed -n '700,725p' docs/testing.md
```

完成标准：分支基线仍可追溯到 `upstream/main=e2bce66`，工作区状态已理解，正式测试文档与本交接内容一致。

必须先读 `AGENTS.md`、`.agents/rules/development-workflow.md`、`.agents/rules/testing-and-verification.md`、`TODO.md`；涉及状态时读 `.agents/skills/runnerd-state-schema/SKILL.md`。

核心文件：

- `internal/server/server_runner_lifecycle.go`：正常退出、cleanupSandboxAfterExit、stopRunner、Job hook。
- `internal/state/runner_requests.go`：写入及 applyStateTimestamps。
- `internal/state/records.go`、`internal/state/db.go`：模型和兼容迁移。
- `internal/server/server_webhooks.go`、`internal/server/server_loops.go`：webhook、补查和收敛。
- `internal/sandboxrunner/runner.go`、`internal/sandboxrunner/scripts/start-github-runner.sh`：Sandbox 生命周期和 hook。
- `templates/github-runner-ubuntu-24.04/scripts/setup-template.sh`：实际模板构建及 APT 配置。
- `internal/sandboxrunner/runner_test.go`、`internal/server/server_test.go`、`internal/state/store_test.go`：已有相关测试，按函数名定位。

基线刚包含 #95 的 Runner request investigation。先用以下搜索了解已实现能力，避免重新建设或将已有能力当缺失：

```bash
rg -n 'control_log|before_id|after_id|conclusion|annotation|investigation' internal/server ui/src
rg -n 'applyStateTimestamps|StoppingAt|CompletedAt' internal/state/runner_requests.go internal/server/server_runner_lifecycle.go
rg -n 'configure_reliable_apt_sources|Acquire::' templates/github-runner-ubuntu-24.04/scripts/setup-template.sh
```

现有约束包括：admin-only 诊断、凭证脱敏、有界 control_log findings、GitHub 成功查询短缓存和并发合并、before_id/after_id 独立游标与过期响应保护。保持与用户普通 Jobs 权限边界隔离。

## 9. 恢复原始证据的命令

需要新电脑自己的 `gh` 登录与仓库读取权限。只读命令：

```bash
gh api repos/xgo-dev/llgo/actions/jobs/103238551606
gh api repos/xgo-dev/llgo/check-runs/103238551606/annotations
gh api repos/xgo-dev/llgo/actions/jobs/103238551606/logs --allow-escape-sequences > /tmp/llgo-job-103238551606.log
gh api 'repos/xgo-dev/llgo/contents/.github/workflows/benchmark.yml?ref=1e8c6818ca3cee7efa19e4cfddf307bba443c478' -H 'Accept: application/vnd.github.raw+json'
gh api 'repos/xgo-dev/llgo/contents/.github/actions/setup-deps/action.yml?ref=1e8c6818ca3cee7efa19e4cfddf307bba443c478' -H 'Accept: application/vnd.github.raw+json'
```

`--allow-escape-sequences` 是因为日志含终端颜色转义；初次不加该参数时 gh 拒绝输出，随后成功。`gh run view --json path` 不支持 path 字段；要查工作流路径用 `gh api repos/xgo-dev/llgo/actions/runs/34580926740`。该接口返回最新 attempt，不能替代特定 Job 证据。

旧电脑临时文件 `/tmp/llgo-job-103238551606.log`、`/tmp/llgo-benchmark-34580926740.yml`、`/tmp/llgo-setup-deps-34580926740.yml` 不随 Git 传输；上述命令可重新取得。`/tmp/runnerd-103238551606-control.log` 的 curl 尝试失败，不能把该文件当作有效导出。control 全文已保存在本文。GitHub 日志未来可能到期，届时说明证据只剩本文历史摘录。

## 10. 后续执行顺序和验收边界

1. 仓库复核已完成：`upstream/main=e2bce66` 已包含 PR #96，工作区从干净的 `main` 创建 `feat/github-job-result-retention`。
2. 生命周期时间戳、详情页字段和结构化事件已进入生产；不批量修复历史数据。
3. 当前增量已完成 additive 终态字段、原始 Job guard、历史查询回退和 UI 来源／新鲜度；环境快照和 typed exit payload 保持后续独立范围，不扩展成全量网络监控。
4. 环境与 Job 结果若新增状态字段，使用现有 SQLite additive migration 约束，不让 GORM 重建旧 runner_requests 表，不持久化 raw webhook。
5. 状态或调用方变更继续先运行 `go test ./internal/state -count=1`，再运行 focused package 和更广验证。跨数据库 schema/审计改动使用名称以 `_test` 结尾的专用 PostgreSQL/MySQL 数据库；生产 SQLite 快照测试需要单独提供文件，不伪造结果。
6. UI 文案改动运行 `task ui-i18n-check` 与相关 Bun tests；依赖、构建、公共指南或 Jobs 滚动布局改动运行 `task ui-production-smoke`。生产嵌入 UI 用 `task build`；禁止手改 `internal/server/ui/`。
7. 按实际变更同步 README 中英文、testing 中英文、TODO 和相关 agent 规则。模板验证遵守 qshell ready + 双区域真实 smoke；本地 Docker build 不能冒充 Sandbox 模板可用证据。
8. 服务团队调查与实现可独立推进，但服务提单、真实新建 Sandbox、发布模板和线上部署均未在本会话执行。不要认为“建议下一步”就是已经完成。

## 11. 验证、工作区与关闭条件

- 原分析阶段只做 GitHub/API/浏览器只读调查和本地静态代码阅读；没有跑 Bun、部署或模板测试，也没有重跑 GitHub Job。
- 当前增量开始时工作区干净，分支 `feat/github-job-result-retention` 基于 `upstream/main=e2bce66`。变更集中在 Runner request additive Job-result 字段、终态捕获、详情诊断来源／新鲜度、对应 Go/Bun tests、TODO、正式中英文文档和 agent 规则。
- 当前增量 TDD RED 证据：状态测试先因五个快照字段不存在而编译失败；生命周期 helper 测试先因函数不存在而失败，非终态拒绝测试先证明 `in_progress` 会被错误保留；诊断测试先证明详情 API 仍调用 GitHub 且没有来源字段；UI 测试先证明持久化来源与采集时间未渲染。
- PR review 修复继续按 TDD 推进：Runner 退出回归测试先证明清理结束后没有发起终态 Job 查询，重复终态测试先证明仅刷新 `observed_at` 仍触发写入，错配 Job 重排队测试先证明旧快照仍残留；对应实现完成后，三项定向测试和完整 `internal/server` 测试均转绿。
- 已通过 `go test ./internal/state -count=1`、Runner exit/cleanup focused tests、`go test ./...`、`task ui-i18n-check`、`task build`、最终 `task test` 和 `GOTOOLCHAIN=go1.26.3 task lint`。最终 UI 全套为 225 项测试、836 个断言全部通过；`task test` 重建生产 UI 并完成 Go race/coverage 全套，退出码为 0。lint 使用 `go.mod` 固定的 Go 1.26.3 后为 0 error，仍有 3 条位于未修改文件的既有 hook dependency warning；本机默认 Go 1.27 会让 staticcheck v0.7.0 因 export-data 版本不兼容而失败。专用 PostgreSQL/MySQL 与生产 SQLite snapshot 因未提供测试环境而跳过，未运行部署或真实模板测试。
- PR review 修复后再次通过 `go test ./... -count=1`、`task test` 和 `GOTOOLCHAIN=go1.26.3 task lint`；完整测试仍只跳过需要外部 PostgreSQL/MySQL 数据库和生产 SQLite snapshot 的可选验证，lint 仍为 0 error 与上述 3 条既有 warning。
- 只读代码审查第一轮发现 stopping 写失败后仍继续外部清理的问题；修复并补回归测试后复审为 Critical 0、Important 0、Minor 0，相关 server/state race 测试各重复 10 次通过。
- 当前阶段的独立只读审查先发现 Go 零时间处理和两个测试覆盖缺口；修复后复审未发现剩余可操作问题，`git diff --check` 通过。
- 时间戳修复的远程 SHA 为 `71743735870bf40dc30d223df36acb2067cc6927`；详情页与结构化事件阶段的远程 SHA 为 `ca0eca45244208e22b299941604828ee91bc629b`。本阶段没有 stash。
- 可继续静态设计，无凭证阻塞。真正复现需要新电脑的 GitHub 权限、runnerd 管理登录及单独授权的沙箱凭证/测试环境。线上版本、模板构建版本、底层网络原因仍未知。
- 继续自：本次会话，无前置 handoff 文件。当前记录仍承载 Job 结果留存与环境快照的事故背景；这些后续范围完成、取消或迁入正式文档后，更新 TODO，并删除或明确标记这份交接记录为历史资料。
