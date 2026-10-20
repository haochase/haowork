# 工作流

1. Human 在 Haowork 确定 Mission、受限 Scope、可追溯 Context、完成条件和必要审批。TeamAI 分发团队资产；这不会创建治理授权。
2. AgentTeams 执行者先读取实际治理状态并记录版本；缺项、冲突、审批不足或无法读取时停止，报告 BLOCKED。严格在 Scope 内实现与验证。
3. Reviewer 和 Evidence Checker 检查实际证据、测试退出码和 NOT_RUN，只提出候选结论。发现越界或证据缺口时退回处理，不能自批。
4. 服务端认证的 Delivery Leader 读取当前 `haowork.submit_execution_report` schema，按真实合同提交结构化候选回执。普通 Agent 只传递候选材料；工具缺失或身份不可验证即阻断。
5. 核查工具返回及服务端状态。Human 执行高风险审批和最终验收；聊天、Git 合并和 learning 不决定完成。
6. Human 审核脱敏后的经验，再考虑提交 TeamAI learning 候选。候选保留来源、适用范围、审核记录；不能自动提升为 Requirement/Evidence/Approval。
