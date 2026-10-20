# 个人项目面试简报：团队 AI 资产与工程治理

我实现的是团队 AI 资产分发与工程治理之间的兼容原型：TeamAI 分发共用规则和 Skill，Haowork 保存需求、责任、审批、证据和 SCM 关联，AgentTeams 编排执行，Git 保存代码与远端观察事实。目标是让协作过程中的需求漂移、身份错配和缺证据成为可检查状态。

兼容对象是腾讯 `teamai-cli@0.27.0`，不是腾讯合作、联合开发或官方背书。项目没有可据此声称的客户上线、生产成功或效率提升百分比。

## 可展示的工程能力

- 设计边界：把资产提示、服务端权限、候选回执、Evidence 与 Human 验收分开，避免 Agent 自称成功就完成任务。
- 一致性：绑定声明与资产 SHA-256 校验，追加事件与重放，声明冲突不覆盖历史。资产校验不冒充 Git HEAD 校验。
- 失败关闭：本地合同覆盖缺回执、principal/revision 错配、摘要错误；GitHub GET-only 观察不产生完成状态。
- 交付工程：脱敏模板、五端构造场景、Windows/Ubuntu 合同 workflow、手动真实联调入口与显式公开白名单。workflow 文件存在不代表远端已执行。

## 可复现指标

| 指标 | 取得方式 | 解释边界 |
| --- | --- | --- |
| 资产 validator 通过/失败数 | `node examples/teamai-haowork-team/test-validators.mjs` 的当次输出 | 结构合同，不是团队采用率 |
| 五端场景合同通过/失败数 | `node examples/teamai-haowork-demo/scripts/test-demo.mjs` 的当次输出 | 五个平台资料，不是五端设备兼容验收 |
| 绑定、配置与资产完整性 | `go test ./internal/teamai ./internal/model ./internal/app ./internal/cli -run 'TestPublicTeamAI|TestTeamAI' -count=1` | 公开单元合同与受控 Runner，不是真实编排；完整回执 E2E 留在私有仓 |
| 公开扫描拒绝行为 | `node --test scripts/check-teamai-public.test.mjs` | 有限规则检测，不是无秘密的形式化证明 |

指标使用当次测试输出，不把旧次数或耗时写成性能收益。讲解时可演示一个输入错误、查看拒绝、修正后重放，再解释保留历史与人工门禁的原因。

## 当前未证明什么

真实 TeamAI 分发、远端 GitHub Actions、TeamAI 与 AgentTeams 双命名空间联调均 `NOT_RUN`；同一 TeamAI Binding/Mission/Receipt 的不可变跨系统链 `NOT_PROVEN`。五端原生设备与真实支付未测试。在线展示是独立部署的只读预置案例，不能证明生产项目已通过验收。

下一项有价值的验证是在具备身份、模型、集群与明确授权后，保留同一实际运行的资产、Mission、Receipt 和验收关联证据，而不是增加虚构成功截图。具体步骤见 [演示脚本](../../../docs/teamai-demo-script.md)。
