package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
)

type Config struct {
	Engine, Core, StateDir, Bind, TLSCert, TLSKey string
}

func kernelConfig(c Config, n Node, u User, statsAddr string) ([]byte, string, error) {
	if net.ParseIP(c.Bind) == nil {
		return nil, "", fmt.Errorf("-bind must be an IP address")
	}
	if u.UUID == "" && (n.Protocol == "vless" || n.Protocol == "vmess") {
		return nil, "", fmt.Errorf("user UUID missing")
	}
	if u.Password == "" && (n.Protocol == "trojan" || n.Protocol == "anytls" || n.Protocol == "ss" || n.Protocol == "shadowsocks") {
		return nil, "", fmt.Errorf("user password missing")
	}
	tlsEnabled := n.TLS || n.Protocol == "trojan" || n.Protocol == "anytls"
	if tlsEnabled {
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
		if tlsEnabled {
			stream["security"] = "tls"
			stream["tlsSettings"] = map[string]any{"certificates": []any{map[string]any{"certificateFile": c.TLSCert, "keyFile": c.TLSKey}}}
		}
		out = map[string]any{"log": map[string]any{"loglevel": "warning"}, "api": map[string]any{"tag": "stats-api", "listen": statsAddr, "services": []string{"StatsService"}}, "stats": map[string]any{}, "policy": map[string]any{"levels": map[string]any{"0": map[string]any{"statsUserUplink": true, "statsUserDownlink": true}}}, "inbounds": []any{map[string]any{"tag": u.StatsName, "listen": c.Bind, "port": u.Port, "protocol": protocol, "settings": settings, "streamSettings": stream}}, "outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}}}
	} else if c.Engine == "sing-box" {
		user := map[string]any{"name": u.StatsName}
		inbound := map[string]any{"type": protocol, "tag": u.StatsName, "listen": c.Bind, "listen_port": u.Port}
		switch protocol {
		case "vless", "vmess":
			user["uuid"] = u.UUID
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
		if tlsEnabled {
			inbound["tls"] = map[string]any{"enabled": true, "server_name": n.ServerName, "certificate_path": c.TLSCert, "key_path": c.TLSKey}
		}
		out = map[string]any{"log": map[string]any{"level": "warn"}, "inbounds": []any{inbound}, "outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}}, "experimental": map[string]any{"v2ray_api": map[string]any{"listen": statsAddr, "stats": map[string]any{"enabled": true, "users": []string{u.StatsName}, "inbounds": []string{u.StatsName}}}}}
	} else {
		return nil, "", fmt.Errorf("unsupported engine %q", c.Engine)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	return b, prefix, err
}
