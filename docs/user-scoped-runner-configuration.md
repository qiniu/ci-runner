# 用户作用域 Runner Spec 架构与发布门禁

> **Lifecycle:** 本文记录 `feat/user-scoped-runner-types` 的最终产品边界、兼容性约束和发布门禁。当前分支尚未上线；未经用户明确授权不得推送、合并或部署。

**Goal:** 在不开放 Admin Runner Spec API、不破坏全局目录和历史数据库的前提下，让账户或 GitHub Organization 使用自己的 Sandbox 模板创建自定义 Runner Spec，并确保该规格只能服务对应 owner 的仓库。

**Implementation branch:** `feat/user-scoped-runner-types`。

## 1. 最终产品决策

- 产品统一使用“Runner Spec／Runner 规格”，不再对用户显示“Runner Type”。
- Admin 管理全局平台 Runner Spec。其名称、标签、模板映射、启用状态和并发策略均为平台级配置。
- 普通用户在顶层 `/runner-specs` 只读浏览平台规格并复制 `runs-on` 标签。该页面不选择账户或 Organization，也不提供启用、停用或并发编辑。
- 账户在 `/account/runner-specs` 管理自己的自定义规格；Organization 在 `/organizations/{login}/runner-specs` 管理自己的自定义规格。
- 自定义规格只能被同一作用域 owner 的仓库匹配。对其他仓库只有访问权的外部协作者不能读取或修改该作用域配置。
- 账户自定义规格不支持 `runner_group`；Organization 自定义规格可以设置 GitHub Runner Group。
- 自定义规格最大并发默认值为 `10`；`0` 仍保留为“不额外限制”的显式高级值，运行时还受 runnerd 全局容量约束。

平台页坚持只读，避免同一个平台规格在不同页面呈现互相冲突的状态，也避免用户把平台容量误解为账户配额。如果以后需要租户配额，应设计独立的配额产品和数据模型，而不是复用 Runner Spec 的启用字段。

## 2. 页面与权限边界

| 页面 | 内容 | 可执行操作 |
| --- | --- | --- |
| `/admin/runner_specs` | 全局 managed 与 Admin 自定义平台规格 | Admin 创建、修改、启停、删除和设置全局并发 |
| `/runner-specs` | 平台规格目录 | 普通用户只读、复制工作流标签 |
| `/account/runner-specs` | 当前账户自定义规格 | 当前账户创建、修改、启停和删除 |
| `/organizations/{login}/runner-specs` | Organization 自定义规格 | 现有 manageable 成员创建、修改、启停和删除 |
| `/repositories` | 仓库对应作用域的 Runner 与 Sandbox 就绪状态 | 有仓库访问权的用户只读 |

授权要求：

- Account 请求只能解析为当前登录账户。
- Organization 请求必须经过 `accountPreferenceScopeManageable`；repository-only、失效成员关系或未关联 Installation 均不得读取完整目录或执行 Mutation。
- `/runner-specs` 在 UI 上没有作用域概念。当前实现通过登录账户调用 `/user/runner-specs` 获取经过脱敏的平台目录，并只渲染 `managed` 与 `platform_custom` 来源。
- Settings 页面只渲染当前请求作用域自己的 `scoped_custom` 条目。
- 异步列表、模板加载、Mutation 和后续刷新必须绑定发起请求的 scope；旧 Organization 响应不能覆盖当前页面或向新 scope 提交。

## 3. User API

`/user/runner-specs` 是登录用户目录与自定义规格 Mutation API，不替代 Admin `/runner_specs`：

```text
GET    /user/runner-specs
GET    /user/runner-specs?installation_id=<id>
POST   /user/runner-specs
PATCH  /user/runner-specs/{name}
DELETE /user/runner-specs/{name}?expected_updated_at=<RFC3339>
```

曾在分支早期实现的以下控制路由已移除，不属于发布合同：

```text
PUT    /user/runner-specs/{name}/control
DELETE /user/runner-specs/{name}/control
```

列表必须区分三种来源：

| `source` | 含义 | 用户可见字段与能力 |
| --- | --- | --- |
| `managed` | runnerd 托管的平台规格 | 稳定公共模板名、工作流标签、全局只读 `enabled` 和 `max_concurrency` |
| `platform_custom` | Admin 创建的平台自定义规格 | 工作流标签与全局只读策略；不得返回私有 `template_id` |
| `scoped_custom` | 当前账户或 Organization 自定义规格 | 当前 scope 自己的 `template_id`、策略和修订时间；Organization 可见 `runner_group` |

