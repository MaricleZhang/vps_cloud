package main

import (
	"database/sql"
	"time"
)

// lifecycle enforces local database state only. It cannot revoke a connected client
// or meter a real node until a node-side authenticated integration is implemented.
func (a *App) lifecycle(at time.Time) error {
	return a.transaction(func(tx *sql.Tx) error {
		orders, e := list(tx, "orders", 0)
		if e != nil {
			return e
		}
		for _, o := range orders {
			if text(o, "status") != "pending" {
				continue
			}
			created, e := time.Parse(time.RFC3339, text(o, "created_at"))
			if e == nil && at.Sub(created) >= 30*time.Minute {
				o["status"] = "cancelled"
				o["cancel_reason"] = "timeout"
				if _, e = put(tx, "orders", integer(o, "user_id"), o); e != nil {
					return e
				}
			}
		}
		subs, e := list(tx, "subscriptions", 0)
		if e != nil {
			return e
		}
		for _, s := range subs {
			if text(s, "status") != "active" {
				continue
			}
			changed := false
			expires, e := time.Parse(time.RFC3339, text(s, "expires_at"))
			if e == nil && !expires.After(at) {
				s["status"] = "expired"
				changed = true
			} else if text(s, "kind") != "onetime" {
				reset, e := time.Parse(time.RFC3339, text(s, "reset_at"))
				if e == nil && !reset.After(at) {
					for !reset.After(at) {
						reset = reset.AddDate(0, 1, 0)
					}
					s["traffic_used"] = 0
					s["reset_at"] = reset.UTC().Format(time.RFC3339)
					changed = true
				}
			}
			if changed {
				if _, e = put(tx, "subscriptions", integer(s, "user_id"), s); e != nil {
					return e
				}
			}
		}
		_, e = tx.Exec("DELETE FROM sessions WHERE expires_at<=?", at.UTC().Format(time.RFC3339))
		return e
	})
}
