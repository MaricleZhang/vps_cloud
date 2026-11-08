# 本地实现接口约定

本文件描述本项目接口，不是参考站的内部 API。前端经 Vite 代理访问 Go，生产由 Go 同源提供前端构建产物。

- 基础路径 `/api`，JSON 响应 `{ "data": ... }`；失败 `{ "error": "可读错误" }`，正确 HTTP 状态。
- Cookie 会话，前端 `credentials: 'include'`；所有管理路由要求管理员。
- 所有金额字段使用整数分。所有日期为 RFC3339 字符串；流量使用整数 bytes 计量，兼容 GB 展示字段按 `1 GB = 1024³ bytes`。倍率用 `multiplier_ppm`，余数结转。节点接口与浏览器 Cookie 鉴权相互隔离。
- `GET /api/health` -> `{status, demo}`。
- `GET /api/auth/me` -> `User`；`POST /api/auth/login` `{email,password}` -> User；`POST /api/auth/register` `{email,password,name,invite_code?}` -> User；`POST /api/auth/logout`；`POST /api/auth/password` `{current_password,new_password}`。
- `GET /api/dashboard` -> `{user, subscription: Subscription|null, announcements: Article[], traffic: TrafficRecord[], subscription_url:string}`。
- `GET /api/plans` -> Plan[]；`GET /api/nodes` -> Node[]；`GET /api/articles` -> Article[]。
- `GET /api/orders` -> Order[]；`POST /api/orders` `{plan_id,period}` -> Order；`POST /api/orders/{id}/pay` 使用余额结算 -> Order；`POST /api/orders/{id}/cancel` -> Order。
- `GET /api/subscription` -> `{subscription:Subscription|null,url:string}`；`POST /api/subscription/reset` -> `{url:string}`。
- `GET /api/traffic` -> TrafficRecord[]。
- `GET /api/invites` -> `{code:string,invite_url:string,registered:number,balance:number,pending:number,rate:number,records:Commission[],withdrawals:Withdrawal[]}`；`POST /api/invites/code` 生成或取现有码；`POST /api/withdrawals` `{amount,account}` -> Withdrawal。
- `GET /api/tickets` -> Ticket[]；`POST /api/tickets` `{subject,priority,message}` -> Ticket；`GET /api/tickets/{id}` -> Ticket；`POST /api/tickets/{id}/messages` `{message}` -> Ticket；`POST /api/tickets/{id}/close` -> Ticket。
- `GET /api/notices` -> Article[]；`GET /api/settings/public` -> `{site_name,site_description,contact_email,demo}`。
- 管理：`GET /api/admin/stats` -> `{users,orders,revenue,active_subscriptions,nodes,open_tickets,pending_withdrawals,traffic_gb}`。
- 管理：`GET /api/admin/users` -> User[]；`PATCH /api/admin/users/{id}` `{name?,status?,balance_delta?}` -> User。
- 管理：`GET/POST /api/admin/plans`；`PATCH /api/admin/plans/{id}`，使用 Plan 字段。
- 管理：`GET/POST /api/admin/nodes`；`PATCH /api/admin/nodes/{id}`，使用 Node 字段。节点秘钥不放到用户接口中。
- 管理：`GET /api/admin/orders` -> Order[]；`GET /api/admin/subscriptions` -> Subscription[]；`PATCH /api/admin/subscriptions/{id}` `{expires_at?,traffic_limit?,traffic_used?,status?}`。
- 管理：`GET /api/admin/tickets` -> Ticket[]；回复/关闭沿用受权限校验的工单接口。
- 管理：`GET/POST /api/admin/articles`；`PATCH /api/admin/articles/{id}`，使用 Article 字段。
- 管理：`GET /api/admin/withdrawals` -> Withdrawal[]；`PATCH /api/admin/withdrawals/{id}` `{status:approved|rejected}`。
- 管理：`GET /api/admin/commissions` -> Commission[]；`GET /api/admin/traffic` -> TrafficRecord[]；`GET /api/admin/audit` -> Audit[]；`GET /api/admin/ledger` -> Ledger[]。
- 管理：`GET/PATCH /api/admin/settings` -> `Record<string,string>`；敏感渠道参数不经公共接口返回。
- 管理：`POST /api/admin/nodes/{id}/agent-token` `{engine:"xray"|"sing-box",port_start,port_end}` -> `{token,node_id}`；token 仅本次返回，重签撤销旧 token。
- 管理：`GET /api/admin/traffic/enforcement` -> 节点/用户的期望状态、当前策略版本、ACK、最近联络与执行错误；未确认或过期 ACK 的 `applied_enabled` 为 null。
- 管理：`POST /api/admin/users/{id}/traffic/reset` -> 新周期订阅；保留暂停、封禁与到期状态并写审计。
- 节点：`GET /api/node/policy`、`POST /api/node/traffic`、`POST /api/node/ack` 均使用独立 `Authorization: Bearer <node-token>`；完整 DTO、累计计数及租约约定见 [TRAFFIC_PROTOCOL.md](TRAFFIC_PROTOCOL.md)。

