# 构建并使用自定义 Runner 模板

为自己的工具构建私有 Qiniu Sandbox 模板，将它关联到自定义 Runner Spec，并在 GitHub Actions workflow 中选中它。

## 先判断是否需要自定义模板

如果已维护的 Ubuntu 镜像能够满足任务需求，请优先使用[托管 Runner 标签](/docs/reference/runner-labels)。当你需要不同的基础镜像、预装工具链、系统软件包、证书或其他镜像级定制时，再选择自定义模板。

自定义模板属于对应的 Sandbox 环境，不需要发布为公共模板。请在 runnerd 为目标仓库使用的同一个 Sandbox 区域及账号或组织 scope 中构建。

## 开始前

你需要准备：

- qshell 2.19.10 或更高版本；
- 目标区域的 Qiniu Sandbox endpoint 和 API Key；
- runnerd 管理员权限；
- 已连接到同一账号或组织 scope 的 GitHub 仓库。

请把凭证放在环境变量或本地密钥存储中，不要提交 API Key 或特定区域的 template ID。

## 从兼容的 Runner 镜像开始

最稳妥的起点是 Qiniu CI Runner 仓库中已有的 `templates/github-runner-*` 目录。复制最接近需求的镜像，为它重命名，并且只增加 workflow 所需的工具。

兼容镜像必须在以下位置提供可执行的 GitHub Actions Runner 脚本：

```text
/opt/actions-runner/config.sh
/opt/actions-runner/run.sh
```

镜像还应提供可写的 `/home/runner`，并允许通过 HTTPS 访问 GitHub。`runner` 用户、`/opt/hostedtoolcache` 和 `/usr/local/bin/ensure-docker` 属于已维护镜像的约定。对于自定义 spec，Docker 启动是尽力而为；只有在 workflow 不使用容器或 service container 时才应省略 Docker。

预装 Runner 是启动基线。runnerd 注册托管模板和自定义模板 Runner 时都不会传入 `--disableupdate`，因此 GitHub 可以在 Runner 接受 Job 前更新工作目录中的可写副本。请保持到 GitHub 的出站网络可用，并定期更新模板的预装版本，避免 Sandbox 启动时需要执行跨度过大的升级。

runnerd 会向 Sandbox 注入一段 Bash 启动脚本。镜像必须提供 `bash`、`base64`、`install`、`cp`、`mkdir` 和 `id`。如果镜像包含约定的 `runner` 用户，还必须提供 `sudo`，使启动脚本可以无交互地切换到该用户。

本地 `docker build` 成功只能作为诊断依据，不能证明远端 Sandbox 模板已存在，也不能证明它能在目标区域中启动。

## 配置模板构建

将 `qshell.sandbox.toml` 放在 Dockerfile 旁边。先使用只包含可移植名称的配置：

```toml
name = "acme-runner-ubuntu-24-04"
dockerfile = "./Dockerfile"
path = "."
cpu_count = 8
memory_mb = 8192
no_cache = false
```

名称在单个 Sandbox 环境中必须唯一。qshell 可以按名称找到并重新构建已有模板；找不到时则创建模板。部分 qshell 版本会在首次构建后把 `template_id` 写回文件。提交前请检查该文件，并从共享源码中移除这个与区域绑定的值。

## 在 Qiniu Sandbox 中构建

设置目标区域的凭证，进入模板目录，然后执行远端构建：

```bash
export QINIU_SANDBOX_API_URL="https://<sandbox-region-endpoint>"
read -r -s QINIU_API_KEY
export QINIU_API_KEY

cd path/to/acme-runner-ubuntu-24-04
qshell sandbox template build --wait
```

在 qshell 报告 `Status: ready` 之前不要继续。修改 Dockerfile 后，使用同一个命令重新构建按名称解析的模板。然后列出模板，记录当前区域返回的 ID：

```bash
qshell sandbox template list --format json
qshell sandbox template get <template-id>
```

