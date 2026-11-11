# Shared Web Terminal sessions

Web Terminal sessions are owned by the shared database and the Sandbox PTY.
Any runnerd node can serve the next request. There is no terminal registry,
replay tail, owner-node URL or per-session idle timer in runnerd.

## State and failure handling

- `terminal_sessions` records the opening account, Job attempt or Deploy revision,
  Sandbox ID, PTY PID and stream generation, initial output cursor, encrypted
  provider credential snapshot, status and cleanup claim. It stores no shell
  input or output.
- The Sandbox envd retains the last 64 KiB of PTY bytes independently of readers.
  A reconnect supplies the same generation and byte offset. A missing generation
  or replay gap ends the terminal explicitly. PID reuse cannot redirect input,
  resize or cleanup to a different process.
- Opening persists an `opening` record and unique provider tag before creating the
  PTY. A late result can fill its PID/generation without reopening a closing session.
  An unknown creation result remains eligible for tag reconciliation until the
  PTY is found or the Sandbox is confirmed absent; a missing tag alone is not proof
  that a delayed creation failed. Such records remain visible in the database.
- Active subscriptions renew a 30-second database deadline every five seconds.
  Each refresh rechecks the current repository access and execution binding.
  An observed disconnect renews the shared 30-second grace. After a hard node exit,
  the last renewal leaves at least 25 seconds for the browser's 20-second retry
  window. Cleanup polling adds up to five seconds; provider failures retain the
  closing record for another node to retry. Multiple subscribers never shorten
  each other's deadline. Database clock comparisons decide expiry.
- Cleanup first changes the shared status to `closing`, takes a 20-second claim,
  and makes a bounded 10-second provider call. A node exit releases no session:
  another node can reconnect or claim expired cleanup. Closed rows retain metadata
  and clear the copied provider credential. No automatic history purge is included.
- Each input request is sent once. A transport error has an unknown outcome and
  must not trigger automatic input replay. Only output supports cursor replay.
- Sandbox access uses the read-only SDK `Attach` operation: terminal polling and
  reconnection do not resume paused Sandboxes or extend their configured lifetime.

## Compatibility and deployment order

This feature requires the companion envd PTY replay protocol and Go SDK replay API.
`List.pty_replay_supported` is checked before creating a PTY. Old envd instances
return a clear upgrade-required error; there is no fallback to node-owned state.
The protocol fields are additive and legacy SDK/process calls retain their behavior.
For replay-enabled PTYs, the opening RPC deadline limits the HTTP subscription,
not the lifetime of the PTY.

1. Review and release the envd protocol/implementation, then distribute that binary
   through the Sandbox template build/promotion path. Updating the Sandbox backend's
   Go module alone does not replace envd inside existing Sandbox instances.
2. Release the corresponding Go SDK and pin runnerd to that reviewed release.
3. Verify a newly created Sandbox advertises replay support, including each region
   and template used by Jobs and Deploy. Existing Sandbox instances may still carry
   old envd and will require replacement before opening these terminals.
4. Deploy runnerd first as one node, then verify Jobs and Deploy terminals, access
   revocation, gap handling and cleanup. The additive table migration leaves Job
   and Deploy records unchanged. An old in-memory terminal cannot be migrated:
   users reopen it after upgrading. Existing Jobs/Deployments keep running.
5. Expand with the same runnerd version, shared database, session secret and
   encryption key. `server.terminal_peer_origin` is retired and ignored when left
   in an older YAML file. Nodes need access to the shared database and Sandbox API,
   without a separate peer HTTPS endpoint or sticky terminal routing.

Do not mix old and new terminal handlers behind the load balancer during the
cutover: their session IDs and ownership models differ. Drain terminal connections
before the change. A rollback leaves the additive table intact; first let the new
version finish its PTY cleanup because old binaries do not reconcile these rows.
This change alone is not evidence that the new envd binary is deployed in production.

## Local verification

The feature branches span runnerd, envd and the Go SDK. The draft runnerd branch pins the reviewed-source SDK feature commit through a
remote module replacement so CI can reproduce the tests. Replace this override
with the official qiniu/go-sdk release before merging or publishing production.
A local Go workspace containing runnerd and the SDK can be used during development.

- envd: `go test -race ./internal/services/process/...` includes a real PTY and an
  opening-RPC deadline regression, generation checks, bounded replay and retained exit.
- SDK: `go test -race -tags unit ./sandbox` checks old-envd rejection and cursor /
  generation transport. Run the repository's full `make unittest` gate as well.
- runnerd: `go test ./...`; dedicated PostgreSQL/MySQL `TestTerminalSQLBackends`;
  `TestSharedTerminalThreeProcessesPostgres` force-kills the creation process and
  resumes output and cleanup on the other two, using an external provider fixture.
- `task ui-terminal-fault-test` exercises real local PTYs and Chromium under measured
  5/15/25-second transport outages. It uses a provider fixture and does not prove
  a production Sandbox template has the new envd binary.
