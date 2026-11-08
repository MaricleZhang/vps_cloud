package main

import (
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const gib int64 = 1 << 30
const policyLease = 15

func (a *App) initTraffic() error {
	_, e := a.db.Exec(`CREATE TABLE IF NOT EXISTS traffic_cycles(cycle_id TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id),created_at TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS node_tokens(node_id INTEGER PRIMARY KEY,token_hash TEXT NOT NULL UNIQUE,last_seen TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS node_bindings(node_id INTEGER NOT NULL,user_id INTEGER NOT NULL REFERENCES users(id),port INTEGER NOT NULL,uuid TEXT NOT NULL,password TEXT NOT NULL,PRIMARY KEY(node_id,user_id),UNIQUE(node_id,port));
 CREATE TABLE IF NOT EXISTS node_cycles(node_id INTEGER NOT NULL,user_id INTEGER NOT NULL,cycle_id TEXT NOT NULL,PRIMARY KEY(node_id,user_id,cycle_id));
 CREATE TABLE IF NOT EXISTS traffic_samples(node_id INTEGER NOT NULL,user_id INTEGER NOT NULL,cycle_id TEXT NOT NULL,session_id TEXT NOT NULL,upload_bytes INTEGER NOT NULL,download_bytes INTEGER NOT NULL,PRIMARY KEY(node_id,user_id,cycle_id,session_id));
 CREATE TABLE IF NOT EXISTS traffic_remainders(node_id INTEGER NOT NULL,user_id INTEGER NOT NULL,cycle_id TEXT NOT NULL,remainder INTEGER NOT NULL,PRIMARY KEY(node_id,user_id,cycle_id));
 CREATE TABLE IF NOT EXISTS node_acks(node_id INTEGER NOT NULL,user_id INTEGER NOT NULL,policy_version TEXT NOT NULL,enabled INTEGER NOT NULL,error TEXT NOT NULL,ack_at TEXT NOT NULL,PRIMARY KEY(node_id,user_id));`)
	return e
}

func bytesFromGB(v float64) (int64, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v >= float64(math.MaxInt64)/float64(gib) {
		return 0, bad("流量超出可计量范围")
	}
	return int64(math.Round(v * float64(gib))), nil
}
func syncGB(s M) {
	s["traffic_limit"] = float64(integer(s, "traffic_limit_bytes")) / float64(gib)
	s["traffic_used"] = float64(integer(s, "traffic_used_bytes")) / float64(gib)
}
func newTrafficCycle(q queryer, uid int64, s M) error {
	s["cycle_id"] = randomToken(24)
	s["traffic_used_bytes"] = int64(0)
	s["traffic_used"] = float64(0)
	s["upload_bytes"] = int64(0)
	s["download_bytes"] = int64(0)
	_, e := q.Exec("INSERT INTO traffic_cycles(cycle_id,user_id,created_at)VALUES(?,?,?)", text(s, "cycle_id"), uid, now())
	return e
}
func prepareSubscription(q queryer, uid int64, s M) error {
	for _, pair := range [][2]string{{"traffic_limit", "traffic_limit_bytes"}, {"traffic_used", "traffic_used_bytes"}} {
		v, e := bytesFromGB(num(s, pair[0]))
		if e != nil {
			return e
		}
		// Legacy administrative writers change GB fields; metering writes both together.
		if _, ok := s[pair[1]]; !ok || math.Abs(num(s, pair[0])-float64(integer(s, pair[1]))/float64(gib)) > 1e-9 {
			s[pair[1]] = v
		}
	}
	if text(s, "cycle_id") == "" {
		s["cycle_id"] = randomToken(24)
		if _, e := q.Exec("INSERT INTO traffic_cycles(cycle_id,user_id,created_at)VALUES(?,?,?)", text(s, "cycle_id"), uid, now()); e != nil {
			return e
		}
	}
	if _, ok := s["upload_bytes"]; !ok {
		s["upload_bytes"] = int64(0)
	}
	if _, ok := s["download_bytes"]; !ok {
		s["download_bytes"] = int64(0)
	}
	u, e := getUser(q, uid)
	if e != nil {
		return e
	}
	enabled, reason := serviceState(u, s, time.Now().UTC())
	s["service_enabled"] = enabled
	s["service_reason"] = reason
	syncGB(s)
	return nil
}
func serviceState(u, s M, at time.Time) (bool, string) {
	if text(u, "status") != "active" {
		return false, "account_disabled"
	}
	if s == nil {
		return false, "no_subscription"
	}
	if text(s, "status") == "suspended" {
		return false, "suspended"
	}
	ex, e := time.Parse(time.RFC3339, text(s, "expires_at"))
	if e != nil || !ex.After(at) || text(s, "status") == "expired" {
		return false, "expired"
	}
	if text(s, "status") != "active" {
		return false, "suspended"
	}
	if integer(s, "traffic_used_bytes") >= integer(s, "traffic_limit_bytes") {
		return false, "quota_exceeded"
	}
	return true, "active"
}
func strictInt(m M, key string) (int64, error) {
	switch v := m[key].(type) {
	case json.Number:
		n, e := v.Int64()
		if e == nil && n >= 0 {
			return n, nil
		}
	case int64:
		if v >= 0 {
			return v, nil
		}
	case int:
		if v >= 0 {
			return int64(v), nil
		}
	}
	return 0, bad(key + " 必须是非负整数且不得溢出")
}
func uuidToken() string {
	b := make([]byte, 16)
	raw := randomToken(18)
	h := digest(raw)
	decoded, _ := hex.DecodeString(h[:32])
	copy(b, decoded)
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func nodeMultiplier(n M) int64 { return int64(math.Round(num(n, "multiplier") * 1e6)) }
func nodeSafe(n M) M {
	out := M{}
	for k, v := range n {
		if k != "agent_token" && k != "token_hash" && k != "secret" {
			out[k] = v
		}
	}
	return out
}
func (a *App) issueNodeToken(r *http.Request, actor M, id int64) (any, error) {
	m, e := body(r)
	if e != nil {
		return nil, e
	}
	start, e := strictInt(m, "port_start")
	if e != nil {
		return nil, e
	}
	end, e := strictInt(m, "port_end")
	if e != nil {
		return nil, e
	}
	engine := text(m, "engine")
	if (engine != "xray" && engine != "sing-box") || start < 1024 || end > 65535 || end < start {
		return nil, bad("内核或专属端口范围无效（1024–65535）")
	}
	token := randomToken(32)
	e = a.transaction(func(tx *sql.Tx) error {
		n, e := item(tx, "nodes", id)
		if e != nil {
			return e
		}
		protocol := text(n, "protocol")
		supported := protocol == "vless" || protocol == "trojan" || protocol == "ss" || protocol == "shadowsocks" || engine == "xray" && protocol == "vmess" || engine == "sing-box" && protocol == "anytls"
		if !supported {
			return bad("所选内核不支持该节点协议")
		}
		var outside int
		if e = tx.QueryRow("SELECT count(*) FROM node_bindings WHERE node_id=? AND (port<? OR port>?)", id, start, end).Scan(&outside); e != nil {
			return e
		}
		if outside > 0 {
			return bad("新端口范围必须保留已经分配的用户端口")
		}
		n["managed"] = true
		n["agent_engine"] = engine
		n["port_start"] = start
		n["port_end"] = end
		n["status"] = "not_connected"
		n["agent_last_seen"] = ""
		if _, e = put(tx, "nodes", 0, n); e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO node_tokens(node_id,token_hash,last_seen)VALUES(?,?,'') ON CONFLICT(node_id)DO UPDATE SET token_hash=excluded.token_hash,last_seen=''", id, digest(token)); e != nil {
			return e
		}
		if _, e = tx.Exec("DELETE FROM node_acks WHERE node_id=?", id); e != nil {
			return e
		}
		return audit(tx, actor, "nodes.agent_token", strconv.FormatInt(id, 10))
	})
	return M{"token": token, "node_id": id}, e
}
func (a *App) authenticatedNode(r *http.Request) (int64, error) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") || len(h) < 40 {
		return 0, apiError{401, "节点凭据无效"}
	}
	hash := digest(strings.TrimPrefix(h, "Bearer "))
	var id int64
	var stored string
	e := a.db.QueryRow("SELECT node_id,token_hash FROM node_tokens WHERE token_hash=?", hash).Scan(&id, &stored)
	if e != nil || subtle.ConstantTimeCompare([]byte(stored), []byte(hash)) != 1 {
		return 0, apiError{401, "节点凭据无效"}
	}
	return id, nil
}
func policyVersion(n, user M) string {
	copy := M{"node": n, "user": M{}}
	state := copy["user"].(M)
	for _, k := range []string{"user_id", "port", "uuid", "password", "stats_name", "cycle_id", "enabled", "reason"} {
		state[k] = user[k]
	}
	b, _ := json.Marshal(copy)
	return digest(string(b))
}
func (a *App) policy(q queryer, id int64) (M, error) {
	n, e := item(q, "nodes", id)
	if e != nil {
		return nil, e
	}
	node := M{}
	for _, k := range []string{"id", "name", "protocol", "tls", "server_name", "port_start", "port_end", "agent_engine", "cipher", "host", "active"} {
		node[k] = n[k]
	}
	node["multiplier_ppm"] = nodeMultiplier(n)
	users, e := listUsers(q)
	if e != nil {
		return nil, e
	}
	out := []M{}
	// Bindings are stable. Keep disabled users in policy to revoke existing processes.
	for _, u := range users {
		uid := integer(u, "id")
		s, e := a.subscription(q, uid)
		if e != nil {
			return nil, e
		}
		var port int64
		var uuid, password string
		e = q.QueryRow("SELECT port,uuid,password FROM node_bindings WHERE node_id=? AND user_id=?", id, uid).Scan(&port, &uuid, &password)
		if e == sql.ErrNoRows {
			if s == nil {
				continue
			}
			ports := map[int64]bool{}
			rows, e := q.Query("SELECT port FROM node_bindings WHERE node_id=?", id)
			if e != nil {
				return nil, e
			}
			for rows.Next() {
				var p int64
				if e = rows.Scan(&p); e != nil {
					rows.Close()
					return nil, e
				}
				ports[p] = true
			}
			if e = rows.Err(); e != nil {
				rows.Close()
				return nil, e
			}
			rows.Close()
			for p := integer(n, "port_start"); p <= integer(n, "port_end"); p++ {
				if !ports[p] {
					port = p
					break
				}
			}
			if port != 0 {
				uuid = uuidToken()
				password = randomToken(24)
				if _, e = q.Exec("INSERT INTO node_bindings(node_id,user_id,port,uuid,password)VALUES(?,?,?,?,?)", id, uid, port, uuid, password); e != nil {
					return nil, e
				}
			}
		} else if e != nil {
			return nil, e
		}
		enabled, reason := serviceState(u, s, time.Now().UTC())
		if port == 0 {
			enabled = false
			reason = "port_exhausted"
		}
		if !boolean(n, "active") {
			enabled = false
			reason = "node_disabled"
		}
		if text(n, "status") == "demo" {
			enabled = false
			reason = "demo"
		}
		user := M{"user_id": uid, "port": port, "uuid": uuid, "password": password, "stats_name": fmt.Sprintf("user-%d", uid), "enabled": enabled, "reason": reason}
		for _, k := range []string{"cycle_id", "traffic_limit_bytes", "traffic_used_bytes"} {
			user[k] = s[k]
		}
		remaining := integer(s, "traffic_limit_bytes") - integer(s, "traffic_used_bytes")
		if remaining < 0 {
			remaining = 0
		}
		user["remaining_bytes"] = remaining
		if s != nil {
			if _, e = q.Exec("INSERT OR IGNORE INTO node_cycles(node_id,user_id,cycle_id)VALUES(?,?,?)", id, uid, text(s, "cycle_id")); e != nil {
				return nil, e
			}
		}
		user["policy_version"] = policyVersion(node, user)
		out = append(out, user)
	}
	return M{"node": node, "lease_seconds": policyLease, "users": out}, nil
}
func touchNode(q queryer, id int64) error {
	stamp := now()
	if _, e := q.Exec("UPDATE node_tokens SET last_seen=? WHERE node_id=?", stamp, id); e != nil {
		return e
	}
	n, e := item(q, "nodes", id)
	if e != nil {
		return e
	}
	n["agent_last_seen"] = stamp
	n["status"] = "connected"
	_, e = put(q, "nodes", 0, n)
	return e
}
func (a *App) nodeRoute(r *http.Request, path string) (any, error) {
	id, e := a.authenticatedNode(r)
	if e != nil {
		return nil, e
	}
	var out M
	e = a.transaction(func(tx *sql.Tx) error {
		if path == "traffic" && r.Method == "POST" {
			m, e := body(r)
			if e != nil {
				return e
			}
			accepted, e := a.meter(tx, id, m)
			if e != nil {
				return e
			}
			if e = touchNode(tx, id); e != nil {
				return e
			}
			p, e := a.policy(tx, id)
			out = M{"accepted": accepted, "policy": p}
			return e
		}
		if path == "policy" && r.Method == "GET" {
			if e := touchNode(tx, id); e != nil {
				return e
			}
			out, e = a.policy(tx, id)
			return e
		}
		if path == "ack" && r.Method == "POST" {
			m, e := body(r)
			if e != nil {
				return e
			}
			if e = touchNode(tx, id); e != nil {
				return e
			}
			return a.ack(tx, id, m)
		}
		return apiError{404, "节点接口不存在"}
	})
	return out, e
}
func (a *App) meter(q queryer, nodeID int64, m M) (int, error) {
	samples, ok := m["samples"].([]any)
	if !ok || len(samples) == 0 || len(samples) > 1000 {
		return 0, bad("samples 数量须为 1–1000")
	}
	n, e := item(q, "nodes", nodeID)
	if e != nil {
		return 0, e
	}
	ppm := nodeMultiplier(n)
	if ppm < 1 || ppm > 100000000 {
		return 0, bad("节点倍率无效")
	}
	accepted := 0
	for _, v := range samples {
		raw, ok := v.(map[string]any)
		if !ok {
			return 0, bad("sample 无效")
		}
		smp := M(raw)
		uid, e := strictInt(smp, "user_id")
		if e != nil || uid == 0 {
			return 0, bad("用户 ID 无效")
		}
		up, e := strictInt(smp, "upload_bytes")
		if e != nil {
			return 0, e
		}
		down, e := strictInt(smp, "download_bytes")
		if e != nil {
			return 0, e
		}
		cycle, session := text(smp, "cycle_id"), text(smp, "session_id")
		if cycle == "" || len(cycle) > 128 || session == "" || len(session) > 128 || strings.ContainsAny(session, "\x00\n\r") {
			return 0, bad("周期或 session 无效")
		}
		var exists int
		if e = q.QueryRow("SELECT count(*) FROM node_cycles nc JOIN traffic_cycles c ON nc.cycle_id=c.cycle_id AND nc.user_id=c.user_id WHERE nc.node_id=? AND nc.user_id=? AND nc.cycle_id=?", nodeID, uid, cycle).Scan(&exists); e != nil {
			return 0, e
		}
		if exists != 1 {
			return 0, bad("未知用户或未授权周期")
		}
		var oldUp, oldDown int64
		e = q.QueryRow("SELECT upload_bytes,download_bytes FROM traffic_samples WHERE node_id=? AND user_id=? AND cycle_id=? AND session_id=?", nodeID, uid, cycle, session).Scan(&oldUp, &oldDown)
		if e != nil && e != sql.ErrNoRows {
			return 0, e
		}
		if up < oldUp {
			up = oldUp
		}
		if down < oldDown {
			down = oldDown
		}
		du, dd := up-oldUp, down-oldDown
		if du == 0 && dd == 0 {
			accepted++
			continue
		}
		if du > math.MaxInt64-dd {
			return 0, bad("流量计数溢出")
		}
		var remainder int64
		e = q.QueryRow("SELECT remainder FROM traffic_remainders WHERE node_id=? AND user_id=? AND cycle_id=?", nodeID, uid, cycle).Scan(&remainder)
		if e != nil && e != sql.ErrNoRows {
			return 0, e
		}
		weighted := new(big.Int).Mul(big.NewInt(du+dd), big.NewInt(ppm))
		weighted.Add(weighted, big.NewInt(remainder))
		bill, rem := new(big.Int), new(big.Int)
		bill.QuoRem(weighted, big.NewInt(1000000), rem)
		if !bill.IsInt64() {
			return 0, bad("计费用量溢出")
		}
		used := bill.Int64()
		sub, e := a.subscription(q, uid)
		if e != nil {
			return 0, e
		}
		if sub == nil {
			return 0, bad("用户订阅不存在")
		}
		current := text(sub, "cycle_id") == cycle
		if current {
			if integer(sub, "traffic_used_bytes") > math.MaxInt64-used || integer(sub, "upload_bytes") > math.MaxInt64-du || integer(sub, "download_bytes") > math.MaxInt64-dd {
				return 0, bad("累计用量溢出")
			}
			sub["traffic_used_bytes"] = integer(sub, "traffic_used_bytes") + used
			sub["upload_bytes"] = integer(sub, "upload_bytes") + du
			sub["download_bytes"] = integer(sub, "download_bytes") + dd
			syncGB(sub)
			if _, e = put(q, "subscriptions", uid, sub); e != nil {
				return 0, e
			}
		}
		if _, e = q.Exec("INSERT INTO traffic_samples(node_id,user_id,cycle_id,session_id,upload_bytes,download_bytes)VALUES(?,?,?,?,?,?) ON CONFLICT(node_id,user_id,cycle_id,session_id)DO UPDATE SET upload_bytes=excluded.upload_bytes,download_bytes=excluded.download_bytes", nodeID, uid, cycle, session, up, down); e != nil {
			return 0, e
		}
		if _, e = q.Exec("INSERT INTO traffic_remainders(node_id,user_id,cycle_id,remainder)VALUES(?,?,?,?) ON CONFLICT(node_id,user_id,cycle_id)DO UPDATE SET remainder=excluded.remainder", nodeID, uid, cycle, rem.Int64()); e != nil {
			return 0, e
		}
		u, e := getUser(q, uid)
		if e != nil {
			return 0, e
		}
		_, e = put(q, "traffic", uid, M{"source": "metered", "date": now(), "user_email": text(u, "email"), "node_id": nodeID, "node_name": text(n, "name"), "cycle_id": cycle, "session_id": session, "upload_bytes": du, "download_bytes": dd, "billed_bytes": used, "total_bytes": used, "upload_gb": float64(du) / float64(gib), "download_gb": float64(dd) / float64(gib), "total_gb": float64(used) / float64(gib), "multiplier": float64(ppm) / 1e6, "multiplier_ppm": ppm, "current_cycle": current})
		if e != nil {
			return 0, e
		}
		accepted++
	}
	return accepted, nil
}
func (a *App) ack(q queryer, nodeID int64, m M) error {
	users, ok := m["users"].([]any)
	if !ok || len(users) > 10000 {
		return bad("ACK users 无效")
	}
	p, e := a.policy(q, nodeID)
	if e != nil {
		return e
	}
	byID := map[int64]M{}
	for _, u := range p["users"].([]M) {
		byID[integer(u, "user_id")] = u
	}
	for _, v := range users {
		raw, ok := v.(map[string]any)
		if !ok {
			return bad("ACK 无效")
		}
		ack := M(raw)
		uid, e := strictInt(ack, "user_id")
		if e != nil {
			return e
		}
		state, ok := ack["enabled"].(bool)
		if !ok {
			return bad("ACK enabled 无效")
		}
		user := byID[uid]
		if user == nil {
			return bad("ACK 用户不存在")
		}
		if text(ack, "policy_version") != text(user, "policy_version") {
			return apiError{409, "策略已更新，请重新获取"}
		}
		errText := text(ack, "error")
		if len(errText) > 2000 {
			return bad("ACK 错误过长")
		}
		if errText == "" && state != boolean(user, "enabled") {
			return bad("ACK 执行状态不匹配")
		}
		if _, e = q.Exec("INSERT INTO node_acks(node_id,user_id,policy_version,enabled,error,ack_at)VALUES(?,?,?,?,?,?) ON CONFLICT(node_id,user_id)DO UPDATE SET policy_version=excluded.policy_version,enabled=excluded.enabled,error=excluded.error,ack_at=excluded.ack_at", nodeID, uid, text(user, "policy_version"), state, errText, now()); e != nil {
			return e
		}
	}
	return nil
}
func fresh(stamp string) bool {
	t, e := time.Parse(time.RFC3339, stamp)
	return e == nil && time.Since(t) <= policyLease*time.Second && time.Since(t) >= -time.Second
}
func (a *App) enforcement() (any, error) {
	out := []M{}
	e := a.transaction(func(tx *sql.Tx) error {
		nodes, e := list(tx, "nodes", 0)
		if e != nil {
			return e
		}
		for _, n := range nodes {
			if !boolean(n, "managed") {
				continue
			}
			p, e := a.policy(tx, integer(n, "id"))
			if e != nil {
				return e
			}
			var last string
			if e = tx.QueryRow("SELECT last_seen FROM node_tokens WHERE node_id=?", integer(n, "id")).Scan(&last); e != nil && e != sql.ErrNoRows {
				return e
			}
			for _, user := range p["users"].([]M) {
				uid := integer(user, "user_id")
				u, e := getUser(tx, uid)
				if e != nil {
					return e
				}
				var version, errText, ackAt string
				var applied bool
				e = tx.QueryRow("SELECT policy_version,enabled,error,ack_at FROM node_acks WHERE node_id=? AND user_id=?", integer(n, "id"), uid).Scan(&version, &applied, &errText, &ackAt)
				if e != nil && e != sql.ErrNoRows {
					return e
				}
				var confirmed any
				if fresh(last) && fresh(ackAt) && version == text(user, "policy_version") && errText == "" && e == nil {
					confirmed = applied
				}
				out = append(out, M{"node_id": integer(n, "id"), "node_name": text(n, "name"), "user_id": uid, "user_email": text(u, "email"), "desired_enabled": user["enabled"], "applied_enabled": confirmed, "reason": user["reason"], "policy_version": user["policy_version"], "ack_at": ackAt, "last_seen": last, "error": errText, "lease_seconds": policyLease})
			}
		}
		return nil
	})
	return out, e
}
func (a *App) resetTraffic(actor M, uid int64) (any, error) {
	var out M
	e := a.transaction(func(tx *sql.Tx) error {
		s, e := a.subscription(tx, uid)
		if e != nil {
			return e
		}
		if s == nil {
			return bad("用户没有订阅")
		}
		if e = newTrafficCycle(tx, uid, s); e != nil {
			return e
		}
		if text(s, "kind") != "onetime" {
			s["reset_at"] = time.Now().UTC().AddDate(0, 1, 0).Format(time.RFC3339)
		}
		out, e = put(tx, "subscriptions", uid, s)
		if e != nil {
			return e
		}
		return audit(tx, actor, "traffic.reset", strconv.FormatInt(uid, 10))
	})
	return M{"cycle_id": text(out, "cycle_id"), "subscription": out}, e
}

