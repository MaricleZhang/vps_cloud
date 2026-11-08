package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func nodeRequest(t *testing.T, a *App, token, method, path string, payload any, code int) M {
	t.Helper()
	var b bytes.Buffer
	if payload != nil {
		json.NewEncoder(&b).Encode(payload)
	}
	r := httptest.NewRequest(method, "/api/node/"+path, &b)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != code {
		t.Fatalf("node %s %s: %d want %d: %s", method, path, w.Code, code, w.Body.String())
	}
	result := M{}
	d := json.NewDecoder(w.Body)
	d.UseNumber()
	d.Decode(&result)
	data, _ := result["data"].(map[string]any)
	return M(data)
}
func nodeFixture(t *testing.T) (*App, *http.Cookie, int64, int64, string, M) {
	t.Helper()
	a := fixture(t)
	ad := login(t, a, "admin@example.test")
	var uid int64
	a.db.QueryRow("SELECT id FROM users WHERE email='demo@example.test'").Scan(&uid)
	n := mustRequest(t, a, "POST", "/api/admin/nodes", M{"name": "Managed", "host": "node.example.test", "protocol": "vless", "tls": false, "multiplier": 1.5}, ad, 200)
	id := integer(n, "id")
	tok := mustRequest(t, a, "POST", fmt.Sprintf("/api/admin/nodes/%d/agent-token", id), M{"engine": "xray", "port_start": 20000, "port_end": 20010}, ad, 200)
	mustRequest(t, a, "POST", fmt.Sprintf("/api/admin/users/%d/traffic/reset", uid), M{}, ad, 200)
	s, _ := a.subscription(a.db, uid)
	s["traffic_limit"] = float64(100) / float64(gib)
	put(a.db, "subscriptions", uid, s)
	p := nodeRequest(t, a, text(tok, "token"), "GET", "policy", nil, 200)
	return a, ad, uid, id, text(tok, "token"), policyUser(t, p, uid)
}
func policyUser(t *testing.T, p M, uid int64) M {
	t.Helper()
	for _, v := range p["users"].([]any) {
		u := M(v.(map[string]any))
		if integer(u, "user_id") == uid {
			return u
		}
	}
	t.Fatalf("missing user %d in %v", uid, p)
	return nil
}
func sample(u M, session string, up, down any) M {
	return M{"user_id": integer(u, "user_id"), "cycle_id": text(u, "cycle_id"), "session_id": session, "upload_bytes": up, "download_bytes": down}
}
func samples(v ...M) M { return M{"samples": v} }
func TestTrafficCumulativeRemainderQuotaAndACK(t *testing.T) {
	a, ad, uid, nodeID, token, u := nodeFixture(t)
	// Fractional multiplier bytes carry across separate process sessions.
	for _, session := range []string{"one", "two"} {
		nodeRequest(t, a, token, "POST", "traffic", samples(sample(u, session, 1, 0)), 200)
	}
	s, _ := a.subscription(a.db, uid)
	if integer(s, "traffic_used_bytes") != 3 {
		t.Fatalf("fractional bytes lost: %v", s)
	}
	// Retry and older cumulative observations do not add usage.
	nodeRequest(t, a, token, "POST", "traffic", samples(sample(u, "one", 1, 0), sample(u, "one", 0, 0)), 200)
	s, _ = a.subscription(a.db, uid)
	if integer(s, "traffic_used_bytes") != 3 {
		t.Fatal("duplicate billed")
	}
	nodeRequest(t, a, token, "POST", "ack", M{"users": []M{{"user_id": uid, "policy_version": text(u, "policy_version"), "enabled": true, "error": ""}}}, 200)
	effective, ready, e := a.managedSubscriptionNode(nodeID, uid)
	if e != nil || !ready || integer(effective, "port") != 20000 || text(effective, "uuid") == "" {
		t.Fatalf("managed credentials not published %v %v %v", effective, ready, e)
	}
	// The shared quota state affects every managed node policy immediately.
	result := nodeRequest(t, a, token, "POST", "traffic", samples(sample(u, "one", 70, 0)), 200)
	p := M(result["policy"].(map[string]any))
	stopped := policyUser(t, p, uid)
	if boolean(stopped, "enabled") || text(stopped, "reason") != "quota_exceeded" {
		t.Fatalf("quota not enforced: %v", stopped)
	}
	nodeRequest(t, a, token, "POST", "ack", M{"users": []M{{"user_id": uid, "policy_version": text(u, "policy_version"), "enabled": true, "error": ""}}}, 409)
	enforce, e := a.enforcement()
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range enforce.([]M) {
		if integer(row, "node_id") == nodeID && row["applied_enabled"] != nil {
			t.Fatal("old enabled ack used after stop")
		}
	}
	nodeRequest(t, a, token, "POST", "ack", M{"users": []M{{"user_id": uid, "policy_version": text(stopped, "policy_version"), "enabled": false, "error": ""}}}, 200)
	enforce, _ = a.enforcement()
	if enforce.([]M)[0]["applied_enabled"] != false {
		t.Fatalf("stop ACK missing: %v", enforce)
	}
	// Administrator can inspect execution but cookie never authenticates a node.
	mustRequest(t, a, "GET", "/api/admin/traffic/enforcement", nil, ad, 200)
	mustRequest(t, a, "GET", "/api/node/policy", nil, ad, 401)
	// Rotation invalidates old token, and old ACKs no longer make node available.
	mustRequest(t, a, "POST", fmt.Sprintf("/api/admin/nodes/%d/agent-token", nodeID), M{"engine": "xray", "port_start": 20000, "port_end": 20010}, ad, 200)
	nodeRequest(t, a, token, "GET", "policy", nil, 401)
	var stored string
	a.db.QueryRow("SELECT token_hash FROM node_tokens WHERE node_id=?", nodeID).Scan(&stored)
	if stored == token || len(stored) != 64 {
		t.Fatal("node token stored in plaintext")
	}
}
func TestTrafficAtomicValidationAndPrecision(t *testing.T) {
	a, _, uid, _, token, u := nodeFixture(t)
	for _, invalid := range []any{-1, 0.5, "1", json.Number("9223372036854775808")} {
		nodeRequest(t, a, token, "POST", "traffic", samples(sample(u, "valid", 10, 0), sample(u, "invalid", invalid, 0)), 400)
		s, _ := a.subscription(a.db, uid)
		if integer(s, "traffic_used_bytes") != 0 {
			t.Fatal("partially committed invalid batch")
		}
	}
	unknown := sample(u, "unknown", 1, 0)
	unknown["cycle_id"] = "unknown"
	nodeRequest(t, a, token, "POST", "traffic", samples(unknown), 400)
	huge := int64(9007199254740993)
	// 1x multiplier verifies precision above JS integer range through JSON and DB.
	nodes, _ := list(a.db, "nodes", 0)
	for _, n := range nodes {
		if boolean(n, "managed") {
			n["multiplier"] = 1
			put(a.db, "nodes", 0, n)
		}
	}
	nodeRequest(t, a, token, "POST", "traffic", samples(sample(u, "huge", huge, 0)), 200)
	s, _ := a.subscription(a.db, uid)
	if integer(s, "traffic_used_bytes") != huge {
		t.Fatalf("int64 precision lost: %d", integer(s, "traffic_used_bytes"))
	}
	nodeRequest(t, a, token, "POST", "traffic", samples(sample(u, "huge", huge, 0)), 200)
	records, _ := list(a.db, "traffic", uid)
	if integer(records[0], "upload_bytes") != huge {
		t.Fatal("history int64 precision lost")
	}
}
func TestTrafficResetOldCycleAndSuspension(t *testing.T) {
	a, ad, uid, _, token, u := nodeFixture(t)
	nodeRequest(t, a, token, "POST", "traffic", samples(sample(u, "old", 20, 0)), 200)
	s, _ := a.subscription(a.db, uid)
	s["status"] = "suspended"
	put(a.db, "subscriptions", uid, s)
	mustRequest(t, a, "POST", fmt.Sprintf("/api/admin/users/%d/traffic/reset", uid), M{}, ad, 200)
	s, _ = a.subscription(a.db, uid)
	if text(s, "status") != "suspended" || boolean(s, "service_enabled") || integer(s, "traffic_used_bytes") != 0 {
		t.Fatalf("reset resumed suspended account: %v", s)
	}
	nodeRequest(t, a, token, "POST", "traffic", samples(sample(u, "old", 30, 0)), 200)
	s, _ = a.subscription(a.db, uid)
	if integer(s, "traffic_used_bytes") != 0 {
		t.Fatal("late old-cycle data billed into new cycle")
	}
	history, _ := list(a.db, "traffic", uid)
	if boolean(history[0], "current_cycle") {
		t.Fatal("late record not marked historical")
	}
	s["reset_at"] = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	s["traffic_used"] = 1
	put(a.db, "subscriptions", uid, s)
	old := text(s, "cycle_id")
	if e := a.lifecycle(time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	s, _ = a.subscription(a.db, uid)
	if text(s, "cycle_id") == old || text(s, "status") != "suspended" || integer(s, "traffic_used_bytes") != 0 {
		t.Fatalf("period suspension lifecycle failed: %v", s)
	}
	a.db.Exec("UPDATE users SET status='disabled' WHERE id=?", uid)
	p := nodeRequest(t, a, token, "GET", "policy", nil, 200)
	if text(policyUser(t, p, uid), "reason") != "account_disabled" {
		t.Fatal("account ban not propagated")
	}
}
func TestTrafficConcurrentDuplicateAndNodeIsolation(t *testing.T) {
	a, _, uid, _, token, u := nodeFixture(t)
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var b bytes.Buffer
			json.NewEncoder(&b).Encode(samples(sample(u, "same", 10, 2)))
			r := httptest.NewRequest("POST", "/api/node/traffic", &b)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			a.ServeHTTP(w, r)
			codes <- w.Code
		}()
	}
	wg.Wait()
	close(codes)
	for c := range codes {
		if c != 200 {
			t.Fatalf("concurrent report %d", c)
		}
	}
	s, _ := a.subscription(a.db, uid)
	if integer(s, "traffic_used_bytes") != 18 {
		t.Fatalf("concurrent duplicate billing: %v", s)
	}
	rows, _ := list(a.db, "traffic", uid)
	count := 0
	for _, row := range rows {
		if text(row, "source") == "metered" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("duplicate history records %d", count)
	}
	if strings.Contains(text(s, "cycle_id"), " ") {
		t.Fatal("invalid cycle")
	}
}

