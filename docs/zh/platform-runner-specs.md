# 平台 Runner 规格管理

数据库是平台 Runner 规格的权威来源。管理员在 `/admin/runner_specs` 维护所有规格，启动不再创建或协调代码内置目录。新安装的目录为空，需要恢复数据库备份或在后台创建规格后再提交 workflow。

## 模板绑定与公开发布

| 字段 | 约束 |
| --- | --- |
| `template_source` | `public` 绑定稳定公共名称；`private` 绑定明确的物理 ID。 |
| `default_template_name` | 公共绑定必填；每个请求按有效 Sandbox 区域独立解析。 |
| `template_id` | 私有绑定必填；公共绑定必须为空。 |
| `published` | 明确选择发布到公共目录；私有绑定不能发布。 |

新建规格默认使用私有绑定，不进入公共目录。后台只增加模板绑定和目录展示配置，不新增 Runner 准备、Docker 或 fork 赞助策略开关。公共名称规格沿用原托管规格的行为：使用预装 Runner，要求 Docker 就绪，fork 赞助仍须通过组织原有授权策略；私有 ID 规格沿用原自定义规格的行为：注册前检查并更新官方 Runner，Docker 尽力配置，不使用赞助。两类规格均保留 GitHub Runner 官方自动更新，目录公开展示不会授予凭据访问权。

创建、更换绑定、发布、重新启用已发布规格时，只使用配置的 **Admin** Sandbox endpoint/key 校验，即使运行时 fallback 已关闭。公共名称必须唯一解析到 `public: true` 且状态为 `ready` 或 `uploaded` 的模板；私有 ID 保留原来的访问权限和有效默认构建检查。本地字段先校验，远程调用总计限时 5 秒，并位于审计事务之外。拒绝或过期保存不修改规格和审计记录。

`/runner-specs` 仅展示启用且已发布的公共规格。`/user/runner-specs` 以 `platform_public` 返回已发布公共条目，并返回所选 scope 自有的 `scoped_custom` 条目，省略未发布或私有的平台规格。只有 scope 自定义条目可返回物理 ID 和 GitHub Runner Group。`GET /api/public/runner-templates` 按稳定名称汇总启用且已发布的公共规格，只公开名称和 workflow labels，每个服务进程将公开投影缓存 60 秒，并合并并发加载；Admin 成功保存或删除规格后会使当前进程缓存失效，浏览器仍可缓存响应 60 秒。Provider 目录仍是独立、受凭据约束的资源。Large 规格配置并通过公共名称校验后也可发布，不能仅根据名称自动归入公共目录。

## 修改与升级兼容

名称保持为稳定资源标识。Admin 可编辑标签、必需标签、模板绑定、GitHub Runner Group、优先级、启用状态、容量及目录展示设置。匹配继续遵守 `required_labels ⊆ job_labels ⊆ labels` 和 scope 精确覆盖。全局规格仍有 queued、creating、running 或 stopping 请求时，修改执行配置或删除会返回 `409 runner_spec_in_use`；容量、启用状态和发布状态仍可调整。CAS 拒绝过期保存，数据修改与审计事件原子提交。

增量迁移只转换尚无模板来源的旧行，保留标签、名称、管理员策略、时间戳和索引。绑定公共名称的旧托管行保留公开展示、预装 Runner、Docker 和赞助行为，清空已无用途的物理模板 ID。旧私有行保持不公开并使用官方更新准备；清空不参与运行的残留公共模板名称，避免后续修改其他字段时校验失败，同时保留物理模板 ID。缺少公共名称的历史托管行仍保持公共绑定且不公开，不将它自动改成可运行的私有规格。旧 `managed_by`、`catalog_revision` 列和值保留为不参与行为判断的兼容元数据。历史空／global 请求来源和保存的 Sandbox 快照仍有效。后续修改和删除在重启后保持，迁移不补建缺失规格，也不自动提升私有 large 规格。

升级前备份数据库。旧版本不理解新增目录字段，仍可能恢复代码内置规格，因此降级行为需要单独验证。未发布开发版本曾增加的 `runner_update_policy`、`require_docker` 和 `fork_sponsorship` 列不再创建或使用；已有这些列的数据库保留列和值，不做破坏性删除。另一个已退役的 `runner_profile_scope_controls` 表若已存在，也保持完整且不参与行为判断。修改公共别名后需验证 workflow 和区域运行 smoke，因为元数据校验不证明镜像内容。镜像构建、版本 pin 和发布仍由模板发布流程维护。
