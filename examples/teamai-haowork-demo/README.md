# TeamAI × Haowork 五端治理样例

这是虚构、脱敏的支付 SDK 治理构造场景，产品是 Haowork 的治理闭环。没有支付实现，不处理真钱。所有机构、角色、任务标签与路径是示例；真实账户、仓库地址、任务 ID、OID、摘要、回执须运行时取得。`project/scenario.json` 是单一事实源，包含目标、历史需求根因、版本、任务、五端、负责人、Scope、决策和预期检查。

## 0. 本地静态检查

需要 Node.js，无依赖安装。从本目录执行：

```powershell
node scripts/test-demo.mjs
./scripts/validate-demo.ps1
```

```bash
bash scripts/validate-demo.sh
```

唯一静态成功标记为 `TEAMAI_HAOWORK_DEMO_CONTRACT_OK`。校验器只检查结构、引用和基础敏感内容，不证明运行、身份、设备兼容或完整秘密扫描。以下流程是人工操作指南，当前实际执行状态见 evidence/README.md，均 NOT_RUN。

## 1. 建立自己的团队仓库

复制 `../teamai-haowork-team/` 到自己管理的新团队仓库，人工设置真实 repo URL 和固定 revision；不要把 example.invalid 当成已分发证明。先阅读该模板 docs/onboarding.md，按其 teamai.yaml 的实际字段配置。团队根目录的 teamai.yaml 属于 TeamAI；下面项目的 .haowork/teamai.yaml 属于 Haowork。

打开专用演示 PowerShell 会话，先按仓库 docs/cli-p0.md 构建当前 haowork。下面命令从本样例目录开始执行；也可把 `$demoRoot` 替换成自己的实际绝对路径。先创建 E 盘治理项目，再安装固定 CLI；不会运行 teamai push：

```powershell
$ErrorActionPreference = 'Stop'
$demoRoot = (Resolve-Path '.').Path
if (-not (Test-Path (Join-Path $demoRoot 'project/scenario.json'))) { throw '请从本样例目录执行' }
$sourceRoot = (Resolve-Path (Join-Path $demoRoot '../..')).Path
$haowork = (Resolve-Path (Join-Path $sourceRoot 'bin/haowork.exe')).Path
$project = Join-Path $sourceRoot '.demo-runtime/payment-governance'
if ([IO.Path]::GetPathRoot($project) -ne 'E:\') { throw '请将演示源码放在 E 盘' }
New-Item -ItemType Directory -Force $project | Out-Null
$demoUserDirectory = Join-Path $project '.demo-user'
$env:USERPROFILE = $demoUserDirectory
# TeamAI 0.27.0 首选 HOME，缺失时才用 USERPROFILE。
# 仅从此专用演示进程移除 HOME，使其使用 E 盘 USERPROFILE；不修改系统配置。
Remove-Item Env:HOME -ErrorAction SilentlyContinue
$env:npm_config_cache = Join-Path $project '.cache/npm'
$env:TEMP = Join-Path $project '.cache/tmp'
$env:TMP = $env:TEMP
New-Item -ItemType Directory -Force $env:TEMP, $demoUserDirectory | Out-Null
node -e "if(require('node:os').homedir().slice(0,2).toUpperCase() !== 'E:') process.exit(1)"
if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_USER_DIRECTORY' }
$repo = Read-Host '自己的真实完整 HTTPS 团队 Git URL'
if ($repo -notmatch '^https://[^/]+/.+' -or $repo -match 'example\.invalid|@') { throw 'BLOCKED_TEAM_REPO' }
$tools = Join-Path $project '.tools'
npm install --prefix $tools --no-save teamai-cli@0.27.0
if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_TEAMAI_INSTALL' }
$teamaiBin = (Resolve-Path (Join-Path $tools 'node_modules/.bin')).Path
$env:PATH = $teamaiBin + [IO.Path]::PathSeparator + $env:PATH
$teamai = (Get-Command teamai -ErrorAction Stop).Source
& $teamai --version
& $teamai init --help
Push-Location $project
try {
  & $teamai init $repo --scope project
  if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_TEAMAI_INIT' }
  & $teamai doctor --json
  if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_TEAMAI_DOCTOR' }
} finally { Pop-Location }
```

