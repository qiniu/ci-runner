# 部署 runnerd

在自己的基础设施上运行开源 Qiniu CI Runner 控制面，并使用 Qiniu Sandbox 提供隔离的任务算力。

## 选择部署方式

最短的生产路径是使用[一键部署到七牛云 LAS](https://app-6a6b0d723d3a24e095531129.app.qiniucc.com/)。本地开发或自定义主机可以从源码构建 runnerd。

两种方式都需要 GitHub.com、GitHub App、Qiniu Sandbox 凭据、数据库，以及 GitHub webhook 可以访问的 HTTPS 地址。当前不支持 GitHub Enterprise Server。

## 1. 创建 GitHub App

在负责运行服务的账号或组织下创建 GitHub App，并配置以下权限：

| 范围 | 权限 | 访问级别 |
| --- | --- | --- |
| Repository | Actions | Read-only |
| Repository | Administration | Read and write |
| Repository | Metadata | Read-only |
| Repository | Pull requests | Read-only |
| Organization | Members | Read-only |
| Organization | Self-hosted runners | Read and write |

组织 Settings 和组织级 Runner group 需要 Organization 权限。增加权限后，现有 installation 的所有者必须完成审批。

必须订阅 **Workflow jobs**。**Workflow runs** 是可选的补偿信号，可在漏收 workflow-job 事件时提供帮助。

## 2. 配置 OAuth 和 webhook

部署获得稳定 HTTPS 地址后，配置：

- Homepage URL：`https://<runner-host>/`
- OAuth callback：`https://<runner-host>/auth/github/callback`
- Webhook URL：`https://<runner-host>/webhooks/github`

OAuth Client Secret、Webhook Secret、Session Secret 和 Encryption Key 应使用不同的随机值。App 私钥不能放入仓库。

## 3. 从源码构建

如果 LAS 已经提供 runnerd 部署，可以跳过本节。

```bash
task deps
task ui-deps
task build
cp runnerd.yaml.example runnerd.yaml
```

编辑 `runnerd.yaml`，配置数据库、GitHub App、OAuth、webhook、server 和 worker。runnerd 默认读取 `./runnerd.yaml`，也可以通过 `--config` 指定其他文件。

使用稳定的 GitHub 数字用户 ID 初始化第一个管理员：

```bash
./bin/runnerd --bootstrap-admin github:<github-user-id> --config runnerd.yaml
./bin/runnerd --config runnerd.yaml
```

Bootstrap 命令只更新账号角色并退出，不会启动服务。

## 4. 配置 Sandbox 所有权

普通用户在 Preferences 中管理个人或组织的 Sandbox 凭据。管理员可以在 `/admin/sandbox_service` 中配置独立的平台兜底，并限制适用的 repository owner。

不要把普通用户的 Sandbox 凭据写入 `runnerd.yaml`。应用会加密保存 scoped API Key，并且不会向浏览器返回完整值。

Cache S3 也由用户或组织在 Preferences 中配置。管理员需要在 `runnerd.yaml` 的 `sandbox.regions` 中为每个支持缓存的区域同时提供 `s3_region` 和 `s3_endpoint`，并设置 `cache.sts_endpoint`。完整说明见[配置并使用 Cache S3](/docs/guides/cache)。

## 5. 验证托管 Runner Spec

runnerd 会协调 `ubuntu-slim`、`ubuntu-22.04`、`ubuntu-24.04`、预览版 `ubuntu-26.04` 和 `ubuntu-latest` 的托管 spec。Operator 可以控制每个托管 spec 是否启用，以及并发和 idle capacity；catalog 标签和公共模板名称仍由 runnerd 管理。

第一次测试使用：

```yaml
runs-on: [qiniu, ubuntu-24.04]
```

自定义 spec 仍可用于 operator 自有模板和标签，但托管版首次运行不需要创建自定义 spec。

## 6. 执行生产 Smoke

向用户开放部署前，需要验证：

- 公开首页和 `/docs` 可以通过 HTTPS 加载；
- GitHub OAuth 能返回原来的目标路由；
- GitHub App installation 和授权仓库可见；
- 仓库所有者的 Sandbox 就绪状态可以解析；
- 真实 workflow 被临时 Runner 接走；
- GitHub 日志和 Runner 日志可读；
- 完成后 Runner 注册和 Sandbox 资源被清理；
- diagnostics 中没有未解决的创建或清理失败。

使用仓库中的 `docs/zh/deployment-smoke.md` 作为 operator 验收清单。
