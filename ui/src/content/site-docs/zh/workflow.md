# 运行第一个工作流

在已连接仓库中添加一个完整 workflow，手动触发，并同时验证任务执行与资源清理。

## 创建 workflow

创建 `.github/workflows/qiniu-runner-smoke.yml`：

```yaml
name: Qiniu CI Runner smoke

on:
  workflow_dispatch:

jobs:
  smoke:
    runs-on: [qiniu, ubuntu-24.04]
    timeout-minutes: 10
    steps:
      - name: Show runner environment
        run: |
          echo "Runner OS: $RUNNER_OS"
          echo "Runner architecture: $RUNNER_ARCH"
          uname -a

      - name: Verify the temporary workspace
        run: |
          pwd
          df -h
```

将文件提交到已经授权给 Qiniu CI Runner GitHub App 的仓库。

如果目标仓库是个人 Fork，并且需要使用上游组织的 Sandbox 服务，请在触发 workflow 前完成[组织 Fork 赞助配置](/docs/guides/fork-sponsorship)。

## 标签为什么能够匹配

托管 spec 使用 `必需标签 ⊆ Job 标签 ⊆ 声明标签` 规则。

Ubuntu 24.04 的规则是：

- 必需标签：`qiniu`、`ubuntu-24.04`；
- Runner 同时声明：`self-hosted`、`linux`、`x64`。

因此，最短的受支持请求是 `[qiniu, ubuntu-24.04]`。加入 `gpu` 等不受支持的标签会导致无法匹配。只请求 `qiniu` 或只请求 `ubuntu-24.04` 同样不能匹配。

## 触发运行

打开 GitHub 仓库，依次选择 **Actions → Qiniu CI Runner smoke → Run workflow**。

任务最初会等待匹配的 Runner。Qiniu CI Runner 收到 queued 事件后才创建 Sandbox 并注册短生命周期 Runner，因此短暂的 queued 状态是正常现象。

## 跟踪进度

同时使用两个视图：

- **GitHub Actions：**查看 workflow 状态、step 输出、结论和分配到的 Runner。
- **Qiniu CI Runner Jobs：**查看匹配的 spec、Runner 生命周期事件、GitHub 日志、Runner 日志、详细信息，以及 Sandbox 运行期间的 Web Console。

不要把“Sandbox 已创建”当作完成。任务还必须被分配、执行、结束并完成清理。

## 验证清理

成功运行后：

1. GitHub 显示成功结论。
2. Qiniu CI Runner 任务进入**已完成**。
3. Runner 日志包含注册和清理事件。
4. 临时 GitHub Runner 已被移除。
5. Qiniu Sandbox 实例已经停止，Web Console 不再可用。

如果任一检查失败，请继续查看[故障排查](/docs/troubleshooting)。
