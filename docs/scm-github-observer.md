# GitHub 远端 SCM 只读观察

## 1. 用途与边界

该模块将 GitHub 的受监控分支、Pull Request、Review、Check Run 和传统 Commit Status 读取为
Haowork 的追加式观察事实，并通过完整 Commit OID 与本地 CommitObservation、Governed Binding 对账。

它不会创建、修改或删除 GitHub 资源，也不会执行本地 Git 写操作。Pull Request 已合并或 Check 显示
`success` 只说明托管平台的观察结果，不能自动成为 Evidence，不能自动批准绑定，也不能自动完成 Task。

轮询只比较两次成功同步时的引用快照，不能证明期间发生过的每一次 Push，也不能把引用变化自动解释为
force-push 或历史重写。历史可达性仍需通过本地 `haowork scm verify-history` 和人工审查判断。

当前只支持 `github.com` 的 GitHub REST API。GitLab、GitHub Enterprise Server、Webhook、GitHub App、
自动 commit、自动 Pull Request、自动 Review、自动 merge 都不在本阶段范围内。

## 2. 凭据与最小权限

公开仓库可不设置令牌读取，额度较低。私有仓库建议使用 fine-grained personal access token，并只授予目标
仓库以下 read 权限：

| 权限 | 用途 |
| --- | --- |
| Metadata | 验证 GitHub repository ID 与默认分支 |
| Contents | 读取受监控 Git ref |
| Pull requests | 读取 PR、提交列表和 Review 状态 |
| Checks | 读取 Check Run 状态 |
| Commit statuses | 读取传统 Commit Status |

令牌只从当前进程环境变量读取，不写入 `.haowork` 事件、Cursor、配置、CLI JSON、Workbench、日志或 Git。

```powershell
$env:HAOWORK_GITHUB_TOKEN = '<只读令牌>'
```

不要把令牌写入 `.env.example`、项目配置、Capsule、提交信息或任何公开仓库文件。`gh auth login` 适合维护
仓库和人工诊断，但不是 Haowork 运行时的强制依赖。

## 3. 连接与同步

先启动项目的 Local Core，并注册本地 Git 仓库。连接不会接受手工输入的 owner、repo、URL 或 token；它只读取
已注册仓库的唯一 `origin`，仅允许以下 `github.com` 地址形式：

```text
https://github.com/OWNER/REPOSITORY.git
ssh://git@github.com/OWNER/REPOSITORY.git
git@github.com:OWNER/REPOSITORY.git
```

```powershell
haowork serve .example-project

haowork scm register --project .example-project --actor USR-OWNER --role owner --json
haowork scm github connect --project .example-project --actor USR-OWNER --role owner --json
haowork scm github sync --project .example-project --actor USR-OWNER --role owner --json
haowork scm github status --project .example-project --json
```

`connect` 只允许 Human Owner。`sync` 只允许 Human Owner、Lead 或 Reviewer；Agent 不能在没有专门授权
模型的情况下自动同步仓库级远端事实。Workbench 的 **GitHub 远端观察** 区块使用相同 Local API；只读模式不显示
连接或同步控件。

## 4. 读取范围与可靠性

请求显式使用 GitHub REST API 版本 `2026-03-10`，并只发送 `GET` 请求：

| 观察事实 | GitHub REST endpoint |
| --- | --- |
| 仓库身份 | `GET /repos/{owner}/{repo}` |
| 分支快照 | `GET /repos/{owner}/{repo}/git/ref/{ref}` |
| Pull Request | `GET /repos/{owner}/{repo}/pulls` 与 `pulls/{number}` |
| PR Commit / Review | `GET /repos/{owner}/{repo}/pulls/{number}/commits`、`reviews` |
| Check Run | `GET /repos/{owner}/{repo}/commits/{ref}/check-runs` |
| Commit Status | `GET /repos/{owner}/{repo}/commits/{ref}/status` |

同步使用 ETag、`If-None-Match`、固定每页 100 条、GitHub `Link` 分页和同源 URL 校验。请求串行执行，响应上限
为 8 MiB；遇到 `Retry-After`、rate-limit reset、401、403/429、410、分页异常或单页失败时，操作失败关闭：

- 不追加部分远端事件；
- 不推进成功 Cursor；
- 不覆盖上次成功的远端事实；
- 下次可重试，重复事实不会重复写入。

运行状态位于已忽略的 `.haowork/runtime/scm/`。删除 Cursor 后可重新同步；治理事件仍是长期事实来源。

## 5. 隐私与安全

持久化投影只包含必要的标识摘要、OID、状态和时间。它不会保存 PR/Review 正文、评论、diff、Check 日志、
annotation、完整 GitHub 用户名、完整 fork 仓库名、远端 URL 或令牌。PR 标题、用户与 fork 身份仅以 SHA-256
摘要出现；Workbench 只展示 `PR #<number>`、短 OID、状态和与本地治理链的确定性命中数。

## 6. 验证含义

`haowork scm github status` 可以回答某个 PR 是否包含本地已观察 Commit、是否存在已确认 Binding、其 Review
状态和 Check 状态。它不能回答“任务已经完成”或“代码必然满足需求”。这些结论仍由 Requirement、Mission、
Evidence、Approval 和 Task completion gate 共同决定。