`init [repo] --scope project` 在治理项目 cwd 初始化，并交互选择实际已安装工具。TeamAI bin 必须保留在这个会话 PATH：Haowork detector 固定执行 `teamai`，不能只用一次绝对路径调用代替。TeamAI 0.27.0 的用户目录 fallback 与上述 HOME 移除仅限专用会话；完成后关闭该会话，不修改系统 HOME。未配置仓库或运行身份时为 NOT_RUN / BLOCKED_TEAM_REPO，不能继续宣称分发成功。

## 2. 创建治理对象和项目期望声明

在上一步同一会话继续，保留 PATH 和 E 盘用户目录设置。以下 PowerShell 中的 actor 是虚构的本地归属标签；CLI actor flags 不能认证身份。`$haowork`、`$project`、`$demoRoot` 已在上一步赋值并校验。

```powershell
& $haowork init --project $project --name 'fictional-payment-sdk' --actor 'USR-DEMO-OWNER' --goal '五端结果与超时语义一致' --done-when '证据完整且 Human 验收' --invariant '不得静默删除历史约束' --json
$plan = & $haowork plan create --project $project --title '保留历史超时对账约束' --task '复核五端取消与迟到结果合同' --acceptance '五端证据及 Human 验收' --constraint '不接入真实支付' --actor 'USR-DEMO-OWNER' --role owner --json | ConvertFrom-Json
$taskId = $plan.tasks[0].id
& $haowork approve $plan.requirement.id --project $project --actor 'USR-DEMO-OWNER' --role owner --json
```

`init` 创建 GoalVersion v1；当前 P0 没有 goal-mutation 命令，不能编辑 manifest 冒充新版本。以下 `$demoRoot` 是本样例目录的实际绝对路径；先复制资料，再创建 Context：

```powershell
Copy-Item -Recurse (Join-Path $demoRoot 'project') $project
$context = & $haowork context build $taskId --project $project --source project/scenario.json --source project/docs/decision-history.md --allow project/docs --allow project/adapters --reason '保留需求根因并限制编辑范围' --actor 'USR-DEMO-OWNER' --role owner --json | ConvertFrom-Json
$contextId = $context.id
if (-not $contextId) { throw 'BLOCKED_CONTEXT' }
```

`$contextId` 来自实际返回的 ContextSlice.id，不手填。根据实际安装目录内每个治理资产的文件字节生成摘要，而不是复制模板摘要。下面以必须包含的 Skill 为例；应把其他实际使用的 agents/rules/learnings 资产也逐个加入 required_assets：

```powershell
$assetRoot = (Resolve-Path (Read-Host '实际安装的团队资产根目录')).Path
$revision = Read-Host '固定 revision（40 或 64 位十六进制声明）'
if ($repo -match 'example\.invalid') { throw 'BLOCKED_TEAM_REPO' }
$asset = 'skills/haowork-governed-coding/SKILL.md'
$digest = (Get-FileHash -Algorithm SHA256 (Join-Path $assetRoot $asset)).Hash.ToLowerInvariant()
$config = @{
  schema_version = '1'; team_repo = $repo; team_revision = $revision
  scope = 'project'; auto_update = 'manual'
  required_assets = @(@{ path = $asset; kind = 'skill'; sha256 = $digest })
}
# JSON 是 YAML 的子集；以 UTF-8 写入项目期望配置。
$config | ConvertTo-Json -Depth 5 | Set-Content -Encoding utf8 (Join-Path $project '.haowork/teamai.yaml')
& $haowork teamai bind --project $project --team-repo $repo --revision $revision --actor 'USR-DEMO-OWNER' --role owner --json
& $haowork teamai verify --project $project --asset-root $assetRoot --json
```

bind 只允许 Human Owner/Lead；team_revision 是声明值，资产字节校验不证明 Git HEAD。verify 还要求实际 TeamAI 初始化与环境检测通过，失败不可伪装成功。

## 3. 受限 Mission 与运行回执

另一个终端运行 `haowork serve <自己的演示项目目录>`，按服务实际身份注册/认证流程建立 Manager、Delivery Leader、Research、Build、Verify。Manager 协调，Leader 分派和汇报，Research 查需求根因，Build 修改允许的合同资料，Verify 独立核验证据。身份、Skill grant、Lease 和环境均来自 Local Core，不来自 Skill 自述。

先执行 `haowork mission issue --help`，人工填入真实任务/Context/Agent ID、环境和 policy 后执行：

