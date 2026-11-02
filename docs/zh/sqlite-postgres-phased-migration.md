# SQLite 到 PostgreSQL 分阶段迁移

目标是把耗时的预复制和验证放在服务正常运行期间，只在最后同步变化、封锁旧库和切换配置时停服。仍使用独立的 `runnerd-migrate`，不需要 runnerd 双写。原来的[完整停服迁移](sqlite-postgres-offline-migration.md)保留为默认 `--mode offline`。

## 阶段与边界

| 命令 | SQLite runnerd | PG runnerd | 行为 |
| --- | --- | --- | --- |
| `prepare` | 可运行 | 必须停止 | 在线备份，绑定源和目标，安装变更捕获及写入保护，初始化空 PG schema |
| `sync` | 可运行 | 必须停止 | 刷新其他表，复制请求存量、追请求变化，按需复制事件；可重复、中断、续跑 |
| `status` | 按当前阶段 | 按当前阶段 | 显示持久化检查点、待追请求变更数、其他表行数和待复制事件行数 |
| `cutover` | 必须停止 | 必须停止 | 检查工作量，封锁 SQLite，最终同步、校验和保留序列，放行 PG |
| `resume-source` | 必须停止 | 必须停止 | 仅 PG 尚未放行时解除 SQLite 封锁，随后可恢复 SQLite 服务 |
| `backfill` | 保持停止和封锁 | 可运行 | 只追加缺失历史事件并校验；不会更新请求或重置序列 |

任务结束后仍可能补写退出码、Runner 版本、GitHub 结果，失败请求也可重试。工具使用 SQLite 触发器记录请求变更键，包含删除及不修改 `updated_at` 的写入，不把终态或时间戳当作不可变保证。每批请求使用同一个源库快照，确认目标中的旧请求在源库中是否仍拥有该 Job ID，包含同一请求 ID 已被重新创建、但 Job ID 已改变或为空的情况；旧行清理、新行写入校验和进度保存同属一个 PG 事务，避免待回放的删除阻塞基线。变更日志继续用于恢复重新创建的请求并回放后续变化。其他 16 张表在每次同步及最终切换时按一致性读快照刷新，包含删除。

请求记录必须全部迁齐才能放行 PG，保留 Job 去重和恢复依据。可以延后的是 `runner_events`，历史日志在补齐前可能不完整。事件在迁移期间必须保持追加：修改、删除或插入倒退 ID 会明确失败。源库所有业务表在切换时持久化封锁，旧 runnerd 的写操作也会被触发器拒绝。

## 白天在线准备

1. 构建包含分阶段功能的 `runnerd` 和 `runnerd-migrate`，保留正常服务的 SQLite 配置。准备单独的 `runnerd.postgres.yaml`、专用空 PG schema 和原来的加密/session 密钥；不要提前启动 PG 服务。目标账户需要建表、函数、触发器及序列权限。
2. 源库必须已经是 WAL 模式。工具不在运行中的库上自动切换日志模式，不对运行中的库使用 `immutable=1`，也不持有覆盖整个迁移的 SQLite 写锁。首次准备的 `VACUUM INTO` 是在线一致性备份，但仍会消耗 CPU、I/O 和空间，长读期间 WAL 可能增长。先在代表性负载下演练。
3. 使用固定的源文件、目标 DSN/schema 和工具版本执行：

   ```sh
   install -d -m 700 ./var/offline-backups
   ./runnerd-migrate --mode prepare \
     --config ./runnerd.postgres.yaml --sqlite "$PWD/var/runnerd.db" \
     --backup "$PWD/var/offline-backups/online-before-postgres-20261102.db" \
     --timeout 2h > ./var/offline-backups/prepare.json
   ```

   目录必须由当前用户拥有且为 `0700`，备份文件为 `0600`，不覆盖已有文件。首次准备失败且尚未安装源捕获时，失败备份保留，下次选择新路径。已经绑定源后，重复 `prepare` 继续初始化同一目标，不重新生成备份。
4. 先把服务需要的数据同步好，把大日志留到之后：

   ```sh
   ./runnerd-migrate --mode sync --skip-events \
     --config ./runnerd.postgres.yaml --sqlite "$PWD/var/runnerd.db" \
     --timeout 2h > ./var/offline-backups/sync.json
   ```

   去掉 `--skip-events` 可以在白天同时预复制日志。请求基线及事件以 256 行为批次，每批逐行校验并与检查点一起提交；超时、Ctrl-C 或进程退出后重复同一命令续跑。其他表按表事务刷新。CLI 默认在批次之间暂停 `10ms`，可以用 `--batch-pause 50ms` 降低持续负载；切换阶段不暂停。不要仅因超时就清空 PG 或删除迁移元数据。
5. 持续运行 `sync` 追平，并检查：

   ```sh
   ./runnerd-migrate --mode status \
     --config ./runnerd.postgres.yaml --sqlite "$PWD/var/runnerd.db" \
     > ./var/offline-backups/status.json
   ```

   `ready_for_cutover` 表示请求存量完成，待追变化和其他表行数在预算内；不等于已经停服或所有历史已齐。`requests_copied` 是请求基线处理数，不是当前请求总数；`pending_request_changes` 是日志条目数，同一请求可能出现多次。`pending_event_rows` 是观察到的剩余事件行数。源仍在写入时，状态仅供安排窗口，切换会再次检查。

