# Runnerd 实现评审

[English](../runner-implementation-review.md)

首次评审：2026-07-16。当前基线刷新：2026-08-28。

范围：

- 评审目标：file-based config、GORM-backed DB schema migration、retry/lease/audit handling、ordinary-user Sandbox catalogs 与作用域 Runner Specs、admin account-role controls、embedded UI assets 和 local development workflow 更新后的实现状态。
- 仍可用于后续对比的参考：actions-runner-controller 风格 reconciliation，以及 fireactions 风格 pool/config modeling。

## 摘要

Runnerd 已经越过最初 2026-05-19 的差距清单。Runtime configuration 现在是 file-first，runner state 已 DB-backed，schema creation 主要由 GORM model tags 驱动，retry/lease/audit 字段已经存在，GitHub App auth 可以动态解析 installations，ordinary-user UI 覆盖 job/repository/account setup flows 以及个人账户／Organization Runner Specs，admin console 覆盖包括带审计 account-role 修改在内的核心管理流程，diagnostics 暴露 pprof/expvar state，文档化的本地 workflow 包含 `task dev`。

剩余工作不再是基础架构补课，而是产品和运维 hardening：是否保留 token/basic auth 作为本地兼容模式，多少 config management 应进入 admin console，以及在把服务视为 production-ready 前持续执行并维护 canonical deployment smoke checklist。

## 当前基线

- 配置默认从 `runnerd.yaml` 加载，也可通过 `--config` 指定。相对 sqlite database paths 和 GitHub App private-key paths 会按配置文件目录解析。
- Config schema 覆盖 server、database、OAuth session auth、Sandbox lifecycle timeouts、GitHub webhook/auth/OAuth、allowed repositories，以及 worker retry/lease/concurrency behavior。Sandbox service API URL 和 API key 来自 database-backed scoped Preferences 或默认关闭的 admin fallback，不是 file config。
- GitHub API auth mode 必须三选一：GitHub App、token 或 basic auth。GitHub App mode 支持可选静态 `installation_id`；省略时，runnerd 会按 job repository 解析 installation access 并缓存 transports。
- Runner requests、events、Runner Specs、retry metadata、leases、accounts、GitHub installations、scoped preferences/secrets 和 audit events 存储在配置的 database backend 中。内部 Runner Group 和 Repository Policy 模型已在 Release C 完全移除，不属于受支持的 state 行为。
- State schema creation 会先对旧 columns、obsolete OAuth constraints 和不兼容的 legacy scope tables 执行窄范围 compatibility pass，再运行 GORM `AutoMigrate`。GORM foreign-key creation 有意保持关闭。缺少 scope columns 的 legacy account preference/secret tables 会被有意重置；升级后需要重新配置 Sandbox settings/API keys，相关用户还需重新通过 GitHub 认证后才能同步 installations。
- Worker processing 使用 DB claim/lease semantics 和 retry scheduling，而不是只依赖 in-memory queue ownership。
- Qiniu sandbox、GitHub、rate-limit、timeout 和 temporary network transient failures 会被分类为 retry 或 queue deferral。确定性的 auth/config/template failures 会立即失败。
- Admin routes 通过 `/admin/accounts` 和 `/admin/api/accounts` 暴露账户列表与带审计的角色控制，同时提供 runner request management、retry/stop/log access、Runner Specs、平台 Sandbox fallback、match tests、audit events 和 diagnostics。Account 管理仅包含角色控制；系统拒绝修改自身角色，以及可能导致没有管理员的变更。已退役的 Group/Policy 和 catalog-readiness API 返回 404。
- `/` 是公开首页，`/jobs` 是受保护的 ordinary-user Jobs dashboard。Stable GitHub-context job-group routes 包括 `/github/pulls/{owner}/{repo}/{number}/jobs`；统一 repository/Sandbox readiness 位于 `/repositories`。`/account/repositories` 和 `/organizations/{login}/repositories` 保留为指向同一页面的 scoped compatibility links；Sandbox Service、Templates 和 Instances 继续位于账户或可管理组织的设置路由。
- Admin console 通过 `/admin/sandbox_service` 和 role-gated `/admin/api/sandbox-service-default` endpoints 管理全局 fallback，包括 all/selected repository-owner audience controls；provider catalogs 仍属于 ordinary-user resources。
- 登录用户目录 API 通过 `/user/sandbox/templates` 提供区域过滤模板，并通过 `/user/sandbox/instances` 提供区域和模板过滤的 runner instances。接口从选中的 account 或 installation scope 解析加密凭据，不会暴露 provider secrets。
- 普通用户在 `/runner-specs` 浏览不带作用域选择的只读平台目录；`/account/runner-specs` 与 `/organizations/{login}/runner-specs` 只管理对应作用域自有的自定义规格，三个页面都使用 `/user/runner-specs`。托管目录的身份、启用状态和并发由 Admin 全局管理；作用域自定义模板只用选定作用域的 Sandbox 凭据验证。repository-only 协作者仅能在 `/repositories` 查看就绪状态。用户响应只为当前作用域自己的自定义条目返回物理 template ID；排队请求启动前会重新校验最新的全局或作用域自定义启用状态和 labels。Admin 全局 Runner Specs API 仍是独立的 role-gated surface。
- `ui/` 中的 React UI 会从 `internal/server/ui/*` 嵌入生产构建；development builds 通过 `internal/server/ui_assets_development.go` 代理到 Vite。
- `task dev` 会一起启动 Vite 和 Go service development mode。`task build` 先构建 UI，再用 embedded production assets 编译 `bin/runnerd`。
- Diagnostics 可通过 admin UI 和 `/diagnostics/pprof` / `/diagnostics/vars` 访问，底层是 `github.com/jimmicro/pprof` 和 expvar。

