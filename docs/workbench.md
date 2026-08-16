# Workbench 使用说明

Workbench 是项目级 Local Core 的浏览器界面。浏览器、CLI 和 API 使用同一个领域服务与
治理策略，Workbench 不直接修改事件历史或索引。

## 启动与停止

在已初始化的 Project Capsule 中运行：

```powershell
haowork serve .
haowork open .
```

`serve` 持有项目锁并在 loopback 地址监听。`open` 复用健康的 Local Core，或按需启动
新实例，然后使用一次性 bootstrap token 建立浏览器会话。

结束本地会话：

```powershell
haowork stop .
```

## 界面范围

Workbench 提供项目概览、需求与任务树、Mission、Agent 拓扑、审批、Context、Run
Timeline、Evidence、历史、租约、同步队列、冲突处置和迁移入口。所有写操作都必须经过
服务端授权和状态门禁；前端角色判断只用于界面呈现。

## 安全边界

- Local Core 默认只绑定 `127.0.0.1`。
- bootstrap token 只通过 URL fragment 传递，换取 HttpOnly 会话后应立即清除。
- 浏览器不保存 Team Token、模型 API Key、签名私钥或 Kubernetes 凭据。
- `.haowork/runtime/` 与 `.haowork/index/` 是可重建状态；事件历史和 Capsule 才是长期事实。
- 真实 AgentTeams、跨环境迁移和外部模型调用需要独立配置，界面不能以模拟状态冒充成功。
