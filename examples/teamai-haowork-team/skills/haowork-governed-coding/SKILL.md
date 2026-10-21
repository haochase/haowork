---
name: haowork-governed-coding
description: 在 Haowork 实际治理状态和受限 Scope 内执行编码任务
---

入口第一步：读取当前项目 Haowork 实际治理状态，使用项目文档指定的只读 CLI/API/MCP；读取失败或状态来源无法验证即停止。不得用聊天、TeamAI 文件或历史缓存代替状态。

执行前逐项核对有效 Mission、当前 GoalVersion、受限 Scope、可追溯 Context 与完成条件。记录各自 ID/版本及读取时间；任何一项缺失、过期、冲突或无法读取时，停止并报告 BLOCKED、缺失项及需要 Human 补充的具体决定。

仅在 Mission 授予的路径、动作和资源范围内工作。Scope 外需求必须停止相关动作并交给 Human；不得自批、扩大 Scope、伪造身份或绕过租约和审批。TeamAI 只分发 Skill/Rule/Agent；Haowork 管理 Mission、Scope、Context、审批、结构化回执与完成判定；AgentTeams 执行工作。

先确认现有状态和测试基线，再做小范围实现和相称验证。记录实际命令、退出码、证据引用与未执行项 NOT_RUN。保留无关修改，不复制真实密钥、私有地址、人员资料、聊天或制品到团队资产。

完成实现后仅形成候选结论。交由经服务端认证的 Delivery Leader 依 `haowork-submit-receipt` 提交候选回执；普通 Agent 聊天、Git 合并和 TeamAI learning 均不能使任务最终完成。高风险审批和最终验收由 Human 执行。
