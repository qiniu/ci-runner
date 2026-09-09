# 开始使用托管服务

无需自行部署 runnerd，即可让 GitHub Actions 任务运行在干净的 Qiniu Sandbox 中。

## 开始前

你需要准备：

- 一个 GitHub.com 账号，以及一个可用于测试的仓库；
- 为该仓库安装 Qiniu CI Runner GitHub App 的权限，或者可以协助安装的账号或组织所有者；
- 如果账号或组织还没有有效的 Sandbox 服务，则需要七牛云账号和 Sandbox API Key。

不要把 Sandbox API Key、GitHub Secret 或私钥放入 workflow、仓库、Issue、截图或聊天消息。

## 1. 使用 GitHub 登录

打开 [Qiniu CI Runner Jobs](/jobs)，然后使用 GitHub 继续。授权完成后，你会回到 Jobs 工作区。

首次访问会显示简短的产品导览，介绍 Jobs、仓库就绪状态、Settings 和 Sandbox 配置。之后可以从账号菜单重新查看。

## 2. 连接仓库

打开[仓库](/repositories)。

1. 安装已配置的 GitHub App，或者同步现有安装。
2. 只选择需要使用 Qiniu CI Runner 的仓库。
3. 返回 Repositories，确认目标仓库已经显示。

可见仓库是你的 GitHub 访问范围与 GitHub App installation 授权仓库的交集。只有 active member 才能在 Settings 中看到对应组织。Outside collaborator 可以查看已授权仓库的就绪状态，但不能管理该组织的 Sandbox 设置。

## 3. 检查 Sandbox 就绪状态

选择仓库所属的账号或组织。就绪卡片会同时检查仓库访问权限和有效的 Sandbox 服务。

- **已就绪：**继续配置 workflow。服务可能来自当前 scope、继承的账号配置或符合条件的平台默认配置。
- **需要设置：**选择**配置 Sandbox**，进入准确的账号或组织 Preferences 页面。
- **只读：**请联系组织的 active member 配置 Sandbox 服务。

个人账号使用[账号 Preferences](/account/preferences)。选择支持的区域，并填写从[七牛云 API Key 页面](https://portal.qiniu.com/developer/user/api-key)获取的 API Key。保存后页面不会再次显示完整密钥。

## 4. 添加托管 Runner 标签

在已连接的仓库中创建 workflow，并同时使用两个必需标签：

```yaml
runs-on: [qiniu, ubuntu-24.04]
```

`qiniu` 标签选择七牛托管路由，准确的操作系统标签选择公共 Runner 模板。只写其中一个标签都无法匹配。

[复制完整 workflow](/docs/guides/workflow)

需要跨运行缓存时，先在 Preferences 保存 Cache S3，再使用 `qiniu/actions-cache@v5`。见[配置并使用 Cache S3](/docs/guides/cache)。

## 5. 运行并验证任务

从 GitHub Actions 触发 workflow。任务运行过程中：

1. GitHub 发送 queued workflow-job webhook。
2. runnerd 匹配托管 spec 并创建 Sandbox。
3. 临时 Runner 注册到 GitHub 并接收任务。
4. Qiniu CI Runner 显示任务、Runner 日志、GitHub 日志、详细信息，以及可用期间的 Web Console。
5. 任务结束后，runnerd 移除 Runner 注册并停止 Sandbox。

## 成功标准

满足以下条件才算完成：

- GitHub Actions 显示任务成功；
- Qiniu CI Runner 显示相同仓库、workflow 和已完成任务；
- Runner 日志包含创建、注册、执行和清理过程，并且没有未解决的失败；
- 清理完成后，临时 Sandbox 不再保持运行。

如果任务一直排队或 Sandbox 无法启动，请打开[故障排查](/docs/troubleshooting)。
