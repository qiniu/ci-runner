# llgo Job 超时与 runnerd 可观测性交接

更新时间：2026-09-14，Asia/Shanghai。本文用中文保留本次会话的业务上下文、证据、判断和待办，供另一台电脑直接续接。

## 1. 目标、授权与交付位置

- 仓库：`miclle/qiniu-ci-runner`；上游：`qiniu/ci-runner`。
- 原电脑路径：`/Users/miclle/github/miclle/qiniu-ci-runner`；接收电脑可使用其他路径。
- 起始分支：`main`；分析及本次交接的代码基线：`c384341a5ec261903e1e165461ff601c2d3ee132`，`feat(diagnostics): add runner request investigation (#95)`。
- 交接分支：`docs/llgo-job-diagnostics-handoff-20260914`；推送目标：`origin`，即 `git@github.com:miclle/qiniu-ci-runner.git`。
- 提交标题：`docs: add llgo diagnostics handoff`。文档所在提交通过 `git log -1 --format=fuller -- docs/current-work-handoff.md` 获取，避免在提交内容中自引用提交 SHA。
- 接收人：用户在另一台电脑上的后续会话。
- 状态：故障分析完成；改进方案仅为建议，尚未实施。当前明确授权是编写交接文档、创建分支、提交并推送。
- 传输载体：上述远程分支；推送成功后可跨电脑获取。推送是否成功和精确 SHA 以本次会话最终交付消息及 `git ls-remote` 为准。

用户先要求分析一个 llgo GitHub Actions Job，随后提供 runner_requests 查询结果和 control.log，询问 runnerd 是否有问题、如何向沙箱团队提单、runnerd 能否增加诊断能力。最后要求完整交接并在新分支提交推送。用户没有要求在本次交接中实现诊断功能、修复时间戳、重跑 Job、发布模板或部署线上服务，也没有授权代发服务团队消息。

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

### 5.3 时间戳语义瑕疵，尚未修复

本例 `stopping_at` 比 `completed_at` 晚约 269 ms。当前代码能产生这个现象：退出处理先设置 CompletedAt；`internal/state/runner_requests.go` 的 `applyStateTimestamps()` 在写入 StatusCompleted 时发现 StoppingAt 为空，就用更晚的 now 补齐。

这足以解释字段倒置，不证明清理本身倒序执行。建议后续记录真实清理开始时刻，补充覆盖直接退出、webhook stop、重复完成及重试路径的测试。不要未经授权批量改写历史数据库时间戳。

### 5.4 模板 APT 设置可能放大故障

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

## 7. 已提出但未实施的优化

| 优先级 | 提案 | 目的与边界 |
| --- | --- | --- |
| P0 | 运行环境快照 | 记录实际 Sandbox 区域、解析后的模板 ID、可取得的构建版本、Runner 版本；不能把区域物理 ID 写回 managed spec |
| P0 | GitHub Job 结果留存 | 生命周期 completed 与 conclusion cancelled 分开；复用已有详情页远程查询/缓存能力，评估是否确需新增持久化字段及补查注释 |
| P0 | 完善控制事件和时间戳 | 退出码、事件来源、清理开始/结束/耗时；修复停止时间倒置，复用已有逐条事件时间线 |
| P1 | 按需网络诊断 | 存活 Sandbox 内的受限目标 DNS/连接/下载探测，记录连接 IP、阶段耗时、HTTP 状态和退出码 |
| P1 | 脱敏诊断包导出 | 环境快照、请求关联信息、控制事件、Job 结果、探测输出一次导出 |

推荐先评估 P0 三项，之后再做主动网络探测。环境采集可由模板脚本完成，runnerd 负责触发和保存。APT 快照只取允许字段：镜像、超时、重试、代理是否存在，避免复制全环境或代理凭证。

时机很关键：GitHub 完成通知可能晚于 Sandbox 清理，不能依赖事后连接已销毁实例来收证。启动时保存轻量快照，存活时允许管理员按需触发；若做自动失败采集，应在模板/Job 失败处理阶段给定严格时限，不无限延迟回收。无输出不等于卡死，不能因此杀任务。

网络探测须限制授权、目标、次数、输出和时间，避免任意 URL 请求及签名地址泄漏。不是为所有任务固定增加大量网络流量，也不自动延长工作流时限。

## 8. 新电脑最先读取与复核

```bash
git fetch origin docs/llgo-job-diagnostics-handoff-20260914
git switch --track origin/docs/llgo-job-diagnostics-handoff-20260914
git status --short --branch
git rev-parse HEAD
git log --oneline -5
git diff --stat
git diff --cached --stat
git rev-list --left-right --count 'HEAD...@{upstream}'
```

若本地同名分支已存在，使用 `git switch docs/llgo-job-diagnostics-handoff-20260914` 并检查差异，不强制覆盖。第一步完成标准：文档可读、基线可定位、工作区/远程差异已理解，保留接收电脑已有工作。

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

1. 完成第 8 节仓库复核；判断文档相对新代码是否漂移，再读现有诊断实现。
2. 在获得后续实施指令后，把 P0 缩成明确的字段、事件和 UI 范围；不要直接扩展成全量网络监控。
3. 优先为时间戳倒置建立回归证据，再修正真实清理开始时间语义，覆盖进程退出和 webhook 收敛路径。历史数据不做自动修复。
4. 环境与 Job 结果若新增状态字段，使用现有 SQLite additive migration 约束，不让 GORM 重建旧 runner_requests 表，不持久化 raw webhook。
5. `go test ./internal/state -count=1` 先于相关广泛验证；按变更运行 focused server/sandboxrunner tests。跨数据库 schema/审计改动使用名称以 `_test` 结尾的专用 PostgreSQL/MySQL 数据库运行项目要求的后端测试；生产 SQLite 快照测试需要单独提供文件，不伪造结果。
6. UI 文案改动运行 `task ui-i18n-check` 与相关 Bun tests；依赖、构建、公共指南或 Jobs 滚动布局改动运行 `task ui-production-smoke`。生产嵌入 UI 用 `task build`；禁止手改 `internal/server/ui/`。
7. 按实际变更同步 README 中英文、testing 中英文、TODO 和相关 agent 规则。模板验证遵守 qshell ready + 双区域真实 smoke；本地 Docker build 不能冒充 Sandbox 模板可用证据。
8. 服务团队调查与实现可独立推进，但服务提单、真实新建 Sandbox、发布模板和线上部署均未在本会话执行。不要认为“建议下一步”就是已经完成。

## 11. 验证、工作区与关闭条件

- 原分析阶段只做 GitHub/API/浏览器只读调查和本地静态代码阅读；没有修改业务代码，没有跑 Go/Bun/部署/模板测试，也没有重跑 GitHub Job。
- 本次交接开始时工作区干净：无 staged、modified、untracked 文件；main 与本地 tracking ref ahead/behind 为 0/0。该计数不是远程实时查询的替代。
- 本次计划提交仅包含本文、TODO 链接和中英文 docs 索引链接。没有其他实现成果等待搬运，没有 stash 或未提交修复。
- 文档验证使用路径检查及 `git diff --check`；精确提交后状态和远程 SHA 由交付时验证，不把未运行的测试写成通过。
- 可继续静态设计，无凭证阻塞。真正复现需要新电脑的 GitHub 权限、runnerd 管理登录及单独授权的沙箱凭证/测试环境。线上版本、模板构建版本、底层网络原因仍未知。
- 继续自：本次会话，无前置 handoff 文件。完成选定改进且取得相应验证后，将耐久结论迁入正式文档，更新 TODO，并删除或标记完成这份临时交接记录。
