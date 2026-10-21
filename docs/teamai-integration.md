# TeamAI 集成：安装、权限与公开边界

这是腾讯 `teamai-cli@0.27.0` 的兼容原型，没有腾讯合作、联合开发或官方背书。TeamAI 负责团队 AI 资产分发，Haowork 负责需求、责任、审批、证据与 SCM 关联，AgentTeams 负责执行编排，Git 负责源码历史与远端只读观察。它们不共享一个万能的管理员身份。

## 使用形态

1. 团队维护独立的 [TeamAI 资产模板](../examples/teamai-haowork-team/docs/onboarding.md)，包含 rules、agents、skills、teamwiki 与 learnings。模板内是占位仓库，不是已发布的服务。
2. 开发者在自己的工具环境安装固定版本 TeamAI CLI，再按其实际命令初始化团队资产。完整人工步骤见 [五端样例](../examples/teamai-haowork-demo/README.md)。安装缓存、工具和演示状态放在非系统盘；本地静态验证不需要下载 TeamAI。
3. Haowork CLI 是 Go 程序；Workbench、Local API 与 MCP 是同一治理系统的交互入口。运行 `haowork teamai doctor --help`、`bind --help`、`verify --help` 查看本版本的真实参数。使用服务端 MCP 前必须先发现实际 schema 并取得经认证的运行身份。

团队根目录 `teamai.yaml` 是 TeamAI 分发配置；消费项目 `.haowork/teamai.yaml` 是 Haowork 的显式绑定声明，二者不可互换。`bind` 只允许 Human Owner/Lead，并要求 repo/revision 与项目配置一致；`verify` 检查实际初始化状态与必需资产 SHA-256。`team_revision` 是声明值，资产字节校验不证明资产仓库 Git HEAD。

Skill 是给开发者工具的使用说明，不能授予服务端角色或绕过认证。Mission、Lease、Scope、Context、RuntimeBinding 和 Skill grant 来自 Haowork 服务端。认证 Delivery Leader 提交 `haowork.submit_execution_report` 后，`accepted_as_candidate` 只代表候选回执。独立 Evidence、所需审批和 Human 验收仍是后续门禁。GitHub merged、Review 或 Check 只读事实不能自动完成 Task。

## Mission 来源快照与回执绑定

`haowork mission issue` 只接受 `--teamai-binding <binding-id>`。Haowork 回放 Capsule 后确认该绑定属于当前项目且状态为 `Ready`，再生成不可变 `teamai_source`：`binding_id`、`team_repo`、声明的 `team_revision` 和已核验资产 `asset_digest`。该快照参与 Mission canonical hash，随后由 Assignment、Receipt 和 Trace 服务端派生，官方 `submit_execution_report` schema 不增加 TeamAI 输入字段。

本地失败关闭合同覆盖非 Ready 绑定和 Assignment 与 Receipt 快照不一致；失败发生在候选写入前时，Receipt Store 和 Trace Store 都保持无新增记录。报告 JSON 即使额外注入 `teamai_source`，也会由真实 skillapi resolver/adapter 路径剥离，最终 Receipt/Trace 仍从 Assignment 派生原始快照。旧 Mission 没有显式绑定时仍使用 `nil` 快照并可回放。

## 验证状态

| 层级 | 当前证据与边界 |
| --- | --- |
| 本地 Go 合同 | 已通过配置、绑定事件、Mission/Assignment/Receipt/Trace 同快照重放与失败关闭测试；TeamAI doctor 使用受控 Runner |
| 资产与五端样例 | 静态合同通过；五端为 Windows、macOS、Web、Android、iOS；没有原生设备或支付实现验收 |
| 真实分发与远端 CI | TeamAI init/分发与远端 GitHub Actions `NOT_RUN`；本机 PowerShell/Git Bash 通过不代表 Ubuntu runner 已执行 |
| TeamAI + AgentTeams 双命名空间 | `NOT_RUN`，需要真实模型、集群、身份、现成服务和人工授权 |
| 外部系统链 | `NOT_RUN`；本地合同只证明 Capsule 与 JSONL 存储中的服务端派生和回放，不代表 AgentTeams、MinIO、GitHub 或模型联调 |

更早的 AgentTeams 双区记录是另一个验证范围。当前 [演示脚本](teamai-demo-script.md) 不用这些历史记录替代新增集成验收。

## 双仓晋升

`haowork-internal` 保留完整设计、运行证据、Runner、模型、物理环境与原始日志。公开 `haowork` 只接收显式审查的通用接口、脱敏资产、样例和文档增量；不是把私有仓库直接镜像出去。

[精确白名单](../.github/public-path-allowlist-teamai.txt) 每行一个文件，不支持通配符。它定义候选增量，不是完整可编译仓库清单；依赖的已有公开基线必须在最终 PR 中另行核对。新增路径必须经过人工审查后显式列入，候选目录不可含 Git 元数据。

共享文件 `internal/model/model.go`、`internal/model/reducer.go`、`internal/cli/project_commands.go` 只允许晋升 TeamAI 专属 hunks：状态字段、两个事件分支、初始化 map、CLI 注册。必须在固定公开基线上应用这些差异，不得拿私有分支整文件覆盖公开版本。README 和 ignore 同样只应用本轮说明/忽略目录差异，保留公开基线已有历史与导航。白名单放行文件路径不等于放行该文件的全部私有改动。

[公开合同 workflow](../.github/workflows/teamai-public-contract.yml) 采用 JSON-compatible YAML，只执行公开 TeamAI 单元、模板、场景和扫描器合同，不依赖私有 P0-05、真实 preflight 或 receipt E2E。完整跨模块回执验证与物理 Runner 合同留在私有仓；公开流程通过不证明这些私有或真实系统路径已运行。服务端是否存在回执工具，仍须从目标环境发现 schema，不能从本文推定公开基线已部署。

在非系统盘建立只含待公开文件的候选目录，保持仓库相对路径，然后运行同一无依赖核心的入口：

```powershell
pwsh -NoProfile -File scripts/check-teamai-public.ps1 -CandidateDirectory <candidate-directory>
node --test scripts/check-teamai-public.test.mjs
```

```bash
bash scripts/check-teamai-public.sh <candidate-directory>
```

成功只输出 `TEAMAI_PUBLIC_SCAN_OK`；失败只输出稳定的 `TEAMAI_PUBLIC_*` 错误码，不回显路径或内容。扫描拒绝链接/目录重定向、白名单外路径、凭据文件、明显 Token、含 Unicode 的个人绝对路径、真实凭据赋值、无效 UTF-8、二进制控制字节与超过 1 MiB 的单文件。URL 使用标准 URL 与 IP 解析，拒绝全部 IP literal（含 IPv4-mapped IPv6、整数/缩写 IP）、内网域名和 URL 凭据。普通 API Key 术语、带引号且无尾斜杠的 example.invalid endpoint 占位，以及公开展示链接允许通过。规则扫描不能证明不存在任何未知秘密；人工差异审阅仍不可省略。

必须排除 `.haowork/`、`.cache/`、`.worktrees/`、`docs/superpowers/`、`docs/testing/`、`deploy/`、物理 Runner/preflight、真实配置、原始证据和门户/隧道部署。白名单不授权 Git 写操作或远端发布。

晋升顺序为：显式白名单 → 内容扫描 → 跨平台合同测试 → 最终 staged diff/PR 人工审阅 → 经用户授权的公开 PR。本轮只准备规则并验证本地候选，未提交、推送或创建 PR。现有 [在线展示](https://haowork.112318.xyz/) 独立部署，其源码与运行配置不进入本仓。
