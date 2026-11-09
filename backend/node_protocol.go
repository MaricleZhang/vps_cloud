package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"net"
	"strings"
)

// These are public transport parameters. Server-side private keys stay in the
// node's local files and are deliberately absent from both storage and policy.
var nodeProtocolFields = []string{
	"network", "tls_mode", "flow", "client_fingerprint", "udp",
	"reality_public_key", "reality_short_id", "reality_handshake_server", "reality_handshake_port",
	"ech_enabled", "ech_config", "ech_query_server_name",
}

func nodeTLSMode(n M) string {
	if mode := text(n, "tls_mode"); mode != "" {
		return mode
	}
	if boolean(n, "tls") || text(n, "protocol") == "anytls" || text(n, "protocol") == "trojan" {
		return "tls"
	}
	return "none"
}

func validNodeHost(value string) bool {
	if value == "" || len(value) > 253 || strings.ContainsAny(value, "\r\n\t /:@\\") {
		// Unbracketed IPv6 is accepted as a handshake destination.
		return net.ParseIP(value) != nil
	}
	if net.ParseIP(value) != nil {
		return true
	}
	for _, label := range strings.Split(strings.TrimSuffix(value, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func rejectNodePrivateKeys(value any) error {
	var fields map[string]any
	switch v := value.(type) {
	case M:
		fields = v
	case map[string]any:
		fields = v
	case []any:
		for _, item := range v {
			if err := rejectNodePrivateKeys(item); err != nil {
				return err
			}
		}
	}
	for key, child := range fields {
		name := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
		compact := strings.ReplaceAll(name, "_", "")
		if strings.Contains(compact, "privatekey") || compact == "echkey" || compact == "echkeyfile" || compact == "tlskey" || compact == "tlskeyfile" {
			return bad("服务端私钥只能保存在节点本地文件，请提交对应公钥或 ECH 公共配置")
		}
		if err := rejectNodePrivateKeys(child); err != nil {
			return err
		}
	}
	return nil
}

// Check framing without interpreting or recreating the TLS protocol. The node
// kernel validates the actual ECH key/config association during startup.
func validECHConfig(encoded string) bool {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) < 6 || len(raw) > 65537 || int(binary.BigEndian.Uint16(raw[:2])) != len(raw)-2 {
		return false
	}
	remaining := raw[2:]
	supported := false
	for len(remaining) > 0 {
		if len(remaining) < 4 {
			return false
		}
		size := int(binary.BigEndian.Uint16(remaining[2:4]))
		if size == 0 || size > len(remaining)-4 {
			return false
		}
		if binary.BigEndian.Uint16(remaining[:2]) == 0xfe0d {
			supported = true
		}
		remaining = remaining[4+size:]
	}
	return supported
}

func normalizeNodeProtocol(n M) error {
	for _, key := range []string{"network", "tls_mode", "flow", "client_fingerprint", "server_name", "reality_public_key", "reality_short_id", "reality_handshake_server", "ech_config", "ech_query_server_name"} {
		if value, ok := n[key]; ok {
			if _, valid := value.(string); !valid {
				return bad("节点协议参数必须使用正确的字符串类型")
			}
		} else {
			n[key] = ""
		}
	}
	for _, key := range []string{"tls", "udp", "ech_enabled"} {
		if value, ok := n[key]; ok {
			if _, valid := value.(bool); !valid {
				return bad("节点 TLS、UDP 与 ECH 开关必须是布尔值")
			}
		}
	}
	if _, ok := n["ech_enabled"]; !ok {
		n["ech_enabled"] = false
	}
	protocol := text(n, "protocol")
	switch protocol {
	case "vless", "vmess", "trojan", "anytls", "shadowsocks", "ss":
	default:
		return bad("节点协议不受支持")
	}
	if text(n, "network") == "" {
		n["network"] = "tcp"
	}
	if text(n, "network") != "tcp" {
		return bad("当前托管节点只支持 TCP 传输")
	}
	mode := nodeTLSMode(n)
	if mode != "none" && mode != "tls" && mode != "reality" {
		return bad("TLS 模式须为 none、tls 或 reality")
	}
	n["tls_mode"] = mode
	n["tls"] = mode != "none"
	if (protocol == "anytls" || protocol == "trojan") && mode != "tls" {
		return bad("AnyTLS 与 Trojan 必须使用 TLS 模式")
	}
	if _, ok := n["udp"]; !ok {
		n["udp"] = true
	}
	flow := text(n, "flow")
	if flow != "" && (protocol != "vless" || flow != "xtls-rprx-vision" || mode == "none") {
		return bad("Vision flow 仅支持启用 TLS 或 REALITY 的 VLESS 节点")
	}
	fingerprint := text(n, "client_fingerprint")
	if fingerprint == "" {
		fingerprint = "chrome"
	}
	if len(fingerprint) > 64 || strings.ContainsAny(fingerprint, "\r\n\t ") {
		return bad("客户端 TLS 指纹参数无效")
	}
	n["client_fingerprint"] = fingerprint
	if host := text(n, "server_name"); host != "" && !validNodeHost(host) {
		return bad("TLS 服务器名称无效")
	}
	if host := text(n, "ech_query_server_name"); host != "" && !validNodeHost(host) {
		return bad("ECH 查询名称无效")
	}
	if host := text(n, "reality_handshake_server"); host != "" && !validNodeHost(host) {
		return bad("REALITY 握手目标无效")
	}
	if _, ok := n["reality_handshake_port"]; ok {
		if !isNumeric(n["reality_handshake_port"]) || num(n, "reality_handshake_port") != float64(integer(n, "reality_handshake_port")) || integer(n, "reality_handshake_port") < 1 || integer(n, "reality_handshake_port") > 65535 {
			return bad("REALITY 握手端口必须是 1–65535 的整数")
		}
	} else {
		n["reality_handshake_port"] = int64(443)
	}
	if mode == "reality" {
		if protocol != "vless" {
			return bad("REALITY 模式只支持 VLESS 节点")
		}
		if text(n, "server_name") == "" {
			return bad("REALITY 需要服务器名称")
		}
		key, err := base64.RawURLEncoding.DecodeString(text(n, "reality_public_key"))
		if err != nil || len(key) != 32 {
			return bad("REALITY 公钥必须是 X25519 的 Base64URL 公钥")
		}
		shortID := text(n, "reality_short_id")
		if _, err := hex.DecodeString(shortID); err != nil || len(shortID) == 0 || len(shortID) > 16 || len(shortID)%2 != 0 {
			return bad("REALITY short ID 必须为 2–16 位偶数长度十六进制")
		}
		if text(n, "reality_handshake_server") == "" {
			n["reality_handshake_server"] = text(n, "server_name")
		}
		if flow == "" {
			n["flow"] = "xtls-rprx-vision"
		}
	}
	if boolean(n, "ech_enabled") {
		if protocol != "anytls" || mode != "tls" || text(n, "server_name") == "" {
			return bad("ECH 需要带服务器名称的 AnyTLS TLS 节点")
		}
		if !validECHConfig(text(n, "ech_config")) {
			return bad("ECH 公共配置必须是有效的 Base64 ECHConfigList")
		}
	}
	return nil
}

func validateNodeEngine(n M, engine string) error {
	protocol := text(n, "protocol")
	supported := protocol == "vless" || protocol == "trojan" || protocol == "ss" || protocol == "shadowsocks" || engine == "xray" && protocol == "vmess" || engine == "sing-box" && protocol == "anytls"
	if (engine != "xray" && engine != "sing-box") || !supported {
		return bad("所选内核不支持该节点协议")
	}
	if boolean(n, "ech_enabled") && engine != "sing-box" {
		return bad("ECH 托管节点需要 sing-box 内核")
	}
	return nil
}

func normalizedNodeProtocol(n M) (M, error) {
	out := M{}
	for key, value := range n {
		out[key] = value
	}
	return out, normalizeNodeProtocol(out)
}

func clashProxy(n M) (M, bool, error) {
	n, err := normalizedNodeProtocol(n)
	if err != nil {
		return nil, false, err
	}
	p := M{"name": text(n, "name"), "server": text(n, "host"), "port": integer(n, "port"), "udp": boolean(n, "udp")}
	switch text(n, "protocol") {
	case "shadowsocks", "ss":
		if text(n, "password") == "" {
			return nil, false, nil
		}
		p["type"], p["password"], p["cipher"] = "ss", text(n, "password"), text(n, "cipher")
		if text(p, "cipher") == "" {
			p["cipher"] = "aes-128-gcm"
		}
	case "trojan", "anytls":
		if text(n, "password") == "" {
			return nil, false, nil
		}
		p["type"], p["password"], p["sni"] = text(n, "protocol"), text(n, "password"), text(n, "server_name")
		p["client-fingerprint"], p["skip-cert-verify"] = text(n, "client_fingerprint"), false
		if boolean(n, "ech_enabled") {
			ech := M{"enable": true, "config": text(n, "ech_config")}
			if name := text(n, "ech_query_server_name"); name != "" {
				ech["query-server-name"] = name
			}
			p["ech-opts"] = ech
		}
	case "vless", "vmess":
		if text(n, "uuid") == "" {
			return nil, false, nil
		}
		p["type"], p["uuid"], p["tls"] = text(n, "protocol"), text(n, "uuid"), boolean(n, "tls")
		p["servername"], p["network"] = text(n, "server_name"), text(n, "network")
		p["client-fingerprint"], p["skip-cert-verify"] = text(n, "client_fingerprint"), false
		if text(n, "protocol") == "vmess" {
			p["alterId"], p["cipher"] = 0, "auto"
		} else {
			p["encryption"] = "none"
			if flow := text(n, "flow"); flow != "" {
				p["flow"] = flow
			}
			if text(n, "tls_mode") == "reality" {
				p["reality-opts"] = M{"public-key": text(n, "reality_public_key"), "short-id": text(n, "reality_short_id")}
			}
		}
	default:
		return nil, false, nil
	}
	return p, true, nil
}
