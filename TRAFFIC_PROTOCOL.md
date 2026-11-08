# 流量计量与节点停服协议（实现约定）

本协议是本项目控制面与 node-agent 的接口，不冒充 XrayR 原生面板协议。

## 安全与计量

- 节点使用管理员签发的高熵独立 Bearer token，数据库仅保存摘要；节点身份由 token 确定，不能从请求体冒用 node_id。
- 原始数据用整数 bytes，`1 GB` 展示按 `1 GiB = 1073741824 bytes`。节点倍率按百万分固定精度 `multiplier_ppm`，使用整数运算和余数结转。
- 节点读取内核累计计数（禁止 reset=true），每个用户代理实例生成独立 session_id。上报持久化至 outbox 后发送；重试不能重复计量。
- 一个 sample 为该 session、该周期内从零开始的累计上传和下载。周期切换：先将旧周期已捕获数据持久化，再为新周期建立新 session 与计数基线。
- 服务端按节点/用户/session/cycle 保存最大已计入累计值；更早样本不重复计量；负数、小数、溢出、非法周期和未知用户必须拒绝。过期周期的有效延迟数据仅入历史，不扣新周期额度。
- 正常周期套餐在周期切换恢复额度；人工暂停/账户封禁/到期不因流量重置而恢复。一次性套餐不自动重置。

## 管理端

- `POST /api/admin/nodes/{id}/agent-token` `{engine:"xray"|"sing-box",port_start:20000,port_end:20999}` -> `{token:string,node_id:number}`，仅此次响应返回明文 token。重发使旧 token 失效。
- Node 新增可选 `managed:boolean,agent_engine:string,port_start:number,port_end:number,agent_last_seen:string`；普通节点列表绝不返回 token 或凭据。
- `GET /api/admin/traffic/enforcement` -> 执行状态数组，每项至少 `{node_id,node_name,user_id,user_email,desired_enabled,applied_enabled:boolean|null,reason,policy_version,ack_at,last_seen}`。
- `POST /api/admin/users/{id}/traffic/reset` -> 新周期与当前订阅；写审计，不改变人工暂停和账户封禁。

## 节点接口

全部使用 `/api/node/`，不使用浏览器 Cookie，`Authorization: Bearer <token>`。

`GET /api/node/policy` 返回：

```json
{"data":{
  "node":{"id":1,"name":"节点","protocol":"vless","tls":true,"server_name":"example.com","port_start":20000,"port_end":20999,"multiplier_ppm":1000000},
  "lease_seconds":15,
  "users":[{"user_id":2,"port":20000,"uuid":"UUID","password":"random","stats_name":"user-2","cycle_id":"random","policy_version":"hash","enabled":true,"reason":"active","traffic_limit_bytes":96636764160,"traffic_used_bytes":0,"remaining_bytes":96636764160}]
}}
```

策略包含受该节点托管的用户，包括已停用用户，以便停止残留实例。用户名使用内部 stats_name，不把真实用户邮箱作为节点识别名。有效策略租约到期、持续无法安全采集统计，或计量报告持续无法送达超过执行租约时，agent 必须停止本地实例（fail closed）。策略 GET 成功不能替代流量 POST 成功。策略失联和执行未确认在后台明确显示，不能声称已经停服。

`POST /api/node/traffic`：

```json
{"samples":[{"user_id":2,"cycle_id":"random","session_id":"worker-instance-cycle-epoch","upload_bytes":1024,"download_bytes":2048}]}
```

返回 `{data:{accepted:number,policy:<与 GET policy 相同结构>}}`。倍率与用户额度只由服务端决定。达到或超过额度时当前周期 `enabled=false,reason=quota_exceeded`，所有节点的后续策略都必须停用该用户。所有样本在事务中校验/计入；重复与乱序累计值不得额外收费。

`POST /api/node/ack`：

```json
{"users":[{"user_id":2,"policy_version":"hash","enabled":false,"error":""}]}
```

只有当前策略版本一致、实际停止实例成功的 ACK 才能记为停服已执行。过期 ACK 不得覆盖新策略。节点启用 ACK 也需要匹配当前可用策略。

## node-agent 托管运行方式

为可靠断开现有连接，不使用“只删除 Xray 用户”的方式。每位用户拥有独立内核进程、端口与认证凭据，Xray/sing-box 仅是该用户实例。超额或暂停时终止该用户进程，进程退出会关闭该用户所有连接；不会为停用单个用户而重启整个共享节点。

- 管理员部署 agent 时明确提供内核路径、节点 token、公开绑定地址、TLS 证书路径（如使用 TLS）。
- Xray 与 sing-box 通过本机 gRPC StatsService 查询 `user>>>stats_name>>>traffic>>>uplink/downlink` 累计值。sing-box 的单用户 Shadowsocks 入站没有可配置用户名，使用该用户独占入站 `inbound>>>stats_name>>>traffic>>>uplink/downlink`，与用户一一对应；不会混入其他用户。统计端口只监听 loopback。sing-box 必须包含 `with_v2ray_api` 构建标签。
- agent 持久化出站报告、累计基线与实例周期；API 不可用时保留待发数据。
- Linux 子进程必须设置 parent-death signal；agent 非正常退出也不允许代理子进程无限运行。
- 轮询计量有检测延迟，默认每秒一次；检测窗口内可能超用，超用量取决于连接速率和并发节点数。本实现不是严格零字节透支的全局配额租约系统。
- 既有共享进程的 XrayR/其他面板节点不能仅配置 URL 就自动获得本项目的强制断开能力，必须迁移为托管模式，或者另写支持真实踢连接的适配器。

## UI 兼容字段

Subscription 增加 `traffic_limit_bytes,traffic_used_bytes,upload_bytes,download_bytes,cycle_id,service_enabled,service_reason`，原 GB 字段保留用于已有页面。

TrafficRecord 保留 `upload_gb,download_gb,total_gb,multiplier` 并可带原始 bytes。GET 用户与管理员流量接口返回真实计量记录；历史演示记录要可区分 `source:demo|metered`。

托管节点的订阅输出必须使用该用户专属 UUID/密码与分配端口；不能继续为所有用户输出共享节点密码。尚未由 agent 启动成功并确认的实例不得显示为“停用已执行”或“在线可用”。
