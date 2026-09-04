# AgentTeams v1.2.2 部署契约

该目录保存 Haowork 对官方 AgentTeams `v1.2.2` 的可审计事实锁、双区配置模板和网络
策略，不复制或修改上游源码。

机器可读契约位于 `upstream.lock.json`，固定上游 tag、commit、Helm Chart、CRD、
镜像仓库、标签与摘要。部署入口必须以锁文件为准，不得使用 `latest`、占位摘要或一个
摘要替代多组件镜像清单。

## 上游缓存

上游源码缓存位于当前仓库：

```text
.haowork/cache/upstream/AgentTeams-v1.2.2
```

执行静态合同检查：

```powershell
powershell.exe -NoProfile -File .\scripts\p0-05-v122-common.tests.ps1
```

检查内容包括固定缓存路径、Git HEAD/tag、工作树洁净状态、Chart 元数据、CRD 和镜像
合同。缓存、渲染结果、Kubeconfig、Secret 和运行证据只允许写入 `.haowork/cache/`。

## 模型服务配置

复制 `.env.example` 为 `.env.local`，分别填写 Public 和 Internal 区使用的
OpenAI-compatible Provider、Base URL、API Key、模型 ID 和最小出口 CIDR：

```powershell
Copy-Item .\deploy\agentteams\v1.2.2\.env.example `
  .\deploy\agentteams\v1.2.2\.env.local
notepad .\deploy\agentteams\v1.2.2\.env.local
```

`.env.local` 已被 Git 忽略。加载器只接受白名单变量，不执行文件内容；同名进程环境变量
优先于文件值。Base URL 应为 API 根路径，例如 `https://provider.example/v1`。

运行配置合同测试：

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

## GitHub Actions 分层验证

`.github/workflows/agentteams-v122.yml` 提供两层互不混淆的验证：

- 普通 Pull Request、`main` 推送和手动运行都会执行无凭据合同测试。Ubuntu 验证
  workflow 结构，Windows 额外验证上游锁、配置加载、脚本合同、网络证明和离线收据协议。
- 真实 Kind 双命名空间 E2E 只在手动运行时显式选择 `run_real_e2e=true` 后进入
  `agentteams-e2e` GitHub Environment，并等待该 Environment 的人工审批。

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

workflow 权限只有 `contents: read`，不包含 `secrets.*`，不上传原始 Artifact，也不执行 Commit、Push、PR 或
自动合并。没有手动触发真实 job 时，真实 AgentTeams E2E 状态必须记为 `NOT_RUN`；自动
合同测试通过不能替代真实集群验收或物理双区验收。