func isNumeric(v any) bool {
	switch v.(type) {
	case int, int64, float64, json.Number:
		return true
	}
	return false
}

// managedSubscriptionNode only publishes credentials after current successful ACK.
func (a *App) managedSubscriptionNode(nodeID, uid int64) (M, bool, error) {
	var out M
	ready := false
	e := a.transaction(func(tx *sql.Tx) error {
		p, e := a.policy(tx, nodeID)
		if e != nil {
			return e
		}
		for _, u := range p["users"].([]M) {
			if integer(u, "user_id") != uid {
				continue
			}
			if !boolean(u, "enabled") {
				return nil
			}
			var version, last, errText, ackAt string
			var enabled bool
			e = tx.QueryRow("SELECT a.policy_version,a.enabled,a.error,t.last_seen,a.ack_at FROM node_acks a JOIN node_tokens t ON a.node_id=t.node_id WHERE a.node_id=? AND a.user_id=?", nodeID, uid).Scan(&version, &enabled, &errText, &last, &ackAt)
			if e == sql.ErrNoRows {
				return nil
			}
			if e != nil {
				return e
			}
			if !enabled || errText != "" || version != text(u, "policy_version") || !fresh(last) || !fresh(ackAt) {
				return nil
			}
			out, e = item(tx, "nodes", nodeID)
			if e != nil {
				return e
			}
			for _, k := range []string{"uuid", "password", "port"} {
				out[k] = u[k]
			}
			ready = true
			return nil
		}
		return nil
	})
	return out, ready, e
}
