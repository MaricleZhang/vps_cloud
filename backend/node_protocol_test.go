package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func testRealityPublicKey(t *testing.T) string {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
}

// The center transports an opaque ECHConfigList. Its key association is checked
// by the node kernel; this small framed fixture tests policy/subscription carry.
func testECHPublicConfig() string {
	return base64.StdEncoding.EncodeToString([]byte{0, 8, 0xfe, 0x0d, 0, 4, 1, 2, 3, 4})
}

func TestNodeProtocolValidationAndLegacyTLS(t *testing.T) {
	validReality := M{"name": "Reality", "protocol": "vless", "tls_mode": "reality", "server_name": "cover.example.test", "reality_public_key": testRealityPublicKey(t), "reality_short_id": "012345"}
	legacy := defaultsFor("nodes")
	legacy["name"], legacy["protocol"], legacy["tls"] = "Legacy", "vless", false
	if err := validateEntity("nodes", legacy); err != nil || text(legacy, "tls_mode") != "none" || text(legacy, "network") != "tcp" {
		t.Fatalf("legacy plaintext node changed: %v", err)
	}
	base := defaultsFor("nodes")
	for key, value := range validReality {
		base[key] = value
	}
	if err := validateEntity("nodes", base); err != nil {
		t.Fatal(err)
	}
	if text(base, "flow") != "xtls-rprx-vision" || text(base, "reality_handshake_server") != "cover.example.test" || !boolean(base, "tls") {
		t.Fatal("REALITY defaults did not preserve the required transport")
	}
	for _, test := range []struct {
		name, field string
		value       any
	}{
		{"wrong public key", "reality_public_key", "not-a-public-key"},
		{"odd short ID", "reality_short_id", "123"},
		{"long short ID", "reality_short_id", strings.Repeat("12", 9)},
		{"nonhex short ID", "reality_short_id", "zz"},
		{"network typo", "network", "websocket"},
		{"TLS mode typo", "tls_mode", "other"},
		{"wrong flow", "flow", "invalid"},
		{"string UDP", "udp", "true"},
		{"bad SNI", "server_name", "example.test:443"},
		{"bad destination", "reality_handshake_server", "https://example.test"},
		{"fractional destination port", "reality_handshake_port", 443.5},
		{"zero destination port", "reality_handshake_port", 0},
		{"non VLESS reality", "protocol", "vmess"},
		{"ECH over REALITY", "ech_enabled", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := M{}
			for key, value := range base {
				candidate[key] = value
			}
			candidate[test.field] = test.value
			if err := validateEntity("nodes", candidate); err == nil {
				t.Fatalf("accepted invalid %s", test.field)
			}
		})
	}
	anytls := defaultsFor("nodes")
	anytls["name"], anytls["protocol"], anytls["tls_mode"], anytls["server_name"] = "ECH", "anytls", "tls", "anytls.example.test"
	anytls["ech_enabled"], anytls["ech_config"] = true, testECHPublicConfig()
	if err := validateEntity("nodes", anytls); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"not-base64", base64.StdEncoding.EncodeToString([]byte{0, 8, 0xfe, 0x0d}), base64.StdEncoding.EncodeToString([]byte{0, 8, 0xfe, 0x0d, 0, 5, 1, 2, 3, 4})} {
		anytls["ech_config"] = invalid
		if err := validateEntity("nodes", anytls); err == nil {
			t.Fatal("accepted malformed public ECHConfigList")
		}
	}
}

