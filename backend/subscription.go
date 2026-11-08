package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) subscriptionOutput(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if e := a.lifecycle(time.Now().UTC()); e != nil {
		http.Error(w, "server error", 500)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method != "GET" && r.Method != "HEAD" {
		http.Error(w, "method not allowed", 405)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/subscribe/")
	if len(token) < 32 || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}
	var uid int64
	var status string
	if e := a.db.QueryRow("SELECT id,status FROM users WHERE subscription_token=?", token).Scan(&uid, &status); e != nil || status != "active" {
		http.NotFound(w, r)
		return
	}
	s, e := a.subscription(a.db, uid)
	if e != nil || s == nil || text(s, "status") != "active" || integer(s, "traffic_used_bytes") >= integer(s, "traffic_limit_bytes") {
		http.Error(w, "subscription inactive or quota exhausted", 403)
		return
	}
	nodes, e := list(a.db, "nodes", 0)
	if e != nil {
		http.Error(w, "server error", 500)
		return
	}
	proxies := []M{}
	for _, n := range nodes {
		if !boolean(n, "active") || text(n, "host") == "" || text(n, "status") == "demo" {
			continue
		}
		if boolean(n, "managed") {
			effective, ok, e := a.managedSubscriptionNode(integer(n, "id"), uid)
			if e != nil {
				http.Error(w, "server error", 500)
				return
			}
			if !ok {
				continue
			}
			n = effective
		}
		p := M{"name": text(n, "name"), "server": text(n, "host"), "port": integer(n, "port")}
		switch text(n, "protocol") {
		case "shadowsocks", "ss":
			if text(n, "password") == "" {
				continue
			}
			p["type"] = "ss"
			p["cipher"] = text(n, "cipher")
			if p["cipher"] == "" {
				p["cipher"] = "aes-128-gcm"
			}
			p["password"] = text(n, "password")
		case "trojan":
			if text(n, "password") == "" {
				continue
			}
			p["type"] = "trojan"
			p["password"] = text(n, "password")
			p["sni"] = text(n, "server_name")
		case "vless":
			if text(n, "uuid") == "" {
				continue
			}
			p["type"] = "vless"
			p["uuid"] = text(n, "uuid")
			p["tls"] = boolean(n, "tls")
			p["servername"] = text(n, "server_name")
			p["network"] = "tcp"
		case "anytls":
			if text(n, "password") == "" {
				continue
			}
			p["type"] = "anytls"
			p["password"] = text(n, "password")
			p["sni"] = text(n, "server_name")
			p["client-fingerprint"] = "chrome"
		case "vmess":
			if text(n, "uuid") == "" {
				continue
			}
			p["type"] = "vmess"
			p["uuid"] = text(n, "uuid")
			p["alterId"] = 0
			p["cipher"] = "auto"
			p["tls"] = boolean(n, "tls")
			p["servername"] = text(n, "server_name")
		default:
			continue
		}
		proxies = append(proxies, p)
	}
	expires, _ := time.Parse(time.RFC3339, text(s, "expires_at"))
	w.Header().Set("Subscription-Userinfo", fmt.Sprintf("upload=0; download=%d; total=%d; expire=%d", integer(s, "traffic_used_bytes"), integer(s, "traffic_limit_bytes"), expires.Unix()))
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(M{"demo": a.demo, "integration": "managed_agent_ack_required", "subscription": M{"traffic_limit": num(s, "traffic_limit"), "traffic_used": num(s, "traffic_used"), "status": text(s, "status"), "expires_at": text(s, "expires_at")}, "proxies": proxies})
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline; filename=subscription.yaml")
	fmt.Fprintln(w, "# NiceCloud subscription. Managed nodes require current successful agent acknowledgement.")
	if len(proxies) == 0 {
		fmt.Fprintln(w, "# No real configured nodes. Demo nodes cannot connect.\nproxies: []\nproxy-groups: []\nrules: []")
		return
	}
	fmt.Fprintln(w, "proxies:")
	names := []string{}
	for _, p := range proxies {
		b, _ := json.Marshal(p)
		fmt.Fprintln(w, "  - "+string(b))
		names = append(names, text(p, "name"))
	}
	names = append(names, "DIRECT")
	group, _ := json.Marshal(M{"name": "NiceCloud", "type": "select", "proxies": names})
	fmt.Fprintln(w, "proxy-groups:\n  - "+string(group))
	fmt.Fprintln(w, "rules:\n  - "+strconv.Quote("MATCH,NiceCloud"))
}
