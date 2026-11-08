package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) admin(r *http.Request, u M, p string) (any, error) {
	parts := strings.Split(p, "/")
	kind := parts[0]
	method := r.Method
	if p == "traffic/enforcement" && method == "GET" {
		return a.enforcement()
	}
	if len(parts) == 3 && kind == "nodes" && parts[2] == "agent-token" && method == "POST" {
		id, e := strconv.ParseInt(parts[1], 10, 64)
		if e != nil || id <= 0 {
			return nil, bad("节点 ID 无效")
		}
		return a.issueNodeToken(r, u, id)
	}
	if len(parts) == 4 && kind == "users" && parts[2] == "traffic" && parts[3] == "reset" && method == "POST" {
		id, e := strconv.ParseInt(parts[1], 10, 64)
		if e != nil || id <= 0 {
			return nil, bad("用户 ID 无效")
		}
		return a.resetTraffic(u, id)
	}
	if p == "stats" && method == "GET" {
		return a.stats()
	}
	if p == "users" && method == "GET" {
		return listUsers(a.db)
	}
	if p == "settings" {
		if method == "GET" {
			return a.settings()
		}
		if method == "PATCH" {
			m, e := body(r)
			if e != nil {
				return nil, e
			}
			e = a.transaction(func(tx *sql.Tx) error {
				for k, v := range m {
					s, ok := v.(string)
					if !ok || len(k) > 100 || len(s) > 10000 {
						return bad("设置值必须是有效字符串")
					}
					if k == "invite_rate" {
						v, e := strconv.ParseFloat(s, 64)
						if e != nil || !(v >= 0 && v <= 1) {
							return bad("邀请比例需在 0 至 1 之间")
						}
					}
					if k == "registration_enabled" && s != "true" && s != "false" {
						return bad("注册开关值无效")
					}
					if _, e := tx.Exec("INSERT INTO settings(key,value)VALUES(?,?) ON CONFLICT(key)DO UPDATE SET value=excluded.value", k, s); e != nil {
						return e
					}
				}
				return audit(tx, u, "settings.update", "site")
			})
			if e != nil {
				return nil, e
			}
			return a.settings()
		}
	}
	allowed := map[string]bool{"plans": true, "nodes": true, "articles": true, "orders": true, "subscriptions": true, "tickets": true, "withdrawals": true, "commissions": true, "traffic": true, "audit": true, "ledger": true}
	if len(parts) == 1 && method == "GET" && allowed[kind] {
		rows, e := list(a.db, kind, 0)
		if kind == "subscriptions" {
			for _, s := range rows {
				if err := prepareSubscription(a.db, integer(s, "user_id"), s); err != nil {
					return nil, err
				}
			}
		}
		if kind == "nodes" {
			for i, n := range rows {
				rows[i] = nodeSafe(n)
			}
		}
		return rows, e
	}
	if (kind == "plans" || kind == "nodes" || kind == "articles") && (method == "POST" && len(parts) == 1 || method == "PATCH" && len(parts) == 2) {
		m, e := body(r)
		if e != nil {
			return nil, e
		}
		var out M
		e = a.transaction(func(tx *sql.Tx) error {
			base := defaultsFor(kind)
			action := "create"
			if method == "PATCH" {
				id, e := strconv.ParseInt(parts[1], 10, 64)
				if e != nil {
					return bad("ID 无效")
				}
				base, e = item(tx, kind, id)
				if e != nil {
					return e
				}
				action = "update"
			}
			fields := fieldsFor(kind)
			for k, v := range m {
				if fields[k] {
					base[k] = v
				}
			}
			if e := validateEntity(kind, base); e != nil {
				return e
			}
			if kind == "nodes" || kind == "articles" {
				base["updated_at"] = now()
			}
			out, e = put(tx, kind, 0, base)
			if e != nil {
				return e
			}
			return audit(tx, u, kind+"."+action, fmt.Sprint(out["id"]))
		})
		return out, e
	}
	if len(parts) != 2 || method != "PATCH" {
		return nil, apiError{404, "管理接口不存在"}
	}
	id, e := strconv.ParseInt(parts[1], 10, 64)
	if e != nil || id <= 0 {
		return nil, bad("ID 无效")
	}
	m, e := body(r)
	if e != nil {
		return nil, e
	}
	var out M
	e = a.transaction(func(tx *sql.Tx) error {
		actor, e := getUser(tx, integer(u, "id"))
		if e != nil {
			return e
		}
		if text(actor, "role") != "admin" || text(actor, "status") != "active" {
			return apiError{403, "管理员会话已失效"}
		}
		if kind == "users" {
			target, e := getUser(tx, id)
			if e != nil {
				return e
			}
			if v, ok := m["name"]; ok {
				name, ok := v.(string)
				if !ok || strings.TrimSpace(name) == "" || len(name) > 80 {
					return bad("姓名无效")
				}
				target["name"] = name
			}
			if v, ok := m["status"]; ok {
				status, ok := v.(string)
				if !ok || status != "active" && status != "disabled" {
					return bad("用户状态无效")
				}
				if id == integer(u, "id") && status == "disabled" {
					return bad("不能停用当前管理员")
				}
				if status == "disabled" && text(target, "role") == "admin" && text(target, "status") == "active" {
					var count int
					if e := tx.QueryRow("SELECT count(*) FROM users WHERE role='admin' AND status='active'").Scan(&count); e != nil {
						return e
					}
					if count <= 1 {
						return bad("不能停用最后一名有效管理员")
					}
				}
				target["status"] = status
			}
			delta := int64(0)
			if _, ok := m["balance_delta"]; ok {
				if !validMoney(m, "balance_delta") {
					return bad("余额调整必须为整数分")
				}
				delta = integer(m, "balance_delta")
				if integer(target, "balance")+delta < 0 {
					return bad("余额不能小于零")
				}
				target["balance"] = integer(target, "balance") + delta
			}
			if _, e = tx.Exec("UPDATE users SET name=?,status=?,balance=? WHERE id=?", text(target, "name"), text(target, "status"), integer(target, "balance"), id); e != nil {
				return e
			}
			if text(target, "status") == "disabled" {
				if _, e = tx.Exec("DELETE FROM sessions WHERE user_id=?", id); e != nil {
					return e
				}
			}
			if delta != 0 {
				if e = ledger(tx, target, delta, "admin_adjustment", "管理员调整余额"); e != nil {
					return e
				}
			}
			out = target
			return audit(tx, u, "users.update", strconv.FormatInt(id, 10))
		}
		target, e := item(tx, kind, id)
		if e != nil {
			return e
		}
		switch kind {
		case "subscriptions":
			if v, ok := m["expires_at"]; ok {
				s, ok := v.(string)
				if !ok {
					return bad("到期时间无效")
				}
				if _, e = time.Parse(time.RFC3339, s); e != nil {
					return bad("到期时间须为 RFC3339")
				}
				target["expires_at"] = s
			}
			for _, key := range []string{"traffic_limit", "traffic_used"} {
				if _, ok := m[key]; ok {
					if _, e := bytesFromGB(num(m, key)); e != nil || !isNumeric(m[key]) {
						return bad("流量数值无效")
					}
					target[key] = num(m, key)
				}
			}
			if v, ok := m["status"]; ok {
				s, ok := v.(string)
				if !ok || s != "active" && s != "expired" && s != "suspended" {
					return bad("订阅状态无效")
				}
				target["status"] = s
			}
		case "withdrawals":
			status := text(m, "status")
			if status != "approved" && status != "rejected" {
				return bad("提现审核状态无效")
			}
			if text(target, "status") != "pending" {
				return bad("提现已审核，不可重复处理")
			}
			if status == "disabled" && text(target, "role") == "admin" && text(target, "status") == "active" {
				var count int
				if e := tx.QueryRow("SELECT count(*) FROM users WHERE role='admin' AND status='active'").Scan(&count); e != nil {
					return e
				}
				if count <= 1 {
					return bad("不能停用最后一名有效管理员")
				}
			}
			target["status"] = status
		default:
			return apiError{404, "管理接口不存在"}
		}
		out, e = put(tx, kind, integer(target, "user_id"), target)
		if e != nil {
			return e
		}
		return audit(tx, u, kind+".update", strconv.FormatInt(id, 10))
	})
	return out, e
}
func defaultsFor(kind string) M {
	switch kind {
	case "plans":
		return M{"name": "", "description": "", "traffic_gb": 0, "device_limit": 1, "speed_mbps": 100, "monthly_price": 0, "quarterly_price": 0, "yearly_price": 0, "onetime_price": 0, "kind": "recurring", "recommended": false, "active": true, "sort_order": 0}
	case "nodes":
		return M{"name": "", "region": "", "flag": "🌐", "protocol": "shadowsocks", "status": "not_connected", "multiplier": 1, "tags": "", "host": "", "port": 443, "tls": true, "server_name": "", "active": true, "sort_order": 0, "updated_at": now()}
	case "articles":
		return M{"title": "", "category": "帮助中心", "kind": "doc", "body": "", "published": false, "updated_at": now()}
	}
	return M{}
}
func fieldsFor(kind string) map[string]bool {
	m := map[string]bool{}
	for k := range defaultsFor(kind) {
		m[k] = true
	}
	if kind == "nodes" {
		for _, k := range []string{"secret", "uuid", "password", "cipher"} {
			m[k] = true
		}
	}
	return m
}
func validateEntity(kind string, m M) error {
	switch kind {
	case "plans":
		if strings.TrimSpace(text(m, "name")) == "" || len(text(m, "name")) > 120 {
			return bad("套餐名称无效")
		}
		if text(m, "kind") != "recurring" && text(m, "kind") != "onetime" {
			return bad("套餐类型无效")
		}
		for _, k := range []string{"monthly_price", "quarterly_price", "yearly_price", "onetime_price"} {
			if !validMoney(m, k) || integer(m, k) < 0 {
				return bad("价格须为非负整数分")
			}
		}
		if _, e := bytesFromGB(num(m, "traffic_gb")); e != nil {
			return e
		}
		for _, k := range []string{"traffic_gb", "device_limit", "speed_mbps"} {
			if num(m, k) < 0 || num(m, k) > 1e12 || !isNumeric(m[k]) {
				return bad("套餐限制无效")
			}
		}
	case "nodes":
		if !isNumeric(m["multiplier"]) || nodeMultiplier(m) < 1 {
			return bad("节点倍率至少为 0.000001")
		}
		if strings.TrimSpace(text(m, "name")) == "" || num(m, "multiplier") <= 0 || num(m, "multiplier") > 100 || integer(m, "port") < 1 || integer(m, "port") > 65535 {
			return bad("节点名称、倍率或端口无效")
		}
		if text(m, "host") != "" && (strings.ContainsAny(text(m, "host"), "\n\r\t /")) {
			return bad("节点地址无效")
		}
	case "articles":
		if strings.TrimSpace(text(m, "title")) == "" || len(text(m, "title")) > 200 || len(text(m, "body")) > 200000 {
			return bad("文章标题或正文无效")
		}
		k := text(m, "kind")
		if k != "doc" && k != "announcement" && k != "notice" {
			return bad("文章类型无效")
		}
	}
	return nil
}
func (a *App) stats() (any, error) {
	users, e := listUsers(a.db)
	if e != nil {
		return nil, e
	}
	orders, e := list(a.db, "orders", 0)
	if e != nil {
		return nil, e
	}
	subs, e := list(a.db, "subscriptions", 0)
	if e != nil {
		return nil, e
	}
	nodes, e := list(a.db, "nodes", 0)
	if e != nil {
		return nil, e
	}
	tickets, e := list(a.db, "tickets", 0)
	if e != nil {
		return nil, e
	}
	withdrawals, e := list(a.db, "withdrawals", 0)
	if e != nil {
		return nil, e
	}
	traffic, e := list(a.db, "traffic", 0)
	if e != nil {
		return nil, e
	}
	var revenue, active, open, pending int64
	var total float64
	for _, m := range orders {
		if text(m, "status") == "paid" {
			revenue += integer(m, "amount")
		}
	}
	for _, m := range subs {
		exp, _ := time.Parse(time.RFC3339, text(m, "expires_at"))
		if text(m, "status") == "active" && exp.After(time.Now()) {
			active++
		}
	}
	for _, m := range tickets {
		if text(m, "status") != "closed" {
			open++
		}
	}
	for _, m := range withdrawals {
		if text(m, "status") == "pending" {
			pending++
		}
	}
	for _, m := range traffic {
		total += num(m, "total_gb")
	}
	return M{"users": len(users), "orders": len(orders), "revenue": revenue, "active_subscriptions": active, "nodes": len(nodes), "open_tickets": open, "pending_withdrawals": pending, "traffic_gb": total}, nil
}
