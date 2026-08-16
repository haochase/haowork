# Haowork 只读 Demo

`haowork-demo` 是用于公开展示产品设计的独立进程，不是 Local Core，也不连接真实 AgentTeams、模型
服务、项目目录或团队 Token。它只返回仓库内编译的预置公开快照。

## 安全边界

- 仅允许 `GET` 与 `HEAD`：`/`、`/api/demo/snapshot`、`/healthz`。
- `POST`、`PUT`、`PATCH`、`DELETE` 一律返回 `405 Method Not Allowed`。
- 不读取 `.env.local`、Capsule、Git 工作区、Kubeconfig、模型 Key 或 Cloudflare Token。
- 默认只监听 `127.0.0.1:4175`；公网访问必须由 Cloudflare Tunnel 转发。
- 预置编号、摘要、时间和证据仅用于演示，不能当作真实双区 E2E 的通过证据。

## Ubuntu 部署

在发布机器构建 Linux 二进制后，将 `haowork-demo` 和
[`deploy/demo/haowork-demo.service`](../deploy/demo/haowork-demo.service) 上传到
`/home/codechase/haowork-demo/` 与 `~/.config/systemd/user/`：

```bash
mkdir -p ~/.config/systemd/user ~/haowork-demo
install -m 0755 ./haowork-demo ~/haowork-demo/haowork-demo
install -m 0644 ./haowork-demo.service ~/.config/systemd/user/haowork-demo.service
systemctl --user daemon-reload
systemctl --user enable --now haowork-demo
curl --fail http://127.0.0.1:4175/healthz
```

公网域名可选两种安全方式，二选一即可：

1. **由系统管理员扩展既有 Tunnel**：在既有 ingress 中增加：

```yaml
- hostname: haowork.112318.xyz
  service: http://127.0.0.1:4175
```

   然后创建该 hostname 的 Tunnel DNS 路由并重启 `cloudflared`。

2. **使用独立的用户级 Tunnel（公开 Demo 推荐）**：创建专用 Tunnel，并将凭据限制在
   `~/.cloudflared/`。配置文件应只包含当前 Demo hostname 和 `127.0.0.1:4175`：

```yaml
tunnel: <新建 Tunnel 的 UUID>
credentials-file: /home/codechase/.cloudflared/haowork-demo.json
ingress:
  - hostname: haowork.112318.xyz
    service: http://127.0.0.1:4175
  - service: http_status:404
```

   使用 [`deploy/demo/cloudflared-haowork-demo.service`](../deploy/demo/cloudflared-haowork-demo.service)
   作为用户级 systemd 单元模板。独立 Tunnel 不应复用到其他站点的 ingress 配置，否则不同连接器的
   路由规则可能不一致。

不要把 Tunnel 凭据、证书、API Key 或 `.env.local` 上传到本仓库。外网验证应同时检查首页、快照接口、
以及写请求返回 `405`：

```bash
curl --fail https://haowork.112318.xyz/healthz
curl --fail https://haowork.112318.xyz/api/demo/snapshot
curl -i -X POST https://haowork.112318.xyz/api/demo/snapshot
```
