# Qiniu CI Runner 指南

根据你的目标选择一条路径，并沿着同一篇指南完成操作，直到得到可验证的结果。

## 使用托管服务

登录托管版 Qiniu CI Runner，连接 GitHub 仓库，确认 Sandbox 就绪，然后运行第一个托管任务。

[开始使用托管服务](/docs/getting-started/hosted)

## 部署 runnerd

自行运行 runnerd 控制面，工作流任务仍在 Qiniu Sandbox 中执行。

[部署 runnerd](/docs/getting-started/deploy)

## 创建第一个工作流

复制使用当前托管标签的完整 workflow，并了解成功运行应当具备哪些结果。

[运行第一个工作流](/docs/guides/workflow)

## 配置缓存

在 Preferences 中保存 Cache S3，然后在 workflow 中使用 `qiniu/actions-cache@v5`。

[配置并使用 Cache S3](/docs/guides/cache)

## 构建自定义 Runner 模板

把自己的工具链加入私有 Sandbox 模板，关联到自定义 Runner Spec，并验证完整 workflow 链路。

[构建并使用自定义 Runner 模板](/docs/guides/custom-templates)

## 诊断问题

从 GitHub 或 Qiniu CI Runner 中看到的现象开始，按顺序检查每一层。

[打开故障排查](/docs/troubleshooting)
