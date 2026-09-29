# Sponsor trusted forks with an organization Sandbox

Let eligible personal forks run managed GitHub Actions jobs with an organization's Sandbox service without sharing credentials or moving the job out of the fork.

## Who this guide is for

This guide has two participants:

- an **organization owner** creates and maintains the sponsorship policy for an upstream repository;
- a **fork owner or contributor** installs the GitHub App for the personal fork and runs a managed workflow there.

The job, workflow history, and temporary GitHub Runner remain owned by the fork. The organization sponsors only Sandbox provisioning.

## Before you start

Confirm all of the following:

- the Qiniu CI Runner GitHub App is installed for the organization repository and for the personal fork;
- the organization installation has approved **Members: Read-only**;
- the signed-in policy administrator is an active organization owner;
- the organization has an effective Sandbox service, either from its own Preferences or an eligible platform default;
- the workflow uses a managed Runner Spec such as `[qiniu, ubuntu-24.04]`.

Fork sponsorship does not apply to platform custom or organization custom Runner Specs.

## Choose an eligibility rule

Each upstream repository has one policy and exactly one eligibility rule.

| Eligibility | Use it when | Requirement at job start |
| --- | --- | --- |
| **Exact approval required** | You want the narrowest allowlist. This is the default and recommended option. | The organization owner has approved the exact fork repository. |
| **Upstream write permission** | Maintainers use personal forks and already have trusted upstream access. | The personal fork owner has `write`, `maintain`, or `admin` permission on the upstream repository. |
| **Organization member** | Any active organization member may use a personal fork. | The personal fork owner is an active member of the sponsoring organization. |

Automatic rules apply only to personal forks. A fork owned by another organization always requires an exact approval.

## Create the sponsorship policy

Only an active organization owner can manage this page.

1. Sign in to Qiniu CI Runner and open **Settings**.
2. Select the organization, then open **Fork sponsorship**.
3. Under **Organization repository**, search for and select the upstream non-fork repository.
4. Choose **Add policy**. A new policy starts disabled with **Exact approval required** and a maximum concurrency of `1`.
5. Select the eligibility rule and enter a positive **Maximum concurrency**.
6. Turn on **Enabled**, then choose **Save**.

The repository picker shows repositories owned by the selected organization and visible through both your GitHub access and the GitHub App installation. The policy is bound to GitHub's stable repository identity, not only its name.

When using **Exact approval required**, enter the fork as `owner/repository` under **Approved forks**, then choose **Approve fork**. The platform verifies that it is a real fork of the selected upstream repository.

## Configure the fork

The fork owner must install or authorize the Qiniu CI Runner GitHub App for the fork repository. No personal Sandbox API Key is required when organization sponsorship is eligible.

Add a workflow to the fork with managed labels:

```yaml
name: Sponsored fork job

on:
  workflow_dispatch:

jobs:
  verify:
    runs-on: [qiniu, ubuntu-24.04]
    steps:
      - run: |
          echo "repository=$GITHUB_REPOSITORY"
          echo "runner=$RUNNER_NAME"
```

A job-level expression such as `if: github.repository == 'owner/repository'` is optional protection against accidental execution. It is evaluated by GitHub before Runner scheduling and is not a sponsorship security boundary. Eligibility is always enforced by runnerd immediately before Sandbox creation.

Do not put an organization Sandbox API Key, GitHub App credential, or other organization secret in the fork or workflow.

## Run and verify a sponsored job

Trigger the workflow from the fork repository.

1. GitHub places the job in the Runner queue.
2. runnerd resolves the repository as a real fork and checks its original upstream repository.
3. runnerd reloads the enabled policy, verifies the selected eligibility rule, and checks policy capacity.
4. If eligible, runnerd uses the organization's effective Sandbox service to create an ephemeral Sandbox.
5. A repository-level Runner registers with the fork, accepts the job, and is removed after completion.
6. The Sandbox is stopped during cleanup.

The run is successful when GitHub shows the fork job as completed, Qiniu CI Runner records the same fork repository and managed labels, and no temporary Sandbox or Runner registration remains after cleanup.

Users continue to see only jobs allowed by their own GitHub repository access. Sponsorship does not grant the organization owner access to private fork jobs.

## Change or revoke sponsorship

From **Settings → Fork sponsorship**, an organization owner can:

- disable or delete the policy;
- change the eligibility rule;
- raise or lower the positive concurrency limit;
- add or remove exact fork approvals.

Changes apply to new starts and fresh retries. Disabling a policy or removing an approval does not terminate a Sandbox that is already running; its saved configuration remains available only for cleanup. When the policy concurrency limit is full, additional eligible jobs wait for capacity instead of switching to another sponsor.

## What sponsorship does not share

Organization sponsorship never gives the fork access to:

- the organization Sandbox API Key or endpoint configuration;
- organization Cache S3;
- custom Runner Specs or physical Sandbox template IDs;
- organization Runner Groups;
- Sandbox template and instance catalogs;
- organization Settings, audit data, or unrelated repositories.

If the fork needs Cache S3 or a custom Runner Spec, configure those resources in the fork owner's own scope instead of relying on sponsorship.

## Troubleshooting

### The job is skipped

Inspect the workflow's job-level `if` expression. A skipped job never requests a Runner and therefore never reaches sponsorship validation.

### The job is queued but Qiniu CI Runner has no request

Confirm that the GitHub App is installed for the fork, **Workflow jobs** events are enabled, webhook delivery succeeds, and the workflow requests both `qiniu` and an exact managed OS label.

### The request fails before Sandbox creation

Check that the policy is enabled and that the fork satisfies the current eligibility rule. For exact approval, approve the exact fork repository. For automatic rules, confirm the fork owner still has the required upstream permission or active organization membership.

A rejected request should have no Sandbox ID or Runner process. The GitHub job may remain queued because no Runner was registered; cancel that test run after collecting the failure evidence.

### An eligible request cannot resolve a Sandbox service

Ask an organization owner to check the organization's Sandbox readiness. An eligible platform default must include the sponsoring organization in its audience.

### The request waits for policy capacity

Wait for an existing sponsored job to finish, or ask an organization owner to increase **Maximum concurrency**. The limit must remain a positive integer.

For general webhook, label, registration, and Sandbox checks, see [Troubleshooting](/docs/troubleshooting).
