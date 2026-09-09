# 配置并使用 Cache S3

在 Preferences 中保存 Cache S3，然后在 workflow 中使用 `qiniu/actions-cache@v5`。缓存数据直接写入用户自己的 Bucket，runnerd 不转发字节。

## 开始前

需要准备：

- 已连接的 GitHub 仓库，以及有效的 Sandbox 服务；
- 位于所选 Sandbox Region 对应 S3 Region 的 Bucket；
- 对该 Bucket 有对象读写权限的 AK / SK。

个人账号在[账号 Preferences](/account/preferences) 配置。组织仓库由 active member 在 `/organizations/{login}/preferences` 配置。Outside collaborator 只能查看就绪状态，不能保存组织 Cache S3。

不要把 Cache AK/SK 写入 workflow、仓库 Secrets、Issue 或聊天消息。

## 与官方 cache 的区别

官方 `actions/cache` 把缓存存在 GitHub Cache Service。沙箱 runner 不会签发那套凭证，所以官方 cache 在这里不可用，也不应再打开 `actions/setup-go` 自带 cache。

`qiniu/actions-cache@v5` 把缓存写到你在网页上配置的 S3 Bucket：

- 数据面直连 S3，runnerd 不转发缓存字节；
- 凭证是 runnerd 签发的短期 STS，不是长期 AK/SK；
- 读写范围由 runnerd 按仓库和分支 / PR 注入，workflow 改环境变量也无法超出 STS 授权；
- Fork PR 先读 PR，再读 base 和默认分支，只写入自己的 `pr-N`。

workflow 的 `key` / `restore-keys` 写法仍接近官方 cache，但 `uses` 必须换成 `qiniu/actions-cache@v5`。

## 1. 在网页上配置 Cache S3

Cache S3 没有全局配置，每个账号或组织都要自己填。个人账号打开[账号 Preferences](/account/preferences)；组织仓库由 active member 打开 `/organizations/{login}/preferences`。

先选择并保存 Sandbox 服务区域，再填写 **Cache S3**：

| 字段 | 谁填 | 说明 |
| --- | --- | --- |
| Region / Endpoint | 自动带出 | 由所选 Sandbox 区域决定，不能手改 |
| Bucket | 用户填写 | 必须位于该 Sandbox Region 对应的 S3 Region |
| Prefix | 可选 | 默认 `gh-actions-cache`。对象路径是 `<prefix>/<owner>/<repo>/scopes/...` |
| Access Key ID | 用户填写 | 对该 Bucket 有对象读写权限的 AK |
| Secret Access Key | 用户填写 | 对应 SK。保存后加密存储，页面不再回显 |

保存后，页面只会显示“已配置”，不会再次给出完整密钥。替换时重新输入新的 AK/SK。如果提示所选区域未配置 S3 Endpoint，请联系 runnerd 管理员补齐 `sandbox.regions` 的 `s3_region` 和 `s3_endpoint`。

也可以使用 IAM 子账号，而不是主账号密钥。在 [IAM 概览](https://portal.qiniu.com/iam/overview) 创建用户，并分配：

- Bucket：读取文件、上传文件、修改文件、删除文件；
- 安全凭证服务：获取临时身份凭证。

runnerd 用这对 AK/SK 签发 STS，沙箱里的 cache action 只使用短期凭证。

建议每个 GitHub installation 使用独立 Bucket，并在 Bucket 上配置生命周期规则，让过期缓存自动删除。

## 2. 在 workflow 中使用缓存

使用 [`qiniu/actions-cache@v5`](https://github.com/qiniu/actions-cache) 替代 `actions/cache`。workflow 不需要填写 Bucket、endpoint、region、AK、SK：

```yaml
jobs:
  check:
    runs-on: [qiniu, ubuntu-24.04]
    steps:
      - uses: actions/checkout@v6
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
          cache: false
      - uses: qiniu/actions-cache@v5
        with:
          path: |
            ${{ github.workspace }}/.cache/go/pkg/mod
            /tmp/go-build-cache
          key: ${{ runner.os }}-go-check-${{ hashFiles('**/go.sum') }}
          restore-keys: |
            ${{ runner.os }}-go-check-
            ${{ runner.os }}-go-
```

`setup-go` 只负责安装 Go；缓存统一走 `qiniu/actions-cache@v5`。step 开始时 restore，job 成功结束后由 post step save。job 失败不会 save。

## 3. 缓存隔离

- 分支 job 先读当前分支，再回退默认分支，只写入当前分支。
- 内部 PR 先读 PR，再读 base 和默认分支，只写入自己的 `pr-N`。
- Fork PR 先读 PR，再读 base 和默认分支，只写入自己的 `pr-N`。如果 GitHub 没有给出可信 PR 元数据，该 job 会保持默认分支只读，日志出现 `Cache save skipped: RUNS_ON_S3_CACHE_WRITE_PREFIX is not set`。
- `pull_request_target`、`workflow_run`、`issue_comment` 以及无法验证的元数据也是默认分支只读。

并行 job 不要共用同一个 write key。可选方案：

1. 每个会 save 的 job 使用独立 key，例如 `Linux-go-tidy-` 和 `Linux-go-test-`。
2. 单独做一个预热 job 负责 save，其他 job 只 restore。

同一次 run 里，A job 的 restore 可能发生在 B job 的 save 之前。需要复用镜像或产物时，脚本必须自带 miss 兜底。

## 4. 验证

成功时日志应包含：

```text
The cache action detected a local S3 bucket cache. Using it.
Cache restored successfully
Cache saved successfully
```

Fork PR 无法验证时，save 行是 `Cache save skipped`。如果没有 `local S3 bucket cache`，说明该 job 未注入 Cache S3，action 已回退到 GitHub 官方 cache。

## runnerd 管理员配置

部署 runnerd 的管理员需要在 `runnerd.yaml` 中提供区域目录和 STS 端点：

```yaml
sandbox:
  regions:
    - id: us-south-1
      label: "United States · Dallas 1"
      sandbox_api_url: https://us-south-1-sandbox.qiniuapi.com
      s3_region: us-north-1
      s3_endpoint: https://internal-s3-las-us-north-1-dal.qiniucs.com
    - id: cn-yangzhou-1
      label: "China · Yangzhou 1"
      sandbox_api_url: https://cn-yangzhou-1-sandbox.qiniuapi.com
      s3_region: cn-east-1
      s3_endpoint: https://internal-s3-las-cn-east-1-yz.qiniucs.com

cache:
  sts_endpoint: https://sts-ov.qiniuapi.com
```

每个 region 必须包含 `id`、`label` 和 `sandbox_api_url`。`s3_region` 与 `s3_endpoint` 必须成对出现；只有两者都配置的区域才支持 Cache S3。Endpoint 可能只在 Sandbox 内网可达，所以 runnerd 保存用户配置时只校验格式，不从控制面探测 Bucket。

每次启动沙箱时，runnerd 校验 Workflow Run 上下文，签发有效期为沙箱生命周期加五分钟的 STS，并注入 `RUNS_ON_S3_*` 环境变量。没有刷新机制。

完整部署步骤见[部署 runnerd](/docs/getting-started/deploy)。