## 数据类型

```ts
type User = {id:number,email:string,name:string,role:'admin'|'user',status:string,balance:number,created_at:string};
type Plan = {id:number,name:string,description:string,traffic_gb:number,device_limit:number,speed_mbps:number,monthly_price:number,quarterly_price:number,yearly_price:number,onetime_price:number,kind:'recurring'|'onetime',recommended:boolean,active:boolean,sort_order:number};
type Node = {id:number,name:string,region:string,flag:string,protocol:string,status:string,multiplier:number,tags:string,host?:string,port?:number,tls?:boolean,server_name?:string,active:boolean,sort_order:number,updated_at:string};
type Subscription = {id:number,user_id:number,user_email?:string,plan_id:number,plan_name:string,traffic_limit:number,traffic_used:number,device_limit:number,speed_mbps:number,status:string,expires_at:string,reset_at:string};
type Order = {id:number,order_no:string,user_id:number,user_email?:string,plan_id:number,plan_name:string,period:string,amount:number,status:string,created_at:string,paid_at?:string};
type Article = {id:number,title:string,category:string,kind:'doc'|'announcement'|'notice',body:string,published:boolean,updated_at:string};
type TrafficRecord = {id:number,user_id:number,user_email?:string,date:string,upload_gb:number,download_gb:number,multiplier:number,total_gb:number,node_name?:string};
type TicketMessage = {id:number,role:string,message:string,created_at:string};
type Ticket = {id:number,user_id:number,user_email?:string,subject:string,priority:string,status:string,created_at:string,updated_at:string,messages?:TicketMessage[]};
type Commission = {id:number,user_id:number,user_email?:string,order_no:string,amount:number,status:string,created_at:string};
type Withdrawal = {id:number,user_id:number,user_email?:string,amount:number,account:string,status:string,created_at:string};
type Audit = {id:number,actor:string,action:string,target:string,created_at:string};
type Ledger = {id:number,user_id:number,user_email?:string,amount:number,balance_after:number,kind:string,description:string,created_at:string};
```

Subscription 增加 `traffic_limit_bytes,traffic_used_bytes,upload_bytes,download_bytes,cycle_id,service_enabled,service_reason`；TrafficRecord 增加 `source:demo|metered,upload_bytes,download_bytes,billed_bytes,multiplier_ppm,cycle_id,session_id,current_cycle`；Node 增加 `managed,agent_engine,port_start,port_end,agent_last_seen`。兼容字段不表示实际设备/限速已执行。

未有真实支付商户与节点环境时，第三方支付、限速执行与生产部署必须标为未接入或未验收。托管节点同步和停服只在实际 Agent 当前 ACK 后确认，非托管节点无强制断开保证。余额支付可作为本地实际交易闭环。演示账户和模拟业务数据只在显式 demo 模式开启。
