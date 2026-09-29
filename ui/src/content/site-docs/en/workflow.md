# Run your first workflow

Add one complete workflow to a connected repository, trigger it manually, and verify both execution and cleanup.

## Create the workflow

Create `.github/workflows/qiniu-runner-smoke.yml`:

```yaml
name: Qiniu CI Runner smoke

on:
  workflow_dispatch:

jobs:
  smoke:
    runs-on: [qiniu, ubuntu-24.04]
    timeout-minutes: 10
    steps:
      - name: Show runner environment
        run: |
          echo "Runner OS: $RUNNER_OS"
          echo "Runner architecture: $RUNNER_ARCH"
          uname -a

      - name: Verify the temporary workspace
        run: |
          pwd
          df -h
```

Commit the file to a repository authorized for the Qiniu CI Runner GitHub App.

When the repository is a personal fork that should use an upstream organization's Sandbox service, complete [organization fork sponsorship](/docs/guides/fork-sponsorship) before triggering the workflow.

## Why these labels work

Managed specs use the rule `required labels ⊆ job labels ⊆ advertised labels`.

For Ubuntu 24.04:

- required: `qiniu`, `ubuntu-24.04`;
- also advertised by the Runner: `self-hosted`, `linux`, `x64`.

The shortest supported request is therefore `[qiniu, ubuntu-24.04]`. Adding an unsupported label such as `gpu` prevents a match. Requests containing only `qiniu` or only `ubuntu-24.04` also do not match.

## Trigger the run

Open the repository on GitHub, then choose **Actions → Qiniu CI Runner smoke → Run workflow**.

The job initially waits for a matching Runner. Qiniu CI Runner creates the Sandbox and registers a short-lived Runner only after receiving the queued event, so a brief queued period is expected.

## Follow progress

Use both views:

- **GitHub Actions:** workflow state, step output, conclusion, and the assigned Runner.
- **Qiniu CI Runner Jobs:** matched spec, Runner lifecycle events, GitHub logs, Runner logs, details, and the Web Console while the Sandbox is running.

Do not treat “Sandbox created” as completion. The job must be assigned, execute, finish, and clean up.

## Verify cleanup

After a successful run:

1. GitHub reports a successful conclusion.
2. The Qiniu CI Runner job reaches **Completed**.
3. Runner logs contain registration and cleanup events.
4. The ephemeral GitHub Runner is removed.
5. The Qiniu Sandbox instance is stopped and no longer usable through the Web Console.

If one of these checks fails, continue with [Troubleshooting](/docs/troubleshooting).
