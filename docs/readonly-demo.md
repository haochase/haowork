# Haowork 只读 Demo

`haowork-demo` 是用于公开展示产品设计的独立进程，不是 Local Core，也不连接真实 AgentTeams、模型
服务、项目目录或团队 Token。它只返回仓库内编译的预置公开快照。

## 安全边界

- 仅允许 `GET` 与 `HEAD`：`/`、`/api/demo/snapshot`、`/healthz`。
- `POST`、`PUT`、`PATCH`、`DELETE` 一律返回 `405 Method Not Allowed`。
- 不读取 `.env.local`、Capsule、Git 工作区、Kubeconfig、模型 Key 或反向代理凭据。
- 默认只监听 `127.0.0.1:4175`；如需对外发布，由部署者在仓库之外配置受信任的反向代理。
- 预置编号、摘要、时间和证据仅用于演示，不能当作真实双区 E2E 的通过证据。

## 运行与发布边界

构建并在本机启动只读 Demo：

```bash
go build -o haowork-demo ./cmd/haowork-demo
HAOWORK_DEMO_ADDR=127.0.0.1:4175 ./haowork-demo
curl --fail http://127.0.0.1:4175/healthz
```

域名、TLS、反向代理、进程守护和服务器目录均属于部署者的运维边界，不在本仓库提供具体配置。
不要把代理凭据、证书、API Key 或 `.env.local` 上传到仓库。发布后应验证首页、快照接口以及写请求
返回 `405`：

```bash
curl --fail https://<demo-host>/healthz
curl --fail https://<demo-host>/api/demo/snapshot
curl -i -X POST https://<demo-host>/api/demo/snapshot
```
