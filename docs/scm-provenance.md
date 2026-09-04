# Git / SCM 原生追溯

## 1. 目标

该能力把一个已经存在的本地 Git Commit 关联到 Haowork 已确认的目标版本、任务、Mission 和 Evidence。
它解决的是“这次不可变代码变化对应哪条治理链”，不是替代 Git 执行提交、推送或代码评审。

## 2. 操作流程

```powershell
# 所有命令只连接正在运行的 Local Core，不直接写事件文件。
haowork scm register --actor USR-OWNER --role owner
haowork scm scan --repository SCM-001 --commit <完整OID> --actor AGT-BUILD --kind agent --role agent
haowork scm propose --repository SCM-001 --commit <完整OID> --tasks TSK-001 `
  --mission MSN-001 --evidence EVD-001 --trace TRC-001 --actor AGT-BUILD --kind agent --role agent
haowork scm confirm --binding SCB-001 --actor USR-REVIEWER --role reviewer
haowork scm verify-history --repository SCM-001 --refs refs/heads/main --actor USR-REVIEWER --role reviewer
```

也可以在 Workbench 的 **Git / SCM** 面板查看仓库、Commit 时间线、绑定和失效状态。Workbench 的写入
动作经过同一 Local API 和 Service 策略；只读模式不渲染注册、确认、拒绝或历史验证控件。

## 3. 事实模型

| 事件 | 含义 |
| --- | --- |
| `scm.repository.registered` | 注册项目本地 Git 身份与对象格式 |
| `scm.commit.observed` | 记录显式选择的完整 Commit OID 及只读对象事实 |
| `scm.binding.proposed` | 提议 Commit 与 Goal/Task/Mission/Evidence 的关系 |
| `scm.binding.confirmed` | 具备权限的 Human 确认该关系 |
| `scm.binding.rejected` | Human 拒绝候选关系并保留历史 |
| `scm.commit.superseded` | Commit 不再由指定可信引用可达 |
| `scm.binding.invalidated` | 已确认关系因历史可达性变化而失效 |

重复读取或重启 Core 会从追加式事件确定性恢复相同投影。精确重复的仓库和 Commit 事实幂等；同一标识
对应不同载荷时失败关闭。

## 4. 策略边界

- 仓库注册只允许 Human Owner。
- Commit OID 必须是仓库对象格式对应的完整小写十六进制值，并且对象类型必须是 `commit`。
- Commit 的全部当前路径和 rename/copy 前路径必须落在 Mission 允许范围内。
- 每个绑定至少引用一个现有 Evidence；Trace 是补充引用，不能替代 Evidence。
- L0/L1 由 Human Owner 确认。
- L2 由 Human Lead 或 Reviewer 确认，且必须存在 subject、risk 与规范化 binding SHA-256 完全匹配的审批。
- L3 由 Human Owner 确认，并要求同样的 hash-bound 审批。
- 确认绑定不改变 Task 完成状态，也不绕过独立验证。
- 历史验证只接受 `refs/heads/`、`refs/remotes/` 或 `refs/tags/` 下的显式可信引用。

## 5. 隐私与安全

Git 子进程采用固定命令 allowlist、固定超时和 4 MiB 输出上限，并清除可影响对象解析、SSH、凭据、
askpass、外部 diff、pager、replace object 和全局配置的环境变量。当前实现不访问网络。

持久化和 API 投影包含：

- repository ID、对象格式和可选 remote SHA-256 指纹；
- Commit/tree/parent/blob OID；
- 作者与提交者显示名、邮箱 SHA-256；
- Commit message、时间、变化状态和相对路径。

明确不包含：

- 原始邮箱和 remote URL；
- 私有仓库绝对路径；
- 源码、patch 或 diff 内容；
- Git 凭据、token、SSH 配置或客户端环境。

## 6. 当前不包含

- 自动 `git commit` 或 `git push`；
- Git hooks；
- GitLab API、GitHub Webhook、GitHub App、GitHub 写操作或自动 Pull Request / Review / merge；
- 自动猜测历史 Commit 与需求的关系；
- 以 Commit 绑定替代 Evidence、审批或 Task 完成门禁。

## 7. GitHub 远端观察

GitHub `github.com` 的受监控 ref、Pull Request、Review、Check Run 和 Commit Status 可以作为只读观察事实
进入同一 SCM 投影，并通过完整 Commit OID 与本地 Commit / Binding 对账。观察器不读取 PR/Review 正文、评论、
diff、Check 日志或完整用户身份，也不执行任何 GitHub 写请求。

PR 已合并或 Check `success` 不会自动确认绑定、创建 Evidence 或完成 Task。轮询只能比较同步快照，不能证明完整
Push 历史。具体配置、最小权限和命令见 [GitHub 远端 SCM 只读观察](scm-github-observer.md)。
