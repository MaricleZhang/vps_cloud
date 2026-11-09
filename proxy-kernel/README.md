# NiceCloud 协议内核

本模块直接依赖 [sing-box v1.12.12](https://github.com/SagerNet/sing-box/tree/v1.12.12)，提供本项目使用的 `nicecloud-kernel` 程序。VLESS/REALITY/Vision、AnyTLS/ECH、路由和 V2Ray 统计均由上游库执行，本模块只提供进程入口与安全密钥文件输出，不重新实现代理协议。

固定使用 Go 1.25+；上游模块声明 Go 1.23.1，服务端原生 ECH 需要 Go 1.24+。在本模块目录构建，必须包括两个标签（项目根目录也可直接运行 `make build-kernel`）：

```sh
mkdir -p ../.local
go build -tags with_utls,with_v2ray_api -trimpath -o ../.local/nicecloud-kernel .
go test -tags with_utls,with_v2ray_api ./...
```

`with_utls` 提供 REALITY，`with_v2ray_api` 提供节点 Agent 使用的累计统计。不要传入已废弃的 `with_reality_server` 标签。未包含所需标签的二进制会拒绝启动服务，并在 `version` 显示真实构建能力。

与原 sing-box JSON 配置及节点 Agent 的调用方式兼容：

```sh
.local/nicecloud-kernel version
.local/nicecloud-kernel check -c /absolute/path/to/server.json
.local/nicecloud-kernel run -c /absolute/path/to/server.json
```

以上示例从项目根目录执行。节点 Agent 设置 `engine: "sing-box"`，`core` 指向此程序的绝对路径。统计接口只允许节点生成配置中的 loopback 监听；本模块不另外暴露管理接口。

## 生成新服务端密钥

客户端的 REALITY 公钥与 ECH 公共配置不能还原原服务端私钥。为新节点生成密钥，或配置你实际掌握的对应服务端私钥；原 `clash.yaml` 中的秘密不会复制到此模块。

```sh
.local/nicecloud-kernel generate reality-keypair --out /private/node-keys
.local/nicecloud-kernel generate ech-keypair --server-name public.example.com --out /private/node-keys
```

分别写入 `reality.key`（单行 X25519 Raw URL Base64 私钥）与 `ech.pem`（上游原生 `ECH KEYS` PEM）。文件权限为 `0600`，新目录为 `0700`，已有文件不会被覆盖。也可用 `--private-key-file /absolute/new-key-file` 代替 `--out`。

标准输出只有可下发的 JSON：REALITY 的 `reality_public_key`、`reality_short_id`，或 ECH 的 `ech_config`（Base64 ECHConfigList）、`ech_query_server_name`。私钥不会打印。ECH 私钥生成直接调用上游 `tls.ECHKeygenDefault`。ECH 公共名称是外层可见名称；真实节点证书仍须验证内层 SNI，不应通过跳过证书校验来替代有效证书。

`run` 响应 SIGINT/SIGTERM 并关闭所有由上游管理的连接。用户超额停服由外层 Agent 终止其独立内核进程，继续沿用中心额度与持久流量计量协议。

许可和分发说明见 [NOTICE.md](NOTICE.md)、`LICENSE`、`UPSTREAM-LICENSE` 与 `COPYING`。
