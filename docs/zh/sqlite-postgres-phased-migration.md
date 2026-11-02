# SQLite 到 PostgreSQL 迁移操作手册

本手册采用 `runnerd-migrate` 分阶段迁移：**服务运行时预复制 → 短暂停服完成切换 → PG 服务运行时补历史日志**。先阅读切换和恢复边界，再按阶段执行；不要一次性粘贴执行整篇命令。

仅发布新版 `runnerd` 和 `runnerd-migrate`、继续使用原 SQLite 配置，不会自动启动迁移。需要全程停服、一次事务完成导入时，使用另一份[完整停服迁移手册](sqlite-postgres-offline-migration.md)。两种流程不能混用；本手册每次都显式指定 `--mode`，省略它会进入 `offline` 模式。

## 1. 确定这次先做到哪一步

| 阶段 | SQLite runnerd | PG runnerd | 完成条件 |
| --- | --- | --- | --- |
| 发布新版 | 正常服务 | 不启动 | 原配置启动、健康检查及真实任务正常 |
| `prepare` | 可运行 | 不启动 | 在线备份完成、源和目标绑定、目标处于 `copying` |
| `sync --skip-events` | 可运行 | 不启动 | 请求基线完成，变化追平到切换预算内 |
| `status` | 按当前阶段 | 按当前阶段 | 读取状态，不切换服务 |
| `cutover` | 所有写入者停止 | 不启动 | 成功退出，`ready_for_pg: true` 且 `source_sealed: true` |
| 改配置并启动 PG runnerd | 保持停止 | 只启动一个实例 | 健康检查、登录、真实任务及部署验收通过 |
| `backfill` | 保持停止和封锁 | 可运行 | `history_complete: true` |

第一次在生产环境验证时，建议先完成发布、`prepare` 和 `sync`，观察服务和资源使用，再单独安排切换窗口。在线迁移仍会消耗 CPU、磁盘 I/O 和空间，不保证对业务延迟没有影响。

**`cutover` 成功就是切换边界。** PG 一旦进入 `ready` 或 `complete`，工具就拒绝恢复 SQLite 写入，即使尚未启动 PG runnerd。它不提供双写、反向复制或无损切回 SQLite 的功能。

## 2. 发布与准备配置

### 二进制与运行环境

- 从同一版本构建、发布 `runnerd` 和 `runnerd-migrate`；源码构建使用 `task build`，产物为 `bin/runnerd` 和 `bin/runnerd-migrate`。
- 先保持原 SQLite 配置启动新版 runnerd，验证健康检查、任务执行、日志和恢复行为。迁移期间固定工具版本，不升级业务模型。
- 确认服务实际使用的 SQLite 路径。下面的 `./var/runnerd.db` 只是部署目录示例，必须与运行中的服务配置一致。
- 源库必须已经使用 WAL。不要为了迁移在运行中的数据库上修改日志模式，也不要直接复制正在使用的 `.db` 文件作为一致性备份。
- 预留备份、SQLite WAL/变更日志以及 PG 数据/WAL 的磁盘空间。使用能读写源库、创建私有备份目录的同一服务用户运行工具。

### PostgreSQL 配置

保留现有 SQLite 配置，另外准备受保护的 `runnerd.postgres.yaml`。以完整的现有配置为基础，只调整数据库连接；保留 `auth.encryption_key`、`auth.session_secret`、GitHub/Sandbox 配置和密钥文件。将配置放在最终使用的位置，避免相对路径变化。

数据库部分示例（需要替换目标地址和凭据，不是完整配置）：

```yaml
database:
  backend: postgres
  dsn: "host=PG_HOST port=5432 user=ci_runner dbname=ci_runner password=RUNNERD_ENC(...) sslmode=disable"
```

