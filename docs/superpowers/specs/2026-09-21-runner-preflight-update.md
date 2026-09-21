# GitHub Actions Runner Preflight Update Design

## Context

PR #107 enabled GitHub's official Runner self-update and added separate
preinstalled/effective version evidence. Production requests from the custom
`qbox-kodo-ubuntu-16-04` template proved that an ephemeral Runner can receive a
Job before it receives the refresh message. In that ordering the updater
downloads the new release in the background, waits for the accepted Job to
finish, and applies the update only as the one-shot Sandbox is being cleaned.
The Job therefore executes on the preinstalled version, and the next Sandbox
starts from that same immutable template version.

The product requirement is narrower than persistent template mutation: a Job
that is still queued on GitHub must be accepted only after the writable Runner
copy in its newly created Sandbox has been refreshed. A later Sandbox is
expected to repeat the refresh from the template baseline.

## Decision

- Apply a registration-before-update gate to custom Runner Specs. Managed
  Runner Specs continue to use the release-tested template pin and periodic
  rebuild process.
- Resolve the current official Linux Runner applications through the same
  repository or organization GitHub scope used for Runner registration.
- Keep GitHub credentials in runnerd. Pass only the server-owned public
  download URL, version, architecture, and SHA-256 digest to the
  Sandbox.
- In the writable Runner directory, compare the preinstalled version with the
  application matching the Sandbox architecture. When they differ, download,
  bound, checksum, extract, and verify the candidate before running
  `config.sh`.
- Fail before Runner registration when metadata resolution, package selection,
  download, checksum, extraction, or version verification fails. Never claim
  the queued Job with the old version as a fallback.
- Leave official self-update enabled after registration. It remains a safety
  net for a release published after the preflight metadata was resolved.

## Runtime flow

1. runnerd reloads and validates the selected Runner Spec and resolves the
   physical Sandbox template as today.
2. For a custom Spec, runnerd queries GitHub's
   `/{repos|orgs}/.../actions/runners/downloads` endpoint. Results are cached
   for ten minutes, returned as cloned slices, and concurrent misses for one
   target are coalesced.
3. runnerd creates the Sandbox and retains the existing preinstalled template
   snapshot.
4. The startup script copies `/opt/actions-runner` into the writable work
   directory and selects `x64`, `arm64`, or `arm` from `uname -m`.
5. If the writable Runner is already the selected version, startup continues
   without a download.
6. Otherwise the script downloads the public archive with fixed connection,
   total-time, retry, and size limits; validates the GitHub-provided SHA-256;
   extracts into a fresh directory; and verifies the candidate
   `Runner.Listener --version` exactly matches the selected version.
7. The script keeps the previous work directory until the verified candidate
   is in place. A catchable HUP, INT, or TERM during the swap, or a failed
   candidate move, restores the previous directory before exit. If restoration
   fails, the backup is preserved and its path is reported.
8. Only after successful verification does the script run `config.sh`, expose
   the real Job labels, and start `run.sh`. The queued Job is therefore claimed
   by the refreshed Runner.
9. Existing hook evidence persists the version that actually reaches the Job.
   The Sandbox is destroyed after the Job, so a future Sandbox repeats the
   process from its template baseline.

## Security and compatibility

- Accept only GitHub-owned HTTPS release URLs for
  `actions/runner/releases/download`; reject malformed versions, filenames,
  architectures, and digests before Sandbox creation. Because the downloads
  endpoint does not publish archive size, the Sandbox enforces a fixed
  512-MiB transfer cap.
- Never send an API token, installation token, or full GitHub API response to
  the Sandbox.
- Custom templates must provide `curl`, `tar`, `sha256sum`, `mktemp`, and the
  runtime dependencies required by the selected Runner release. Missing
  dependencies fail before registration with actionable stderr.
- The compressed archive limit is 512 MiB. Download time is bounded to five
  minutes.
- GitHub Enterprise Server remains unsupported by runnerd configuration, so
  the allowlist is intentionally limited to GitHub.com release assets.
- No state schema changes are required. `runner_version` remains the template
  baseline and `effective_runner_version` proves the version used by the Job.

## Verification

- GitHub client tests cover repository and organization endpoints, metadata
  validation, cache reuse, concurrent request coalescing, and error responses.
- Startup-script execution tests prove same-version bypass, successful
  `2.336.0` to `2.337.0` replacement before `config.sh`, checksum rejection,
  unsupported architecture rejection, missing-tool rejection, rollback after a
  catchable interruption between the two directory moves, and observed/target
  diagnostics for a candidate-version mismatch.
- Lifecycle tests prove managed Specs do not query preflight metadata, custom
  Specs fail before registration on metadata errors, and valid metadata reaches
  the Sandbox input without credentials.
- Production acceptance uses an immutable custom template with preinstalled
  `2.336.0`. The request must persist effective version `2.337.0`, with update
  logs before Runner configuration and no Job accepted by `2.336.0`.
