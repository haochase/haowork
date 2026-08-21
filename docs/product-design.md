# Haowork 产品设计与工程治理模型

## 1. 产品定义

Haowork 是一个 **AI 原生软件工程治理与追溯平台**。它不负责取代代码托管平台，也不重新实现
多智能体框架，而是在人的工程决策、Agent 的执行过程和 Git 的代码变化之间建立可验证的治理链。

技术上，Haowork 是一个**软件工程治理控制面**：

- 把需求、设计和完成条件记录为可版本化事实；
- 把任务责任、上下文、权限和风险绑定到每次执行；
- 将多智能体运行、工具调用、验证结果和制品转化为可审计证据；
- 在多人、离线、跨环境和长期维护中保持工程连续性；
- 对目标漂移、设计分歧、身份漂移和证据不足执行失败关闭。

## 2. 问题背景

AI Coding 正在把软件开发从“人编写大部分代码”转向“人把握需求、设计与验证，Agent 执行大量
实现”。效率提高之后，团队的主要瓶颈也发生了变化：

1. 需求和设计散落在聊天、Issue、文档与个人记忆中，难以形成统一版本。
2. Agent 的输出速度超过人的逐文件审查能力，长期任务容易出现目标漂移。
3. 多个成员和多个 Agent 的责任、上下文和证据难以对应到同一条工程链。
4. 换设备、换 Agent、换模型或换网络环境后，项目很容易失去前期设计连续性。
5. 仅凭 Git 历史可以看到代码变化，却不一定能判断变化背后的需求、约束和验证依据。

Haowork 的设计目标不是保存更多聊天，而是把会影响工程判断的内容提升为结构化、追加式、可验证
的工程事实。

## 3. 治理对象

```text
Requirement
  -> GoalVersion
  -> Task + Responsibility
  -> Context + Constraints
  -> Mission + Lease + Approval
  -> AgentTeams Run
  -> Trace + Evidence + Artifact
  -> File Attribution + Workspace Digest
  -> Observed Commit + Governed Binding
```

- **Requirement**：人明确提出的需求及其验收条件。
- **GoalVersion**：需求与设计目标的版本边界；变更目标必须留下显式事件。
- **Task / Responsibility**：工作拆分以及 Human、Logical Agent、Agent Function 的责任归属。
- **Context**：本次执行允许读取的事实、约束、基线和任务范围。
- **Mission**：交付给 AgentTeams 的规范化执行合同，包含完成条件和载荷哈希。
- **Lease / Approval**：执行权限、时限、范围和高风险人工决策。
- **Trace / Evidence**：策略判断、调用生命周期、验证结果和制品摘要。
- **File Attribution / Workspace Digest**：执行结果与工作区变化之间的当前关联边界。
- **Observed Commit / Governed Binding**：只读解析不可变本地 Git Commit，并将其与目标版本、任务、
  Mission 和已投影 Evidence 建立经审批关系；不等同于 Push 或 Pull Request 集成。

## 4. 三层责任模型

### 4.1 Haowork 治理控制面

负责需求版本、责任、上下文、权限、证据、冲突、恢复和跨区连续性。治理规则位于模型之外，模型
不能通过输出文本修改自身权限或宣布完成。

### 4.2 AgentTeams 执行面

负责创建 Manager、Delivery Leader、Research、Build、Verify 等角色，完成消息协作、任务执行、
制品交换和工具调用。Haowork 通过官方 CRD、Matrix、MinIO/S3、Higress 和 MCP 与其连接。

### 4.3 Git / SCM 代码变更面

负责文件版本、分支、提交和协作开发。Haowork 当前记录文件归属与工作区摘要，并可显式观察本地
Commit、提出治理绑定、按 Mission 风险审批确认，以及在 Commit 不再由可信引用可达时使绑定失效。
Push、Pull Request、webhook 与托管平台 API 尚未接入。

## 5. 关键设计原则

### 5.1 不重复 AgentTeams

AgentTeams 解决多 Agent 如何执行。Haowork 只增加工程治理合同，不把角色编排、Matrix 消息或
制品存储重新包装成自身创新。

### 5.2 需求和设计必须版本化

需求变化不是覆盖旧文档，而是生成新的 GoalVersion。执行仍绑定旧版本时，系统应识别 stale
goal，而不是默许 Agent 继续完成已经失效的目标。

### 5.3 执行必须绑定上下文和权限

Mission 绑定 Project、GoalVersion、Task、ContextHash、Lease、Build/Verify Agent、Skill Scope
和规范化载荷哈希。任一绑定漂移，执行或回放都应失败关闭。

