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
