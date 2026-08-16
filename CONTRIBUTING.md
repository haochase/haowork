# 参与 Haowork 开发

感谢你参与 Haowork。提交变更前，请先确认改动属于产品源码、测试、构建配置、公开文档
或可复现的部署工具。

不要提交以下内容：

- API Key、Token、私钥、Cookie、Kubeconfig 或包含凭据的 URL；
- `.env.local`、运行时状态、缓存、日志、数据库和生成的证据文件；
- 原始对话、个人信息、内部计划、竞赛手册或第三方私有材料；
- 未经授权的二进制文件、数据集或外部项目副本。

## 分支与提交

从最新 `main` 创建短生命周期分支，使用 `feat/`、`fix/`、`docs/`、`test/` 或
`chore/` 前缀。不要直接向 `main` 推送，也不要使用 `codex/` 分支前缀。

提交信息遵循 Conventional Commits，可使用中文或英文。请在同一条提交信息中保持语言一致，
并使用简洁、明确的祈使语气描述变更：

```text
<type>(<scope>): <简洁的祈使句>
```

支持的类型为 `feat`、`fix`、`docs`、`style`、`refactor`、`perf`、`test`、
`build`、`chore`、`revert`、`merge` 和 `sync`。每行不超过 100 个字符。

示例：

```text
feat(sync): 增加离线队列恢复
fix(bridge): reject mismatched runtime identities
```

## 本地验证

```powershell
npm ci --prefix web
npm test --prefix web
npm run build --prefix web
gofmt -w cmd internal
git diff --check
go vet ./...
go test ./... -count=1
go build ./cmd/haowork
```

Pull Request 的标题和说明可以使用中文或英文。请说明变更目的、行为差异、测试证据和仍未
验证的外部环境边界；面向同一问题的讨论尽量保持语言一致，方便审阅者跟踪上下文。
