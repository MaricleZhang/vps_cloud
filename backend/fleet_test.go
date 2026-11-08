package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Five independently authenticated nodes share one user's quota, while sample
// identity, credentials, and execution confirmations remain local to each node.
func TestFiveNodeFleetSharedQuota(t *testing.T) {
	a, admin, uid, firstNode, firstToken, _ := nodeFixture(t)
	sub, err := a.subscription(a.db, uid)
	if err != nil {
		t.Fatal(err)
	}
	sub["traffic_limit"] = float64(1000) / float64(gib)
	if _, err = put(a.db, "subscriptions", uid, sub); err != nil {
		t.Fatal(err)
	}
	other, err := a.createUser(a.db, "other-fleet@example.test", "FleetPassword2026!", "Unaffected user", "user", 0)
	if err != nil {
		t.Fatal(err)
	}
	otherID := integer(other, "id")
	otherSub := M{}
	for key, value := range sub {
		if key != "id" && key != "cycle_id" {
			otherSub[key] = value
		}
	}
	otherSub["user_email"] = text(other, "email")
	if _, err = put(a.db, "subscriptions", otherID, otherSub); err != nil {
		t.Fatal(err)
	}
	type fleetNode struct {
		id    int64
		token string
		user  M
		other M
	}
	fleet := []fleetNode{{id: firstNode, token: firstToken}}
	for i, multiplier := range []float64{1, 2, 0.5, 1.25} {
		node := mustRequest(t, a, "POST", "/api/admin/nodes", M{
			"name":       fmt.Sprintf("Fleet node %d", i+2),
			"host":       fmt.Sprintf("node-%d.example.test", i+2),
			"protocol":   "vless",
			"tls":        false,
			"multiplier": multiplier,
		}, admin, 200)
		id := integer(node, "id")
		credential := mustRequest(t, a, "POST", fmt.Sprintf("/api/admin/nodes/%d/agent-token", id), M{
			"engine": "xray", "port_start": 20000, "port_end": 20010,
		}, admin, 200)
		fleet = append(fleet, fleetNode{id: id, token: text(credential, "token")})
	}
	oldCycle := text(sub, "cycle_id")
	for i := range fleet {
		policy := nodeRequest(t, a, fleet[i].token, "GET", "policy", nil, 200)
		fleet[i].user = policyUser(t, policy, uid)
		fleet[i].other = policyUser(t, policy, otherID)
		if text(fleet[i].user, "cycle_id") != oldCycle || !boolean(fleet[i].user, "enabled") || !boolean(fleet[i].other, "enabled") {
			t.Fatalf("node %d did not receive shared active cycle: %v", i+1, policy)
		}
		if integer(fleet[i].user, "port") == integer(fleet[i].other, "port") || text(fleet[i].user, "uuid") == text(fleet[i].other, "uuid") {
			t.Fatalf("node %d did not isolate user credentials/ports", i+1)
		}
	}

	// Duplicate requests race across every node. Reusing the session name on
	// different nodes must not cause their separate observations to be deduped.
	reportAll := func(upload, download int64) {
		t.Helper()
		type report struct {
			token string
			body  []byte
		}
		reports := make([]report, len(fleet))
		for i, node := range fleet {
			payload := samples(
				sample(node.user, "shared-session", upload, download),
				sample(node.user, "shared-session", upload/2, download/2),
			)
			// The claimed node ID cannot override the Bearer token identity.
			payload["node_id"] = fleet[(i+1)%len(fleet)].id
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			reports[i] = report{token: node.token, body: body}
		}
		errors := make(chan string, len(reports)*2)
		var wg sync.WaitGroup
		for _, report := range reports {
			for retry := 0; retry < 2; retry++ {
				wg.Add(1)
				go func(token string, body []byte) {
					defer wg.Done()
					request := httptest.NewRequest("POST", "/api/node/traffic", bytes.NewReader(body))
					request.Header.Set("Authorization", "Bearer "+token)
					response := httptest.NewRecorder()
					a.ServeHTTP(response, request)
					if response.Code != 200 {
						errors <- fmt.Sprintf("%d: %s", response.Code, response.Body.String())
					}
				}(report.token, report.body)
			}
		}
		wg.Wait()
		close(errors)
		for failure := range errors {
			t.Errorf("concurrent fleet report: %s", failure)
		}
		if t.Failed() {
			t.FailNow()
		}
	}
	assertUsage := func(used, upload, download int64) M {
		t.Helper()
		s, err := a.subscription(a.db, uid)
		if err != nil {
			t.Fatal(err)
		}
		if integer(s, "traffic_used_bytes") != used || integer(s, "upload_bytes") != upload || integer(s, "download_bytes") != download {
			t.Fatalf("fleet total = used %d upload %d download %d, want %d/%d/%d", integer(s, "traffic_used_bytes"), integer(s, "upload_bytes"), integer(s, "download_bytes"), used, upload, download)
		}
		return s
	}
	reportAll(20, 60)
	assertUsage(500, 100, 300) // 80 bytes × (1.5 + 1 + 2 + 0.5 + 1.25).
	rows, err := list(a.db, "traffic", uid)
	if err != nil {
		t.Fatal(err)
	}
	billedByNode := map[int64]int64{}
	for _, row := range rows {
		if text(row, "source") == "metered" {
			billedByNode[integer(row, "node_id")] += integer(row, "billed_bytes")
		}
	}
	for i, want := range []int64{120, 80, 160, 40, 100} {
		if billedByNode[fleet[i].id] != want {
			t.Fatalf("node %d billed %d, want %d; node identity or multiplier mixed", i+1, billedByNode[fleet[i].id], want)
		}
	}

	// No single node has consumed the whole 1000-byte allowance, but their
	// aggregate has; the center must revoke this user on all five nodes.
	reportAll(50, 150)
	assertUsage(1250, 250, 750)
	nodeRequest(t, a, fleet[3].token, "POST", "traffic", samples(sample(fleet[3].other, "shared-session", 2, 2)), 200)
	stopped := make([]M, len(fleet))
	for i, node := range fleet {
		policy := nodeRequest(t, a, node.token, "GET", "policy", nil, 200)
		stopped[i] = policyUser(t, policy, uid)
		otherPolicy := policyUser(t, policy, otherID)
		if boolean(stopped[i], "enabled") || text(stopped[i], "reason") != "quota_exceeded" || !boolean(otherPolicy, "enabled") {
			t.Fatalf("quota or user isolation failed on node %d: %v", i+1, policy)
		}
		nodeRequest(t, a, node.token, "POST", "ack", M{"users": []M{
			{"user_id": uid, "policy_version": text(stopped[i], "policy_version"), "enabled": false, "error": ""},
			{"user_id": otherID, "policy_version": text(otherPolicy, "policy_version"), "enabled": true, "error": ""},
		}}, 200)
	}
	otherSub, err = a.subscription(a.db, otherID)
	if err != nil || integer(otherSub, "traffic_used_bytes") != 2 {
		t.Fatalf("separate user's usage was mixed into shared quota: %v %v", otherSub, err)
	}
	enforcement, err := a.enforcement()
	if err != nil {
		t.Fatal(err)
	}
	confirmedStops := 0
	for _, state := range enforcement.([]M) {
		if integer(state, "user_id") == uid && state["applied_enabled"] == false {
			confirmedStops++
		}
	}
	if confirmedStops != len(fleet) {
		t.Fatalf("got %d confirmed stops, want %d", confirmedStops, len(fleet))
	}

	mustRequest(t, a, "POST", fmt.Sprintf("/api/admin/users/%d/traffic/reset", uid), M{}, admin, 200)
	newSub := assertUsage(0, 0, 0)
	newCycle := text(newSub, "cycle_id")
	if newCycle == oldCycle {
		t.Fatal("reset did not create a new shared cycle")
	}
	for i := range fleet {
		policy := nodeRequest(t, a, fleet[i].token, "GET", "policy", nil, 200)
		current := policyUser(t, policy, uid)
		if !boolean(current, "enabled") || text(current, "cycle_id") != newCycle {
			t.Fatalf("reset not propagated to node %d: %v", i+1, current)
		}
		nodeRequest(t, a, fleet[i].token, "POST", "ack", M{"users": []M{{"user_id": uid, "policy_version": text(stopped[i], "policy_version"), "enabled": false, "error": ""}}}, 409)
		// Captured late on the old cycle: preserve history, not the new allowance.
		nodeRequest(t, a, fleet[i].token, "POST", "traffic", samples(sample(fleet[i].user, "shared-session", 60, 160)), 200)
		fleet[i].user = current
	}
	assertUsage(0, 0, 0)
	reportAll(2, 2)
	assertUsage(25, 10, 10)

	mustRequest(t, a, "PATCH", fmt.Sprintf("/api/admin/users/%d", uid), M{"status": "disabled"}, admin, 200)
	mustRequest(t, a, "POST", fmt.Sprintf("/api/admin/users/%d/traffic/reset", uid), M{}, admin, 200)
	for i, node := range fleet {
		policy := nodeRequest(t, a, node.token, "GET", "policy", nil, 200)
		blocked := policyUser(t, policy, uid)
		if boolean(blocked, "enabled") || text(blocked, "reason") != "account_disabled" || !boolean(policyUser(t, policy, otherID), "enabled") {
			t.Fatalf("ban/reset isolation failed on node %d: %v", i+1, policy)
		}
	}
	mustRequest(t, a, "PATCH", fmt.Sprintf("/api/admin/users/%d", uid), M{"status": "active"}, admin, 200)
	newSub = assertUsage(0, 0, 0)
	previousCycle := text(newSub, "cycle_id")
	newSub["reset_at"] = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if _, err = put(a.db, "subscriptions", uid, newSub); err != nil {
		t.Fatal(err)
	}
	if err = a.lifecycle(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	newSub = assertUsage(0, 0, 0)
	for i, node := range fleet {
		current := policyUser(t, nodeRequest(t, a, node.token, "GET", "policy", nil, 200), uid)
		if !boolean(current, "enabled") || text(current, "cycle_id") != text(newSub, "cycle_id") || text(current, "cycle_id") == previousCycle {
			t.Fatalf("automatic new cycle not shared by node %d: %v", i+1, current)
		}
	}
}
