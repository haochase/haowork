# TeamAI 治理样例演示脚本

使用 [五端样例](../examples/teamai-haowork-demo/README.md) 的虚构支付 SDK 升级场景：Web、Windows、macOS、Android、iOS 的负责人和 Scope 分开，取消、超时、迟到/重复回调与恢复要求可追溯。它没有支付实现，没有客户数据，原生平台测试 `NOT_RUN`。

## 可在本地重现

从仓库根目录运行，无需安装 TeamAI、模型或集群：

```powershell
node examples/teamai-haowork-team/test-validators.mjs
node examples/teamai-haowork-team/validate-assets.mjs
node examples/teamai-haowork-demo/scripts/test-demo.mjs
node examples/teamai-haowork-demo/scripts/validate-demo.mjs
node --test scripts/check-teamai-public.test.mjs
```

说明静态成功标记只证明资产/场景结构和扫描规则。展示 scenario.json 的目标、根因、Task、Scope、责任人和历史决策；对照五端适配说明，解释为什么共同需求不意味着五端实现与验收相同。

Go 依赖准备完成且缓存位于非系统盘后，可重放公开合同：

```powershell
go test ./internal/teamai ./internal/model ./internal/app ./internal/cli -run 'TestPublicTeamAI|TestTeamAI' -count=1
```

这些公开测试使用受控 CLI Runner、临时项目和本地绑定账本，展示绑定、资产字节验证与重放。候选回执的完整跨模块合同保留在私有仓，不属于公开 workflow 或本命令；若使用私有环境演示该合同，还必须说明 assignment 注册不是实际 Matrix dispatch，candidate 不等于完成。同一 TeamAI Binding/Mission/Receipt 不可变链仍 `NOT_PROVEN`。

## 需要外部准备，当前 NOT_RUN

1. 人工创建自己的团队仓库，固定 `teamai-cli@0.27.0`，按 [样例安装步骤](../examples/teamai-haowork-demo/README.md) 初始化。模板 URL 只是占位；不把安装或 doctor 成功叫做分发验收。
2. 初始化独立 Haowork 项目，人工填写 `.haowork/teamai.yaml`，从实际资产取得 SHA-256。运行 `haowork teamai doctor --project <project> --asset-root <assets> --json`。
3. Human Owner/Lead 执行 `haowork teamai bind --project <project> --team-repo <repo> --revision <revision> --actor <human-id> --role owner --json`，然后执行 `haowork teamai verify --project <project> --asset-root <assets> --json`。revision 是声明，verify 不证明 Git HEAD。
4. 准备真实 Local Core、AgentTeams、模型、运行身份、Lease 和 Scope。根据 `haowork mission issue --help` 取得实际参数；使用样例的 Mission 命令，但所有 ID 必须来自当前环境。缺一项就停止，不伪造 Mission 或回执。
5. 认证 Delivery Leader 从服务端发现 `haowork.submit_execution_report` schema，再提交 assignment_id、status、summary_sha256、artifacts、checks。以实际 schema 为准。回执仅成为 candidate。
6. 独立 Verify 提交并验证 Evidence；GitHub 只读观察 Commit/PR/Check。Human 依据 `haowork complete --help` 及实际权限验收，未验收时任务不能完成。

## 失败演练的讲解顺序

| 输入 | 预期边界 | 当前证据 |
| --- | --- | --- |
| 只有聊天，没有回执 | 不产生 accepted candidate | 本地合同；真实演练 NOT_RUN |
| 资产字节或声明 revision 不符 | 保留原绑定，不追加成功验证 | 本地合同；真实演练 NOT_RUN |
| Leader principal 或运行 revision 错 | 服务端拒绝，不能用 Skill 自授权限 | 本地合同；真实演练 NOT_RUN |
| 制品摘要不符 | 拒绝接收或禁止 Evidence 升级 | 本地合同；真实演练 NOT_RUN |
| PR merged 但无人验收 | 只保留观察，不完成 Task | 本地只读合同；真实演练 NOT_RUN |

最后展示 [架构图](teamai-architecture.md) 和 [面试简报](../examples/teamai-haowork-demo/docs/interview-brief.md)。明确远端 GitHub Actions、真实分发、双命名空间联调仍 `NOT_RUN`。在线门户是独立部署的预置只读展示，不能作为这些步骤的验收证据。