func TestTrafficStaleACKDoesNotReviveOnHeartbeat(t *testing.T) {
	a, _, uid, nodeID, token, u := nodeFixture(t)
	nodeRequest(t, a, token, "POST", "ack", M{"users": []M{{"user_id": uid, "policy_version": text(u, "policy_version"), "enabled": true, "error": ""}}}, 200)
	old := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	a.db.Exec("UPDATE node_acks SET ack_at=? WHERE node_id=?", old, nodeID)
	a.db.Exec("UPDATE node_tokens SET last_seen=? WHERE node_id=?", old, nodeID)
	nodeRequest(t, a, token, "GET", "policy", nil, 200)
	_, ready, e := a.managedSubscriptionNode(nodeID, uid)
	if e != nil || ready {
		t.Fatalf("heartbeat revived stale ACK: %v %v", ready, e)
	}
	rows, _ := a.enforcement()
	if rows.([]M)[0]["applied_enabled"] != nil {
		t.Fatal("stale ACK shown as applied")
	}
}
func TestTrafficQuotaPropagatesAcrossNodes(t *testing.T) {
	a, ad, uid, _, token, u := nodeFixture(t)
	n := mustRequest(t, a, "POST", "/api/admin/nodes", M{"name": "Managed second", "host": "second.example.test", "protocol": "vless", "tls": false, "multiplier": 1}, ad, 200)
	tok := mustRequest(t, a, "POST", fmt.Sprintf("/api/admin/nodes/%d/agent-token", integer(n, "id")), M{"engine": "xray", "port_start": 21000, "port_end": 21005}, ad, 200)
	token2 := text(tok, "token")
	nodeRequest(t, a, token2, "GET", "policy", nil, 200)
	nodeRequest(t, a, token, "POST", "traffic", samples(sample(u, "quota", 100, 0)), 200)
	p := nodeRequest(t, a, token2, "GET", "policy", nil, 200)
	stopped := policyUser(t, p, uid)
	if boolean(stopped, "enabled") || text(stopped, "reason") != "quota_exceeded" {
		t.Fatalf("other node not stopped: %v", stopped)
	}
}

