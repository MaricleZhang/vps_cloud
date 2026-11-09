package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func protocolUser() User {
	return User{ID: 2, Port: 20000, UUID: "00112233-4455-6677-8899-aabbccddeeff", Password: "fixture-user-password", StatsName: "user-2", CycleID: "cycle", Version: "profile-v1", Enabled: true}
}

func writeRealityFixture(t *testing.T) (path, privateKey, publicKey string) {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateKey = base64.RawURLEncoding.EncodeToString(key.Bytes())
	publicKey = base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	path = filepath.Join(t.TempDir(), "reality.key")
	if err = os.WriteFile(path, []byte(privateKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return
}

func appendVector16(dst, value []byte) []byte {
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(value)))
	return append(dst, value...)
}

// Canonical draft-13 X25519 ECH fixture, in the documented sing-box PEM format.
// The real upstream generator and Mihomo interoperability are tested by root.
func writeECHFixture(t *testing.T) (path, publicConfig string) {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	contents := []byte{7, 0, 0x20} // config_id and DHKEM_X25519_HKDF_SHA256
	contents = appendVector16(contents, key.PublicKey().Bytes())
	contents = appendVector16(contents, []byte{0, 1, 0, 1}) // HKDF-SHA256 / AES-128-GCM
	name := []byte("public.example.test")
	contents = append(contents, 0, byte(len(name)))
	contents = append(contents, name...)
	contents = append(contents, 0, 0) // no extensions
	config := appendVector16([]byte{0xfe, 0x0d}, contents)
	list := appendVector16(nil, config)
	keys := appendVector16(nil, key.Bytes())
	keys = appendVector16(keys, config)
	path = filepath.Join(t.TempDir(), "ech.pem")
	if err = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "ECH KEYS", Bytes: keys}), 0600); err != nil {
		t.Fatal(err)
	}
	return path, base64.StdEncoding.EncodeToString(list)
}

