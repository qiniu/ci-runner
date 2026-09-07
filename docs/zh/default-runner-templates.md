# 公共 Runner 模板

[English](../default-runner-templates.md)

Qiniu 维护 8 个用于 GitHub Actions 的 Linux x64 Sandbox 物理模板：4 个标准
镜像（Ubuntu Slim、Ubuntu 22.04、Ubuntu 24.04，以及处于预览阶段的 Ubuntu
26.04）和 4 个使用 80 GiB provider 磁盘的 `-large` 变体。`ubuntu-latest` 是
Runner catalog 中指向 Ubuntu 24.04 的逻辑映射，不是第 9 个镜像。

4 个标准物理模板已于 2026-08-03 在两个受支持的 Sandbox 区域完成发布、catalog
检查和 release smoke 验证；区域 ID 和证据保存在
[Issue #38](https://github.com/qiniu/ci-runner/issues/38#issuecomment-5164811404)。
Managed Runner Spec rollout 已于 2026-08-04（CST）通过
[GitHub Actions run 30858489153](https://github.com/miclle/qiniu-ci-runner-test/actions/runs/30858489153)
完成端到端验证：5 个逻辑 labels 全部成功，5 条 Runner requests 均进入
`completed`，所有 Sandbox 均已清理，且未残留 self-hosted Runner registration。
上游版本来源、兼容性契约和各镜像差异见
[`templates/README.md`](../../templates/README.md)。

4 个 `-large` 变体通过仓库软链复用标准 Dockerfile 和脚本，并使用不同的物理
模板名称。它们是对外可用的 operator 配置默认 Runner Spec：operator 通过自定义
spec 路径在 Admin 中创建并启用带显式 template ID 的条目。它们不属于 runnerd
managed defaults，但对应 spec 启用后，所有允许的 workflow 都可以使用文档中的
labels。其 80 GiB 根磁盘来自 Sandbox provider 的 team/tier 构建配额，不是
`qshell.sandbox.toml` 字段或 qshell CLI 参数。构建 large 变体前应把该配额配置为
81,920 MiB，并在发布前验证 catalog 返回的 `disk_size_mb`。

## 公共 Catalog API

未登录和已登录客户端都可以访问 `GET /api/public/runner-templates`，并取得相同的、
可缓存的 runnerd-owned catalog。响应按稳定顺序返回 4 个标准托管模板对象，且只包含以下字段：

```json
[
  {
    "default_template_name": "github-runner-ubuntu-24-04",
    "runner_spec_names": ["qiniu-ubuntu-24.04", "qiniu-ubuntu-latest"],
    "workflow_labels": [
      ["qiniu", "ubuntu-24.04"],
      ["qiniu", "ubuntu-latest"]
    ]
  }
]
```

示例只展示其中一项；完整响应包含 4 个标准托管模板。该 API 不包含 provider
template ID、region、credential、endpoint，也不会暴露私有、自定义或 4 个 large
物理模板。Provider
可见模板仍通过依赖 credential、按账户或组织 scope 隔离的
`GET /user/sandbox/templates?region=<id>` API 获取。普通用户的 Sandbox Templates
页面把两个 catalog 作为独立 section 渲染，因此 provider catalog 失败不会隐藏
公共 catalog，反之亦然。

## Workflow labels

请根据所需环境使用准确的 label 组合：

```yaml
jobs:
  slim:
    runs-on: [qiniu, ubuntu-slim]
    steps:
      - uses: actions/checkout@v4
      - run: uname -a

  ubuntu_22:
    runs-on: [qiniu, ubuntu-22.04]
    steps:
      - uses: actions/checkout@v4
      - run: uname -a

  ubuntu_24:
    runs-on: [qiniu, ubuntu-24.04]
    steps:
      - uses: actions/checkout@v4
      - run: uname -a

  ubuntu_26_preview:
    runs-on: [qiniu, ubuntu-26.04]
    steps:
      - uses: actions/checkout@v4
      - run: uname -a

  latest:
    runs-on: [qiniu, ubuntu-latest]
    steps:
      - uses: actions/checkout@v4
      - run: uname -a
```

对外的 large 默认规格使用相同的标签契约和资源配置，但系统盘为 80 GiB：

| Workflow label | 物理模板 | 系统盘 |
| --- | --- | --- |
| `[qiniu, ubuntu-slim-large]` | `github-runner-ubuntu-slim-large` | 80 GiB |
| `[qiniu, ubuntu-22.04-large]` | `github-runner-ubuntu-22-04-large` | 80 GiB |
| `[qiniu, ubuntu-24.04-large]` | `github-runner-ubuntu-24-04-large` | 80 GiB |
| `[qiniu, ubuntu-26.04-large]` | `github-runner-ubuntu-26-04-large` | 80 GiB |
| `[qiniu, ubuntu-latest-large]` | `github-runner-ubuntu-24-04-large` | 80 GiB |

`ubuntu-latest-large` 是映射到 Ubuntu 24.04 large 物理模板的对外逻辑标签，不会新增第 5 个物理 large 镜像。这些 large spec 已在公共文档中列出；只要 operator 在 Admin 中启用对应条目，所有允许的 workflow 都可以使用，虽然它们不会出现在 runnerd-owned managed-template API 中。

`qiniu` label 是必需项。Managed 匹配遵守
`required_labels ⊆ job_labels ⊆ labels`，因此 `[ubuntu-24.04]`、`[qiniu]`
和带有不受支持额外 labels 的请求都不会匹配 managed default。Operator 可以在
Admin 中禁用单个 managed spec；从 workflow 中移除 `qiniu` 则会从 workflow
侧阻止 managed-default selection。自定义 spec 仍可使用 operator 定义的
required labels 和显式 template ID。

large workflow labels 使用 operator 配置的对外默认 spec；需要先在 Admin 中创建并
启用对应条目。4 个 large 物理模板仍可通过相同的 task targets 构建、发布、catalog
检查和 smoke 验证，但不会出现在这个公共 managed catalog 中。

Runner 启动时，如果模板无法使 Docker daemon 可用，managed spec 会直接失败，
因为 Docker 属于 managed 兼容性契约。自定义 spec 保留原有的 best-effort 行为：
runnerd 记录 warning 后继续注册，使不依赖 Docker 的 jobs 仍可运行。

## 软件兼容性

这些模板会逐项跟踪固定版本的 `actions/runner-images` 软件报告，但并非与 GitHub
托管 Runner 镜像逐字节一致。当前 Qiniu Sandbox 公共模板构建配额提供
22,222 MiB 根磁盘，而完整 GitHub 托管 Runner 软件清单需要更大空间。因此，3 个
版本化模板保证在对应 Ubuntu 版本上提供与 Ubuntu Slim 兼容的核心工具，并额外
提供 Apache、Podman、Buildah、Skopeo、Ninja、Docker 支持、预装 Actions
Runner、用于 installer 验证的固定版本 Pester，以及 Runner 文件系统契约。

[`templates/runner-images-compatibility.json`](../../templates/runner-images-compatibility.json)
是可执行的逐项契约。`provided` 表示发布 conformance 必须验证该项目；`excluded`
表示公共模板不保证该项目，需要时应在 workflow 中安装，或构建自定义 Sandbox
模板。某个 `excluded` 可执行文件可能因操作系统包依赖而碰巧存在，但 workflow
不得依赖这一点。

## 环境要求

- `qiniu/qshell` 2.19.10 或更高版本；
- `task`、`jq` 和 `curl`；
- 当前 Sandbox 区域的 `QINIU_API_KEY`；
- 指向当前区域端点的 `QINIU_SANDBOX_API_URL`。

如果所需版本的 qshell 不在 `PATH` 中，请显式传入可执行文件：

```bash
task template-build-ubuntu-24-04 QSHELL=/path/to/qshell
```

任一凭据变量为空时，所有远端任务都会直接失败。仓库中的
`qshell.sandbox.toml` 只保存稳定名称和资源设置；构建任务让 qshell 使用临时
副本，因此不会提交区域相关的 template ID。

## 在单个区域构建与验证

先运行无需凭据的快速源码检查：

```bash
task template-check-all
```

然后使用 qshell 构建真正的 Sandbox 模板。每个任务都会等待 qshell 输出终态
`Status: ready`；如果进程退出码为 0，却没有出现该状态，任务仍会判定构建失败。

源码门槛会拒绝低于 `2.336.0` 的 Actions Runner。Release smoke 会检查
Dockerfile 固定的 Runner 精确版本，以及持久化到 Sandbox 运行时环境中的模板名和
模板版本。它还会以 `runner` 用户加载 NVM，并要求 `/home/runner/.nvm` 可写，
防止 root 所有的构建 skeleton 错误通过发布门禁。完整 runtime conformance 还会
检查固定的 Azure CLI 精确版本，因此 Dockerfile 中的版本、官方校验和与
compatibility verification 必须同步更新。Python 和 pipx 安装会对软件包索引的
瞬时失败执行有限重试；其他上游安装器可能不具备幂等性，因此不会自动重试。

```bash
task template-build-ubuntu-slim
task template-build-ubuntu-22-04
task template-build-ubuntu-24-04
task template-build-ubuntu-26-04
task template-build-ubuntu-slim-large
task template-build-ubuntu-22-04-large
task template-build-ubuntu-24-04-large
task template-build-ubuntu-26-04-large
```

运行 large 构建目标前，必须把 Sandbox provider 的构建配额配置为 81,920 MiB。
Qshell 不携带单模板磁盘参数，因此 operator 必须在发布和 smoke 前验证模板 catalog
返回的 `disk_size_mb`。

Dockerfile 会按需将 `bootstrap`、`platform`、`node`、`toolchain` 和
`runtime` 工作保留为独立的 qshell 兼容缓存层。模板版本元数据会在预置工作
结束后才写入，runner 所有的 NVM 副本则独立放在 `toolchain` 与 `runtime` 之间，
因此两类变更都不会让重型安装层的缓存失效。
这些层结束后，每个 Dockerfile 都会在切换到非特权 runner 用户前，将最终
`/etc/resolv.conf` 写为 Cloudflare `1.1.1.1` 和 `1.0.0.1`。把覆盖操作放在
最后可以避免重型预置层缓存失效。发布前的真实 Sandbox smoke 会检查该文件
严格包含这两行配置。
在 `platform` 之前，每个 Dockerfile 还会将固定校验和的 AWS SAM 归档拆成
不超过 16 MiB 的独立 Range 下载层。服务超时后可复用已经完成的分块；
这些分块保存在 `/opt/qiniu-runner-build-cache`，因为 qshell 不会恢复缓存层中
写入 `/tmp` 的输出。Dockerfile 会校验拼接后的精确字节数和完整 SHA-256，
但不会把单个大归档作为缓存层输出保留；校验通过后会在同一个 `RUN` 中继续
执行 `platform` 安装，立即消费该归档。
Ubuntu 26.04 还会先将固定的
runner-images apt 软件包列表拆成 18 个可缓存批次安装，再由上游 platform 安装器
逐包确认并运行 Pester 契约。大体积的 emoji 字体、ICU、RPM、Tk、Xvfb、
binutils 和 `systemd-coredump` 依赖集分别独立成层，最后一批使用开放区间，
避免遗漏固定列表后续新增项。如果远程构建
在已有阶段完成后触及服务时限，请保留
默认缓存并重跑同一命令，不要强制使用 `--no-cache`。发布门槛仍然是某一次构建
最终达到 `Status: ready`。

8 个构建全部 ready 后再发布：

```bash
task template-publish-ubuntu-slim
task template-publish-ubuntu-22-04
task template-publish-ubuntu-24-04
task template-publish-ubuntu-26-04
task template-publish-ubuntu-slim-large
task template-publish-ubuntu-22-04-large
task template-publish-ubuntu-24-04-large
task template-publish-ubuntu-26-04-large
task template-defaults-check
```

`template-defaults-check` 要求每个物理名称（包括 4 个 large 变体）都恰好对应 1 个
公共的 `ready` 或 `uploaded` 模板，且 template ID 非空；缺失或重复条目都会失败。

保留 catalog 检查输出的 template ID，然后执行真实的 Sandbox smoke：

```bash
task template-smoke IMAGE_KEY=ubuntu-slim TEMPLATE_ID=<slim-template-id>
task template-smoke IMAGE_KEY=ubuntu-22.04 TEMPLATE_ID=<22.04-template-id>
task template-smoke IMAGE_KEY=ubuntu-24.04 TEMPLATE_ID=<24.04-template-id>
task template-smoke IMAGE_KEY=ubuntu-26.04 TEMPLATE_ID=<26.04-template-id>
task template-smoke IMAGE_KEY=ubuntu-slim-large TEMPLATE_ID=<slim-large-template-id>
task template-smoke IMAGE_KEY=ubuntu-22.04-large TEMPLATE_ID=<22.04-large-template-id>
task template-smoke IMAGE_KEY=ubuntu-24.04-large TEMPLATE_ID=<24.04-large-template-id>
task template-smoke IMAGE_KEY=ubuntu-26.04-large TEMPLATE_ID=<26.04-large-template-id>
```

Smoke 会通过 qshell 创建临时 Sandbox，检查系统版本、架构、预装 Actions
Runner、出站 HTTPS、Docker daemon、可写 work/tool-cache 路径和清理行为。
无论验证是否成功，脚本都会尝试终止临时 Sandbox。请保存命令输出的 JSON 路径
作为发布证据。完整 compatibility manifest 仍是静态 inventory contract；
逐条 runtime conformance 只作为可选诊断，不阻塞发布可用性门槛。
Docker 检查会导入并运行一个关闭网络的本地最小 rootfs，用于验证 daemon、
socket 和容器执行能力；registry 可达性不属于该检查，出站 HTTPS 会独立验证。

本地 Docker 构建和 `task template-conformance-local` 只用于按需诊断，不能替代
qshell 模板构建或 Sandbox smoke。

## 首次双区域发布

按以下顺序完成全部构建、发布、catalog 和 smoke 流程：

1. 导出
   `QINIU_SANDBOX_API_URL=https://cn-yangzhou-1-sandbox.qiniuapi.com`，并设置
   扬州区域的 `QINIU_API_KEY`。
2. 使用 qshell 构建并发布 4 个标准模板。provider 磁盘配额门禁可用后，再构建并发布
   4 个 large 变体，然后运行 `task template-defaults-check`，并对全部 8 个 ID 做
   smoke 验证。
3. 保存构建输出、catalog ID、smoke JSON 和相关 workflow URL。
4. 导出
   `QINIU_SANDBOX_API_URL=https://us-south-1-sandbox.qiniuapi.com`，并设置
   美国南部区域的 `QINIU_API_KEY`。
5. 重复 8 个模板的构建、发布、catalog 检查和 smoke 验证。
6. 确认两个 catalog 结果都为每个稳定物理名称返回且仅返回 1 个可运行的公共
   模板。
7. 在将模板状态改为 verified 或启用独立的 managed Runner rollout 前，把双
   区域证据附加到 Issue #38。

Qiniu 负责维护 8 个物理镜像。只有经过评审的 Runner catalog revision 并取得
新的区域 smoke 证据后，才能修改 `ubuntu-latest` 映射。`ubuntu-26.04` 在上游
正式发布前始终属于预览模板。

## 回滚

移除公共可用性前，应先禁用 managed Runner Specs 和已启用的 large 自定义 specs，然后运行对应的可逆发布
回滚命令：

```bash
task template-unpublish-ubuntu-slim
task template-unpublish-ubuntu-22-04
task template-unpublish-ubuntu-24-04
task template-unpublish-ubuntu-26-04
task template-unpublish-ubuntu-slim-large
task template-unpublish-ubuntu-22-04-large
task template-unpublish-ubuntu-24-04-large
task template-unpublish-ubuntu-26-04-large
```

普通回滚不要删除模板对象。保留私有模板可以保存构建历史，并允许在不改变
自定义 Runner Specs 的前提下重新发布经过评审的版本。
