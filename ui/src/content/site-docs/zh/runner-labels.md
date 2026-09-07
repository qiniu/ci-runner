# 托管 Runner 标签

使用受支持的七牛标签组合选择已维护的公共 Sandbox 模板，无需创建自定义 Runner Spec。

## 支持的标签与资源规格

| Workflow 请求 | CPU | 内存 | 系统盘 | 模板状态 | 说明 |
| --- | --- | --- | --- | --- | --- |
| `[qiniu, ubuntu-slim]` | 8 vCPU | 8 GiB | 20 GiB | 稳定 | 更小的通用镜像 |
| `[qiniu, ubuntu-22.04]` | 8 vCPU | 8 GiB | 20 GiB | 稳定 | Ubuntu 22.04 x64 |
| `[qiniu, ubuntu-24.04]` | 8 vCPU | 8 GiB | 20 GiB | 稳定 | 推荐默认值 |
| `[qiniu, ubuntu-26.04]` | 8 vCPU | 8 GiB | 20 GiB | 预览 | 预览镜像，请明确选择 |
| `[qiniu, ubuntu-latest]` | 8 vCPU | 8 GiB | 20 GiB | 稳定映射 | 当前映射到 Ubuntu 24.04 |
| `[qiniu, ubuntu-slim-large]` | 8 vCPU | 8 GiB | 80 GiB | 大系统盘 | 使用更大系统盘的 Ubuntu Slim |
| `[qiniu, ubuntu-22.04-large]` | 8 vCPU | 8 GiB | 80 GiB | 大系统盘 | 使用更大系统盘的 Ubuntu 22.04 x64 |
| `[qiniu, ubuntu-24.04-large]` | 8 vCPU | 8 GiB | 80 GiB | 大系统盘 | 推荐用于磁盘密集型任务 |
| `[qiniu, ubuntu-26.04-large]` | 8 vCPU | 8 GiB | 80 GiB | 预览 | 使用更大系统盘的 Ubuntu 26.04 预览镜像 |
| `[qiniu, ubuntu-latest-large]` | 8 vCPU | 8 GiB | 80 GiB | 稳定映射 | 当前映射到 Ubuntu 24.04 large |

## 资源规格说明

所有托管模板当前均提供 8 vCPU 和 8 GiB 内存。`-large` 规格复用对应标准规格的
操作系统镜像和预装软件，只将系统盘从 20 GiB 扩大到 80 GiB。磁盘容量由
Sandbox provider 分配，不是 workflow 中可调整的参数；格式化和预留空间可能使
文件系统显示的可用容量略低于标称值。

容器构建、依赖缓存或大型中间产物超出标准系统盘时，应选择 `-large` 标签。
切换到 `-large` 不会增加 CPU 或内存。

## 匹配规则

每个托管 spec 都声明 `self-hosted`、`linux`、`x64`、`qiniu` 和准确的操作系统标签，并要求 `qiniu` 与该操作系统标签。

匹配始终保持：

```text
必需标签 ⊆ Job 标签 ⊆ 声明标签
```

因此，`[qiniu, ubuntu-24.04-large]` 和完整声明标签都可以匹配。标签不完整或包含不受支持的标签时不能匹配。

## 托管与自定义的所有权

runnerd 管理托管名称、标签、必需标签、公共模板名称和优先级。Operator 控制 `enabled`、`max_concurrency` 和 `min_idle`。

自定义 spec 仍由 operator 管理，使用显式 template ID，并且可以定义其他声明标签和必需标签。保存自定义 spec 并不能证明模板在所选 Sandbox 区域中存在或可用。

## 模板解析

注册前，runnerd 会通过有效的账号或组织 Sandbox endpoint 解析托管公共模板名称。因此，同一个稳定公共名称可以在不同区域解析为不同 template ID，无需把某个区域的 ID 保存在 spec 中。

托管示例见[运行第一个工作流](/docs/guides/workflow)，完整自定义流程见[构建并使用自定义 Runner 模板](/docs/guides/custom-templates)。
