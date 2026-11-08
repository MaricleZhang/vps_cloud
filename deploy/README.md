# 一台中心服务器与多台节点服务器

本项目有两个独立程序：中心端 `cloud` 保存账户、套餐、全局用量和节点策略；节点端 `node-agent` 在每台代理服务器本地运行、采集统计和执行停服。用户访问目标网站的数据由所选节点直接转发，中心只接收用量报告及提供控制 API。

五台节点都连接同一个中心 URL，分别使用独立节点 token、独立状态目录。节点之间不需要连接。中心也可与某个节点同机，但仍只有一份中心数据库，两个角色使用各自的配置与数据目录。

```text
用户设备 <──> 节点 1 / 2 / 3 / 4 / 5 <──> 目标网站
                    │       ▲
              上传/下载统计  启停策略
                    ▼       │
                 中心 cloud + SQLite
```

## 目录与配置

| 角色 | 程序及静态资源 | 配置 | 持久数据 |
| --- | --- | --- | --- |
| 中心 | `/opt/nicecloud-center/bin/cloud`、`/opt/nicecloud-center/web` | `/etc/nicecloud-center/center.env` | `/var/lib/nicecloud-center/nicecloud.db` 及 SQLite 伴随文件 |
| 每台节点 | `/opt/nicecloud-node/bin/node-agent`、另行安装的代理内核 | `/etc/nicecloud-node/config.json`、`node.token`、TLS 文件 | `/var/lib/nicecloud-node/`：累计样本、outbox、用户内核配置与日志 |

目录名是所附 systemd 模板的默认值，修改时保持服务文件与配置一致。程序目录由 root 持有，服务以专用非 root 账户运行。状态目录权限 `0700`，token 和私钥文件权限 `0600`，不要把真实凭据放进代码仓库或发布包。

## 构建发布包

在项目根目录运行；脚本只构建并打包，不安装服务、不改变系统配置，也不下载或捆绑 Xray/sing-box：

```sh
bash scripts/build-release.sh --component all --arch native --version 0.1.0
```

输出到 `.local/releases/`，可用 `--out /absolute/output/path` 修改。每个归档都有单独的 SHA-256 文件：

```text
nicecloud-center-0.1.0-linux-amd64.tar.gz
nicecloud-center-0.1.0-linux-amd64.tar.gz.sha256
nicecloud-node-0.1.0-linux-amd64.tar.gz
nicecloud-node-0.1.0-linux-amd64.tar.gz.sha256
```

传输到服务器后，在同一目录执行 `sha256sum --check <归档名>.sha256` 核对传输完整性，再解压归档。

中心使用 SQLite CGO，发布包必须在目标架构的 Linux 主机上构建，需要 Go 1.25+、C 编译器、Node.js 22.12+ 与 npm。脚本会拒绝把 macOS 构建或未配置交叉 C 工具链的构建标成 Linux 中心包。中心二进制还需要与构建环境兼容的 libc；在不同发行版部署前应实际运行验证。已有项目根目录的 Dockerfile/Compose 仍是另一种中心端交付方式。

节点端不需要 CGO，可从 macOS 或另一种 Linux 架构构建：

```sh
bash scripts/build-release.sh --component node --arch amd64 --version 0.1.0
bash scripts/build-release.sh --component node --arch arm64 --version 0.1.0
```

`--component center` 只构建中心；`--arch` 仅支持 `native`、`amd64`、`arm64`。解压后，中心包包含 `bin/cloud`、`web/`、systemd 和环境文件模板；节点包包含 `bin/node-agent`、systemd、JSON 配置模板与流量协议。发布包没有真实管理员密码、节点 token、证书或代理内核。

## 部署中心

以下命令面向使用 systemd 的 Debian/Ubuntu，其他发行版使用相应的账户管理命令。先解压与 CPU 架构匹配的中心归档并进入解压目录，然后审查模板再安装：

```sh
sudo useradd --system --user-group --home-dir /var/lib/nicecloud-center --shell /usr/sbin/nologin nicecloud-center
sudo install -d -m 0755 /opt/nicecloud-center/bin
sudo install -m 0755 bin/cloud /opt/nicecloud-center/bin/cloud
sudo install -d -m 0755 /opt/nicecloud-center/web
sudo cp -R web/. /opt/nicecloud-center/web/
sudo install -d -m 0700 /etc/nicecloud-center
sudo install -m 0600 deploy/center/center.env.example /etc/nicecloud-center/center.env
sudoedit /etc/nicecloud-center/center.env
sudo install -m 0644 deploy/center/nicecloud-center.service /etc/systemd/system/nicecloud-center.service
sudo systemctl daemon-reload
sudo systemctl enable --now nicecloud-center
sudo systemctl status nicecloud-center
```

首次启动前，在环境文件中设置真实的 `PUBLIC_URL`、相同来源的 `ALLOWED_ORIGINS`、初始管理员邮箱和至少 12 位的独立密码。首次初始化完成后移除 `BOOTSTRAP_ADMIN_PASSWORD` 并重启服务；已有管理员不会被重新创建。

服务仅监听本机 `127.0.0.1:8080`。在同机 Nginx 配置中心域名与有效 TLS 证书，可参考归档的 `deploy/nginx.conf.example`，再将公网 HTTPS 443 反向代理到 8080。节点端使用 HTTPS 访问中心；不要将数据库或本机 8080 直接暴露给公网。

```sh
curl --fail http://127.0.0.1:8080/api/health
sudo journalctl -u nicecloud-center -f
```

先确认中心网页能通过真实 HTTPS 域名登录，再注册节点。证书申请、DNS 和防火墙配置须按实际服务器完成，模板不代表它们已部署。

## 为五台服务器登记节点

