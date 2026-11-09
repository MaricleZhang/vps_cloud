package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Engine, Core, StateDir, Bind, TLSCert, TLSKey string
	RealityPrivateKeyFile, ECHKeyFile             string
}

func kernelConfig(c Config, n Node, u User, statsAddr string) ([]byte, string, error) {
	if net.ParseIP(c.Bind) == nil {
		return nil, "", fmt.Errorf("-bind must be an IP address")
	}
	mode, err := nodeTLSMode(n)
	if err != nil {
		return nil, "", err
	}
	if n.Network != "" && n.Network != "tcp" {
		return nil, "", fmt.Errorf("managed nodes support only tcp transport")
	}
	if n.Flow != "" && (n.Protocol != "vless" || n.Flow != "xtls-rprx-vision" || mode == "none") {
		return nil, "", fmt.Errorf("flow must be empty or xtls-rprx-vision on secured VLESS")
	}
	if n.ECHEnabled && (n.Protocol != "anytls" || mode != "tls" || c.Engine != "sing-box") {
		return nil, "", fmt.Errorf("ECH requires AnyTLS with sing-box TLS")
	}
	if u.UUID == "" && (n.Protocol == "vless" || n.Protocol == "vmess") {
		return nil, "", fmt.Errorf("user UUID missing")
	}
	if u.Password == "" && (n.Protocol == "trojan" || n.Protocol == "anytls" || n.Protocol == "ss" || n.Protocol == "shadowsocks") {
		return nil, "", fmt.Errorf("user password missing")
	}
	if mode == "tls" {
		if c.TLSCert == "" || c.TLSKey == "" {
			return nil, "", fmt.Errorf("TLS policy requires -tls-cert and -tls-key")
		}
		if _, err := os.Stat(c.TLSCert); err != nil {
			return nil, "", fmt.Errorf("TLS certificate unavailable: %w", err)
		}
		if _, err := os.Stat(c.TLSKey); err != nil {
			return nil, "", fmt.Errorf("TLS key unavailable: %w", err)
		}
	}
	var realityKey string
	if mode == "reality" {
		if n.Protocol != "vless" || n.ServerName == "" || n.RealityHandshakeServer == "" || n.RealityHandshakePort < 1 || n.RealityHandshakePort > 65535 {
			return nil, "", fmt.Errorf("REALITY requires VLESS, server_name and a valid handshake server/port")
		}
		realityKey, err = loadRealityPrivateKey(c.RealityPrivateKeyFile, n.RealityPublicKey, n.RealityShortID)
		if err != nil {
			return nil, "", err
		}
	}
	if n.ECHEnabled {
		if err := validateECHKeyFile(c.ECHKeyFile, n.ECHConfig); err != nil {
			return nil, "", err
		}
	}
	protocol := n.Protocol
	if protocol == "ss" {
		protocol = "shadowsocks"
	}
	cipher := n.Cipher
	if cipher == "" {
		cipher = "aes-128-gcm"
	}
	// Managed credentials are arbitrary high-entropy strings. SS2022 requires
	// separate base64 PSKs and client key composition, so reject it explicitly.
	if protocol == "shadowsocks" && strings.HasPrefix(cipher, "2022-") {
		return nil, "", fmt.Errorf("managed SS2022 is not supported; use aes-128-gcm, aes-256-gcm or chacha20-ietf-poly1305")
	}
	prefix := "user>>>" + u.StatsName
	var out map[string]any
	if c.Engine == "xray" {
		client := map[string]any{"email": u.StatsName, "level": 0}
		settings := map[string]any{}
		switch protocol {
		case "vless":
			client["id"] = u.UUID
			if n.Flow != "" {
				client["flow"] = n.Flow
			}
			settings["decryption"] = "none"
			settings["clients"] = []any{client}
		case "vmess":
			client["id"] = u.UUID
			client["alterId"] = 0
			settings["clients"] = []any{client}
		case "trojan":
			client["password"] = u.Password
			settings["clients"] = []any{client}
		case "shadowsocks":
			settings = map[string]any{"method": cipher, "password": u.Password, "email": u.StatsName, "level": 0, "network": "tcp,udp"}
		default:
			return nil, "", fmt.Errorf("protocol %q unsupported by managed Xray", protocol)
		}
		stream := map[string]any{"network": "tcp", "security": "none"}
		if mode == "tls" {
			stream["security"] = "tls"
			stream["tlsSettings"] = map[string]any{"certificates": []any{map[string]any{"certificateFile": c.TLSCert, "keyFile": c.TLSKey}}}
		} else if mode == "reality" {
			stream["security"] = "reality"
			stream["realitySettings"] = map[string]any{
				"target":      net.JoinHostPort(n.RealityHandshakeServer, strconv.Itoa(n.RealityHandshakePort)),
				"serverNames": []string{n.ServerName}, "privateKey": realityKey, "shortIds": []string{n.RealityShortID},
			}
		}
		out = map[string]any{"log": map[string]any{"loglevel": "warning"}, "api": map[string]any{"tag": "stats-api", "listen": statsAddr, "services": []string{"StatsService"}}, "stats": map[string]any{}, "policy": map[string]any{"levels": map[string]any{"0": map[string]any{"statsUserUplink": true, "statsUserDownlink": true}}}, "inbounds": []any{map[string]any{"tag": u.StatsName, "listen": c.Bind, "port": u.Port, "protocol": protocol, "settings": settings, "streamSettings": stream}}, "outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}}}
	} else if c.Engine == "sing-box" {
		user := map[string]any{"name": u.StatsName}
		inbound := map[string]any{"type": protocol, "tag": u.StatsName, "listen": c.Bind, "listen_port": u.Port}
		switch protocol {
		case "vless", "vmess":
			user["uuid"] = u.UUID
			if n.Flow != "" {
				user["flow"] = n.Flow
			}
			inbound["users"] = []any{user}
		case "trojan", "anytls":
			user["password"] = u.Password
			inbound["users"] = []any{user}
		case "shadowsocks":
			inbound["method"] = cipher
			inbound["password"] = u.Password
			prefix = "inbound>>>" + u.StatsName
		default:
			return nil, "", fmt.Errorf("protocol %q unsupported by managed sing-box", protocol)
		}
		if mode != "none" {
			tls := map[string]any{"enabled": true, "server_name": n.ServerName}
			if mode == "reality" {
				tls["reality"] = map[string]any{"enabled": true, "private_key": realityKey,
					"short_id": []string{n.RealityShortID}, "handshake": map[string]any{
						"server": n.RealityHandshakeServer, "server_port": n.RealityHandshakePort}}
			} else {
				tls["certificate_path"] = c.TLSCert
				tls["key_path"] = c.TLSKey
				if n.ECHEnabled {
					tls["min_version"] = "1.3"
					tls["ech"] = map[string]any{"enabled": true, "key_path": c.ECHKeyFile}
				}
			}
			inbound["tls"] = tls
		}
		out = map[string]any{"log": map[string]any{"level": "warn"}, "inbounds": []any{inbound}, "outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}}, "experimental": map[string]any{"v2ray_api": map[string]any{"listen": statsAddr, "stats": map[string]any{"enabled": true, "users": []string{u.StatsName}, "inbounds": []string{u.StatsName}}}}}
	} else {
		return nil, "", fmt.Errorf("unsupported engine %q", c.Engine)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	return b, prefix, err
}

func nodeTLSMode(n Node) (string, error) {
	mode := n.TLSMode
	if mode == "" {
		mode = "none"
		if n.TLS || n.Protocol == "trojan" || n.Protocol == "anytls" {
			mode = "tls"
		}
	}
	if mode != "none" && mode != "tls" && mode != "reality" {
		return "", fmt.Errorf("unsupported tls_mode")
	}
	if mode == "none" && (n.Protocol == "trojan" || n.Protocol == "anytls") {
		return "", fmt.Errorf("Trojan and AnyTLS require TLS")
	}
	return mode, nil
}
