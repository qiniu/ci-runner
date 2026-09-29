# 组织赞助的 Fork Runner

状态：已发布到生产环境，并于 2026-09-29 完成符合条件和拒绝对照的真实 Fork 验收。平台使用流程见公开的 [Fork 赞助指南](https://runner.qiniuinc.com/docs/guides/fork-sponsorship)。

## 问题与产品边界

组织仓库中的 Job 已经使用该仓库的 GitHub App installation 和有效 Sandbox 服务，
包括 head 来自 Fork 的 `pull_request` Job。成员个人 Fork 中直接触发的 Workflow
使用个人 installation，因此当前必须配置个人 Sandbox 服务。

新能力是**组织赞助**，不是凭据继承：

- 组织 owner 为精确的上游仓库启用赞助；
- Job 仍在 Fork 仓库注册 Runner，只有 Sandbox 创建使用赞助组织的服务；
- 组织凭据和 Provider 资源不向贡献者暴露；
- 新策略默认禁用，必须显式开启；
- 现有全局和 Runner Spec 限制继续生效，每个策略另有正数并发上限。

策略支持三种准入模式：

| 模式 | 准入条件 |
| --- | --- |
| `approval_required` | 组织 owner 已批准精确的 Fork 仓库。 |
| `write_permission` | 个人 Fork owner 当前对上游仓库具有 `write`、`maintain` 或 `admin` 权限。 |
| `organization_member` | 个人 Fork owner 当前是赞助组织的 active member。 |

`approval_required` 是推荐模式和 UI 默认值。自动模式仅适用于个人 Fork owner；组织
所有的 Fork 必须精确批准，避免赞助隐式扩散到另一个组织。

## 信任与解析

运行时只使用 GitHub API 元数据，不能信任浏览器提交的仓库关系或名称约定。

1. 使用 Fork installation 解析排队请求的仓库；
2. 要求 `fork: true`、稳定 repository／owner ID 和原始 `source` 仓库；
3. 按 source repository 稳定 ID 查找启用策略；
4. 确认 source owner 仍匹配赞助组织 installation，仓库转移 fail closed；
5. 创建 Sandbox 前重新检查准入条件，GitHub 查询失败时本次启动 fail closed；
6. GitHub I/O 不持有启动锁；完成远程检查后，在短时 per-policy 临界区内重新检查策略和精确审批、检查容量并保存 request 快照；
7. 在 Runner request 保存加密 Sandbox 配置和不含凭据的赞助来源。

Managed Runner request 的解析顺序变为：request 快照、Fork installation 配置、个人
账户回退、符合条件的组织赞助（优先使用 sponsor scope 配置，未配置时可使用受众包含
赞助组织的平台默认值）、赞助缺失或不符合条件时适用于 Fork installation 的平台默认值、
未配置错误。通过平台默认值完成的赞助请求仍保留组织赞助来源。

Fork 或个人配置损坏时继续报错，不能回退。赞助只适用于 runnerd-managed Runner
Specs；platform custom 和 scoped custom Specs 继续使用所属作用域凭据。

## 状态模型

`fork_sponsorship_policies` 保存 sponsor installation、稳定 source repository ID 和
full name、模式、启用状态、正数最大并发和时间戳。Sponsor installation 与 source
repository ID 组成主键；source repository ID 全局唯一，因此一个 Fork 网络只有一个
赞助者。

`fork_sponsorship_approvals` 将策略绑定到稳定 Fork repository ID、full name、owner
ID 和 login。运行时仍检查当前 Fork/source 关系，因此仓库转移和 Fork 网络变化会
fail closed。

赞助 Runner request 保留 sponsor installation ID、source repository ID／full name
和授权原因。加密 Sandbox Key 继续使用现有隐藏快照字段。fresh retry、中断创建后的
requeue 和 mismatched-job requeue 都会清除两类快照，从而重新检查被撤销或修改的策略。

## API 与 UI

owner 可管理的 Organization scope 暴露：

- `GET /user/fork-sponsorship-repositories?installation_id=...` 列出同时对当前登录
  用户和所选 GitHub App installation 可见、由当前组织拥有且不是 Fork 的仓库；
- `GET /user/fork-sponsorship-policies?installation_id=...`
- `POST /user/fork-sponsorship-policies?installation_id=...` 按 full name 解析并创建 source repository 策略；
- `PUT /user/fork-sponsorship-policies/{source_repository_id}?installation_id=...`
- `DELETE /user/fork-sponsorship-policies/{source_repository_id}?installation_id=...`
- `POST /user/fork-sponsorship-policies/{source_repository_id}/approvals?installation_id=...`
- `DELETE /user/fork-sponsorship-policies/{source_repository_id}/approvals/{fork_repository_id}?installation_id=...`

仓库元数据在带审计的数据库事务前验证。数据和 audit evidence 原子提交；被拒绝的写入
不产生 audit event。

Organization Settings 增加 **Fork sponsorship** 标签页，个人账户不显示。页面管理
策略创建、启用状态和并发，不暴露凭据或 Provider 目录。新策略通过可搜索的组织仓库
选择器创建；准入方式为互斥单选，仅在“需要精确审批”模式下显示精确 Fork 审批。

赞助不能扩大 Settings 权限，也不能扩大普通用户现有的精确
`(installation_id, repository_full_name)` Job 授权交集。

## 明确不包含

- 不继承组织 Cache S3；继续使用 Fork／个人账户解析，未配置时不启用 Cache S3；
- 不共享组织 custom Runner Specs、物理 template ID 或 Runner Group；
- 不授予 Settings、Provider catalog、audit log 或无关仓库权限；
- 不回填或重新分类历史 Job。

## 失败与撤销

- Fork 元数据无效／缺失和禁用／不符合条件的策略视为没有赞助，继续现有平台默认解析；
- 符合条件的 sponsor 未配置 scoped Sandbox 服务时，只有平台默认值的受众包含赞助组织
  才能继续使用该默认值；保存的 request 来源仍为组织赞助；
- 符合条件但组织 Sandbox 配置损坏时 fail closed，不能用平台默认值掩盖 owner 配置错误；
- 策略达到容量时以可重试结果延后，不能更换付费来源；
- 删除或禁用策略阻止新启动和 fresh retry，但不终止已经运行的 Sandbox；
- 清理和恢复使用 request 快照，因此撤销不会遗留已创建的 Sandbox。

## 验证

覆盖必须包含 GitHub 元数据／权限客户端、fresh／旧 SQLite schema、可用于
PostgreSQL/MySQL 的审计事务、三种准入模式、转移／撤销／容量／retry、managed-only
限制、owner/member/collaborator API 鉴权、UI 路由隔离和中英文 i18n 一致性。

本地门禁为 `go test ./internal/state -count=1`、GitHub/server 聚焦测试、
`task ui-i18n-check`、`task test` 和 `task ui-production-smoke`。专用 PostgreSQL/MySQL
数据库和生产 SQLite snapshot 是外部发布门禁。生产验收需要一个符合策略的 Fork Run 和
一个拒绝对照 Run，并保留清理与来源证据。

## 交付状态

- [x] 核对当前 installation 与 Sandbox 解析行为。
- [x] 定义产品、信任、状态、API 和验证边界。
- [x] 增加 GitHub 元数据和权限客户端。
- [x] 增加状态模型、带审计 mutation 和 migration 覆盖。
- [x] 增加 owner-only 策略 API。
- [x] 增加运行时解析和 request 来源快照。
- [x] 增加 Organization Settings UI 和多语言文案。
- [x] 同步运维、测试、架构和 agent 文档。
- [x] 通过本地验证门禁。
- [x] 完成符合条件和拒绝对照的真实 Fork 部署验收。