func decodeKernelFixture(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestReferenceVLESSRealityVisionKernelMapping(t *testing.T) {
	path, privateKey, publicKey := writeRealityFixture(t)
	node := Node{ID: 1, Protocol: "vless", Network: "tcp", TLS: true, TLSMode: "reality", Flow: "xtls-rprx-vision", ServerName: "cover.example.test", RealityPublicKey: publicKey, RealityShortID: "0123456789ab", RealityHandshakeServer: "127.0.0.1", RealityHandshakePort: 443, UDP: true, ClientFingerprint: "firefox"}
	for _, engine := range []string{"sing-box", "xray"} {
		t.Run(engine, func(t *testing.T) {
			config := Config{Engine: engine, Bind: "127.0.0.1", RealityPrivateKeyFile: path}
			raw, prefix, err := kernelConfig(config, node, protocolUser(), "127.0.0.1:21000")
			if err != nil {
				t.Fatal(err)
			}
			if prefix != "user>>>user-2" {
				t.Fatal("Reality lost per-user statistics identity")
			}
			root := decodeKernelFixture(t, raw)
			inbound := root["inbounds"].([]any)[0].(map[string]any)
			if engine == "sing-box" {
				user := inbound["users"].([]any)[0].(map[string]any)
				security := inbound["tls"].(map[string]any)
				reality := security["reality"].(map[string]any)
				if user["flow"] != node.Flow || user["uuid"] != protocolUser().UUID || security["enabled"] != true || reality["private_key"] != privateKey || reality["short_id"].([]any)[0] != node.RealityShortID {
					t.Fatal("sing-box Reality/Vision parameters do not match policy and local key")
				}
				if _, found := security["certificate_path"]; found {
					t.Fatal("Reality incorrectly requires a local certificate")
				}
				handshake := reality["handshake"].(map[string]any)
				if handshake["server"] != node.RealityHandshakeServer || int(handshake["server_port"].(float64)) != 443 {
					t.Fatal("Reality handshake target is incorrect")
				}
			} else {
				settings := inbound["settings"].(map[string]any)
				user := settings["clients"].([]any)[0].(map[string]any)
				stream := inbound["streamSettings"].(map[string]any)
				reality := stream["realitySettings"].(map[string]any)
				if user["flow"] != node.Flow || stream["security"] != "reality" || reality["privateKey"] != privateKey || reality["target"] != "127.0.0.1:443" {
					t.Fatal("Xray Reality/Vision mapping is incorrect")
				}
			}
		})
	}
}

func TestReferenceAnyTLSECHKernelMapping(t *testing.T) {
	path, publicConfig := writeECHFixture(t)
	directory := t.TempDir()
	cert, key := filepath.Join(directory, "cert.pem"), filepath.Join(directory, "key.pem")
	for _, path := range []string{cert, key} {
		if err := os.WriteFile(path, []byte("operator-supplied TLS PEM"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config := Config{Engine: "sing-box", Bind: "127.0.0.1", TLSCert: cert, TLSKey: key, ECHKeyFile: path}
	node := Node{Protocol: "anytls", TLS: true, TLSMode: "tls", ServerName: "inner.example.test", ECHEnabled: true, ECHConfig: publicConfig, ECHQueryServerName: "public.example.test", UDP: true}
	raw, prefix, err := kernelConfig(config, node, protocolUser(), "127.0.0.1:21000")
	if err != nil {
		t.Fatal(err)
	}
	if prefix != "user>>>user-2" {
		t.Fatal("AnyTLS lost per-user statistics identity")
	}
	inbound := decodeKernelFixture(t, raw)["inbounds"].([]any)[0].(map[string]any)
	security := inbound["tls"].(map[string]any)
	ech := security["ech"].(map[string]any)
	if inbound["type"] != "anytls" || security["min_version"] != "1.3" || security["certificate_path"] != cert || security["key_path"] != key || ech["enabled"] != true || ech["key_path"] != path {
		t.Fatal("AnyTLS ECH server configuration is incomplete")
	}
	if inbound["users"].([]any)[0].(map[string]any)["password"] != protocolUser().Password {
		t.Fatal("AnyTLS user credential was not assigned")
	}
	if strings.Contains(string(raw), "ech_config") || strings.Contains(string(raw), "query_server_name") {
		t.Fatal("client-only ECH lookup fields were passed into the server core")
	}
}

func TestReferenceProtocolRejectsWrongKeysAndUnsupportedFeatures(t *testing.T) {
	path, privateKey, publicKey := writeRealityFixture(t)
	base := Node{Protocol: "vless", Network: "tcp", TLS: true, TLSMode: "reality", Flow: "xtls-rprx-vision", ServerName: "cover.example.test", RealityPublicKey: publicKey, RealityShortID: "abcd", RealityHandshakeServer: "cover.example.test", RealityHandshakePort: 443}
	config := Config{Engine: "sing-box", Bind: "127.0.0.1", RealityPrivateKeyFile: path}
	_, _, otherPublic := writeRealityFixture(t)
	for _, test := range []struct {
		name   string
		modify func(*Node)
	}{
		{"wrong public key", func(n *Node) { n.RealityPublicKey = otherPublic }},
		{"unsupported network", func(n *Node) { n.Network = "ws" }},
		{"unsupported flow", func(n *Node) { n.Flow = "other-flow" }},
		{"bad short ID", func(n *Node) { n.RealityShortID = "xyz" }},
		{"wrong TLS mode", func(n *Node) { n.TLSMode = "unknown" }},
		{"ECH on Reality", func(n *Node) { n.ECHEnabled = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := base
			test.modify(&node)
			if _, _, err := kernelConfig(config, node, protocolUser(), "127.0.0.1:21000"); err == nil {
				t.Fatal("unsupported or mismatched profile was accepted")
			} else if strings.Contains(err.Error(), privateKey) {
				t.Fatal("private key leaked in an error")
			}
		})
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := kernelConfig(config, base, protocolUser(), "127.0.0.1:21000"); err == nil {
		t.Fatal("world-readable Reality key file accepted")
	}
	echPath, publicECH := writeECHFixture(t)
	if err := validateECHKeyFile(echPath, publicECH); err != nil {
		t.Fatal(err)
	}
	_, unrelatedECH := writeECHFixture(t)
	for _, wrong := range []string{unrelatedECH, "invalid-base64", base64.StdEncoding.EncodeToString([]byte{0, 0})} {
		if err := validateECHKeyFile(echPath, wrong); err == nil {
			t.Fatal("mismatched or invalid ECH public configuration accepted")
		}
	}
	if err := os.WriteFile(echPath, pem.EncodeToMemory(&pem.Block{Type: "ECH KEYS", Bytes: []byte{0, 32, 1}}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateECHKeyFile(echPath, publicECH); err == nil {
		t.Fatal("truncated ECH private container accepted")
	}
}

func TestRuntimeProtocolPrivateKeyPaths(t *testing.T) {
	data := validFileOptions()
	data["reality_private_key_file"] = "keys/reality.key"
	data["ech_key_file"] = "keys/ech.pem"
	path := writeOptionsFile(t, data)
	options, err := parseRuntimeOptions([]string{"-config", path}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if options.Config.RealityPrivateKeyFile != filepath.Join(filepath.Dir(path), "keys/reality.key") || options.Config.ECHKeyFile != filepath.Join(filepath.Dir(path), "keys/ech.pem") {
		t.Fatal("local private key file paths not resolved relative to node configuration")
	}
	options, err = parseRuntimeOptions([]string{"-config", path, "-reality-private-key-file=", "-ech-key-file="}, io.Discard)
	if err != nil || options.Config.RealityPrivateKeyFile != "" || options.Config.ECHKeyFile != "" {
		t.Fatal("explicit CLI could not clear private key file paths")
	}
}
