# Troubleshooting

Start with the symptom you can see, then check each boundary in order instead of changing several settings at once.

## Job stays queued

Check:

1. The GitHub App is installed for the repository.
2. **Workflow jobs** is subscribed under the GitHub App events.
3. Recent webhook deliveries reach `/webhooks/github` and pass signature validation.
4. The workflow requests both `qiniu` and the exact OS label.
5. The managed spec is enabled and has available concurrency.
6. Repositories shows an effective Sandbox service for the repository owner.

If GitHub has no webhook delivery, fix the App event configuration. If runnerd has a request but reports no match, fix the labels or policy. If a matched request is deferred, inspect concurrency and Sandbox capacity.

## Repository is missing

Repository visibility is not copied from an installation alone. It is the intersection of:

- repositories authorized for the linked GitHub App installation; and
- repositories accessible with the signed-in user's GitHub token.

Return to Repositories and sync installations. Confirm that the App installation includes the repository. If GitHub rejected or revoked the user token, sign in with GitHub again.

For organization Settings, confirm that the App has **Organization Members: Read-only**, the installation owner approved that permission, and the signed-in user is an active member. Outside collaborators do not receive organization Settings access.

## Sandbox creation fails

Open the repository owner's readiness card and check:

- a custom, inherited, or eligible platform Sandbox source is active;
- the selected region is supported;
- the saved API Key remains valid;
- the public template is available in that region;
- account quota, balance, and concurrency allow another Sandbox.

Update credentials only from the exact account or organization Preferences page. Do not add credentials to the workflow.

## Runner registration returns 404

A personal repository cannot use an organization Runner group. Clear `runner_group` on a custom spec so runnerd registers a repository-level Runner.

For an organization Runner group, confirm **Organization Self-hosted runners: Read and write** and ensure the group exists and permits the target repository.

## OAuth sign-in fails

Compare the GitHub App callback URL with the deployment origin. It must be:

```text
https://<runner-host>/auth/github/callback
```

Check the protocol, hostname, path, client ID, and client secret. A protected deep link should return to the same same-origin path after authorization.

## Webhook signature is invalid

The GitHub App webhook secret and `github.webhook_secret` must be identical. Copy the complete value without quotes or surrounding whitespace, save it, and redeliver a recent event.

## Cache restore misses or save is skipped

Check:

1. The repository owner saved Cache S3 in Preferences, and the selected Sandbox region has an S3 endpoint.
2. The workflow uses `qiniu/actions-cache@v5`, and `actions/setup-go` sets `cache: false`.
3. The log contains `The cache action detected a local S3 bucket cache.` Missing that line means the job fell back to GitHub's cache.
4. Fork PRs write only their own `pr-N` scope. `Cache save skipped` means trusted PR metadata was unavailable, so the job stayed default-branch read-only.
5. Parallel jobs do not share one write key.

See [Configure and use Cache S3](/docs/guides/cache).

## Collect useful evidence

When asking for help, include:

- repository and workflow name without secrets;
- requested labels;
- GitHub webhook delivery status and timestamp;
- Qiniu CI Runner job ID and lifecycle state;
- the first actionable Runner log error;
- whether the repository owner is a personal account or organization.

Never include client secrets, private keys, webhook secrets, Sandbox API Keys, exported cookies, or full local configuration files.