## 短暂停服切换

1. 暂停新任务入口，安排 webhook 暂存或维护后补投；停止 SQLite runnerd 和自动重启器。按既有维护流程处理运行中的 Runner/Deployment，不能把数据库迁移测试当成真实任务恢复验收。确认 PG runnerd 仍未运行。
2. 执行：

   ```sh
   ./runnerd-migrate --mode cutover --source-stopped \
     --config ./runnerd.postgres.yaml --sqlite "$PWD/var/runnerd.db" \
     --timeout 10m > ./var/offline-backups/cutover.json
   ```

   默认最多接受 `10000` 条未同步请求变更，以及其他表合计 `100000` 行；超预算在封锁 SQLite 前拒绝。先在线追平和演练，必要时明确调整 `--max-final-changes`、`--max-final-rows`。行数预算不是耗时保证，其他表若很大仍会延长最终同步；不要盲目放宽。切换不重做完整备份、不重读全部已验证请求内容、不等待所有历史日志复制。
3. 仅在退出码为 `0`、`ready_for_pg: true` 且 `source_sealed: true` 后，使用 PG 配置启动一个 runnerd，验证健康检查、登录、任务去重/恢复及新任务生命周期，再恢复入口并补投对账。`history_complete: false` 不阻止切换，它只表示历史事件尚未全部补齐。

`cutover` 成功会永久封锁旧源并放行 PG 写入。工具在放行前一次性保留所有自增序列高水位，包括尚未复制的事件；后续 PG 新日志使用更大的 ID。重复 `cutover` 不会再次同步旧状态或重置序列。

## 切换后补历史

```sh
./runnerd-migrate --mode backfill \
  --config ./runnerd.postgres.yaml --sqlite "$PWD/var/runnerd.db" \
  --timeout 2h > ./var/offline-backups/backfill.json
```

保留原 SQLite 文件及其 WAL/SHM 生命周期，不恢复它的写入。工具读取被封锁的源，只插入历史事件，遇到相同 ID 必须内容一致；不覆盖 PG 的请求状态、配置、授权或新事件，不改变序列。可以反复执行，直到 `history_complete: true`。完整性检查覆盖冻结历史前缀，不把 PG 切换后新增的事件计作多余数据。

## 失败、恢复和限制

- `prepare`、`sync` 失败：SQLite 继续服务，PG 保持拒绝业务写入；修复原因后重跑。已经验证并提交的请求/事件批次保留。
- `cutover` 开始最终同步后失败：SQLite 可能已封锁。先运行 `status`。若 PG 仍为 `copying`，可重跑 `cutover`；或在服务都停止时执行同样参数的 `--mode resume-source --source-stopped`，成功后用原 SQLite 配置恢复服务。随后再次 `sync`，捕获会继续记录变化。
- PG 已为 `ready` 或 `complete`：`resume-source` 明确拒绝，即使操作者认为尚无业务写入。提交回执丢失时先查询状态，不能假定事务回滚。放行 PG 后不提供无损反向同步。
- `backfill` 失败：PG 服务可继续工作，检查错误并重跑补历史。冲突不能通过 `ON CONFLICT DO NOTHING` 静默跳过。
- 迁移期间不得换源文件、恢复旧备份到源路径、手改数据/触发器/检查点、升级模型版本或把源复制成第二个写入实例。工具绑定源身份、目标连接/schema 和模型指纹，并核对保护触发器。迁移工具实例由 PG advisory lock 串行化；数据库拥有者仍能执行 DDL，工具不能防止人为拆除保护后篡改历史。
- 准备时的备份是较早快照。最终准确的历史来源是切换时封锁并保留的原 SQLite；不能用早期备份替换它来补历史。额外历史表、额外业务列保留在源和备份，不导入模型以外的数据。
- 没有后台双写、自动切换、反向复制或多 runnerd 实例支持。旧在线迁移协议的未收尾数据库仍被拒绝。

阶段进度和每 10 秒心跳写 stderr，命令结果 JSON 写 stdout。`--timeout` 只约束本次运行；分阶段模式不再声称全库单事务回滚。保留每次报告及进度日志，避免重定向时覆盖之前的验收证据。

## 验证

```sh
RUNNERD_CATALOG_BACKEND_TESTS=1 \
RUNNERD_POSTGRES_TEST_DSN='<专用 _test 数据库 DSN>' \
go test -race ./internal/state ./cmd/runnerd-migrate \
  -run 'Test(PhasedMigration|OfflineMigration|MigrationProgress|RunPhasedMigration)' -count=1
```

测试使用随机 schema，覆盖在线写入、无时间戳变化的补写、删除、分页续跑、准备中断、切换失败恢复、源/目标保护、工作量预算、冲突拒绝和切换后业务写入保护。生产 5.2 GB 数据的耗时、WAL/磁盘增长、日间负载、完整备份恢复和真实 Runner/Deployment 切换验收仍需单独演练。
