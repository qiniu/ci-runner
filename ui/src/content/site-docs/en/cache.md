# Configure and use Cache S3

Save Cache S3 in Preferences, then use `qiniu/actions-cache@v5` in the workflow. Cache bytes go directly to the user bucket. runnerd does not proxy them.

## Before you start

You need:

- a connected GitHub repository with an effective Sandbox service;
- a bucket in the S3 region that matches the selected Sandbox region;
- an access key and secret with object read and write permission on that bucket.

Personal accounts use [Account Preferences](/account/preferences). Organization repositories are configured by an active member at `/organizations/{login}/preferences`. Outside collaborators can see readiness status but cannot save organization Cache S3 settings.

Do not put Cache access keys in a workflow, repository secret, issue, or chat message.

## How this differs from GitHub cache

Official `actions/cache` stores objects in GitHub Cache Service. Sandbox runners do not receive that credential, so GitHub cache does not work here. Do not re-enable the built-in cache on `actions/setup-go`.

`qiniu/actions-cache@v5` writes to the S3 bucket you save in the web UI:

- cache bytes go directly to S3; runnerd does not proxy them;
- credentials are short-lived STS tokens minted by runnerd, not long-lived access keys;
- read and write prefixes are injected per repository and branch or PR, and workflow env changes cannot exceed the STS policy;
- Fork PRs read the PR, base-branch, and default-branch scopes, and write only their own `pr-N` scope.

Keep the familiar `key` / `restore-keys` inputs, but change `uses` to `qiniu/actions-cache@v5`.

## 1. Save Cache S3 in the web UI

There is no global Cache S3 setting. Each account or organization fills in its own values. Personal accounts open [Account Preferences](/account/preferences). Organization repositories are configured by an active member at `/organizations/{login}/preferences`.

Select and save the Sandbox service region first, then fill in **Cache S3**:

| Field | Who sets it | Notes |
| --- | --- | --- |
| Region / Endpoint | Filled automatically | Taken from the selected Sandbox region; not editable |
| Bucket | User | Must live in the S3 region mapped from that Sandbox region |
| Prefix | Optional | Default `gh-actions-cache`. Objects are stored as `<prefix>/<owner>/<repo>/scopes/...` |
| Access Key ID | User | An AK with object read and write permission on the bucket |
| Secret Access Key | User | Matching SK. Stored encrypted; the page never shows it again |

After save, the page only shows that Cache S3 is configured. Enter a new AK/SK to replace the saved values. If the page reports that the selected region has no S3 endpoint, ask the runnerd administrator to add `s3_region` and `s3_endpoint` under `sandbox.regions`.

You can also use an IAM sub-account instead of the primary key. Create a user in [IAM Overview](https://portal.qiniu.com/iam/overview) and grant:

- Bucket: read objects, upload objects, modify objects, and delete objects;
- Security Token Service: get federation tokens.

runnerd uses that AK/SK only to mint STS. The cache action in the sandbox receives the short-lived token.

Use a dedicated bucket per GitHub installation when possible, and configure a bucket lifecycle rule so expired cache objects are deleted automatically.

## 2. Use cache in a workflow

Use [`qiniu/actions-cache@v5`](https://github.com/qiniu/actions-cache) instead of `actions/cache`. The workflow does not set bucket, endpoint, region, access key, or secret:

```yaml
jobs:
  check:
    runs-on: [qiniu, ubuntu-24.04]
    steps:
      - uses: actions/checkout@v6
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
          cache: false
      - uses: qiniu/actions-cache@v5
        with:
          path: |
            ${{ github.workspace }}/.cache/go/pkg/mod
            /tmp/go-build-cache
          key: ${{ runner.os }}-go-check-${{ hashFiles('**/go.sum') }}
          restore-keys: |
            ${{ runner.os }}-go-check-
            ${{ runner.os }}-go-
```

Keep `setup-go` for installing Go. Cache restore and save belong to `qiniu/actions-cache@v5`. Restore runs at the start of the step. Save runs in the post step only after the job succeeds. A failed job does not save.

## 3. Cache isolation

- A trusted branch reads its own scope, then the default branch, and writes only its own scope.
- An internal PR reads the PR scope, then the base branch and default branch, and writes only its own `pr-N` scope.
- A Fork PR reads the PR, base-branch, and default-branch scopes, and writes only its own `pr-N` scope. If GitHub does not expose trusted PR metadata, the job stays default-branch read-only and logs `Cache save skipped: RUNS_ON_S3_CACHE_WRITE_PREFIX is not set`.
- `pull_request_target`, `workflow_run`, `issue_comment`, and unverified metadata are also default-branch read-only.

Do not share one write key across parallel jobs. Choose one of these options:

1. Give each saving job its own key prefix, such as `Linux-go-tidy-` and `Linux-go-test-`.
2. Create a dedicated warmup job that saves, and let other jobs restore only.

Jobs in the same run do not wait for each other. A restore can start before another job finishes saving. Scripts that reuse images or artifacts must handle a cache miss.

## 4. Verify

A successful S3 cache job logs:

```text
The cache action detected a local S3 bucket cache. Using it.
Cache restored successfully
Cache saved successfully
```

If a Fork PR cannot be verified, the save line is `Cache save skipped`. If `local S3 bucket cache` is missing, that job did not receive Cache S3 credentials and the action fell back to GitHub's cache.

## runnerd administrator configuration

The operator who deploys runnerd must provide the region catalog and STS endpoint in `runnerd.yaml`:

```yaml
sandbox:
  regions:
    - id: us-south-1
      label: "United States · Dallas 1"
      sandbox_api_url: https://us-south-1-sandbox.qiniuapi.com
      s3_region: us-north-1
      s3_endpoint: https://internal-s3-las-us-north-1-dal.qiniucs.com
    - id: cn-yangzhou-1
      label: "China · Yangzhou 1"
      sandbox_api_url: https://cn-yangzhou-1-sandbox.qiniuapi.com
      s3_region: cn-east-1
      s3_endpoint: https://internal-s3-las-cn-east-1-yz.qiniucs.com

cache:
  sts_endpoint: https://sts-ov.qiniuapi.com
```

Each region requires `id`, `label`, and `sandbox_api_url`. `s3_region` and `s3_endpoint` must be set together; only regions with both fields support Cache S3. The endpoint may be private to the Sandbox network, so runnerd validates configuration shape only and does not probe the bucket from the control plane.

On every sandbox start, runnerd verifies Workflow Run context, mints an STS token for the sandbox lifetime plus five minutes, and injects `RUNS_ON_S3_*` variables. There is no refresh.

See [Deploy runnerd](/docs/getting-started/deploy) for the full operator path.
