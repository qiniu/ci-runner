# SQLite 到 PostgreSQL 迁移退役记录

一次性 `runnerd-migrate` 命令及其停服、分阶段迁移实现已退役，当前构建只生成 `runnerd`。SQLite、PostgreSQL 和 MySQL 仍受支持；正常 schema 升级和历史数据兼容保持不变。

## 历史操作记录

生产迁移于 2026-11-02 记录为完成：cutover 和 backfill 均成功退出，`phase=complete`、`history_complete=true`，两个待处理计数均为零。最终报告记录已校验 17,928,514 条历史事件，正式服务及发布配置已确认使用 PostgreSQL。详见[完成记录](https://github.com/qbox/sandbox/issues/535)（需要仓库访问权限）。

实现和手册保留在退役前的固定版本：

- [分阶段迁移手册](https://github.com/qiniu/ci-runner/blob/def63260e4ba16f903f8d750c61915ceb68733ad/docs/zh/sqlite-postgres-phased-migration.md)
- [完整停服迁移手册](https://github.com/qiniu/ci-runner/blob/def63260e4ba16f903f8d750c61915ceb68733ad/docs/zh/sqlite-postgres-offline-migration.md)
- [迁移命令源码](https://github.com/qiniu/ci-runner/blob/def63260e4ba16f903f8d750c61915ceb68733ad/cmd/runnerd-migrate)

以上是历史流程，不是当前版本可执行的命令。未完成的迁移必须使用与其匹配的工具、模型版本和原恢复协议；不得将旧导入器用于更新后的运行中 schema，也不得重放已完成的切换。

## 保留的启动保护

runnerd 在 schema 升级或数据补写之前，仍拒绝已封锁的 SQLite 源库、尚未开放的 PG 目标库、未知迁移协议和未收尾的旧在线迁移。协议 1 下，SQLite 的 `copying`、PG 的 `ready` 和 `complete` 沿用原有允许启动行为；旧在线协议的有效 standalone 标记仍可使用。已退役的 `database.online_migration` 配置继续明确报错。

启动仅检查这些标记，不删除迁移表、触发器、函数、备份或封锁。此保护与正常 GORM schema 升级、SQLite 增量升级、Runner Spec 策略兼容相互独立。

## 后续运维事项

源码退役不会执行生产数据库清理。另行安排清理前应：

1. 确认 PostgreSQL 定期备份及恢复演练，按保留策略保存最终报告、封锁的原 SQLite、首次备份、配置和密钥。PG 接受业务写入后，原 SQLite 不再是可无损回退的目标。
2. 只读核对实际数据库对象和完成标记。分阶段工具在 `complete` 后仍保留目标库的 `runnerd_transfer_guard`、`runnerd_transfer_truncate_guard` 触发器及 `runnerd_transfer_guard` 函数，普通写入仍执行行级保护。提出 DDL 前先核对依赖。
3. 审查仅删除已完成目标库迁移对象的事务方案、依赖顺序和锁等待预算。不得在触发器仍引用控制表时先删表。保留源库封锁、业务行及序列水位，操作后验证应用启动和写入；不要在应用启动时自动清理。

本次源码清理不代表 PG 备份恢复验证或生产对象清理已完成。生产迁移验收也不代表多实例运行或 Web Terminal 全程无中断已验证。
