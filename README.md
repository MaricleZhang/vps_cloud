# Nice Cloud 订阅与节点服务平台

参考 https://nicecloud.cyou/ 的用户页面，使用 React + TypeScript 构建用户端与管理端，Go 提供同源 API 和静态页面，SQLite 持久化账户、套餐、订单、权益、内容、工单和账务。

完整需求见 [MODULES.md](MODULES.md)，当前40个模块的逐项完成范围、缺口和验证结果见 [PROGRESS.md](PROGRESS.md)，接口约定见 [API_CONTRACT.md](API_CONTRACT.md)。参考站管理后台不可见，管理端是与用户业务对应的配套实现。

## 本地启动

需要 Node.js 22.12+、npm、Go 1.25+，以及供 SQLite CGO 驱动使用的 C 编译器（macOS 可使用 Xcode Command Line Tools；Linux 可安装 gcc）。

```bash
make dev
```

打开 http://127.0.0.1:5173 。前台登录后访问 `/dashboard`，管理员登录后访问 `/admin/overview`。开发脚本启动两个仅绑定本机的服务；按 Ctrl+C 同时停止。

首次会安装前端依赖并编译 Go。演示数据位于 `.local/demo.db`，重启会保留修改，不会每次重置。

仅在显式 `-demo` 或 `DEMO_MODE=1` 模式下创建以下虚构账户：

| 用途 | 邮箱 | 密码 |
| --- | --- | --- |
| 用户端 | demo@example.test | DemoCloud2026! |
| 管理端 | admin@example.test | DemoCloud2026! |

演示用户有初始余额与订阅，节点和流量为演示数据；演示节点不能连接。请勿将演示数据库作为生产数据库使用。普通启动不会创建这些账户。

## 已实现的本地流程

- 用户注册、登录、退出、修改密码；管理员与用户访问隔离。
- 九个用户页面、十三个管理页面，桌面/移动布局和深浅主题。
- 套餐与周期选择 → 创建订单 → 余额结算 → 权益更新 → 订单与余额流水。
- 套餐、节点、文档、公告和站内通知的管理、发布或停用。
- 订阅链接、二维码与旧令牌撤销；配置输出的兼容范围见后端说明。
- 邀请关系与佣金、提现申请冻结、管理员批准或拒绝。批准仅记录审核结果，不自动打款。
- 用户工单、客服回复与关闭；操作审计和基础运营统计。

金额统一用整数分。订单保留价格与权益快照；余额结算、提现等关键变更放在数据库事务中。

同一周期套餐续费顺延有效期并保留当前已用量；换购其他套餐覆盖权益，不计算原套餐折抵。一次性流量包使用本项目的长期有效模型。原站完整计价和权益规则未核实，不能据此认定业务规则完全一致。

## 构建与检查

```bash
make test
make build
```

构建产物为 `web/dist/` 和 `.local/cloud`。从项目根目录运行 Go 构建产物时需配置静态目录：

```bash
WEB_DIST=web/dist .local/cloud -listen 127.0.0.1:8080 -db .local/production.db
```

Go 服务会对前端路由提供 SPA 回退，浏览器直接访问 `/orders`、`/admin/users` 等路径可正常加载。

## VPS 自部署

已提供 Docker 多阶段构建、Compose 配置和 Nginx TLS 反向代理示例；是否已实际构建镜像和部署见模块状态。需要 Docker Compose：

1. 复制 `.env.example` 为 `.env`，填写真实 HTTPS 地址、管理员邮箱与至少 12 位的初始密码。
2. 执行 `docker compose up -d --build`。服务端口仅暴露到宿主机 `127.0.0.1:8080`。
3. 按 `deploy/nginx.conf.example` 配置域名与实际 TLS 证书，反向代理至 8080。
4. 初始化成功后从 `.env` 移除初始管理员密码，再重建容器使运行时环境也不再携带初始化凭据；已有管理员会保留。

生产模式没有默认管理员密码。`BOOTSTRAP_ADMIN_EMAIL` 和 `BOOTSTRAP_ADMIN_PASSWORD` 仅在没有管理员时初始化首个管理员。

配置项：

| 配置 | 用途 |
| --- | --- |
| `PUBLIC_URL` | 对外完整站点地址，生成订阅与邀请链接；TLS 反向代理时必须正确填写 |
| `WEB_DIST` | 静态构建目录，Go 在 backend 目录运行时默认 `../web/dist` |
| `ALLOWED_ORIGINS` | 允许提交变更的额外完整来源地址，以逗号分隔；同源默认允许 |
| `COOKIE_SECURE=1` | HTTPS 环境下发送 Secure 会话 Cookie |
| `BOOTSTRAP_ADMIN_EMAIL/PASSWORD` | 首次生产管理员初始化；不保存到代码或镜像 |
| `DEMO_MODE=1` | 显式启用虚构数据；生产不要启用 |

数据位于 Compose 的 `cloud-data` 卷。备份应使用 SQLite 在线备份机制（例如 `sqlite3 /data/nicecloud.db '.backup /backup/nicecloud.db'`），或停止应用后备份完整数据库及伴随文件。恢复前停止服务，将备份还原至同一路径并确认文件属主可读写。

## 尚未完成

真实支付商户及回调/退款/对账；真实节点认证、用户同步、流量采集与限速/设备执行；完整客户端适配；邮件找回密码和邮件通知；细粒度角色、节点组、任务监控及原站后台视觉核对。当前这些项目均未宣称已接入。

管理操作及功能页面会展示相应接入状态。页面上维护了速度、设备或节点参数，不表示节点端已实际执行限制。
