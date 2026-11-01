# SQLite 到 PostgreSQL 停服迁移

允许维护窗口时，优先使用一次性离线迁移。业务服务只使用一个数据库，不需要双写、复制线程、在线 authority 或回退协议。本版本已移除在线迁移运行机制；旧配置会明确报错，旧库状态保护仍保留。

## 工具行为与限制

`runnerd-migrate` 不启动 HTTP、GitHub、Runner 或 Deployment 恢复。它要求当前版本的 SQLite 模型结构，拒绝仍处于在线迁移中的源库。缺列时先在副本上完成兼容升级和演练，不允许导入工具修改原库。

工具以 `BEGIN IMMEDIATE` 持有 SQLite 写锁，通过独立连接 `VACUUM INTO` 创建一致性备份（包含 WAL 已提交内容），检查备份完整性。备份目录必须是由当前用户拥有的现有 `0700` 目录，新文件为 `0600`，绝不覆盖已有文件；失败备份也保留，重试使用新路径。

目标必须是专用空 PostgreSQL schema，服务和其他写入者必须停止。导入事务将 `search_path` 固定为选中的目标 schema，避免后备 schema 的同名表被 ORM 使用。目标建表、导入、逐行校验在同一事务内提交，事务失败则回滚；命令因提交连接丢失失败时，结果可能不确定，先检查目标，不能盲目重跑或清空它。仅成功退出和 `verified: true` 表示完整导入已经提交。

复制当前模型的 18 张业务表，保留主键、关联值、空值、布尔值、JSON 和加密字符串。时间按 PG 微秒精度规范化。校验使用二进制排序和完整规范化行内容，报告每表行数及 SHA-256。自增序列同时保留现存最大 ID 和 SQLite 历史分配高水位，避免复用已删除 ID。历史额外表、业务表中的额外列和旧在线迁移元数据均不复制，原始内容保留在备份。报告的 `retained_source_extra_tables` 统计额外表，但不包含已知的旧迁移元数据表和 `sqlite_sequence`；`retained_source_extra_columns` 仅统计业务表中的额外列。

SQLite 锁仅覆盖导入期间，**不能证明服务已经停机，也不负责切换后的写入隔离**。所有源库写入者必须持续停机；PG 直到验收通过前都不能接受正式流量。数据量大时长事务需要足够 PG 磁盘/WAL 和 SQLite 备份空间。默认总时限 30 分钟，可用 `--timeout` 调整。

## 演练和维护步骤

1. 使用 SQLite 一致性副本、专用 `_test` PG 库演练并记录时间。不要直接复制运行中的 `.db`，也不要启动两个携带相同 GitHub/Sandbox 凭据的完整服务。
2. 选择维护时间，暂停新 Workflow 和部署请求，等现有 Runner、Deployment 完成或按已批准方案停止。Web Terminal 会因停服断开。记录维护起止时间及 GitHub webhook 投递范围；GitHub 不会自动重投失败投递，提前准备持久化入口或维护后人工/脚本重投和对账。此工具不提供请求排空或 webhook 缓冲。
3. 停止 runnerd 及其自动重启器，确认所有源库写入者已退出。备份配置、`auth.encryption_key` 和 `auth.session_secret`，保留原值，否则迁移的密文或登录会话可能不可用。
4. 创建专用空 PG 数据库。复制现有配置为受保护的目标配置，只修改 `database.backend: postgres`、`database.dsn`，移除 `online_migration`。目标 DSN 支持现有 `RUNNERD_ENC` 格式，不放在命令参数或日志里。目标配置在最终位置准备，以免相对密钥路径变化。
5. 构建并执行（路径按部署环境替换）：

   ```sh
   go build -o bin/runnerd-migrate ./cmd/runnerd-migrate
   install -d -m 700 ./var/offline-backups
   bin/runnerd-migrate --config ./runnerd.postgres.yaml \
     --sqlite /absolute/path/runnerd.db \
     --backup "$PWD/var/offline-backups/before-postgres-20261029.db" \
     --source-stopped --timeout 30m > ./var/offline-backups/report.json
   ```

6. 检查退出码、`verified: true`、每表行数、额外结构数量，保留报告和备份。启动使用目标配置的单个 runnerd，入口仍处于维护状态，验证健康检查、登录、既有记录、规格原值保存与审计、数据库新增 ID；再验证真实 Runner、Deployment 创建/结束和新 Web Terminal。不要把测试框架/模拟 Sandbox 的结果当成生产业务验收。
7. 验收通过后恢复入口和 Workflow，补投维护期间遗漏事件，并核对请求是否重复、漏建或卡住。保留 SQLite 和配置备份至回退窗口结束。

## 回退边界

导入失败或开放正式写入前验收失败：停止 PG 服务进程，恢复原 SQLite 配置并启动。验收期间在 PG 产生的测试写入不会自动回到 SQLite，必须确认可以放弃或单独处理；恢复 SQLite 前确保不会重复运行已在 PG 启动的任务。

PG 接受正式业务写入后，原 SQLite 已经落后，不能直接切回。需要另开维护窗口进行反向数据迁移或其他恢复方案；本工具不提供反向同步。

## 已进入在线迁移的本地环境

如果数据库由 PR #151 合并前的版本创建，升级前先执行 `git fetch origin pull/151/head`，使用 commit `be3b7911650599e5257febcf94976caf57f9fc32` 的在线迁移二进制和原有协议完成收尾，然后再升级。若迁移尚未切换到 PG 主写，使用旧二进制的 `--migration-command abort` 收尾。若 PG 已经主写，验证反向复制追平后，在需要恢复 SQLite 时执行 `rollback`，在 PG 上结束时执行 `finish`，并在回退窗口结束前保留 fenced 的 PG 副本。新版本不再提供这些命令。不能直接删配置、删 authority 或绕过 replica fence。要重新演练离线流程，先在 SQLite 收尾并停止服务，再导入新的空 PG 库。

## 验证命令

```sh
RUNNERD_CATALOG_BACKEND_TESTS=1 \
RUNNERD_POSTGRES_TEST_DSN='<专用 runnerd_catalog_test DSN>' \
go test -tags development ./internal/state ./internal/server \
  -run 'Test(OfflineMigration|PlatformSpecRevision)' -count=1
```

真实数据库测试使用独立随机 schema，拒绝数据库名不以 `_test` 结尾的目标。覆盖分页、历史序列、大整数、密文、时区与精度、重复导入拒绝、失败事务回滚、备份不覆盖，以及在线 fence 和源写锁拒绝。

## 本地验收记录（2026-10-29）

独立命令从已收尾的 SQLite 副本向新的 PG 测试库导入 18 张业务表、33,827 行，逐表校验通过。真实 PG 专项及 race、全项目 Go 测试通过。

普通 PG 服务完成了 [5 个 Ubuntu Runner Job](https://github.com/miclle/qiniu-ci-runner-test/actions/runs/40849697071)，均成功且有 Sandbox / GitHub Runner 清理记录。测试 Deployment 完成新版本创建、HTTP 200、新 Web Terminal 命令回显、runnerd 重启后保留原 Sandbox 与版本、重新连接新终端，以及停止清理。既有登录会话可用。停服方案不要求原 PTY 跨进程存活。

这些结果不替代生产副本耗时和容量测试、备份恢复演练、维护窗口安排、webhook 补投和任务对账。生产尚未执行迁移。
