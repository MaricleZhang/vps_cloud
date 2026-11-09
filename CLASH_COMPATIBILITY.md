# Clash 参考配置与服务端实现

本项目根目录的 `clash.yaml` 是客户端配置，只作为协议参考；不导入其中的现有服务器、UUID、密码或用户身份，不修改原文件，也不连接原文件中的远程节点。

已识别 25 个代理条目：24 个 VLESS TCP + Vision + REALITY，1 个带 ECH 的 AnyTLS。`mixed-port`、DNS、代理组和规则是客户端设置，继续留在客户端。

## 公开依赖

`proxy-kernel/` 直接依赖 [SagerNet/sing-box v1.12.12](https://github.com/SagerNet/sing-box/tree/v1.12.12)。VLESS、Vision、REALITY、AnyTLS、TLS ECH 和统计服务均由上游处理，本项目只做配置、进程管理、计量与中心策略适配。`go.mod` 和 `go.sum` 锁定依赖版本。

```sh
make build
.local/nicecloud-kernel version
```

构建包含 `with_utls,with_v2ray_api`；无需另装缺少统计功能的内核。`engine` 使用 `sing-box`，`core` 指向本项目构建的 `nicecloud-kernel`。许可及源码分发说明见 [proxy-kernel/NOTICE.md](proxy-kernel/NOTICE.md)。Linux 节点二进制可从源码交叉构建：

```sh
cd proxy-kernel
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags=with_utls,with_v2ray_api -trimpath -o ../.local/nicecloud-kernel-linux-amd64 .
```

arm64 服务器将 `GOARCH` 与输出名称改为 `arm64`。节点发布包继续只分发 Agent；将自行构建的内核单独安装至节点，例如 `/usr/local/bin/nicecloud-kernel`，并保留源码与许可文件。

Mihomo 官方文档提示新版 Xray 的 REALITY 互通限制，因此本参考配置的默认验证路线使用 sing-box；已有 Xray 运行方式保留，不能据此承诺所有 Xray 版本互通。[Mihomo TLS 文档](https://wiki.metacubex.one/en/config/proxies/tls/)

## 参数对应

| Clash 客户端参数 | 中心 Node 参数 | 节点执行方式 |
| --- | --- | --- |
| `type: vless`、`network: tcp` | `protocol: vless`、`network: tcp` | 上游 VLESS inbound |
| `tls: true`、`reality-opts` | `tls_mode: reality` | REALITY TLS inbound |
| `flow: xtls-rprx-vision` | `flow: xtls-rprx-vision` | 用户级 Vision flow |
| `servername` | `server_name` | REALITY 允许的 SNI |
| `reality-opts.public-key` | `reality_public_key` | 必须匹配节点本地 X25519 私钥 |
| `reality-opts.short-id` | `reality_short_id` | REALITY short ID |
| `client-fingerprint` | `client_fingerprint` | 订阅下发给客户端；已验证 chrome/firefox/safari |
| `type: anytls`、`sni` | `protocol: anytls`、`tls_mode: tls`、`server_name` | AnyTLS inbound 与 TLS 证书 |
| `ech-opts.enable` | `ech_enabled` | 服务端 TLS 启用 ECH |
| `ech-opts.config` | `ech_config` | 公共 ECHConfigList，匹配本地 ECH 私钥 |
| `ech-opts.query-server-name` | `ech_query_server_name` | 订阅的 ECH 查询名称 |
| `udp: true` | `udp: true` | 上游协议 UDP 支持；两种协议均实测 |

客户端 `uuid` 和 `password` 对应本项目给每位托管用户分配的独立凭据。每位用户有独立进程、端口与用量；中心合并其跨节点用量，超额时关闭各节点该用户的进程和已有连接。

## REALITY 节点

客户端只有公钥，无法据此还原原服务器私钥。部署新节点时在节点本地生成：

```sh
.local/nicecloud-kernel generate reality-keypair --out /absolute/private/node-keys
```

私钥写入 `node-keys/reality.key`，权限 `0600`，已有文件不覆盖。标准输出仅有公钥和 short ID；在管理后台选择 VLESS / REALITY，填写这两个公共值、SNI 和握手目标。

`reality_handshake_server` 和 `reality_handshake_port` 是 REALITY 握手目标，须支持 TLS 1.3，并与 SNI 对应。节点配置增加：

```json
{
  "engine": "sing-box",
  "core": "/usr/local/bin/nicecloud-kernel",
  "reality_private_key_file": "/etc/nicecloud-node/keys/reality.key"
}
```

这是对原节点 JSON 的补充，其余 `panel`、`token_file`、`state_dir` 等仍需配置。REALITY 模式不要求部署本机 TLS 证书。私钥与中心公钥不匹配时 Agent 拒绝启动该用户实例，不会确认在线。

## AnyTLS / ECH 节点

AnyTLS 仍需与内层 SNI 对应的有效 TLS 证书和私钥。ECH 的外层公共名称、内层 SNI 与客户端查询名称分别配置，不能用 ECH 代替证书校验。

```sh
.local/nicecloud-kernel generate ech-keypair --server-name public.example.com --out /absolute/private/node-keys
```

私钥写入 `node-keys/ech.pem`，标准输出只有可下发的 `ech_config` 与 `ech_query_server_name`。在后台选择 AnyTLS / TLS，启用 ECH 并填写对应公共值；节点 JSON 增加：

```json
{
  "engine": "sing-box",
  "core": "/usr/local/bin/nicecloud-kernel",
  "tls_cert": "/etc/nicecloud-node/tls/fullchain.pem",
  "tls_key": "/etc/nicecloud-node/tls/privkey.pem",
  "ech_key_file": "/etc/nicecloud-node/keys/ech.pem"
}
```

Agent 会检查本地 ECH 私钥与中心公共配置匹配，中心接口拒绝私钥字段。原客户端里的 ECH 公共配置也无法反推旧服务器私钥；没有旧私钥时，应使用新密钥生成的订阅。

同一物理服务器需要提供两种协议时，在中心登记两条节点记录，并使用不同 token、状态目录和互不重叠的用户端口范围运行两个 Agent 实例。

## 验证

```sh
make protocol-smoke MIHOMO_CORE=/absolute/path/to/mihomo
```

脚本使用临时本机中心、两个 Agent、本项目 sing-box 内核和真实 Mihomo 客户端，验证：

- VLESS Vision/REALITY 的 Chrome、Firefox、Safari 指纹互通。
- AnyTLS/ECH 互通，保持证书验证；仅为测试固定临时证书指纹，不修改系统信任、不关闭校验。
- 两类协议的 SOCKS5 UDP 转发。
- 真实字节进入中心账本，统一额度用尽后关闭两类协议已有 TCP 连接。

当前记录：sing-box v1.12.12、Mihomo v1.19.33、本机 macOS arm64 验证通过。公网域名/正式证书、实际 VPS 和其他客户端版本仍需部署验收。测试不读取原始配置中的远程地址和凭据。
