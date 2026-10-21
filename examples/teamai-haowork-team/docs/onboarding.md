# 复制与接入

本目录是独立 TeamAI 团队资产仓库模板，不是 Haowork 的生产配置。复制本目录内容到新的团队资产仓库后，替换 team、description 和 repo。`https://example.invalid/replace-owner/replace-team-assets.git` 是不可用的占位式完整 HTTPS URL，不代表已发布仓库。本目录 `teamai.yaml` 与 Haowork 项目 `.haowork/teamai.yaml` 是不同合同，不能互换。

TeamAI 0.27.0 团队配置使用 team、description、repo、provider、sharing；Agent 使用 name、description、instructions。本模板的 `.yaml` 文件采用 JSON-compatible YAML（YAML 1.2 的 JSON 子集）。验证器刻意只接受此子集，避免引入 YAML 解析依赖；修改后仍须保持该格式。Skill frontmatter 使用 name 和 description 两个单行字段。

本地校验需要 Node.js 18+，PowerShell 或 Bash。运行 `pwsh -NoProfile -File ./validate-assets.ps1` 或 `bash ./validate-assets.sh`；成功仅输出 `TEAMAI_HAOWORK_ASSETS_OK`。回归测试运行 `node ./test-validators.mjs`；设置 BASH_EXE 可同时测试指定的 Bash，设置 PWSH 可指定 PowerShell。测试在模板同级临时目录执行并清理，校验器本身只读，不执行 TeamAI init/pull/push、不提交 Git、不调用服务。

未附带 Hook、MCP 配置、凭据或服务地址。配置明确关闭自动 Hook/MCP 应用、shell profile 注入、recall、贡献提醒、usageReport 和 webhook。分发前由 Human 检查目标工具的安装/拉取行为与权限；校验通过仅证明模板检查通过，不表示运行时权限、生产接入或验收通过。

repo 可保留完整占位 URL，或替换为带非空仓库路径、不含凭据、query 或 fragment 的完整 HTTPS Git URL。验证器只检查结构与资产边界，不验证远端可达性、Git HEAD 或访问权限。

实际接入须先按项目文档定位 Haowork 只读状态入口和 MCP schema，验证 Mission、Scope、Context、完成条件及服务端认证。模板不创建 Mission，不授予 Delivery Leader 身份，不替 Human 批准。按 workflow 执行并保留证据边界。

本模板本身不附带 `haowork.submit_execution_report` MCP 服务端，也不保证目标 Haowork 服务端已部署该工具。接入时必须从目标服务端发现并验证真实 schema 与经认证的 Delivery Leader 身份；缺失任一项即 BLOCKED，不提交回执，不能退回用聊天代替回执。模板校验通过不表示真实 E2E 已完成。
