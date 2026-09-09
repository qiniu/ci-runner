# 故障排查

从你能够看到的现象开始，按顺序检查每个边界，不要同时修改多项设置。

## 任务一直排队

检查：

1. GitHub App 已安装到目标仓库。
2. GitHub App 事件中已经订阅 **Workflow jobs**。
3. 最近的 webhook delivery 可以到达 `/webhooks/github`，并通过签名校验。
4. Workflow 同时请求 `qiniu` 和准确的操作系统标签。
5. 托管 spec 已启用，并且还有可用并发。
6. Repositories 显示仓库 owner 具备有效的 Sandbox 服务。

如果 GitHub 中没有 webhook delivery，请修复 App 事件配置。如果 runnerd 已收到请求但提示没有匹配项，请修复标签或 policy。如果请求已经匹配但被延迟，请检查并发和 Sandbox 容量。

## 仓库没有显示

仓库可见性不能只根据 installation 判断，而是以下两项的交集：

- GitHub App installation 授权的仓库；
- 当前登录用户的 GitHub token 可以访问的仓库。

返回 Repositories 并同步 installations。确认 App installation 包含目标仓库。如果 GitHub 已拒绝或撤销用户 token，请重新使用 GitHub 登录。

对于组织 Settings，请确认 App 具有 **Organization Members: Read-only**，installation owner 已审批该权限，并且当前登录用户是 active member。Outside collaborator 不能访问组织 Settings。

## Sandbox 创建失败

打开仓库 owner 的就绪卡片并检查：

- Custom、inherited 或符合条件的 platform Sandbox 来源处于有效状态；
- 选择了支持的区域；
- 保存的 API Key 仍然有效；
- 公共模板在该区域可用；
- 账号配额、余额和并发允许创建新的 Sandbox。

只通过准确的账号或组织 Preferences 页面更新凭据。不要把凭据加入 workflow。

## Runner 注册返回 404

个人仓库不能使用组织 Runner group。清空自定义 spec 的 `runner_group`，让 runnerd 注册 repository-level Runner。

如果使用组织 Runner group，请确认 App 具有 **Organization Self-hosted runners: Read and write**，对应 group 存在且允许目标仓库。

## OAuth 登录失败

对比 GitHub App callback URL 与部署地址，标准地址必须是：

```text
https://<runner-host>/auth/github/callback
```

检查协议、域名、路径、Client ID 和 Client Secret。访问受保护的深链接时，授权后应该返回相同的同源路径。

## Webhook 签名无效

GitHub App Webhook Secret 必须与 `github.webhook_secret` 完全一致。复制完整值，不要带引号或前后空格，保存后重新投递最近的事件。

## Cache restore 未命中或 save 被跳过

检查：

1. 仓库 owner 的 Preferences 已保存 Cache S3，所选 Sandbox 区域配置了 S3 Endpoint。
2. Workflow 使用 `qiniu/actions-cache@v5`，并且 `actions/setup-go` 设置了 `cache: false`。
3. 日志包含 `The cache action detected a local S3 bucket cache.`。没有这行表示该 job 回退到了 GitHub 官方 cache。
4. Fork PR 只写入自己的 `pr-N`。出现 `Cache save skipped` 表示未能验证 PR 元数据，该 job 保持默认分支只读。
5. 并行 job 没有共用同一个 write key。

完整说明见[配置并使用 Cache S3](/docs/guides/cache)。

## 收集有效证据

请求帮助时，请提供：

- 不含 Secret 的仓库与 workflow 名称；
- 请求的标签；
- GitHub webhook delivery 状态和时间；
- Qiniu CI Runner Job ID 和生命周期状态；
- Runner 日志中的第一条可操作错误；
- 仓库 owner 是个人账号还是组织。

不要提供 Client Secret、Private Key、Webhook Secret、Sandbox API Key、导出的 Cookie 或完整本地配置文件。
