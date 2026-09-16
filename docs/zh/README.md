# 文档索引

[English](../README.md)

这些文档和根目录 `README.zh.md` 配合阅读。

面向最终用户的中英文公开指南位于 [runner.qiniuinc.com/docs](https://runner.qiniuinc.com/docs)，覆盖托管版上手、runnerd 部署、managed workflow、自定义模板从构建到使用的完整流程和故障排查。中英文 Markdown 源文件成对放在 `ui/src/content/site-docs/{en,zh}/`，确保部署的 UI、指南内容和导航一同交付。

- [本地测试与 GitHub 配置](testing.md)：本地配置、GitHub App/OAuth 设置、webhook forwarding、admin API 示例和排障流程。
- [部署 Smoke Checklist](deployment-smoke.md)：面向真实 GitHub App、webhook、Qiniu sandbox template、runner pickup、cleanup 和 diagnostics 的生产风格 smoke checklist。
- [公共 Runner 模板](default-runner-templates.md)：qshell 环境要求、双区域构建与发布顺序、Sandbox smoke 证据、维护责任和回滚流程。
- [Runner 架构对比](runner-architecture-comparison.md)：当前 runnerd 架构基线、Mermaid 系统/生命周期/状态图、DB-backed state model，以及和 Fireactions、Actions Runner Controller 的对比。
- [Runnerd 实现评审](runner-implementation-review.md)：当前实现状态、schema migration notes，以及剩余产品/运维决策。
- [用户作用域 Runner 配置](../user-scoped-runner-configuration.md)：记录个人账户与 Organization 作用域 Runner 管理的已实现需求、架构、历史执行过程、测试、回滚和仍未完成的发布门禁。

根目录 `README.zh.md` 是中文 operator quick start。`TODO.md` 记录仍待决策的事项，不应重复已经在这些文档中描述的已完成行为。

Agent-only rules 和 repeatable workflows 放在 `.agents/` 下。业务、运维、架构和部署文档保留在 `docs/`；只有 durable agent guidance 或可执行 agent workflow instructions 才移动到 `.agents/rules/` 或 `.agents/skills/`。
