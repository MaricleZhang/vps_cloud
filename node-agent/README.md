# NiceCloud 托管节点 Agent

Agent 将每位用户分配到独立端口与 Xray / sing-box 进程，查询本机 gRPC 累计统计，再向 Go 控制面上报。达到额度、人工暂停、账户停用或订阅到期时，终止对应进程并关闭已有连接。它使用本项目 [流量协议](../TRAFFIC_PROTOCOL.md)，不能作为 XrayR 原生面板直接使用；已有共享进程节点需要迁移。

## 当前验收范围

- 已使用官方 **Xray v26.10.31** 完成本机 VLESS、无 TLS 的真实流量端到端验收：用户独立凭据与端口、整数流量入账、达到额度后关闭已有 TCP、节点停止确认、续费保留已用额度、重置保留暂停、旧周期延迟数据隔离及控制面失联租约停服。
- 已通过一中心、五个独立 Agent、两用户十个 Xray 实例的真实联调：不同倍率统一扣量、全节点超额断连、其他用户隔离、单节点停机，以及中心/节点重启后的离线补报和自动恢复。此为本机多进程验收。
- sing-box 配置与 StatsService 适配已实现，需要带 **`with_v2ray_api`** 的构建；尚未完成真实 sing-box 流量验收。
- TLS、其他协议的真实客户端连接、Linux VPS 部署和 systemd 运维尚未完成现场验收。下面给出部署步骤，不将示例等同于已部署。
- 设备数量和速度限制尚未执行。轮询计量有检测与同步延迟，窗口内可能超用，取决于速率与节点数量；这不是全局零透支额度租约。

## 构建与节点准备

需要 Go 1.25+。在本目录构建：

```sh
go build -o ../.local/node-agent .
go test ./...
go test -race ./...
```