响应不得包含早期试验字段：

- `scope_enabled`
- `scope_max_concurrency`
- `scope_control_configured`
- `effective_max_concurrency`
- `global_max_concurrency`

平台条目的 `enabled` 与 `max_concurrency` 是 Admin 全局策略的只读快照。`template_id` 和 `runner_group` 只能为当前 scope 自己的 `scoped_custom` 条目返回。

创建请求：

```json
{
  "name": "org-linux-large",
  "workflow_labels": ["self-hosted", "org-linux-large"],
  "template_id": "template-owned-by-org",
  "runner_group": "large-runners",
  "max_concurrency": 10,
  "enabled": true
}
```

PATCH 与 DELETE 必须携带客户端最后读取到的 `expected_updated_at`。缺失或格式错误返回 `400 invalid_runner_spec_revision`，竞争修改或删除返回 `409 runner_spec_conflict`。

## 4. State 与迁移兼容

### 4.1 当前模型

- `runner_profiles` 继续保存全局平台目录，保持 main 的匹配与 Admin API 兼容。
- `scoped_runner_profiles` 保存账户／Organization 自定义规格，复合身份为 `(scope_type, scope_id, name)`。
- `runner_requests` 保存 `profile_source`、`profile_scope_type` 和 `profile_scope_id`，让排队后的生命周期按准入时的身份重新加载规格，不按名称跨 scope 猜测。
- `scoped_runner_profiles` 使用规范化后的精确标签集合生成 `label_key`；同一 scope 内该 key 唯一。

### 4.2 已移除的试验模型

`runner_profile_scope_controls` 不再属于领域模型、Store、API、匹配、容量或 fresh schema。为了保持增量升级安全：

- 新数据库不得创建该表。
- 如果旧的未上线分支数据库已经包含该表，启动时不读取、不迁移、不自动删除。
- 旧表中的任何行都不能影响匹配、启用状态或并发。
- 删除遗留表属于另行授权的维护操作，不是应用启动迁移。

所有 schema 变更继续遵守 GORM model-driven `AutoMigrate` 和现有 SQLite 窄范围增量迁移规则。不得重建 `runner_profiles` 或 `runner_requests`，不得自动修复历史行。

## 5. 匹配与生命周期

匹配顺序固定为：

1. Webhook 完成仓库 allowlist 和 Installation 作用域解析。
2. 若能解析 scope，按规范化标签查询该 scope 的精确 `scoped_custom`。
3. 精确自定义规格存在且启用时命中该规格。
4. 精确自定义规格存在但停用时，以 `profile_scope_disabled` 明确拒绝，不回退到同标签全局规格。
5. 不存在精确自定义规格时，使用 main 兼容的全局目录匹配。
6. 无法解析 scope 时也只使用全局目录，不猜测其他账户或 Organization。

必须继续保持：

```text
required_labels ⊆ job_labels ⊆ labels
```

排队请求启动前按持久化的 source、scope 和 name 重新加载最新规格，并重新校验启用状态与 requested labels：

- `global` 或历史空 source 直接读取最新全局 `runner_profiles`。
- `scoped_custom` 只读取保存的精确 scope。
- Retry 沿用原身份，不重新匹配其他规格。
- 规格已停用、删除或 labels 不再兼容时，在 `profile_validation` 失败，不启动 GitHub Runner 或 Sandbox。

## 6. 并发语义

并发检查发生在 worker claim queued request 后，超过限制的请求保持 `queued`：

```text
global spec:
  global.max_concurrency <= 0
  OR global_in_flight < global.max_concurrency

scoped custom spec:
  custom.max_concurrency <= 0
  OR scope_in_flight < custom.max_concurrency
```

两类请求都继续受 `worker.max_concurrent_runners` 约束。全局平台规格没有用户作用域附加限制；同名 scoped custom 请求也不计入全局 Spec 的同名计数。

## 7. 自定义规格校验与审计

