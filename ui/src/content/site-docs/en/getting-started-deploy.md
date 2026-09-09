# Deploy runnerd

Operate the open-source Qiniu CI Runner control plane on your own infrastructure while Qiniu Sandbox supplies the isolated job capacity.

## Choose a deployment path

For the shortest production path, use [Deploy to Qiniu LAS](https://app-6a6b0d723d3a24e095531129.app.qiniucc.com/). For development or a custom host, build runnerd from source.

Both paths require GitHub.com, a GitHub App, Qiniu Sandbox credentials, a database, and an HTTPS address reachable by GitHub webhooks. GitHub Enterprise Server is not supported.

## 1. Create the GitHub App

Create a GitHub App under the account or organization that will operate the service. Configure these permissions:

| Scope | Permission | Access |
| --- | --- | --- |
| Repository | Actions | Read-only |
| Repository | Administration | Read and write |
| Repository | Metadata | Read-only |
| Repository | Pull requests | Read-only |
| Organization | Members | Read-only |
| Organization | Self-hosted runners | Read and write |

Organization permissions are needed for organization Settings and organization-level Runner groups. Existing installation owners must approve newly added permissions.

Subscribe to **Workflow jobs**. **Workflow runs** is optional and provides a compensating signal when a workflow-job event is missed.

## 2. Configure OAuth and webhooks

After the deployment has a stable HTTPS origin, configure:

- Homepage URL: `https://<runner-host>/`
- OAuth callback: `https://<runner-host>/auth/github/callback`
- Webhook URL: `https://<runner-host>/webhooks/github`

Use separate random values for the OAuth client secret, webhook secret, session secret, and encryption key. Keep the App private key outside the repository.

## 3. Build from source

Skip this section when LAS supplies the runnerd deployment.

```bash
task deps
task ui-deps
task build
cp runnerd.yaml.example runnerd.yaml
```

Edit `runnerd.yaml` for the database, GitHub App, OAuth, webhook, server, and worker settings. runnerd reads `./runnerd.yaml` by default or the file passed with `--config`.

Bootstrap the first administrator with the stable numeric GitHub user ID:

```bash
./bin/runnerd --bootstrap-admin github:<github-user-id> --config runnerd.yaml
./bin/runnerd --config runnerd.yaml
```

The bootstrap command updates the account role and exits. It does not start the server.

## 4. Configure Sandbox ownership

Ordinary users manage personal or organization Sandbox credentials under Preferences. Administrators may configure a separate platform fallback under `/admin/sandbox_service` for eligible repository owners.

Do not put ordinary-user Sandbox credentials in `runnerd.yaml`. The application stores scoped API Keys encrypted and never returns their full value to the browser.

Cache S3 is also user- or organization-owned in Preferences. Operators must set both `s3_region` and `s3_endpoint` on each cache-capable entry in `sandbox.regions`, plus `cache.sts_endpoint`. See [Configure and use Cache S3](/docs/guides/cache).

## 5. Verify managed runner specs

runnerd reconciles managed specs for `ubuntu-slim`, `ubuntu-22.04`, `ubuntu-24.04`, preview `ubuntu-26.04`, and `ubuntu-latest`. Operators control whether each managed spec is enabled plus its concurrency and idle capacity. The catalog labels and public template names remain runnerd-owned.

For the first test, use:

```yaml
runs-on: [qiniu, ubuntu-24.04]
```

Custom specs remain available for operator-owned templates and labels, but they are not required for the managed first run.

## 6. Run the production smoke

Before opening the deployment to users, verify:

- the public homepage and `/docs` load over HTTPS;
- GitHub OAuth returns to the requested route;
- the GitHub App installation and authorized repository appear;
- Sandbox readiness resolves for the repository owner;
- a real workflow is picked up by an ephemeral Runner;
- GitHub and Runner logs are readable;
- Runner registration and Sandbox resources are removed after completion;
- diagnostics show no unresolved create or cleanup failure.

Use the repository's `docs/deployment-smoke.md` as the operator acceptance checklist.
