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
- 托管节点上传/下载字节采集、累计上报去重、倍率计费、超额停用用户独立进程，以及节点执行确认。

金额统一用整数分。订单保留价格与权益快照；余额结算、提现等关键变更放在数据库事务中。

同一周期套餐续费顺延有效期并保留当前已用量；换购其他套餐覆盖权益，不计算原套餐折抵。一次性流量包使用本项目的长期有效模型。原站完整计价和权益规则未核实，不能据此认定业务规则完全一致。

## 构建与检查

```bash
make test
make build
```

构建产物为 `web/dist/`、`.local/cloud`、`.local/node-agent` 和 `.local/nicecloud-kernel`。从项目根目录运行 Go 服务时需配置静态目录：

```bash
WEB_DIST=web/dist .local/cloud -listen 127.0.0.1:8080 -db .local/production.db
```

Go 服务会对前端路由提供 SPA 回退，浏览器直接访问 `/orders`、`/admin/users` 等路径可正常加载。

## 流量计算与超额停服

计费用量为 `(上传 bytes + 下载 bytes) × 节点倍率`，倍率使用百万分整数精度并结转余数。数据库保存整数 bytes；界面的 GB 按 `1024³ bytes` 展示。重复、乱序和重试不会重复扣量；旧周期延迟报告只进入历史，不扣新周期额度。

在管理端创建节点后点击“接入 Agent”，选择内核和用户端口范围，保存仅显示一次的节点凭据。按照 [节点部署说明](node-agent/README.md) 在实际节点安装并运行 `.local/node-agent`（`make build` 同时构建）。托管模式为每位用户分配独立凭据、端口和内核进程；达到或超过额度、到期、人工暂停或账户封禁时停止该用户进程，关闭已有连接。控制面失联、租约到期或持续无法安全采集时也会停止实例。

后台“流量统计”显示停服策略、最近联络和执行确认，失联或尚未 ACK 的节点显示未确认。“订阅权益”可重置流量周期；重置不会解除封禁、人工暂停或到期。周期套餐自动恢复新周期额度，一次性包不自动重置；同套餐续费仍保留已用量。

已有共享进程节点必须迁移到托管模式才能强制断开连接。轮询默认每秒一次，多节点会有采集窗口内的超用，不提供严格零字节透支保证。sing-box 必须包含 `with_v2ray_api` 构建标签；内核与客户端协议兼容仍需按实际部署验收。

本地真实 Xray 回归脚本使用临时数据库与本机端口，验证字节入账、超额断开已有 TCP、周期恢复及失联停服：

```sh
make traffic-smoke XRAY_CORE=/absolute/path/to/xray
```

接口和安全约定见 [TRAFFIC_PROTOCOL.md](TRAFFIC_PROTOCOL.md)。此项目的协议不兼容 XrayR 原生面板接口。

## Clash 参考配置对应的服务端

已按根目录 `clash.yaml` 的协议类型补齐 VLESS TCP + Vision + REALITY、AnyTLS + TLS ECH。服务端内核位于 `proxy-kernel/`，直接依赖公开的 `SagerNet/sing-box v1.12.12`；`make build` 同时构建 `.local/nicecloud-kernel`，包含 REALITY 与计量需要的构建功能。

节点 `engine` 设为 `sing-box`，`core` 指向该程序。服务端私钥仅在节点本地生成和保存，中心维护对应公共参数并生成个人订阅。原客户端文件的公钥无法还原旧服务器私钥，原文件保留。

参数映射、密钥生成、节点配置与真实 Mihomo 测试见 [Clash 协议兼容说明](CLASH_COMPATIBILITY.md)。可复验：

```sh
make protocol-smoke MIHOMO_CORE=/absolute/path/to/mihomo
```

## 中心端与节点端分别运行

`backend/` 是中心服务器代码，`node-agent/` 是每台节点服务器代码。五台节点共用一个中心地址，各自使用独立 token 和本地状态目录。中心统一累计用户跨节点用量和判断额度，实际代理数据由各节点直接转发。

`make build` 构建两端。中心运行示例：

```sh
PUBLIC_URL=https://panel.example.com WEB_DIST=web/dist \
  .local/cloud -listen 127.0.0.1:8080 -db .local/production.db
```

管理员初始化方式沿用下方配置。每台节点复制 `node-agent/config.example.json`，填写该中心的 HTTPS 地址、本机内核/证书路径和该节点的凭据文件，然后运行：

```sh
.local/node-agent -config /absolute/path/to/node-config.json
```

配置文件中的相对路径以配置文件目录为基准；显式 CLI 参数覆盖文件配置。凭据放在 `token_file` 指定的文件，JSON 不接受明文 `token`。完整的一中心、五节点安装步骤、配置样例与 systemd 服务见 [独立部署说明](deploy/README.md)。

分别构建 Linux 发布包（输出至 `.local/releases/`）：

```sh
make release-center RELEASE_VERSION=0.1.0
make release-node RELEASE_ARCH=amd64 RELEASE_VERSION=0.1.0
make release-node RELEASE_ARCH=arm64 RELEASE_VERSION=0.1.0
```

中心 SQLite CGO 包需要在目标架构 Linux 上构建；节点包支持静态交叉编译。发布包包含对应程序和部署模板，不含真实凭据、证书或代理内核。

真实五节点联调在本机启动一个中心、五个独立 Agent 和两位用户的十个 Xray 实例，检查不同倍率合计、去重、全节点超额断连、用户隔离、单节点停止、中心断线及持久数据恢复：

```sh
make fleet-smoke XRAY_CORE=/absolute/path/to/xray
```

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

真实支付商户及回调/退款/对账；实际限速/设备限制、节点组授权；完整客户端适配；邮件找回密码和邮件通知；细粒度角色、完整任务监控及原站后台视觉核对。VPS 节点部署、TLS 和生产环境验收仍需完成。

管理操作及功能页面会展示相应接入状态。页面上维护了速度、设备或节点参数，不表示节点端已实际执行限制。