在中心管理后台的“节点与线路”新增五条记录。每条填写该服务器的真实公开连接地址、协议、TLS/SNI 与倍率；点击“接入 Agent”，选择对应内核和专属用户端口范围，保存仅显示一次的 token。

| 服务器 | 中心地址 | token | 用户代理端口示例 |
| --- | --- | --- | --- |
| 节点 1 | `https://panel.example.com` | 节点 1 独立 token | `20000–20999` |
| 节点 2 | 同一中心 URL | 节点 2 独立 token | `20000–20999` |
| 节点 3 | 同一中心 URL | 节点 3 独立 token | `20000–20999` |
| 节点 4 | 同一中心 URL | 节点 4 独立 token | `20000–20999` |
| 节点 5 | 同一中心 URL | 节点 5 独立 token | `20000–20999` |

不同服务器可以使用相同端口范围。每台节点的 token 文件路径也可以相同，但内容必须是该节点的独立凭据。不要复制另一节点的状态目录；Agent 会核对状态目录绑定的节点身份。重签 token 会撤销旧 token，应同时更新对应服务器的 token 文件并重启 Agent。

每个托管用户会占用节点上的一个稳定端口和一个独立内核进程。端口耗尽时新用户显示不可分配，不会拖停已分配用户。端口范围大小应结合服务器资源规划，当前实现没有大规模用户容量承诺。

## 在每台服务器部署节点

在节点服务器解压匹配架构的节点归档。另行安装受支持的 Xray 或带 `with_v2ray_api` 的 sing-box，记录内核版本和 SHA-256。发布包没有自动下载未知版本内核的安装动作。

```sh
sudo useradd --system --user-group --home-dir /var/lib/nicecloud-node --shell /usr/sbin/nologin nicecloud-node
sudo install -d -m 0755 /opt/nicecloud-node/bin
sudo install -m 0755 bin/node-agent /opt/nicecloud-node/bin/node-agent
sudo install -d -m 0700 -o nicecloud-node -g nicecloud-node /etc/nicecloud-node
sudo install -m 0600 -o nicecloud-node -g nicecloud-node deploy/node/config.example.json /etc/nicecloud-node/config.json
sudo install -m 0600 -o nicecloud-node -g nicecloud-node /dev/null /etc/nicecloud-node/node.token
sudoedit /etc/nicecloud-node/node.token
sudoedit /etc/nicecloud-node/config.json
sudo install -m 0644 deploy/node/nicecloud-node.service /etc/systemd/system/nicecloud-node.service
sudo systemctl daemon-reload
sudo systemctl enable --now nicecloud-node
sudo systemctl status nicecloud-node
```

token 文件只放该节点的一行凭据。JSON 的 `panel` 指向同一个中心，`core` 指向本机内核可执行文件，`engine` 与中心签发时选择的内核一致。`state_dir` 使用持久磁盘目录；`token_file` 指向该节点凭据文件。JSON 不接受明文 `token` 字段。`-config` 文件的字段说明与 CLI 覆盖规则见 [Agent 说明](../node-agent/README.md)。

如果中心配置该节点使用 TLS，在 `/etc/nicecloud-node/tls/` 安装证书与私钥，服务账户必须能读取；配置 `tls_cert`、`tls_key`，证书必须对应客户端使用的节点域名。Agent 不申请证书。使用无 TLS 的本机测试时可将这两个字段留空；不要把本机明文测试当成公网 TLS 验收。

在每台节点防火墙开放分配给用户的代理端口，按协议需要开放 TCP/UDP；允许节点出站访问中心 HTTPS 443。Agent 使用主动拉取和上报，没有需要向公网开放的管理监听端口。内核统计只监听 loopback，不要开放统计 API。中心与各节点均不需要访问其他节点。

systemd 模板保留 `KillMode=control-group`。Linux 子内核设置 parent-death signal，Agent 异常退出会终止直接子内核；服务停止时 systemd 也会清理整个控制组。非 Linux 平台仅用于开发，不具备 Linux parent-death 保证。

## 运行检查、升级与数据

在中心“流量统计”检查五台节点的最近联络、每位用户的期望状态和当前版本 ACK。只有当前实例真正启动成功后，用户订阅才包含该托管节点的专属端口与凭据。中心按所有节点上报的 `(上传 + 下载) × 节点倍率` 合并同一用户、同一周期的额度，达到额度后所有节点后续取得禁用策略并停止对应用户实例。

上报使用累计计数和持久 outbox；重试不重复扣费。策略失联或计量报告持续不能送达时，最长 15 秒的执行租约到期会停止代理。本地采样仍保存可读取的累计数据，恢复后补交。中心故障会影响全部托管节点；当前没有中心高可用或严格零字节超用保证，采样窗口内可能超用。

```sh
sudo journalctl -u nicecloud-node -f
sudo systemctl restart nicecloud-node
```

升级时先停止相应服务，替换 `/opt/...` 下的程序和中心静态资源，再启动并核对健康与执行状态。保留 `/etc/...` 配置和 `/var/lib/...` 数据。不要通过清空节点状态目录解决重试问题。中心 SQLite 备份使用在线备份 API/工具，或停止服务后复制数据库和必要的伴随文件；只复制运行中的主数据库文件不能保证完整恢复。

既有共享 XrayR/其他面板进程必须迁移成此 Agent 的托管用户实例，才能获得强制断开能力。现有代码已完成本机一中心、五个独立 Agent、两用户十个真实 Xray VLESS 实例的计量、停服、用户隔离和失联恢复验收；公网 TLS、sing-box 真流量、VPS/systemd 现场部署以及设备/限速执行仍需分别验收。在源码目录运行 `make fleet-smoke XRAY_CORE=/absolute/path/to/xray` 可复验。