## 验证远端模板

在连接 runnerd 之前，先基于该模板创建一个短生命周期的 Sandbox：

```bash
qshell sandbox create <template-id-or-name> --timeout 300
```

在 Sandbox 终端中验证 Runner 契约和 workflow 所需工具：

```bash
command -v bash base64 install cp mkdir id
test -x /opt/actions-runner/config.sh
test -x /opt/actions-runner/run.sh
test -w /home/runner
git --version

if id -u runner >/dev/null 2>&1; then
  command -v sudo
  sudo -E -u runner true
fi
```

如果需要 Docker，还要执行 `/usr/local/bin/ensure-docker` 和 `docker info`。退出会话后，使用 `qshell sandbox list` 检查是否仍有实例，并通过 `qshell sandbox kill <sandbox-id>` 清理。

## 创建自定义 Runner Spec

以 runnerd 管理员身份登录，打开 `/admin/runner_specs`，然后创建包含以下信息的自定义 spec：

- 易识别的名称；
- 声明标签，例如 `self-hosted`、`linux`、`x64` 和 `acme-linux-x64`；
- 包含 `acme-linux-x64` 的 required labels；
- 目标 Sandbox 区域中准确的 template ID；
- 组织仓库可选择 GitHub Runner Group，个人仓库应留空；
- 合适的 `max_concurrency`、`min_idle` 和优先级；
- 打开 `enabled`。

所有已启用 spec 都可由已放行仓库按 workflow 标签匹配。请使用唯一的 required labels，并只把这些标签加入目标 workflow；runnerd 不通过内部 Runner Group 或 Repository Policy 授权 spec。

创建 spec 前，请先在 `/admin/sandbox_service` 配置后台 Sandbox endpoint 和 API Key。新建或更换模板 ID 时，会使用后台凭据检查访问权限与可用默认构建。所属团队模板或公共默认模板必须在服务目录中有可用默认构建；新的重建不会使旧的可用默认构建失效。不在该目录中的第三方公共模板无法确认可用状态，会拒绝保存。

缺少配置、模板不存在或未就绪、权限错误、查询失败或超时都会阻止保存；修正后可重试，表单会保留输入。仅修改非模板参数时不重复检查。没有后台 Sandbox 凭据时，仍可管理内置默认 spec。

实际任务继续使用仓库 owner 的有效 Sandbox 配置。请确保该作用域可以访问同一模板；保存校验不检查 Runner 程序文件，也不能替代前面的沙箱冒烟测试。

## 使用自定义标签

Runner 匹配始终保持 `required labels ⊆ Job 标签 ⊆ 声明标签`。Workflow 必须包含所有 required labels，同时不能请求 spec 未声明的标签。

对于上面的示例 spec，可以使用：

```yaml
name: Custom runner check

on:
  workflow_dispatch:

jobs:
  verify:
    runs-on: [self-hosted, linux, x64, acme-linux-x64]
    steps:
      - uses: actions/checkout@v4
      - run: git --version
```

手动触发 workflow，然后在 Qiniu CI Runner 中打开 Jobs。成功结果应显示：请求匹配到自定义 spec，使用其 template ID 创建 Sandbox，完成 Runner 注册并成功结束任务。

## 安全发布与故障排查

先只在一个仓库的手动 smoke workflow 中加入唯一标签。确认任务成功完成且 Sandbox 已清理后，再提高并发或把这些标签加入更多 workflow。

如果任务一直排队，请对比 Job 标签、required labels、声明标签和 `enabled`。如果 Runner 创建失败，请确认有效账号或组织的 Sandbox endpoint、template ID 和 `Status: ready`。如果运行时失败，请重新执行远端模板检查并查看 runnerd 日志。

如果重新构建产生了新的 template ID，请在下一个任务运行前更新自定义 Runner Spec。继续按现象排查时，请参阅[故障排查](/docs/troubleshooting)。