代理内核需另行安装。Xray 可使用[官方发布](https://github.com/XTLS/Xray-core/releases)；sing-box 的 [V2Ray API](https://sing-box.sagernet.org/configuration/experimental/v2ray-api/) 需要相应构建功能。请保留所用二进制的版本与 SHA-256，升级后重跑连接和计量验证。

在管理后台新增节点，填写客户端实际访问的公开地址、协议、TLS/SNI 与流量倍率，然后选择“接入 Agent”，配置内核和用户端口范围并签发独立节点凭据。端口范围必须在 1024–65535 内；一个用户占用一个稳定端口，已分配的端口不能在重新签发时移出范围。根据所用协议开放相应 TCP/UDP 端口，统计 API 只监听 loopback，不能暴露公网。

当前托管协议组合：Xray 支持 VLESS、VMess、Trojan、Shadowsocks；sing-box 支持 VLESS、Trojan、AnyTLS、Shadowsocks。托管 Shadowsocks 使用传统 AEAD 密码，不支持 SS2022。Trojan、AnyTLS 以及节点启用 TLS 时，需要有效证书与私钥，Agent 不负责申请证书。

节点凭据只在签发响应中显示，控制面只保存摘要。重新签发会撤销旧凭据；更新节点上的凭据并重启 Agent，等待新的执行确认。

## 启动

生产控制面必须使用 HTTPS。HTTP 仅允许 `localhost`、`127.0.0.1` 或 `::1`，用于本机联调。

建议将凭据放在仅服务账户可读的文件中，而非命令参数。文件只放一行节点凭据，权限设为 `0600`。也可通过 `NICECLOUD_NODE_TOKEN` 环境变量传入；内核和 Agent 均按同一受限服务账户运行。

节点支持独立 JSON 配置文件，完整样例见 [config.example.json](config.example.json)，部署包中的 systemd 服务使用以下入口：

```sh
/opt/nicecloud-node/bin/node-agent -config /etc/nicecloud-node/config.json
```

JSON 字段为 `panel`、`engine`、`core`、`state_dir`、`bind`、`token_file`、`tls_cert`、`tls_key`、`poll_interval`。`poll_interval` 使用 `"1s"` 这样的持续时间字符串；`state_dir` 指向本机持久数据目录。相对的内核、状态、token 和证书路径按配置文件所在目录解析。

优先级为“显式 CLI 参数 > JSON > 默认值”。仍可用 `-panel`、`-core`、`-state` 等旧参数运行或覆盖配置项；CLI 路径保持原语义，`-core` 要求绝对路径。指定 `token_file` 时从文件读取凭据，否则使用 `NICECLOUD_NODE_TOKEN`；显式 `-token-file=` 可清空文件配置并回退到环境变量。JSON 不接受明文 `token` 或未知字段，避免拼写错误被静默忽略。

以下为保留兼容的完整 CLI 运行方式：

```sh
/usr/local/bin/nicecloud-node \
  -panel https://panel.example.com \
  -token-file /etc/nicecloud-node/node.token \
  -engine xray \
  -core /usr/local/bin/xray \
  -state /var/lib/nicecloud-node \
  -bind 0.0.0.0 \
  -tls-cert /etc/nicecloud-node/tls/fullchain.pem \
  -tls-key /etc/nicecloud-node/tls/privkey.pem
```

使用 sing-box 时改为 `-engine sing-box -core /usr/local/bin/sing-box`。无 TLS 的本机 VLESS 测试可以省略两个证书参数。`-bind` 必须是 IP 地址；默认 `0.0.0.0`，本机测试使用 `127.0.0.1`。`-poll` 默认 `1s`，允许 `100ms` 至 `5s`。

状态目录保存用户凭据配置、累计样本、待发 outbox 与日志，权限为 `0700`，文件为 `0600`。请将它放在持久磁盘，按敏感数据备份，不要加入 Git 或放进公开静态目录。目录锁阻止同一状态目录同时运行两个 Agent；目录还绑定节点 ID，不能换给另一节点复用。重启时保留该目录，不能通过删除状态来“清理流量”。

## Linux systemd 示例

中心和节点现在有独立发布包及可审查的服务文件：[一台中心与五台节点部署](../deploy/README.md)、[节点 systemd 模板](../deploy/node/nicecloud-node.service)、[部署 JSON 样例](../deploy/node/config.example.json)。在项目根目录可单独生成静态 Linux 节点包：

```sh
bash scripts/build-release.sh --component node --arch amd64 --version 0.1.0
```

先建立无登录权限的 `nicecloud-node` 服务账户，安装 Agent 到 `/opt/nicecloud-node/bin/node-agent`，将配置保存到 `/etc/nicecloud-node/config.json`。创建私密的 token 文件，并按实际协议安装证书与私钥。所附服务通过 `-config` 启动，systemd 管理 `/var/lib/nicecloud-node` 持久目录。完整安装命令见部署说明；构建脚本不会自动建立账户或安装服务。

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now nicecloud-node
sudo systemctl status nicecloud-node
sudo journalctl -u nicecloud-node -f
```

Linux 子进程设置独立进程组和 `Pdeathsig=SIGKILL`，Agent 异常退出时直接子内核也会被杀死。systemd 的 `KillMode=control-group` 再保证服务停止时清理整个服务进程组；不要改成只终止主进程。非 Linux 平台用于开发测试，没有 Linux parent-death 保证。

## 计量、失联与故障定位

- 内核统计始终读取累计值，禁止清零。每个用户进程与周期使用独立 session；累计样本先落盘再上报，失败保留 outbox，服务端重试去重并用整数倍率余数结转。
- 周期切换先保存旧进程累计样本，再启动新周期进程；旧周期延迟数据进入历史，不扣新周期额度。每次停止与崩溃存在最后一次采样后的有限计量窗口，应结合实际轮询频率评估。
- 本地采样不依赖面板请求成功。策略失联、持续计量投递失败或统计采集失败会关闭对应代理，不能依靠反复成功的策略 GET 无限延长未能上报的服务。
- 中心恢复且待发报告成功补交后，允许使用的实例可以自动重建。主动停服造成的统计连接断开不作为永久采集故障；真正的统计故障仍停用当前策略，排除内核/统计故障后重启 Agent 并检查执行确认。
- 管理后台“流量统计”区分期望策略、待确认、执行失败和当前成功 ACK。只有进程实际启动/停止成功并确认当前版本才显示执行已确认；失联或过期 ACK 不代表停服已核实。
- Agent 日志在 systemd journal；各用户内核日志在状态目录的 `user-<id>.log`。统计查询失败时检查内核是否启用了 StatsService、sing-box 是否带 `with_v2ray_api`，以及 loopback 是否可达。TLS 启动失败时检查证书路径和服务账户权限。

Xray v26.10.31 默认 Freedom 会阻断 VLESS 等协议访问私网目标，并可能静默等待，因此转发到 `127.0.0.1` 的普通测试会超时。这是[该版本的内核规则](https://github.com/XTLS/Xray-core/blob/v26.10.31/proxy/freedom/freedom.go)，不应通过放开生产私网访问来修复测试。

项目根目录的真实 Xray 验收脚本在临时目录启动控制面和 Agent，并仅为本机 echo 的指定端口注入测试规则；不修改生产 Agent 默认配置：

```sh
make traffic-smoke XRAY_CORE=/absolute/path/to/xray
make fleet-smoke XRAY_CORE=/absolute/path/to/xray
```

该脚本使用临时数据库与端口，自动输出内核 SHA-256，并验证真实连接的计量、额度停服与失联断开。VLESS 本机明文验收不代替 TLS、客户端兼容或 VPS 实际部署验收。