## 剩余决策

### 1. Auth Policy

Token 和 basic auth 仍与 GitHub App auth 并存。它们对本地验证或 legacy credentials 有用，但也意味着产品还不是 GitHub-App-only。需要决定这些模式是 intentional compatibility paths，还是应在 production hardening 前移除。

### 2. Ordinary-User Repository Readiness

Ordinary-user UI 现在已经路由到 `/admin/*` 之外。`/repositories` 会列出用户与 GitHub App 授权交集内的全部 repositories，用本地 job activity 标注运行记录但不会隐藏尚未运行的 repositories，并显示所选账户或组织的有效 Sandbox service 来源。不存在有效来源时，可管理的 scope 会链接到对应账户或组织 Preferences 页面；credentials 只在 Settings 中编辑。

### 3. 作用域 Runner Specs 验证

平台与作用域自定义 Runner Specs 已通过 `/runner-specs`、`/account/runner-specs`、`/organizations/{login}/runner-specs` 和 `/user/runner-specs` 实现。本地 State、API、UI、i18n、Go 全量测试和现有四项 fixture-backed production smoke 检查已通过，但这些浏览器检查覆盖公开页面和 Jobs 布局，不包含 Runner Specs 路由。Runner Specs 专项 browser fixtures、专用 PostgreSQL/MySQL 事务测试、生产 SQLite snapshot 与降级门禁、真实 Sandbox 模板校验、个人／Organization GitHub workflow 执行和部署 canary 仍是发布门禁；这些事项已记录在 `TODO.md`，不能当作已完成证据。

### 4. Config Management

Runtime config 仍是 file-first，但 admin console 尚未提供 effective-config view、config validation preview、reload workflow 或 import/export flow。除非 live config operations 成为明确需求，否则继续保持当前 file-only operations model。

### 5. Deployment Smoke

Local build/lint/test coverage 只能验证代码路径；production readiness 仍依赖真实 GitHub App installation、真实 Qiniu sandbox templates、webhook delivery 和 sandbox runner execution。运行并维护[部署 smoke checklist](deployment-smoke.md)，覆盖 webhook signature handling、installation resolution、runner spec matching、sandbox creation、GitHub job pickup、cleanup 和 diagnostics。

### 6. Multi-Instance And Operations

DB lease model 已经存在，但在记录 multi-instance support 前，仍需用两个 runnerd instances 连接同一个 database 验证 multi-process behavior。Expvar diagnostics 覆盖有用 counters 和 gauges；只有在 deployment observability 需要时才添加 histogram/export adapters。

### 7. Schema Compatibility

当前 migration path 有意避免完整 handwritten migration history。`internal/state/records.go` 中的 GORM tags 定义正常 schema，`internal/state/db.go` 只保留针对旧 state databases 的窄范围 compatibility actions，包括显式重置 pre-scope account preference/secret tables。后续 schema changes 如果新增 required columns、带 uniqueness semantics 的 indexes 或 relationship constraints，应包含 old-schema upgrade tests，并按实际语义断言数据被保留或有意丢弃。

## 建议顺序

1. 所有触及 backend/UI boundaries 的分支保持 `task dev`、`task build`、`task lint` 和 `task test` 绿色。
2. 使用真实 GitHub App、一个 repository 和一个 Qiniu sandbox template 运行并维护 deployment smoke checklist。
3. 决定是否保留 token/basic auth modes。
4. 只有在 config operations model 清晰后，再添加 effective-config diagnostics view。
5. 用并发 runnerd 进程压测 DB lease behavior，再宣传 multi-instance support。
6. 修改 state records 或 GORM migration tags 时，保留 old-schema upgrade coverage。

## 验证说明

2026-05-19 review 中的旧 findings 已废弃，因为相关实现已经发生实质变化。更新本文档时重新运行当前验证命令：

```bash
task lint
task test
task build
```