- 名称 trim 后必须非空且是单路径段，不得包含 `/`，不得等于 `.` 或 `..`。
- `workflow_labels` trim、去空、去重并排序后必须非空；同一 scope 的精确标签集合唯一。
- 名称不能与当前有效的全局 Spec 重复。
- 创建或改变 `template_id` 时，只使用该 scope 显式配置或合法继承的 Sandbox 凭据验证；不得使用 Admin 默认凭据。
- 未改变模板的编辑不调用 Provider。
- Provider I/O 发生在数据库事务之外；写入使用初始 `updated_at` 条件更新。
- 创建、更新和删除必须与对应审计事件在同一事务中提交；校验失败、冲突或审计失败不能留下部分数据。
- 删除或改变模板、标签前，如果仍有 queued、creating、running 或 stopping 请求，返回 `409 runner_spec_in_use`。
- Provider 错误不得转发上游响应体，也不得记录凭据或密文。

## 8. UI 合同

### 8.1 平台目录

- 主导航名称为“Runner 规格／Runner Specs”。
- 页面只显示已启用的平台规格、工作流标签和可公开的稳定模板名；停用规格仅在 Admin 后台可见。
- 每项支持复制可直接用于 GitHub Actions 的 `runs-on` 标签。
- 不显示来源／状态 Badge、scope banner、scope selector、Refresh、编辑、重置、启停或并发详情。
- 不加载 Settings 专用的 GitHub App、Preferences、Onboarding 或 Sandbox 模板资源。

### 8.2 自定义规格 Settings

- Tab 名称为“自定义 Runner 规格／Custom Runner Specs”。
- 页面只有一个与其他 Settings 页面一致的卡片，不重复显示说明区块或手动“刷新”。
- 创建／编辑表单纵向排列，避免窄对话框的双列错位。
- Sandbox 模板选项同时显示易读名称和 ID，便于区分同名或无别名模板。
- `runs-on` 输入提供工作流预览，标签顺序不影响匹配。
- 最大并发默认 `10`，并说明 `0` 表示不额外限制。
- 保存期间阻止重复提交；失败保留表单；成功后只刷新原请求 scope。
- 覆盖全局同标签规格时，在保存前提示，并在列表中标记覆盖关系。

## 9. 向下兼容结论

- Admin `/runner_specs`、Admin UI、全局 `runner_profiles` 和全局匹配合同不变。
- 没有 scoped custom 匹配时，现有 workflow 继续使用原全局目录。
- 历史 request 的空 `profile_source` 继续按 global 处理。
- 新 schema 只增量添加 scoped custom 与 request 身份字段；不删除历史列或表。
- 早期分支的 scope-control API 尚未上线，因此移除不是对 main 的破坏性变更；遗留试验表保持 inert，确保升级无数据破坏。
- 未发布的 `/account/runner-types` 路由无需保留；用户路由统一为 Runner Specs。

## 10. 验证与发布门禁

本地变更至少执行：

```bash
go test ./internal/state -count=1
go test ./internal/server -count=1
cd ui && bun run test
task ui-i18n-check
task build
```

State／API 回归必须证明：

- Fresh schema 不创建 `runner_profile_scope_controls`。
- 人工存在的遗留 control 行不影响全局匹配和容量。
- 已移除 control URL 的 PUT／DELETE 不再提供 Mutation。
- User 列表只返回简化后的全局只读策略字段。
- scoped custom 的跨 scope 隔离、精确标签覆盖、停用屏蔽、CAS 和原子审计保持有效。
- 准入后修改全局或 scoped custom 规格时，生命周期使用最新状态。

UI 回归必须证明：

- `/runner-specs` 只展示已启用的平台规格，且没有来源／状态 Badge、scope selector、控制按钮、并发详情和额外数据依赖。
- Settings 只显示 `scoped_custom`，账户表单不显示 Runner Group。
- Organization-only Runner Group、模板名称加 ID、默认并发 `10`、纵向表单和 stale-scope response isolation 保持有效。

以下仍是生产发布门禁，不能用本地单元测试替代：

- 专用 PostgreSQL／MySQL fresh-schema 与 audited-mutation 矩阵。
- 生产 SQLite snapshot 升级和已记录基线的降版本启动检查。
- 覆盖三个 Runner Spec 路由的 fixture-backed production browser smoke。
- 真实账户和 Organization 的 Sandbox 模板验证及 GitHub Actions E2E。
- 部署环境 canary、版本、日志和清理证据。