func TestReferenceProtocolPolicyAndPersonalSubscription(t *testing.T) {
	a := fixture(t)
	admin := login(t, a, "admin@example.test")
	user := login(t, a, "demo@example.test")
	var uid int64
	a.db.QueryRow("SELECT id FROM users WHERE email='demo@example.test'").Scan(&uid)
	public := testRealityPublicKey(t)
	reality := mustRequest(t, a, "POST", "/api/admin/nodes", M{
		"name": "Reference REALITY", "protocol": "vless", "host": "node.example.test",
		"tls_mode": "reality", "network": "tcp", "flow": "xtls-rprx-vision", "server_name": "cover.example.test",
		"reality_public_key": public, "reality_short_id": "0123456789ab", "client_fingerprint": "firefox", "udp": true,
	}, admin, 200)
	nodeID := integer(reality, "id")
	issued := mustRequest(t, a, "POST", fmt.Sprintf("/api/admin/nodes/%d/agent-token", nodeID), M{"engine": "sing-box", "port_start": 23000, "port_end": 23010}, admin, 200)
	token := text(issued, "token")
	policy := nodeRequest(t, a, token, "GET", "policy", nil, 200)
	transport := M(policy["node"].(map[string]any))
	if text(transport, "tls_mode") != "reality" || text(transport, "reality_public_key") != public || text(transport, "flow") != "xtls-rprx-vision" || text(transport, "client_fingerprint") != "firefox" {
		t.Fatal("public reference transport parameters missing from policy")
	}
	current := policyUser(t, policy, uid)
	ack := func(nodeToken string, state M) {
		t.Helper()
		nodeRequest(t, a, nodeToken, "POST", "ack", M{"users": []M{{"user_id": uid, "policy_version": text(state, "policy_version"), "enabled": true, "error": ""}}}, 200)
	}
	ack(token, current)
	getProxies := func() []any {
		t.Helper()
		sub := mustRequest(t, a, "GET", "/api/subscription", nil, user, 200)
		address, err := url.Parse(text(sub, "url"))
		if err != nil {
			t.Fatal(err)
		}
		w, result := request(a, "GET", address.RequestURI()+"?format=json", nil, nil)
		if w.Code != 200 {
			t.Fatalf("personal subscription unavailable: %d", w.Code)
		}
		return result["proxies"].([]any)
	}
	proxies := getProxies()
	if len(proxies) != 1 {
		t.Fatalf("expected only configured managed node, got %d", len(proxies))
	}
	proxy := M(proxies[0].(map[string]any))
	options := M(proxy["reality-opts"].(map[string]any))
	if text(proxy, "flow") != "xtls-rprx-vision" || text(proxy, "encryption") != "none" || text(proxy, "client-fingerprint") != "firefox" || !boolean(proxy, "udp") || text(proxy, "uuid") != text(current, "uuid") || text(options, "public-key") != public || text(options, "short-id") != "0123456789ab" {
		t.Fatal("Clash REALITY output does not match the configured transport and personal credentials")
	}
	// Transport edits invalidate the earlier execution ACK.
	mustRequest(t, a, "PATCH", fmt.Sprintf("/api/admin/nodes/%d", nodeID), M{"client_fingerprint": "chrome"}, admin, 200)
	updated := policyUser(t, nodeRequest(t, a, token, "GET", "policy", nil, 200), uid)
	if text(updated, "policy_version") == text(current, "policy_version") {
		t.Fatal("transport change did not invalidate policy version")
	}
	nodeRequest(t, a, token, "POST", "ack", M{"users": []M{{"user_id": uid, "policy_version": text(current, "policy_version"), "enabled": true, "error": ""}}}, 409)
	if len(getProxies()) != 0 {
		t.Fatal("subscription published transport before new execution ACK")
	}
	ack(token, updated)
	echConfig := testECHPublicConfig()
	echNode := mustRequest(t, a, "POST", "/api/admin/nodes", M{
		"name": "Reference ECH", "protocol": "anytls", "host": "ech-node.example.test", "server_name": "private.example.test", "tls_mode": "tls",
		"ech_enabled": true, "ech_config": echConfig, "ech_query_server_name": "public.example.test", "udp": true,
	}, admin, 200)
	echIssued := mustRequest(t, a, "POST", fmt.Sprintf("/api/admin/nodes/%d/agent-token", integer(echNode, "id")), M{"engine": "sing-box", "port_start": 24000, "port_end": 24010}, admin, 200)
	echToken := text(echIssued, "token")
	echPolicy := nodeRequest(t, a, echToken, "GET", "policy", nil, 200)
	if !boolean(M(echPolicy["node"].(map[string]any)), "ech_enabled") {
		t.Fatal("ECH public metadata missing from policy")
	}
	ack(echToken, policyUser(t, echPolicy, uid))
	found := false
	for _, value := range getProxies() {
		proxy := M(value.(map[string]any))
		if text(proxy, "type") != "anytls" {
			continue
		}
		ech := M(proxy["ech-opts"].(map[string]any))
		if !boolean(ech, "enable") || text(ech, "config") != echConfig || text(ech, "query-server-name") != "public.example.test" || text(proxy, "sni") != "private.example.test" || !boolean(proxy, "udp") {
			t.Fatal("Clash AnyTLS ECH fields differ from configured public metadata")
		}
		found = true
	}
	if !found {
		t.Fatal("AnyTLS node missing from personal subscription")
	}
	// Ordinary node lists keep their display-only contract.
	w, _ := request(a, "GET", "/api/nodes", nil, user)
	if bytes.Contains(w.Body.Bytes(), []byte(public)) || bytes.Contains(w.Body.Bytes(), []byte(text(updated, "uuid"))) || bytes.Contains(w.Body.Bytes(), []byte(echConfig)) {
		t.Fatal("ordinary node listing exposed transport credentials")
	}
}

func TestNodeAPIRejectsPrivateKeysAndKeepsLegacyTLSPatch(t *testing.T) {
	a := fixture(t)
	admin := login(t, a, "admin@example.test")
	marker := "DO-NOT-STORE-PRIVATE-MATERIAL"
	for _, payload := range []M{
		{"private_key": marker}, {"reality_private_key": marker}, {"tlsKey": marker},
		{"reality-opts": M{"private-key": marker}}, {"ech_key_file": marker},
	} {
		payload["name"], payload["protocol"] = "Rejected", "vless"
		w, _ := request(a, "POST", "/api/admin/nodes", payload, admin)
		if w.Code != 400 || strings.Contains(w.Body.String(), marker) {
			t.Fatalf("private-key submission must reject without echo: %d", w.Code)
		}
	}
	var stored int
	if err := a.db.QueryRow("SELECT count(*) FROM entities WHERE kind='nodes' AND data LIKE ?", "%"+marker+"%").Scan(&stored); err != nil || stored != 0 {
		t.Fatal("a private-key submission reached node storage")
	}
	node := mustRequest(t, a, "POST", "/api/admin/nodes", M{"name": "Legacy TLS", "protocol": "vless", "tls": true}, admin, 200)
	changed := mustRequest(t, a, "PATCH", fmt.Sprintf("/api/admin/nodes/%d", integer(node, "id")), M{"tls": false}, admin, 200)
	if boolean(changed, "tls") || text(changed, "tls_mode") != "none" {
		t.Fatal("legacy TLS-only update stopped working")
	}
	// Administrative serialization is a whitelist even if a legacy DB row
	// contains an unrelated private field inserted outside the supported API.
	filtered := nodeSafe(M{"id": int64(1), "name": "Safe", "reality_private_key": marker, "agent_token": marker, "secret": marker})
	if len(filtered) != 2 {
		t.Fatal("administrative node response exposed an unknown private field")
	}
}
