# P0-05 AgentTeams v1.2.2 官方部署契约

该目录只保存 Haowork 对官方 AgentTeams `v1.2.2` 的可审计事实锁，不复制、修改或
重新发布上游源码。机器可读契约位于 `upstream.lock.json`，并固定以下事实：

- 上游仓库：`https://github.com/agentscope-ai/AgentTeams`
- tag：`v1.2.2`
- commit：`849182af8e017168a5a200a87b1062142caf462d`
- Chart：`helm/agentteams`，其 `version` 和 `appVersion` 均为 `1.1.1`
- Kubernetes API：`agentteams.io/v1beta1`
- CRD：`Manager`、`Worker`、`Team`、`Human`
- 控制器归属标签：`agentteams.io/controller`

Tag 与 Chart 元数据是两个独立事实。`v1.2.2` 不是 Chart 版本，任何部署脚本都必须
分别验证它们，不能把 Chart `1.1.1` 误写成 AgentTeams tag。

上游缓存必须仅位于：

```text
E:\haochase\haowork\.haowork\cache\upstream\AgentTeams-v1.2.2
```

执行本地静态校验：

```powershell
powershell.exe -NoProfile -File .\scripts\p0-05-v122-common.tests.ps1
```

该命令检查缓存路径、Git HEAD/tag、Chart 元数据和四份 CRD。它不联网、不下载镜像，
也不写入上游目录。

## 镜像状态

锁文件还逐项固定 Chart、Chart.lock、values 和四份 CRD 的 SHA-256。当前镜像状态为
`BLOCKED_IMAGE_DIGEST`，且渲染清单状态为 `BLOCKED_RENDERED_IMAGE_INVENTORY`，原因是
尚未在本机取得并审计全部直接镜像及 Higress 依赖 Chart 间接镜像的 digest。这个状态是
部署前置条件的显式阻断，不得替换成
`latest`、`REPLACE_WITH_*` 或单个统一镜像摘要。

当全部 Chart 依赖已经渲染并将完整 image inventory、清单 SHA-256 与每个 digest 固定
为单独的经过审阅的契约变更后，才允许引入 `RESOLVED` 状态。在此之前，Task 2 的预检
和真实部署必须失败关闭；9 个直接镜像或任意 64 位十六进制摘要都不能构成已审计状态。

所有缓存、渲染结果、kubeconfig、Secret 和真实运行证据都保持在
`E:\haochase\haowork\.haowork\cache\`，该路径已被 Git 忽略。不得将 token、
模型密钥、Matrix 密码、MinIO Secret 或 kubeconfig 写入本目录的已跟踪文件。

## 本地大模型配置

复制 `.env.example` 为同目录的 `.env.local`，然后分别填写 Public 与 Internal 区的
OpenAI-compatible Provider、Base URL、API Key、模型 ID 和严格出口 CIDR。`.env.local`
已被 Git 忽略，`preflight` 与 `up` 会自动读取；同名进程环境变量优先于文件值。

```powershell
Copy-Item .\deploy\agentteams\v1.2.2\.env.example `
  .\deploy\agentteams\v1.2.2\.env.local
notepad .\deploy\agentteams\v1.2.2\.env.local
```

Base URL 应是 API 根路径，例如 `https://provider.example/v1`，而不是
`/chat/completions`。模型字段必须填写 Provider 接受的准确模型 ID。API Key 可以使用
单引号或双引号包裹；加载器不会执行文件内容，且拒绝白名单以外的变量。

运行无集群副作用的配置契约测试：

```powershell
powershell.exe -NoProfile -File .\scripts\p0-05-v122-env.tests.ps1
```

## 部署与清理

预检、部署、集群验收和清理分别使用：

```powershell
powershell.exe -NoProfile -File .\scripts\p0-05-v122-preflight.ps1
powershell.exe -NoProfile -File .\scripts\p0-05-v122-up.ps1
powershell.exe -NoProfile -File .\scripts\p0-05-v122-cluster-test.ps1
powershell.exe -NoProfile -File .\scripts\p0-05-v122-down.ps1
```

脚本会在工具、镜像摘要、运行时凭据、网络白名单、浏览器入口或受认证 Core Bridge
缺失时失败关闭，不会用模拟状态生成真实部署证据。

部署脚本不会持久化或后台维护浏览器端口，也不会占用本地浏览器端口。需要访问 UI 时，按区
在两个终端分别以前台方式运行：

```powershell
powershell.exe -NoProfile -File .\scripts\p0-05-v122-browser.ps1 `
  -Zone public -ClusterName haowork-p005-v122

powershell.exe -NoProfile -File .\scripts\p0-05-v122-browser.ps1 `
  -Zone internal -ClusterName haowork-p005-v122
```

Public 固定映射到 `haowork-public-agentteams-element-web:8080` 和
`http://127.0.0.1:18080`；Internal 固定映射到
`haowork-internal-agentteams-element-web:8080` 和 `http://127.0.0.1:18082`。
每个终端只启动一个区的 `kubectl port-forward`，必须保持窗口运行；按 `Ctrl+C` 结束对应转发。
部署验收通过 Kubernetes API Server Service Proxy 对两区 Element Web `:8080` 与
`higress-gateway:80` 做有界 HTTP 就绪验证，Gateway 没有 Ready Endpoint 时会明确失败。