`sslmode` 按实际网络和数据库要求配置。当前版本支持明文密码、仅密码使用 `RUNNERD_ENC(...)`、以及整段加密 DSN；`RUNNERD_ENC(...)` 必须是工具生成的有效值，不可直接复制占位符。生成方式见[README 的配置说明](../../README.zh.md#配置值混淆)。`psql` 不认识这个加密格式；手工连通性测试必须使用相同 host、port、user、dbname、sslmode 和真实密码，避免把凭据放入命令历史或分享输出。

目标必须是专用空 PG schema，账户需要建表、函数、触发器和序列权限。此前失败迁移留下的目标必须先确认状态；不要为了满足“空 schema”要求盲目删除数据。整个预复制阶段不要启动使用目标 PG 的 runnerd。旧版在线迁移协议的未收尾数据库需先按原协议收尾，不能删除标记绕过保护。

### 在服务器打开操作终端

以下命令使用 **Bash**。先进入实际发布目录，再执行 `bash`；后续命令在同一个终端中运行。若 SSH 断开，重新进入目录并设置下面三个变量，不要复用未确认的源路径或改变目标 schema。

```bash
cd ~/qiniu-ci-runner/_package
bash
```

```bash
umask 077
SQLITE_PATH="$PWD/var/runnerd.db"
PG_CONFIG="$PWD/runnerd.postgres.yaml"
MIGRATION_DIR="$PWD/var/postgres-migration"
install -d -m 700 "$MIGRATION_DIR"

./runnerd-migrate --help
test -f "$SQLITE_PATH" && test -r "$PG_CONFIG"
```

检查 `--help` 包含 `prepare`、`sync`、`cutover`、`backfill`，且文件检查成功。确认配置文件权限限制为服务用户可读，例如 `chmod 600 "$PG_CONFIG"`。

若安装了 sqlite3，可只读确认 WAL：

```bash
sqlite3 -readonly "$SQLITE_PATH" 'PRAGMA journal_mode;'
```

预期为 `wal`。工具也会检查；若不是，停止本流程并另行安排日志模式调整。

下面每次执行都会创建独立报告目录：`report.json` 保存成功结果，`progress.log` 保存进度，同时终端仍显示进度。`2> >(tee ... >&2)` 是 Bash 的日志分流语法；不要替换成 `2>&1`，否则会破坏 JSON。退出码紧接命令保存，避免被 `cat`、`echo` 等覆盖。

## 3. 在线准备：prepare

SQLite runnerd 可以继续服务。首次执行会在线备份并校验，再安装请求变更捕获和写入保护，初始化 PG。

```bash
RUN_DIR=$(mktemp -d "$MIGRATION_DIR/prepare.XXXXXX") || exit 1
./runnerd-migrate --mode prepare \
  --config "$PG_CONFIG" --sqlite "$SQLITE_PATH" \
  --backup "$RUN_DIR/before-postgres.db" --timeout 2h \
  > "$RUN_DIR/report.json" 2> >(tee "$RUN_DIR/progress.log" >&2)
migration_exit=$?
printf '退出码：%s；报告目录：%s\n' "$migration_exit" "$RUN_DIR"
cat "$RUN_DIR/report.json"
```

只有退出码为 `0`，且报告为 `phase: "copying"`、`source_sealed: false`，才继续同步。备份目录要求当前用户所有、权限 `0700`；备份文件为 `0600`，不会覆盖已有文件。

备份产生不代表准备完成。若失败，保留该目录，修复原因后使用新的 `RUN_DIR` 重跑同一源和目标。源绑定尚未建立时会生成新备份；源已绑定后，重跑会继续初始化而不重新备份，因此本次目录可能没有新的 `.db`，应保留首次备份。

从此阶段开始，请求写入会记录变更日志，事件只允许追加，禁止修改、删除或插入倒退的事件 ID。停止迁移命令不会卸载捕获触发器，变更日志仍会增长；不要长期搁置而不观察磁盘，也不要手动删除触发器或日志。

## 4. 在线预复制：sync

先同步请求及其他业务表，暂缓历史事件：

```bash
RUN_DIR=$(mktemp -d "$MIGRATION_DIR/sync.XXXXXX") || exit 1
./runnerd-migrate --mode sync --skip-events \
  --config "$PG_CONFIG" --sqlite "$SQLITE_PATH" \
  --batch-pause 50ms --timeout 2h \
  > "$RUN_DIR/report.json" 2> >(tee "$RUN_DIR/progress.log" >&2)
migration_exit=$?
printf '退出码：%s；报告目录：%s\n' "$migration_exit" "$RUN_DIR"
cat "$RUN_DIR/report.json"
```

可重复执行整个代码块；每次保留独立报告。请求和事件按 256 行一批校验并提交检查点；超时或 Ctrl-C 后，等待进程退出，已提交批次可续跑，失败批次回滚。其他 16 张表每次按表一致性快照刷新，包含删除。分阶段模式不是全库单事务回滚。

示例使用 `--batch-pause 50ms` 降低持续复制负载，默认是 `10ms`。暂停只作用于适用的复制批次，不能限制备份、单条查询或整张小表刷新，也不是整体资源上限。观察 runnerd 响应、任务状态、错误日志、CPU、I/O、WAL 和剩余空间。发现业务影响时可中止在线同步，待进程退出后再调整；不要在不了解状态时中止切换后直接恢复 SQLite。

希望提前复制历史事件，可去掉 `--skip-events`。**所有请求必须在切换前同步完成，只有历史事件可以留到 PG 服务启动后补齐。** 终态请求仍可能补写退出码、Runner 版本和 GitHub 结果，不能当作永远不变的数据。

## 5. 查看进度并安排停服窗口

状态检查可在另一个终端执行，先设置相同路径变量：

```bash
RUN_DIR=$(mktemp -d "$MIGRATION_DIR/status.XXXXXX") || exit 1
./runnerd-migrate --mode status \
  --config "$PG_CONFIG" --sqlite "$SQLITE_PATH" --timeout 5m \
  > "$RUN_DIR/report.json" 2> >(tee "$RUN_DIR/progress.log" >&2)
migration_exit=$?
printf '退出码：%s；报告目录：%s\n' "$migration_exit" "$RUN_DIR"
cat "$RUN_DIR/report.json"
```

| 字段 | 如何理解 |
| --- | --- |
| `phase` | PG 的迁移阶段：`copying`、`ready` 或 `complete` |
| `source_sealed` | 是否已封锁 SQLite 业务写入 |
| `requests_copied` | 已处理的请求基线行数，不等于当前请求总数 |
| `pending_request_changes` | 尚未回放的请求变更条目；同一请求可有多条 |
| `final_table_rows` | 切换时需重新同步的其他表合计行数 |
| `pending_event_rows` | 观察到的剩余历史事件数量 |
| `ready_for_cutover` | 请求基线完成且最终工作量在预算内；不代表已停服或保证切换时长 |
| `ready_for_pg` | PG 已放行；不能再恢复 SQLite 写入 |
| `history_complete` | 历史事件已补齐并校验 |

只有 `ready_for_cutover: true` 才安排切换；仍为 `false` 时继续 `sync` 或检查预算。源库运行期间状态会变化，切换会再次检查。若显式调整最终预算，`status` 和 `cutover` 应传入相同参数。

进度和每 10 秒的心跳写 stderr。`stage` 是当前阶段，`rows` 是该阶段计数，`since_progress` 是距上次行数变化或阶段切换的时间。恢复后的 `rows_per_sec` 可能包含已保存的累计行数，不用它推算可靠的完成时间。

- 备份、完整性检查等阶段可能长时间没有行数变化；备份文件已出现不代表完成。
- 心跳只证明报告线程在运行，不证明数据库操作持续推进。结合阶段、进程 CPU 时间、I/O 和数据库等待状态判断。
- `status=cancelling` 表示取消/超时后尚在等待退出，先等进程退出再重跑。
- `report.json` 在命令成功结束前可能为空；`status=finished` 也只表示一个阶段结束，不能据此切换配置。

## 6. 短暂停服并切换：cutover

**以下步骤开始维护窗口。不要在 SQLite runnerd 仍运行时执行。**

1. 再跑一轮 `sync --skip-events` 并检查状态，尽量缩小最后差量。
2. 暂停新 Workflow/部署入口，安排 webhook 缓冲或后续补投和对账。迁移工具不提供排空或 webhook 缓冲。
3. 等待现有 Runner/Deployment 完成，或按既有维护方案停止；Web Terminal 会断开。
4. 备份当前完整配置和密钥。按实际 systemd、Supervisor 或其他发布流程停止 runnerd 及自动重启器，确认所有 SQLite 写入者已退出。PG runnerd 仍保持停止。
5. 执行下面的切换命令。`--source-stopped` 是人工确认，不会替你停止服务。

```bash
RUN_DIR=$(mktemp -d "$MIGRATION_DIR/cutover.XXXXXX") || exit 1
./runnerd-migrate --mode cutover --source-stopped \
  --config "$PG_CONFIG" --sqlite "$SQLITE_PATH" --timeout 10m \
  > "$RUN_DIR/report.json" 2> >(tee "$RUN_DIR/progress.log" >&2)
migration_exit=$?
printf '退出码：%s；报告目录：%s\n' "$migration_exit" "$RUN_DIR"
cat "$RUN_DIR/report.json"
```

默认上限为 `--max-final-changes 10000` 和 `--max-final-rows 100000`。超预算会在本次封锁前拒绝；先在线追平，不要盲目放宽。`10m` 是本次命令时限，不是承诺停机十分钟内完成；根据演练留出恢复时间。切换不另做完整备份、不重新扫描全部已校验请求内容，也不等待历史事件迁完。

### 何时可以修改 runnerd.yaml

退出码为 `0`，且报告同时满足：

```json
{"ready_for_pg": true, "source_sealed": true}
```

上面只是要核对的字段，不是完整报告。`history_complete: false` 允许启动 PG，表示旧日志尚未齐全。

此时再按现有发布方式，把实际生效的 `runnerd.yaml` 数据库配置改为已验证的 PG 配置，或让服务明确使用 `--config ./runnerd.postgres.yaml`。保持原加密/session 密钥及其他业务配置，只启动一个 PG runnerd。不要同时启动旧 SQLite 服务，也不要用带相同生产凭据的第二套完整 runnerd 做旁路验证。

入口仍保持维护状态，先验证健康检查、登录、既有请求与 Runner Specs、任务去重/恢复，再受控验证真实 Runner、Deployment 的创建和结束、新事件与 Web Terminal。通过后恢复入口，补投维护期间遗漏的 webhook 并对账。数据库单元测试不能代替这些真实业务验收。

## 7. PG 正常服务后补历史：backfill

```bash
RUN_DIR=$(mktemp -d "$MIGRATION_DIR/backfill.XXXXXX") || exit 1
./runnerd-migrate --mode backfill \
  --config "$PG_CONFIG" --sqlite "$SQLITE_PATH" \
  --batch-pause 50ms --timeout 2h \
  > "$RUN_DIR/report.json" 2> >(tee "$RUN_DIR/progress.log" >&2)
migration_exit=$?
printf '退出码：%s；报告目录：%s\n' "$migration_exit" "$RUN_DIR"
cat "$RUN_DIR/report.json"
```

成功退出且 `history_complete: true` 表示完成；超时可再次执行，已提交批次保留。补齐前旧事件列表可能不完整，不代表请求没有迁移。

此阶段只插入缺失历史事件并校验相同 ID 的内容，不覆盖 PG 请求、设置、授权或新事件，也不重置正在使用的序列。切换时已预留历史事件 ID 高水位。PG 可继续服务，但应继续观察负载。

保留被封锁的原 SQLite 及其 WAL/SHM 生命周期、首次备份、原配置与密钥、每次报告和日志。首次在线备份是较早快照，不能拿它替换最终封锁的 SQLite 来补历史；完成验收和保留期前不要清理这些文件。

## 8. 失败后如何处理

| 失败位置或状态 | 下一步 |
| --- | --- |
| 连接 PG 失败 | 检查目标配置、真实密码、网络、TLS、权限；不要因为手工 `psql` 成功就假定加密 DSN 和目标 schema 相同 |
| `prepare` 失败 | 保留备份和报告，使用同一源、目标及版本重跑；初始化不完整时 `status` 也可能失败 |
| `sync` 失败/超时 | SQLite 可继续服务；等待工具退出，检查日志，再重跑，不清空 PG 或检查点 |
| `cutover` 失败、退出码非零、报告缺失或提交结果不确定 | 保持两个服务停止，先执行 `status`；不要据失败退出码推断源库未封锁或 PG 未提交 |
| PG 为 `copying`，SQLite 已封锁 | 可重试 `cutover`；若要放弃本次窗口，按下面命令解除封锁后恢复 SQLite |
| PG 为 `ready` / `complete` | 不能恢复 SQLite 写入；按 PG 已放行处理，排查 PG 服务或另定数据恢复方案 |
| `backfill` 失败 | PG 可以继续服务；检查冲突或连接问题后重跑，不静默忽略内容冲突 |

仅在**源和目标服务都停止、确认 PG 仍为 `copying`**时，使用：

```bash
RUN_DIR=$(mktemp -d "$MIGRATION_DIR/resume-source.XXXXXX") || exit 1
./runnerd-migrate --mode resume-source --source-stopped \
  --config "$PG_CONFIG" --sqlite "$SQLITE_PATH" --timeout 5m \
  > "$RUN_DIR/report.json" 2> >(tee "$RUN_DIR/progress.log" >&2)
migration_exit=$?
printf '退出码：%s；报告目录：%s\n' "$migration_exit" "$RUN_DIR"
cat "$RUN_DIR/report.json"
```

成功退出并确认 `phase: "copying"`、`source_sealed: false` 后，才使用原 SQLite 配置恢复 runnerd。捕获仍保留，下次继续 `sync`；这不是取消迁移或卸载触发器的命令。若无法读取状态或解除封锁失败，不手动改表、删触发器或重启旧库绕过保护。

## 9. 数据和验证边界

请求变更通过 SQLite 触发器记录，包含删除、重新创建、终态补写和不更新 `updated_at` 的写入。每批使用同一源快照核对冲突 Job 归属，清理失效目标行、写入/校验替代行及保存检查点同属一个 PG 事务；日志继续恢复重建后的请求。只迁移当前模型的数据，额外历史表和列保留在源与备份。

迁移绑定源身份、目标 host/port/database/schema、模型指纹及触发器定义。不要替换源文件、恢复旧备份覆盖源、变更目标身份、修改迁移元数据或升级模型。导入器通过 advisory lock 串行化；数据库所有者仍能用 DDL 拆除保护，工具无法防止这种人为操作。

开发回归使用专用 `_test` 数据库及随机 schema：

```bash
RUNNERD_CATALOG_BACKEND_TESTS=1 \
RUNNERD_POSTGRES_TEST_DSN='<专用 _test 数据库 DSN>' \
go test -race ./internal/state ./cmd/runnerd-migrate \
  -run 'Test(PhasedMigration|OfflineMigration|MigrationProgress|RunPhasedMigration)' -count=1
```

测试覆盖并发源写入、续跑、请求删除/重建、失败回滚、切换保护、预算、历史冲突和切换后业务写入保护。生产负载、WAL/磁盘增长、备份恢复、真实 Runner/Deployment 及停机时长仍需实际演练；测试通过不是生产迁移验收完成。
