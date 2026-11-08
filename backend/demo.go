package main

import (
	"database/sql"
	"fmt"
	"time"
)

func (a *App) seed() error {
	var seeded string
	if a.db.QueryRow("SELECT value FROM settings WHERE key='demo_seeded'").Scan(&seeded) == nil {
		return nil
	}
	return a.transaction(func(tx *sql.Tx) error {
		admin, e := a.createUser(tx, "admin@example.test", "DemoCloud2026!", "演示管理员", "admin", 0)
		if e != nil {
			return fmt.Errorf("demo seed requires a clean database: %w", e)
		}
		user, e := a.createUser(tx, "demo@example.test", "DemoCloud2026!", "演示用户", "user", integer(admin, "id"))
		if e != nil {
			return e
		}
		tx.Exec("UPDATE users SET balance=20000 WHERE id=?", integer(user, "id"))
		user["balance"] = int64(20000)
		if e = ledger(tx, user, 20000, "demo_credit", "显式演示模式：虚构初始余额"); e != nil {
			return e
		}
		plans := []M{
			{"name": "Lite 轻量套餐", "description": "适合日常浏览和轻量使用", "traffic_gb": 90, "device_limit": 3, "speed_mbps": 200, "monthly_price": 1698, "quarterly_price": 5094, "yearly_price": 20376, "onetime_price": 0, "kind": "recurring", "recommended": false, "active": true, "sort_order": 1},
			{"name": "Basic 标准套餐", "description": "适合影音娱乐和多设备使用", "traffic_gb": 200, "device_limit": 5, "speed_mbps": 300, "monthly_price": 2499, "quarterly_price": 7497, "yearly_price": 29988, "onetime_price": 0, "kind": "recurring", "recommended": true, "active": true, "sort_order": 2},
			{"name": "Pro 专业套餐", "description": "更高流量与速度上限", "traffic_gb": 400, "device_limit": 8, "speed_mbps": 500, "monthly_price": 3699, "quarterly_price": 11097, "yearly_price": 44388, "onetime_price": 0, "kind": "recurring", "recommended": false, "active": true, "sort_order": 3},
			{"name": "Ultra 旗舰套餐", "description": "适合高流量与多设备需求", "traffic_gb": 600, "device_limit": 10, "speed_mbps": 1000, "monthly_price": 4999, "quarterly_price": 14997, "yearly_price": 59988, "onetime_price": 0, "kind": "recurring", "recommended": false, "active": true, "sort_order": 4},
			{"name": "一次性 100GB", "description": "一次购买，流量用完为止", "traffic_gb": 100, "device_limit": 0, "speed_mbps": 0, "monthly_price": 0, "quarterly_price": 0, "yearly_price": 0, "onetime_price": 6900, "kind": "onetime", "recommended": false, "active": true, "sort_order": 5},
			{"name": "一次性 200GB", "description": "一次购买，流量用完为止", "traffic_gb": 200, "device_limit": 0, "speed_mbps": 0, "monthly_price": 0, "quarterly_price": 0, "yearly_price": 0, "onetime_price": 9900, "kind": "onetime", "recommended": false, "active": true, "sort_order": 6},
			{"name": "一次性 500GB", "description": "一次购买，流量用完为止", "traffic_gb": 500, "device_limit": 0, "speed_mbps": 0, "monthly_price": 0, "quarterly_price": 0, "yearly_price": 0, "onetime_price": 18900, "kind": "onetime", "recommended": false, "active": true, "sort_order": 7},
			{"name": "永久 1000GB", "description": "长期流量套餐，流量用完为止", "traffic_gb": 1000, "device_limit": 5, "speed_mbps": 0, "monthly_price": 0, "quarterly_price": 0, "yearly_price": 0, "onetime_price": 99900, "kind": "onetime", "recommended": false, "active": true, "sort_order": 8},
		}
		var first M
		for _, p := range plans {
			saved, e := put(tx, "plans", 0, p)
			if e != nil {
				return e
			}
			if first == nil {
				first = saved
			}
		}
		nodeData := []struct {
			name, region, flag, protocol string
			mult                         float64
		}{{"香港 01 · 演示", "香港", "🇭🇰", "vless", 1}, {"香港 02 · 演示", "香港", "🇭🇰", "anytls", 1}, {"日本 01 · 演示", "日本", "🇯🇵", "vless", 1}, {"新加坡 01 · 演示", "新加坡", "🇸🇬", "vless", 1}, {"美国 01 · 演示", "美国", "🇺🇸", "anytls", 1.5}, {"台湾 01 · 演示", "台湾", "🇹🇼", "vless", 1}}
		for i, n := range nodeData {
			_, e = put(tx, "nodes", 0, M{"name": n.name, "region": n.region, "flag": n.flag, "protocol": n.protocol, "status": "demo", "multiplier": n.mult, "tags": "演示节点,未连接", "host": fmt.Sprintf("node-%d.example.test", i+1), "port": 443, "tls": true, "server_name": "", "active": true, "sort_order": i + 1, "updated_at": now()})
			if e != nil {
				return e
			}
		}
		if _, e = put(tx, "subscriptions", integer(user, "id"), M{"kind": "recurring", "user_email": text(user, "email"), "plan_id": integer(first, "id"), "plan_name": text(first, "name"), "traffic_limit": 90, "traffic_used": 18.6, "device_limit": 3, "speed_mbps": 200, "status": "active", "expires_at": time.Now().UTC().AddDate(0, 1, 0).Format(time.RFC3339), "reset_at": time.Now().UTC().AddDate(0, 1, 0).Format(time.RFC3339)}); e != nil {
			return e
		}
		for i := 0; i < 14; i++ {
			up := float64((i%3)+1) / 10
			down := float64((i%4)+5) / 10
			_, e = put(tx, "traffic", integer(user, "id"), M{"user_email": text(user, "email"), "date": time.Now().UTC().AddDate(0, 0, -i).Format("2006-01-02"), "upload_gb": up, "download_gb": down, "multiplier": 1, "total_gb": up + down, "node_name": "香港 01 · 演示"})
			if e != nil {
				return e
			}
		}
		articles := []M{
			{"title": "欢迎使用 NiceCloud", "category": "站点公告", "kind": "announcement", "body": "欢迎体验 NiceCloud 演示站点。这里的账户、使用记录和节点为体验数据，演示节点无法连接。", "published": true, "updated_at": now()},
			{"title": "客户端使用指南", "category": "快速入门", "kind": "doc", "body": "在仪表盘复制订阅链接，在兼容的客户端中选择添加订阅并粘贴链接。更新订阅后即可查看节点；请将客户端保持为最新版本。", "published": true, "updated_at": now()},
			{"title": "订阅链接安全", "category": "账户安全", "kind": "doc", "body": "订阅链接相当于访问凭据，请勿公开分享。如果链接泄露，请在仪表盘重置链接，旧链接会立即失效。", "published": true, "updated_at": now()},
		}
		for _, m := range articles {
			if _, e = put(tx, "articles", 0, m); e != nil {
				return e
			}
		}
		if _, e = put(tx, "commissions", integer(user, "id"), M{"user_email": "fictional-invite@example.test", "order_no": "DEMO-INVITE-001", "amount": 1500, "status": "available", "created_at": now()}); e != nil {
			return e
		}
		if _, e = put(tx, "tickets", integer(user, "id"), M{"user_email": text(user, "email"), "subject": "如何导入订阅？（演示）", "priority": "normal", "status": "answered", "created_at": now(), "updated_at": now(), "messages": []M{{"id": 1, "role": "user", "message": "请问如何导入订阅？", "created_at": now()}, {"id": 2, "role": "admin", "message": "请在仪表盘复制订阅链接后导入客户端。此处为演示数据，节点未连接。", "created_at": now()}}}); e != nil {
			return e
		}
		if e = audit(tx, admin, "demo.seed", "显式演示数据初始化"); e != nil {
			return e
		}
		_, e = tx.Exec("INSERT INTO settings(key,value)VALUES('demo_seeded','true')")
		return e
	})
}