### 5.4 实现与验证分离

Build Agent 不能仅凭自己的输出成为完成证据。Verify Agent、工具审计和 Evidence 门禁需要独立
身份和可复验制品摘要。

### 5.5 团队同步不采用最后写入覆盖

多人节点使用追加式历史、离线 Outbox 和幂等键对账。Goal、Lease、Design、Evidence 等领域分歧
生成显式冲突，由具备权限的 Human 选择处理动作。

### 5.6 跨区迁移不是复制仓库

Capsule 只包含白名单、来源可验证、签名且经审批的工程事实。目标环境在内存中预览后重新绑定
运行时；密钥、私人记忆和源环境主体不能跨区继承。

### 5.7 区分记录事实与历史推断

Haowork 能可靠追踪由自身记录的需求和执行链。面对旧项目，只能从代码、Git、文档和人工访谈
生成候选关系，再由人确认基线；系统不应声称自动恢复原作者意图。

## 6. 行业场景

### 6.1 受限网络与敏感业务研发

外场使用更强模型和开放生态完成通用设计与实现，内场基于真实业务数据继续二次开发。Haowork
通过签名 Capsule 延续 Requirement、GoalVersion、Task、Context 和验证证据，同时禁止迁移运行
身份和凭据。需要外场协助时，只导出经过审批的最小问题上下文。

适用示例包括银行、证券、国防科研及其他具有网络隔离要求的研发组织。产品提供工程治理与迁移
机制，不代替组织合规和保密制度。

### 6.2 AI Coding 小团队

成员负责提出需求、确认设计和审核关键节点；Agent 执行大量代码与测试。Haowork 提供统一的需求
版本、责任矩阵、Mission、Evidence 和冲突视图，使人可以像审核目录、摘要和关键章节一样审核
快速增长的 Agent 产出。

### 6.3 历史项目与开源项目二次开发

新项目可以建立完整需求到证据链；旧项目通过导入 Git、代码和文档建立候选基线，并由维护者确认。
后续变更便能持续关联需求根因、约束和验证记录，为重构影响分析提供依据。

## 7. 当前能力边界

### 已实现并有本地测试覆盖

- 追加式治理事实与确定性 reducer；
- GoalVersion、Task、Context、Lease、Mission、Approval；
- Logical Agent 与运行时身份绑定；
- Skills、MCP、Trace Ledger 和 Evidence；
- Team Sync、离线队列、冲突检测与处置；
- 签名跨区 Capsule；
- AgentTeams `v1.2.2` 控制面与数据面适配；
- CLI、Local API、Workbench 和部署合同测试。
- 本地 Git Commit 只读观察、治理绑定、风险审批、回放与历史可达性失效。

### 已取得的本机集群证据

- 官方 AgentTeams `v1.2.2` 活跃镜像和渲染 inventory 已锁定到独立 digest；
- 2026-08-16 完成 Kind Public/Internal 双命名空间真实组件运行；
- 五角色拓扑、受认证 Core Bridge、Matrix、MinIO/S3 和 Higress MCP 数据链已验证；
- 双向 NetworkPolicy 拒绝、opaque cursor 和重启后无重复治理事件已进入脱敏证据。

### 尚需物理环境与效果证明

- Windows Public 与 Ubuntu Internal 在业务网络断开时的签名 Capsule/Return 人工交接；
- 物理环境断电、恢复、回迁冲突和最终合并的一次完整 E2E；
- A/B/C/D 每臂至少三次真实运行和可从签名原始事实重算的报告。

### 下一阶段工程

- Git Push、Pull Request 与托管平台只读元数据绑定；
- 面向需求版本与架构约束的可视化审查；
- GoalVersion 漂移和重构影响分析；
- 旧项目基线导入与人工确认；
- 真实双区 E2E 与 A/B/C/D 可复验基准。

## 8. 成功判据

Haowork 的价值不以“启动了多少 Agent”衡量，而以以下问题是否可回答衡量：

1. 能否从一次代码变化追溯到明确需求、目标版本、责任和上下文？
2. 能否证明执行者有权执行，验证者与实现者职责分离？
3. 能否在目标或设计漂移时阻止静默完成？
4. 能否在中断、离线或换环境后从持久状态继续，而不是依赖聊天记忆？
5. 能否只传递经过审批的最小工程事实，并在目标环境重新建立可信身份？

这些判据共同构成 Haowork 作为 AI 原生软件工程治理控制面的核心边界。