从旧版本升级时，`up`、`preflight` 和 `down` 不会读取或自动终止旧的浏览器 state。先确认
`18080`、`18082` 没有监听；确认无监听后，可删除忽略的
`.haowork/cache/kind/<cluster>.browser-port-forwards.json`。若仍有监听，回到启动它的终端按
`Ctrl+C`，或由操作者确认进程后手工结束；脚本不会自动杀进程。

## 结构化回执证据

真实集群验收不再依赖 Matrix 正文中的关联词或自然语言回声。Delivery Leader 必须通过
`haowork.submit_execution_report` 提交结构化执行回执；验收证据只保留可复核的脱敏摘要：

- `assignment`：Assignment ID 与 `REPORT_SUBMITTED` 状态；
- `matrix`：脱敏的 `dispatch_event_id` 与 `leader_dm` 房间类型；
- `receipt`：回执 ID、报告 SHA-256、`delivery_leader` 职能和绑定修订号；
- `artifact`：S3 URI、摘要、大小和环境 ID；
- `mcp`：固定服务器名、`agentteams_worker` 消费者类别和 Trace SHA-256。

不得写入完整 Consumer Name、Worker Gateway Key、Matrix 正文、发送者或房间 ID、私有路径、
IP 地址和主机凭据。缺少真实运行配置、Kind 集群或外部 Provider 时，脚本明确输出
`NOT_RUN`；已开始但不满足合同的运行必须输出 `BLOCKED_*`，不能用模拟结果代替。

## 双区与清理门禁

Public 与 Internal 使用独立的 Helm values、命名空间、ServiceAccount、PVC、Secret 和
NetworkPolicy。Core Bridge 每次运行都要求新的 Run ID；Higress 绑定发布、Binding TTL、
Artifact 摘要和 Trace 摘要均必须可复核。真实 E2E 只由受控 runner 手动触发，清理步骤只
删除本次拥有的 Kind 集群和资源；普通合同测试不会创建或删除真实集群。

## GitHub Actions 分层验证

`.github/workflows/agentteams-v122.yml` 提供两层互不混淆的验证：普通 Pull Request、
`main` 推送和手动运行执行无凭据合同测试；真实 Kind 双命名空间 E2E 只有在手动选择
`run_real_e2e=true` 后才进入 `agentteams-e2e` GitHub Environment 等待人工审批。

真实 E2E 使用标签为 `self-hosted`、`windows`、`x64`、`haowork-agentteams-v122` 的
受控 runner。runner 工作区必须位于非 C 盘，并预装 Docker、Kind、Helm、kubectl、Git、
Go、Node、npm 和 PowerShell 7。Environment 只承担审批与 `main` 分支限制，不保存模型
或网络配置。

runner 进程必须在本机设置 `HAOWORK_AGENTTEAMS_ENV_FILE`，指向非 C 盘、未进入 Git 的
`.env.local`。真实 job 使用严格白名单加载器在单个 PowerShell 进程内读取以下十项值，
执行日志掩码后直接完成预检、部署和验收；不会写入 `$GITHUB_ENV` 或 GitHub Secrets：

```text
HAOWORK_P005_PUBLIC_LLM_PROVIDER
HAOWORK_P005_PUBLIC_LLM_BASE_URL
HAOWORK_P005_PUBLIC_LLM_API_KEY
HAOWORK_P005_PUBLIC_LLM_MODEL
HAOWORK_P005_PUBLIC_EGRESS_CIDRS
HAOWORK_P005_INTERNAL_LLM_PROVIDER
HAOWORK_P005_INTERNAL_LLM_BASE_URL
HAOWORK_P005_INTERNAL_LLM_API_KEY
HAOWORK_P005_INTERNAL_LLM_MODEL
HAOWORK_P005_INTERNAL_EGRESS_CIDRS
```

Public 与 Internal 必须分别解析各自模型 Base URL 的主机，以及以下官方 Higress 基础设施主机：

```text
higress-registry.cn-hangzhou.cr.aliyuncs.com
dockerauth.cn-hangzhou.aliyuncs.com
cri-r0xfyoxudmtseqq7-registry.oss-cn-hangzhou.aliyuncs.com
```

将每个主机当前返回的全部 IPv4 和 IPv6 地址分别以精确 `/32` 或 `/128` 填入对应区的
`HAOWORK_P005_{ZONE}_EGRESS_CIDRS`。预检不会自动扩大网段或改写 `.env.local`；任一模型地址
未覆盖时输出 `BLOCKED_EGRESS_CIDRS`，任一 Higress 基础设施地址未覆盖时输出
`BLOCKED_INFRASTRUCTURE_EGRESS_CIDRS`。解析地址可能变化，因此每次真实部署前都应重新解析并更新本地配置。

workflow 权限只有 `contents: read`，不包含 `secrets.*`，不上传原始 Artifact，也不执行 Commit、Push、PR 或
自动合并。没有手动触发真实 job 时，真实 AgentTeams E2E 状态必须记为 `NOT_RUN`；自动
合同测试通过不能替代真实集群验收或物理双区验收。
