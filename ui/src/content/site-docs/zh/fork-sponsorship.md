# 使用组织 Sandbox 赞助可信 Fork

让符合条件的个人 Fork 使用组织的 Sandbox 服务运行托管 GitHub Actions 任务，无需共享凭据，也不会把任务所有权转移到组织仓库。

## 本指南适合谁

这个流程包含两类参与者：

- **组织所有者**为上游仓库创建并维护 Fork 赞助策略；
- **Fork 所有者或贡献者**为个人 Fork 安装 GitHub App，并在 Fork 中运行托管 workflow。

Job、workflow 历史记录和临时 GitHub Runner 仍归 Fork 所有。组织只赞助 Sandbox 创建。

## 开始前

请确认以下条件全部满足：

- Qiniu CI Runner GitHub App 已安装到组织仓库和个人 Fork；
- 组织 installation 已批准 **Members: Read-only** 权限；
- 管理策略的当前登录用户是 active 组织所有者；
- 组织具备有效的 Sandbox 服务，来源可以是组织自己的 Preferences，也可以是符合条件的平台默认配置；
- workflow 使用托管 Runner 规格，例如 `[qiniu, ubuntu-24.04]`。

Fork 赞助不适用于平台自定义或组织自定义 Runner 规格。

## 选择准入方式

每个上游仓库只有一条策略，并且只能选择一种准入方式。

| 准入方式 | 适用场景 | Job 启动时的要求 |
| --- | --- | --- |
| **需要精确审批** | 需要最小范围的允许列表。这是默认且推荐的选项。 | 组织所有者已经批准准确的 Fork 仓库。 |
| **具有上游写权限** | 维护者使用个人 Fork，并且已经拥有可信的上游访问权限。 | 个人 Fork 所有者对上游仓库具有 `write`、`maintain` 或 `admin` 权限。 |
| **组织成员** | 允许任意 active 组织成员使用个人 Fork。 | 个人 Fork 所有者是赞助组织的 active 成员。 |

自动准入方式只适用于个人 Fork。由其他组织拥有的 Fork 始终需要精确审批。

## 创建赞助策略

只有 active 组织所有者可以管理这个页面。

1. 登录 Qiniu CI Runner，打开**设置**。
2. 选择目标组织，然后打开 **Fork 赞助**。
3. 在**组织仓库**中搜索并选择作为上游的非 Fork 仓库。
4. 选择**添加策略**。新策略默认关闭，准入方式为**需要精确审批**，最大并发为 `1`。
5. 选择准入方式，并填写正数**最大并发**。
6. 打开**已启用**，然后选择**保存**。

仓库选择器只显示由当前组织拥有，并且同时在你的 GitHub 访问范围和 GitHub App installation 授权范围内的仓库。策略绑定 GitHub 的稳定仓库身份，而不只依赖仓库名称。

使用**需要精确审批**时，在**已批准的 Fork**中按 `owner/repository` 填写 Fork，然后选择**批准 Fork**。平台会验证它确实是所选上游仓库的 Fork。

## 配置 Fork

Fork 所有者必须为 Fork 仓库安装或授权 Qiniu CI Runner GitHub App。符合组织赞助条件时，不需要配置个人 Sandbox API Key。

在 Fork 中添加使用托管标签的 workflow：

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

`if: github.repository == 'owner/repository'` 这类 Job 条件可以作为防止误执行的可选保护。GitHub 会在调度 Runner 前计算它，因此它不是赞助安全边界。runnerd 始终会在创建 Sandbox 前重新校验准入条件。

不要把组织 Sandbox API Key、GitHub App 凭据或其他组织 Secret 放入 Fork 或 workflow。

## 运行并验证赞助任务

从 Fork 仓库触发 workflow。

1. GitHub 将 Job 放入 Runner 队列。
2. runnerd 将该仓库解析为真实 Fork，并检查其原始上游仓库。
3. runnerd 重新加载已启用的策略，校验所选准入方式，并检查策略容量。
4. 符合条件时，runnerd 使用组织的有效 Sandbox 服务创建临时 Sandbox。
5. repository-level Runner 注册到 Fork、接收任务，并在任务结束后移除。
6. 清理阶段停止 Sandbox。

GitHub 显示 Fork Job 已完成、Qiniu CI Runner 记录相同的 Fork 仓库和托管标签，并且清理后没有临时 Sandbox 或 Runner 注册残留，才算运行成功。

用户仍然只能看到自己具有 GitHub 仓库访问权限的 Job。赞助不会让组织所有者获得私有 Fork Job 的访问权限。

## 修改或撤销赞助

组织所有者可以在**设置 → Fork 赞助**中：

- 禁用或删除策略；
- 修改准入方式；
- 调高或调低正数并发上限；
- 添加或移除精确 Fork 审批。

变更会影响新启动和 fresh retry。禁用策略或移除审批不会终止已经运行的 Sandbox；已保存的配置只继续用于清理。策略并发已满时，其他符合条件的 Job 会等待容量，而不会切换到其他赞助来源。

## 赞助不会共享什么

组织赞助不会让 Fork 获得以下资源：

- 组织 Sandbox API Key 或 endpoint 配置；
- 组织 Cache S3；
- 自定义 Runner 规格或物理 Sandbox 模板 ID；
- 组织 Runner Group；
- Sandbox 模板和实例目录；
- 组织设置、审计数据或无关仓库。

如果 Fork 需要 Cache S3 或自定义 Runner 规格，请在 Fork 所有者自己的作用域中配置，不要依赖赞助。

## 故障排查

### Job 被跳过

检查 workflow 的 Job-level `if` 表达式。被跳过的 Job 不会请求 Runner，因此不会进入赞助校验。

### Job 已排队，但 Qiniu CI Runner 没有请求

确认 GitHub App 已安装到 Fork、已启用 **Workflow jobs** 事件、webhook delivery 成功，并且 workflow 同时请求 `qiniu` 和准确的托管操作系统标签。

### 请求在创建 Sandbox 前失败

确认策略已启用，并且 Fork 满足当前准入方式。使用精确审批时，需要批准准确的 Fork 仓库；使用自动准入方式时，需要确认 Fork 所有者仍具有要求的上游权限或 active 组织成员身份。

被拒绝的请求不应具有 Sandbox ID 或 Runner 进程。因为没有注册 Runner，GitHub Job 可能继续排队；收集失败证据后，请取消该测试 Run。

### 符合条件的请求无法解析 Sandbox 服务

请组织所有者检查组织的 Sandbox 就绪状态。使用平台默认配置时，其受众必须包含赞助组织。

### 请求等待策略容量

等待已有赞助任务结束，或者请组织所有者调高**最大并发**。该值必须保持为正整数。

Webhook、标签、注册和 Sandbox 的通用检查见[故障排查](/docs/troubleshooting)。
