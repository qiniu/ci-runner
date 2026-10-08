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

## 5. 配置平台 Runner Spec

全部平台规格在 **Admin → Runner Specs** 维护。新数据库目录为空，启动不自动创建规格；已有公共规格一次性迁移并保留配置。

首次标准 workflow 可创建 `qiniu-ubuntu-24.04`，声明标签为 `self-hosted,linux,x64,qiniu,ubuntu-24.04`，必需标签为 `qiniu,ubuntu-24.04`，绑定公共名称 `github-runner-ubuntu-24-04` 并启用。先配置后台 Sandbox 服务，使保存操作能校验公共模板。发布后，该规格会出现在平台目录。

公共名称绑定自动使用模板预装的 Runner，并要求启动时 Docker 就绪；后台没有独立配置这两项行为的开关。组织 Fork 赞助须由组织所有者配置仓库策略，并通过原有的授权和容量检查，不通过规格上的开关启用。详见[组织赞助 Fork Runner](/docs/guides/fork-sponsorship)。

确认每个有效 Sandbox 区域中该公共名称都存在且可运行。私有规格改为绑定物理 ID，并保持在公共目录之外。详见[Runner 标签与模板映射](/docs/reference/runner-labels)。

## 6. 执行生产 Smoke

向用户开放部署前，需要验证：

- 公开首页和 `/docs` 可以通过 HTTPS 加载；
- GitHub OAuth 能返回原来的目标路由；
- GitHub App installation 和授权仓库可见；
- 仓库所有者的 Sandbox 就绪状态可以解析；
- 真实 workflow 被临时 Runner 接走；
- 通过任务名称的 GitHub 链接可阅读工作流步骤日志，**Runner 日志** 标签中的生命周期输出可读；
- 完成后 Runner 注册和 Sandbox 资源被清理；
- diagnostics 中没有未解决的创建或清理失败。

使用仓库中的 `docs/zh/deployment-smoke.md` 作为 operator 验收清单。
