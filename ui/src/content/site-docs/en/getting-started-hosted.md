# Get started with the hosted service

Run a GitHub Actions job on a clean Qiniu Sandbox without deploying runnerd yourself.

## Before you start

You need:

- a GitHub.com account with access to a repository you can test;
- permission to install the Qiniu CI Runner GitHub App for that repository, or help from the account or organization owner;
- a Qiniu account and Sandbox API Key if your account or organization does not already have an effective Sandbox service.

Do not put a Sandbox API Key, GitHub secret, or private key in a workflow, repository, issue, screenshot, or chat message.

## 1. Sign in with GitHub

Open [Qiniu CI Runner Jobs](/jobs) and continue with GitHub. After authorization, you return to the Jobs workspace.

The first visit includes a short product tour. It explains Jobs, repository readiness, Settings, and Sandbox setup. You can replay it later from the account menu.

## 2. Connect a repository

Open [Repositories](/repositories).

1. Install the configured GitHub App, or sync an existing installation.
2. Select only the repositories that should use Qiniu CI Runner.
3. Return to Repositories and confirm that the repository appears.

Repository visibility is the intersection of your GitHub access and the repositories authorized for the GitHub App installation. An organization appears in Settings only when you are an active member. Outside collaborators can see authorized repository readiness but cannot manage that organization's Sandbox settings.

## 3. Check Sandbox readiness

Select the account or organization that owns the repository. The readiness card combines repository access with the effective Sandbox service.

- **Ready:** continue to the workflow step. The service may come from the selected scope, an inherited account setting, or an eligible platform default.
- **Setup required:** choose **Configure Sandbox**. The link opens the exact account or organization Preferences page.
- **Read only:** ask an active organization member to configure the Sandbox service.

For a personal account, the settings route is [Account Preferences](/account/preferences). Choose a supported region and enter the API Key from the [Qiniu API Key page](https://portal.qiniu.com/developer/user/api-key). The saved key is not shown again.

## 4. Add the managed runner label

Create a workflow in the connected repository and use both required labels:

```yaml
runs-on: [qiniu, ubuntu-24.04]
```

The `qiniu` label selects Qiniu-managed routing. The exact OS label selects the public runner template. Neither label is sufficient by itself.

[Copy a complete workflow](/docs/guides/workflow)

To cache across runs, save Cache S3 in Preferences and use `qiniu/actions-cache@v5`. See [Configure and use Cache S3](/docs/guides/cache).

## 5. Run and verify the job

Trigger the workflow from GitHub Actions. While the job runs:

1. GitHub sends a queued workflow-job webhook.
2. runnerd matches the managed spec and creates a Sandbox.
3. The ephemeral Runner registers with GitHub and accepts the job.
4. Qiniu CI Runner shows the job, Runner logs, GitHub logs, details, and the Web Console while it is available.
5. After completion, runnerd removes the Runner registration and stops the Sandbox.

## What success looks like

You are finished when:

- GitHub Actions shows the job as successful;
- Qiniu CI Runner shows the same repository, workflow, and completed job;
- Runner logs show creation, registration, execution, and cleanup without an unresolved failure;
- the temporary Sandbox no longer remains active after cleanup.

If the job stays queued or the Sandbox cannot start, open [Troubleshooting](/docs/troubleshooting).
