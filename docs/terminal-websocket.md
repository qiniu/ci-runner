# Web Console: replaceable runnerd nodes

Jobs and Deploy share one WebSocket for terminal input, output and resize. The
opening request owns the connection and remote stream. No node-local session
index, peer forwarding, sticky routing or `terminal_peer_origin` is needed.
Each explicit Connect creates a new ID and shell. Disconnect ends that session;
reopening does not restore its variables, directory or output, or replay input.
The underlying Job/Deploy and Sandbox continue independently.

## Protocol and authorization

- Jobs: `GET /user/runner_requests/{id}/terminal?cols=100&rows=28` with WebSocket upgrade.
- Deploy: `GET /user/deployments/{id}/revisions/{number}/terminal?cols=100&rows=28` with WebSocket upgrade.
- A read-only HTTP GET to the same route performs the normal authorization
  preflight for OAuth/error UX; it creates no session and grants no capability.
  The WebSocket upgrade repeats every check.
- Require the existing OAuth session and a same-origin `Origin`. The server checks
  exact `(installation_id, repository_full_name)` access before upgrade, after
  creation, before input/resize and every five seconds while connected. These
  checks share the database authorization snapshot (30-second hard expiry).
- Bind the opening account, resource, current attempt and Sandbox. Deploy also
  binds revision and remaining lifetime, using saved credentials. Historical
  revisions cannot open the current Sandbox. An unavailable authorization check
  or changed execution closes the connection.
- Server frames: `ready` (`session_id`, `pid`, `sandbox_id`), `output` (base64
  bytes in `data`), or a generic `error`. PID is display evidence only.
- Client frames: `{"type":"input","data":"pwd\n"}` or
  `{"type":"resize","cols":100,"rows":28}`. No session selector or arbitrary
  operation is accepted. Input is at most 4096 UTF-8 bytes; dimensions are 1–500.
- Writes have a five-second deadline. Ping from upgrade (including creation)
  every five seconds and a twenty-second
  pong deadline detect silent peers. The output queue is 64 KiB. Overflow closes
  the connection rather than dropping output or replaying input.
  On process completion, queued output is drained before the socket closes.

## Database and PTY ownership

The additive `terminal_sessions` table stores creating/active/closed status,
account/resource/attempt binding, Sandbox ID, node identity, a unique connection owner, database
lease, close reason and `cleanup` (`unknown` or `confirmed`). Creation intent is
committed **before** provider I/O. No terminal input/output or reusable PID control
handle is stored. Activation/renewal/finalization require the live owner lease.
Expired rows become closed/unknown; a late writer cannot overwrite that result.
A periodic sweep runs on each node, including after restart.

A small ci-runner-owned Python supervisor is uploaded through the existing Files
API to a unique 0700 directory in the Sandbox. It owns the real Linux PTY and
unreaped direct child. Input, resize, heartbeat and close use a session-specific
Unix socket through short commands. Payloads travel in consumed request files,
not command arguments. Neither API calls nor recovery kill a saved PID. An
exclusive creation marker and close tombstone prevent duplicate or delayed
creation from reviving an ID. A startup deadline bounds dispatch delay. Keep runnerd and Sandbox clocks
synchronized; expired control messages fail closed.

Helper stdout uses marked frames and request-specific acknowledgements so a
login-profile or `BASH_ENV` banner cannot be mistaken for protocol data. Startup
text is ignored up to 16 KiB; malformed frames or larger banners fail closed.

The supervisor exits after 30 seconds without a valid heartbeat or operation, closing the PTY and
killing/reaping its direct shell. Linux parent-death signaling also protects the
direct child on supervisor death. Intentionally detached descendants are not
covered; stopping a whole Sandbox remains the Job/Deploy lifecycle's job.
Same-UID/root workloads inside a Sandbox are not isolated from this supervisor.

The current SDK retains command stdout. To bound that memory without changing
it, the supervisor ends a session after approximately 4 MiB of encoded output.
A user can explicitly open another shell. This is a diagnostic console, not a
log-export channel. Normal operations use one file upload and short command per
input/resize/heartbeat; high-concurrency capacity is not yet qualified.

Confirmed close requires the supervisor's completed cleanup, or authoritative
Sandbox absence. Failed/unknown creation, lost nodes and unconfirmed cleanup stay
`unknown`; elapsed time alone is not cleanup proof. Per-session markers remain
until Sandbox destruction to prevent ID reuse. If file deletion is unavailable,
private transient request files may also remain until Sandbox destruction.

Operators can inspect metadata without reading terminal content:

```sql
SELECT id, node_id, account_id, resource_kind, resource_id, sandbox_id,
       status, cleanup, close_reason, lease_until, updated_at
FROM terminal_sessions
ORDER BY created_at DESC;
```

## Compatibility and rollout

- Only ci-runner changes. The current Sandbox Files/Commands APIs are used; no
  supplier service, SDK release or template modification is required.
- Linux, `python3` (3.8+) and `/bin/bash` are required inside the selected Sandbox.
  Missing prerequisites fail terminal creation without stopping the workload.
  Windows Web Console remains unsupported.
- Existing SQLite/Postgres/MySQL state upgrades add the new table. Runner and
  Deploy data, credentials, permissions and lifecycle schemas are preserved.
- The retired POST/SSE/input/resize/DELETE terminal endpoints are not supported.
  Old UI tabs must refresh after release. Old `terminal_peer_origin` YAML is
  ignored; it can be removed. Nodes still share auth keys and the database.
- Deploy the same build to all nodes and drain old console connections. Mixed
  old/new UI/API versions do not preserve open terminals. Configure the existing
  public load balancer to forward WebSocket upgrades with at least a 30-second
  idle timeout. No per-node HTTPS domain or certificate is needed for consoles.
- Rollback leaves the additive table inert. Refresh clients and reopen consoles;
  do not promise preservation of the previous shell across rollout/rollback.
- No production deployment is performed by this change. Local API tests are not
  regional gateway or production load/capacity acceptance.

## Verification

- `go test ./...` and `task test`: product authorization, execution races,
  input/size validation, cleanup evidence, saved credentials/lifetime and state.
- The existing real-dialect `TestFreshSchemaSQLBackends` gate also exercises
  terminal ownership, expiration and stale completion on PostgreSQL and MySQL.
- `TestTerminalSocketProcess` with `RUNNERD_CATALOG_BACKEND_TESTS=1` and a
  dedicated PostgreSQL database with a name ending in `_test` (an isolated
  schema is created and removed by the test): two OS processes, kill
  one, expire its row, preserve the other connection and Job, open a new session.
- `task ui-terminal-fault-test`: Chromium + real local PTY, dropped WebSocket,
  explicit reconnect/new shell, no input replay, and shell teardown. Auth and
  provider are fixtures; the WebSocket and database paths are product code.
- `RUNNERD_TERMINAL_ENVD_TEST_URL=http://127.0.0.1:<port> go test
  ./internal/sandboxrunner -run TestTerminalExistingAPIIntegration -v`: real SDK
  Files/Commands traffic to an existing unmodified disposable local envd; only
  control-plane Connect is a fixture. Never point this test at production.
- `python3 -B internal/sandboxrunner/terminal/helper_test.py` in a disposable
  Linux container with `--init --cap-add CHECKPOINT_RESTORE --security-opt
  systempaths=unconfined`: real PTY interaction, concurrent creation/close,
  unknown creation, heartbeat expiry, supervisor death and forced kernel PID
  reuse with `RUNNERD_TERMINAL_PID_REUSE_TEST=1` explicitly set only inside
  that container. Never use host PID namespace. Python 3 must be installed in the fixture.
