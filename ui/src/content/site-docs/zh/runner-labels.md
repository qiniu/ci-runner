# Runner 标签

使用下方表格中的受支持七牛标签组合选择 Sandbox 模板。标准标签默认可用；large 标签需要 operator 创建并启用对应的自定义 Runner Spec 后才可用。

## 支持的标签与资源规格

| Workflow 请求 | CPU | 内存 | 系统盘 | 模板状态 | 说明 |
| --- | --- | --- | --- | --- | --- |
| `[qiniu, ubuntu-slim]` | 8 vCPU | 8 GiB | 20 GiB | 稳定 | 更小的通用镜像 |
| `[qiniu, ubuntu-slim-large]` | 8 vCPU | 8 GiB | 80 GiB | 配置后稳定 | 需要启用自定义 Runner Spec；使用更大系统盘的 Ubuntu Slim |
| `[qiniu, ubuntu-22.04]` | 8 vCPU | 8 GiB | 20 GiB | 稳定 | Ubuntu 22.04 x64 |
| `[qiniu, ubuntu-22.04-large]` | 8 vCPU | 8 GiB | 80 GiB | 配置后稳定 | 需要启用自定义 Runner Spec；使用更大系统盘的 Ubuntu 22.04 x64 |
| `[qiniu, ubuntu-24.04]` | 8 vCPU | 8 GiB | 20 GiB | 稳定 | 推荐默认值 |
| `[qiniu, ubuntu-24.04-large]` | 8 vCPU | 8 GiB | 80 GiB | 配置后稳定 | 需要启用自定义 Runner Spec；推荐用于磁盘密集型任务 |
| `[qiniu, ubuntu-latest]` | 8 vCPU | 8 GiB | 20 GiB | 稳定映射 | 当前映射到 Ubuntu 24.04 |
| `[qiniu, ubuntu-latest-large]` | 8 vCPU | 8 GiB | 80 GiB | 配置后稳定映射 | 需要启用自定义 Runner Spec；映射到 Ubuntu 24.04 large 物理模板 |
| `[qiniu, ubuntu-26.04]` | 8 vCPU | 8 GiB | 20 GiB | 预览 | 预览镜像，请明确选择 |
| `[qiniu, ubuntu-26.04-large]` | 8 vCPU | 8 GiB | 80 GiB | 配置后预览 | 需要启用自定义 Runner Spec；使用更大系统盘的 Ubuntu 26.04 预览镜像 |

## 资源规格说明

标准模板当前均提供 8 vCPU 和 8 GiB 内存。`-large` 变体复用对应标准规格的操作系统镜像和预装软件，只将系统盘从 20 GiB 扩大到 80 GiB。磁盘容量由 Sandbox provider 分配，不是 workflow 中可调整的参数；格式化和预留空间可能使文件系统显示的可用容量略低于标称值。

`ubuntu-latest-large` 是指向同一个 Ubuntu 24.04 large 物理模板的逻辑标签，不会新增物理镜像。

容器构建、依赖缓存或大型中间产物超出标准系统盘时，应选择 `-large` 标签。切换到 `-large` 不会增加 CPU 或内存。

## 匹配规则

每个托管 spec 都声明 `self-hosted`、`linux`、`x64`、`qiniu` 和准确的操作系统标签，并要求 `qiniu` 与该操作系统标签。自定义 large spec 也必须定义符合相同约定的声明标签和必需标签。

匹配始终保持：

```text
必需标签 ⊆ Job 标签 ⊆ 声明标签
```

因此，`[qiniu, ubuntu-24.04]` 和完整声明标签都可以匹配。标签不完整或包含不受支持的标签时不能匹配。large 标签只有在对应的自定义 spec 已启用且其标签配置接受该请求时才会匹配。

## 平台规格管理

管理员在后台维护全部平台规格，包括标签、必需标签、模板绑定、容量、优先级、启用状态和公开发布设置。重启保留修改，不会重建已删除规格；新安装须先创建规格。平台目录展示已启用且通过公共模板校验、开启展示的规格，包括固定 ID 引用。

注册前，公共名称通过有效账号或组织的 Sandbox endpoint 解析，同一个稳定名称可以在不同区域映射到不同物理 ID。固定 ID 引用使用显式 template ID，它也可能是 public。保存时通过管理员的 Sandbox 配置校验访问，实际区域可用性仍须用工作流 smoke 验证。

运行行为跟随模板绑定，没有独立的 Runner 准备、Docker 或赞助开关。公共名称绑定使用预装 Runner，并要求 Docker 就绪；固定 ID 引用在注册前检查并按需更新官方 Runner，Docker 保留尽力配置，且不能使用赞助。两种绑定都保留 GitHub Runner 官方自动更新。公共名称规格只有在通过原有组织仓库策略、授权和容量检查后，才能使用[组织 Fork 赞助](/docs/guides/fork-sponsorship)。目录展示须验证模板实际为 public，并明确开启；运行时可用性取决于是否存在可用默认构建；large 规格可保留现有物理 ID，展示不改变运行行为。

托管示例见[运行第一个工作流](/docs/guides/workflow)，完整自定义流程见[构建并使用自定义 Runner 模板](/docs/guides/custom-templates)。
