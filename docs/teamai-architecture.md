# TeamAI 与 Haowork：事实流和门禁

实线表示受治理的操作或事实写入；虚线表示只读观察；菱形表示人工或服务端门禁。图是目标操作流程，不表示真实系统联调已经完成。当前本地跨模块合同覆盖了同一 Binding/Mission/Assignment/Receipt/Trace 快照的重开回放；真实 TeamAI 双命名空间 E2E 仍为 `NOT_RUN`。

```mermaid
flowchart LR
  TA[TeamAI 团队资产] --> TOOL[开发者工具与 Skill]
  TOOL --> G{Haowork 认证与授权}
  HUMAN{Human 目标与风险审批} --> G
  G --> HW[需求 / Context / Mission / Lease]
  HW --> AT[AgentTeams 执行编排]
  AT --> RECEIPT[认证 Leader 候选回执]
  RECEIPT --> EV[独立 Evidence 验证]
  EV --> ACCEPT{Human 最终验收}
  ACCEPT --> DONE[完成事实]
  GIT[Git Commit / PR / Check] -.只读观察.-> SCM[SCM 关联与差异]
  SCM -.辅助审查.-> ACCEPT
  TOOL -.doctor / 资产字节检查.-> BIND[TeamAI Binding 状态]
  BIND -.不是同一不可变链证明.-> HW
```

TeamAI rules/agents/skills 不直接修改治理事实，也不授予权限。Human 声明资产绑定；Haowork 校验后追加验证事实。AgentTeams RuntimeBinding 的 revision 与 TeamAI 声明的 team_revision 是不同合同，不应因为字段都叫 revision 就当成同一证据。

Mission 的 `teamai_source` 只能由服务端根据当前项目的 `Ready` binding ID 派生；客户端不提交仓库、revision 或 digest。Assignment 注册时比较完整快照，Receipt Store 和 Trace 在写入前再次比较；任一不一致均失败关闭。旧 Mission/Assignment/Receipt/Trace 没有该字段时保持 `nil`，不被强行升级。

```mermaid
flowchart TD
  START[读取固定资产与项目声明] --> REV{声明与资产匹配?}
  REV -- 否 --> BA[BLOCKED_ASSET：保留旧事实]
  REV -- 是 --> RUN[受限 Mission / 执行]
  RUN --> HAS{有结构化回执?}
  HAS -- 否 --> BR[缺回执：不产生候选]
  HAS -- 是 --> ID{principal / revision / Lease 合法?}
  ID -- 否 --> BI[BLOCKED_IDENTITY：拒绝候选]
  ID -- 是 --> DIGEST{制品摘要与环境匹配?}
  DIGEST -- 否 --> BD[BLOCKED_DIGEST：不升级证据]
  DIGEST -- 是 --> CAND[accepted_as_candidate]
  CAND --> EVID[独立 Verify 与 Evidence]
  EVID --> H{Human 验收通过?}
  PR[PR 已合并] -.不替代验收.-> H
  H -- 否 --> BH[BLOCKED_HUMAN_ACCEPTANCE：Task 未完成]
  H -- 是 --> END[记录完成]
```

缺回执、错误 principal、错误运行绑定 revision 和摘要错配的本地合同可复现；演示中的 BLOCKED 标签描述阻断层，并非承诺所有 CLI 都输出相同字符串。已落盘候选后发生 Evidence 摘要错配时，保留候选及失败事实，不能抹掉历史；接收阶段校验失败则不产生候选。PR merged 观察不生成 Evidence，也不能旁路 Human 门禁。

正常路径与失败演练见 [演示脚本](teamai-demo-script.md)；产品安装、双仓规则和验证边界见 [集成说明](teamai-integration.md)。
