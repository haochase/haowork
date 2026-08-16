# CLI 使用说明

Haowork CLI 是 Project Capsule 的本地治理入口。它记录目标、需求、任务、执行、证据、
审批和完成事件，并通过重放追加式历史恢复状态。

## 构建

```powershell
npm ci --prefix web
npm run build --prefix web
go test ./... -count=1
go build -trimpath -o bin/haowork.exe ./cmd/haowork
.\bin\haowork.exe --help
```

## 基本流程

```powershell
.\bin\haowork.exe init `
  --project .\example-project `
  --name example `
  --actor USR-OWNER `
  --goal "交付可审计的软件变更" `
  --done-when "验证通过并完成审批" `
  --json

.\bin\haowork.exe plan create `
  --project .\example-project `
  --title "实现示例功能" `
  --task "完成代码与测试" `
  --acceptance "所有门禁通过" `
  --actor USR-LEAD `
  --role lead `
  --json

.\bin\haowork.exe status --project .\example-project --json
.\bin\haowork.exe history --project .\example-project --json
```

运行 `haowork <command> --help` 查看完整参数。JSON 模式用于脚本集成：成功和失败均输出
一个 JSON 文档，凭据和内部运行时秘密不会进入输出。

## 退出码

| 代码 | 含义 |
| --- | --- |
| `0` | 成功 |
| `1` | 运行错误或历史损坏 |
| `2` | 命令、参数或用法错误 |
| `3` | 状态、版本或冲突错误 |
| `4` | 证据门禁失败 |
| `5` | 缺少审批或权限不足 |
| `6` | 依赖或团队服务不可用 |

## Capsule 数据

`.haowork/events.jsonl` 是追加式治理历史；`.haowork/runtime/`、
`.haowork/cache/` 和 `.haowork/index/` 属于机器本地状态，不应提交到 Git。