```powershell
& $haowork mission issue --project $project --task $taskId --context $contextId --environment $environmentId --policy $policyVersion --criteria '五端合同复核' --scope project/docs --scope project/adapters --skill submit_execution_report@1.0.0 --build-agent $buildAgentId --verify-agent $verifyAgentId --actor 'USR-DEMO-OWNER' --role owner --json
```

上述未赋值变量必须来自实际环境；缺 Local Core、身份或授权时 BLOCKED_RUNTIME，不产生假 Mission。服务端认证后的 Delivery Leader 必须先发现运行时真实 MCP schema，再调用 `haowork.submit_execution_report`。当前仓库 skills/core/submit_execution_report.yaml 的输入为 assignment_id、status（succeeded/failed/blocked）、summary_sha256、artifacts、checks；artifact 含 kind/uri/sha256/environment_id/size，check 含 name/status/evidence_sha256。运行时 schema 是最终依据，禁止构造不存在的 wrapper 或手填假回执。

回执的 accepted_as_candidate 仅代表 candidate。摘要、制品大小/环境、身份、Lease、Context 和 Scope 都须校验。Evidence 提交与验证是独立门禁；Git Commit/PR/Check 是只读观察事实；Human 最终验收是另一个门禁。按 `haowork verify --help` 绑定实际测试文件及版本/Context，只有真实通过后再由 Human Owner/Reviewer 按 `haowork complete --help` 完成。不能将本目录静态检查输出冒充支付或五端测试。

Mission 只接收 `--teamai-binding <binding-id>`。服务端从当前 Capsule 回放状态中解析 `Ready` 绑定，生成包含 binding ID、团队仓库、声明 revision 和资产摘要的不可变 `teamai_source` 快照，并把它写入 Mission canonical hash。AgentTeams Assignment、候选 Receipt 和 Trace 都从上一层服务端事实复制该快照；Agent 报告不能注入或覆盖这些字段。未启用 TeamAI 的旧 Mission 保持 `teamai_source: null`，可继续回放。

私有仓库中的 `go test ./internal/e2e -run TestTeamAIMissionReceiptBinding -count=1` 使用真实本地 Capsule、TeamAI 资产字节、JSONL Receipt Store 和 Trace Store，验证重开后仍是同一快照，并验证非 Ready 和 Assignment/Receipt 快照不一致会失败关闭且不产生候选。报告中额外注入的 `teamai_source` 会在真实 skillapi resolver/adapter 路径被剥离，Receipt/Trace 仍从 Assignment 派生原始快照。AgentTeams、MinIO、GitHub、模型和设备验收均标记 `NOT_RUN`，不以本地合同冒充真实联调。

GitHub 只读观察步骤参见仓库 docs/scm-github-observer.md 的 register/connect/sync/status；不自动执行 Git commit/push/PR/merge，不进行真实模型调用或双区部署。

## 4. 成功和失败演练

成功路径是：真实资产校验 → 受限 Mission → 认证 Leader 回执 candidate → 摘要匹配的 Evidence → Git 只读事实对账 → Human 最终验收。当前仅提供构造方案，运行状态 NOT_RUN；每一步保留真实运行证据后才可升级对应状态。

| 情况 | 阻断层 | 可保留事实 | 不能升级的状态 |
| --- | --- | --- | --- |
| 只有自然语言回复、缺回执 | Execution report / receipt gate | 对话可作非权威备注 | 不能成为 accepted candidate、Verified 或 Completed |
| TeamAI 声明或资产字节不一致 | TeamAI bind/verify | 原声明与失败诊断 | 不能宣称资产已验证或授权运行；BLOCKED_ASSET |
| Leader 运行身份错配 | 服务端身份、function 与 Lease gate | 身份拒绝诊断 | 不能接受为 candidate 或升级任务；BLOCKED_IDENTITY |
| 制品摘要不一致 | 制品完整性 / Evidence gate | 原 candidate 回执与失配事实 | candidate 不能转为通过 Evidence、Verified 或 Completed；BLOCKED_DIGEST |
| PR 已合并、任务未 Human 验收 | Human completion gate | 只读 PR merged、Commit、Check 观察 | 不能自动形成 Evidence 或 Completed；BLOCKED_HUMAN_ACCEPTANCE |

这是五种失败情形（需求中合称四类）；不为任何路径提供假成功日志。真实环境缺失时保留 NOT_RUN，不能把预期拒绝写成已验证拒绝。
