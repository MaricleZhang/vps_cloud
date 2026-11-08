# NiceCloud Go 后端

Go 1.25 + SQLite 本地开发版本。HTTP API 与 `../API_CONTRACT.md` 对齐，统一返回 `{ "data": ... }` / `{ "error": "..." }`。金额为整数分，日期为 RFC3339。业务核心可持久化运行，支持本项目 node-agent 的真实流量计量与超额停服；第三方支付尚未接入。

## 运行

需要 Go 1.25、C 编译器（`go-sqlite3` 使用 CGO）。在本目录运行：

```sh
go mod download
go run . -demo -db ../.local/demo.db -listen 127.0.0.1:8080
```

显式 `-demo` 或 `DEMO_MODE=1` 才会初始化虚构示例。演示登录：

| 身份 | 邮箱 | 密码 |
|---|---|---|
| 管理员 | admin@example.test | DemoCloud2026! |
| 用户 | demo@example.test | DemoCloud2026! |

演示用户初始余额 20000 分，90GB 套餐；流量图、节点、文章和邀请佣金均为虚构数据。八个套餐的公开月价/一次性价、流量与限制按已观察参考页面配置；季度、年度价格当前按月价 ×3/×12 计算，是本地规则，未声称复刻原站长期周期定价。演示节点状态为 `demo` 且不会进入可连接的订阅配置。初始化通过 SQLite 事务一次写入并记录 `demo_seeded`；重启不覆盖已编辑记录。请为演示与正式数据使用不同数据库。

不带 `-demo` 时不创建默认账户或密码：

```sh
BOOTSTRAP_ADMIN_EMAIL=admin@your-domain.example \
BOOTSTRAP_ADMIN_PASSWORD='replace-with-a-unique-long-password' \
go run . -db ../.local/production.db
```

仅在数据库没有管理员时使用这两个变量创建首个管理员，密码至少 12 位；已有管理员不会被环境变量覆盖。公开注册默认开启，可在后台设置 `registration_enabled=false` 关闭。

前端开发在 `../web` 运行 Vite。生产先构建前端，Go 默认同源托管 `../web/dist`，支持 BrowserRouter 路由回退到 `index.html`。

| 配置 | 作用 |
|---|---|
| `WEB_DIST` | 前端构建目录，默认 `../web/dist`，相对于 Go 进程当前目录 |
| `PUBLIC_URL` | 对外完整根地址，用于订阅及邀请链接；正式环境配置 HTTPS 地址 |
| `COOKIE_SECURE=1` | 反向代理终止 TLS 时将会话 Cookie 标为 Secure |
| `ALLOWED_ORIGINS` | 额外允许的来源，逗号分隔；默认只接受同 Host 来源，demo 额外允许 localhost/127.0.0.1:5173 |
| `DEMO_MODE=1` | 与 `-demo` 相同，显式开启虚构演示数据 |
| `BOOTSTRAP_ADMIN_EMAIL` / `BOOTSTRAP_ADMIN_PASSWORD` | 创建首个管理员，不是长期运行依赖 |

## 本地业务规则

- 会话是随机高熵凭据，数据库仅存 SHA-256 摘要，Cookie 使用 HttpOnly、SameSite=Lax，七天过期；密码变更清除旧会话。新密码使用 PBKDF2-SHA256，600000 次迭代和独立随机盐。
- 所有管理员 API 校验角色；用户订单、工单、订阅等按所有者隔离。节点列表仅输出展示字段，UUID、密码和连接地址不经普通列表 API 返回。订阅输出使用独立 token，重置后旧 token 立即失效。
- 下单保存价格与套餐权益快照；余额支付在单个 SQLite 事务中完成扣款、账本、订单、订阅和邀请佣金；重复支付同一订单返回已付款结果，不再次扣款。
- 周期套餐同套餐续费顺延有效期、保留当前周期用量；换套餐立即覆盖权益与用量，不进行差价折抵。一次性包再次购买重新获得完整额度、不保留已耗用量。一次性包使用购买日起 100 年作为当前数据库中的长期占位到期日；这不是已核实的原站规则。
- 待支付订单 30 分钟后关闭。周期流量按订阅 `reset_at` 的月份推进并产生新 `cycle_id`；到期订阅变为 expired。生命周期处理在 API/订阅请求前以及后台每分钟执行，并影响下一次节点策略。人工暂停和账户封禁不会因流量重置而解除。
- 节点使用独立 Bearer token，数据库仅存摘要。累计计数按节点/用户/周期/session 去重，整数 bytes × multiplier_ppm 计费并结转余数，事务内入账。非法、负数、小数、溢出和未授权周期拒绝；旧周期延迟上报只记历史。
- 每位托管用户独立凭据、端口和内核进程；达到或超过额度即下发禁用策略。后台区分期望状态和当前版本 ACK 的执行状态，失联不标记执行成功。部署与强制断开由 [node-agent](../node-agent/README.md) 实施，接口见 [TRAFFIC_PROTOCOL.md](../TRAFFIC_PROTOCOL.md)。
- 订阅默认输出 Clash/Mihomo YAML，支持配置了必要凭据的 VLESS、AnyTLS、Shadowsocks、Trojan、VMess 节点；`?format=json` 可查看不含用户身份字段的配置。演示节点不会输出真实代理。不同客户端版本的真实连接尚未验证。
- `Subscription-Userinfo` 使用计费口径 `upload=0; download=traffic_used_bytes; total=traffic_limit_bytes`，避免高倍率节点令客户端误显示剩余额度；原始上传/下载 bytes 保留在业务 API 和流量明细中。
- 邀请佣金比例 `invite_rate` 为 0–1；本地余额订单支付成功后记为可用佣金。提现申请立即冻结额度，并发请求不能超额申请；批准/拒绝为人工审核记录，没有对外打款。`invites.pending` 表示处理中提现冻结金额。
- 工单关闭后不能继续回复。管理员回复后状态为 answered。

## 验证

```sh
go test ./...
go test -race ./...
```

集成测试使用 `httptest.ResponseRecorder`，不监听网络端口。覆盖认证及 Origin 检查、注册开关、NaN 设置拒绝、并发重复支付、权益快照、取消拒付、一次性包重新购买、订阅 token 重置、节点秘密隔离、工单和订单越权、并发提现、本地生命周期、无 demo 默认账户保护。

新增流量专项覆盖整数精度（超过 `2^53`）、非法批次原子回滚、累计乱序/重复、倍率余数、并发与节点身份隔离、跨周期历史、跨节点超额、暂停保留、旧 ACK 不复活、端口耗尽及计费订阅头。实际节点进程与连接测试位于 `../node-agent`；真实 Xray 联调使用 `../scripts/traffic-smoke.py`。

## 未完成的生产能力

- 第三方支付商户、支付回调、退款、自动充值和实际提现打款。
- 节点组权益隔离、设备在线采集与完整网络健康探测；Agent 最近联络不等于公网可达。
- 实际设备数/速度限制。非托管共享节点的既有连接无法由本项目停止，须先迁移。
- 客户端完整格式兼容与真实端到端连接验证；二维码和快捷导入由前端提供。
- 邮件验证、找回密码邮件、通知发送、验证码/多因素认证、管理员细分 RBAC。
- 优惠、周期流量重置细则、换购折抵、佣金结算和提现风控规则，均未声称与原站一致。
- 当前为单进程 SQLite MVP：业务实体以 JSON 存入统一表；计量累计、余数、周期及 ACK 使用独立 SQL 表保证整数精度与唯一性。尚无版本化数据库迁移、统一分页、大规模统计优化、自动备份或分布式任务锁。不要将此实现标为生产系统已完成。