func TestTrafficAdminStateSuspendedRenewalAndBilledHeader(t *testing.T) {
	a, ad, uid, _, token, u := nodeFixture(t)
	c := login(t, a, "demo@example.test")
	nodeRequest(t, a, token, "POST", "traffic", samples(sample(u, "partial", 10, 0)), 200)
	var subToken string
	a.db.QueryRow("SELECT subscription_token FROM users WHERE id=?", uid).Scan(&subToken)
	w, _ := request(a, "GET", "/subscribe/"+subToken, nil, nil)
	if !strings.Contains(w.Header().Get("Subscription-Userinfo"), "upload=0; download=15; total=100;") {
		t.Fatalf("subscription header does not use billed bytes: %s", w.Header().Get("Subscription-Userinfo"))
	}
	s, _ := a.subscription(a.db, uid)
	s["status"] = "suspended"
	put(a.db, "subscriptions", uid, s)
	cycle := text(s, "cycle_id")
	expires, _ := time.Parse(time.RFC3339, text(s, "expires_at"))
	o := order(t, a, c, firstPlan(t, a, "recurring"), "monthly")
	mustRequest(t, a, "POST", entityPath("orders", o, "/pay"), M{}, c, 200)
	s, _ = a.subscription(a.db, uid)
	renewed, _ := time.Parse(time.RFC3339, text(s, "expires_at"))
	if text(s, "status") != "suspended" || integer(s, "traffic_used_bytes") != 15 || text(s, "cycle_id") != cycle || !renewed.After(expires) {
		t.Fatalf("payment bypassed suspension or reset renewal usage: %v", s)
	}
	a.db.Exec("UPDATE users SET status='disabled' WHERE id=?", uid)
	_, result := request(a, "GET", "/api/admin/subscriptions", nil, ad)
	found := false
	for _, raw := range result["data"].([]any) {
		sub := M(raw.(map[string]any))
		if integer(sub, "user_id") == uid {
			found = true
			if boolean(sub, "service_enabled") || text(sub, "service_reason") != "account_disabled" {
				t.Fatalf("admin service state stale: %v", sub)
			}
		}
	}
	if !found {
		t.Fatal("subscription missing")
	}
}
func TestTrafficPortExhaustionLeavesExistingUserEnabled(t *testing.T) {
	a, ad, uid, nodeID, _, _ := nodeFixture(t)
	tok := mustRequest(t, a, "POST", fmt.Sprintf("/api/admin/nodes/%d/agent-token", nodeID), M{"engine": "xray", "port_start": 20000, "port_end": 20000}, ad, 200)
	token := text(tok, "token")
	other, e := a.createUser(a.db, "capacity@example.test", "DemoCloud2026!", "Capacity", "user", 0)
	if e != nil {
		t.Fatal(e)
	}
	otherID := integer(other, "id")
	s, _ := a.subscription(a.db, uid)
	delete(s, "id")
	delete(s, "cycle_id")
	if _, e = put(a.db, "subscriptions", otherID, s); e != nil {
		t.Fatal(e)
	}
	p := nodeRequest(t, a, token, "GET", "policy", nil, 200)
	existing, exhausted := policyUser(t, p, uid), policyUser(t, p, otherID)
	if !boolean(existing, "enabled") || integer(existing, "port") != 20000 {
		t.Fatalf("existing user affected by capacity: %v", existing)
	}
	if boolean(exhausted, "enabled") || integer(exhausted, "port") != 0 || text(exhausted, "reason") != "port_exhausted" {
		t.Fatalf("missing disabled capacity policy: %v", exhausted)
	}
	nodeRequest(t, a, token, "POST", "ack", M{"users": []M{{"user_id": otherID, "policy_version": text(exhausted, "policy_version"), "enabled": false, "error": ""}}}, 200)
	rows, e := a.enforcement()
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, row := range rows.([]M) {
		if integer(row, "user_id") == otherID {
			found = true
			if boolean(row, "desired_enabled") || text(row, "reason") != "port_exhausted" {
				t.Fatalf("bad enforcement capacity row: %v", row)
			}
		}
	}
	if !found {
		t.Fatal("capacity user absent from enforcement")
	}
}
