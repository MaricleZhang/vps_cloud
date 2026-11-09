// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"testing"

	boxTLS "github.com/sagernet/sing-box/common/tls"
)

func TestRealityGenerationWritesPrivateFileAndPublishesMatchingKey(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "keys")
	var output bytes.Buffer
	if err := generate([]string{"reality-keypair", "--out", directory}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "reality.key")
	private, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]string
	if err := json.Unmarshal(output.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(string(bytes.TrimSpace(private)))
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdh.X25519().NewPrivateKey(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if metadata["reality_public_key"] != base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()) || len(metadata["reality_short_id"]) != 16 || len(metadata) != 2 {
		t.Fatal("public metadata does not match the generated REALITY private key")
	}
	if bytes.Contains(output.Bytes(), bytes.TrimSpace(private)) {
		t.Fatal("private key appeared in stdout")
	}
	assertPrivateFile(t, path)
	output.Reset()
	if err := generate([]string{"reality-keypair", "--out", directory}, &output, io.Discard); err == nil {
		t.Fatal("existing private key was overwritten")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(private, after) || output.Len() != 0 {
		t.Fatal("failed generation changed existing key or emitted new metadata")
	}
}

func TestECHGenerationUsesUpstreamKeyFormatAndMatchingClientConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ech.pem")
	var output bytes.Buffer
	if err := generate([]string{"ech-keypair", "--server-name", "public.example.test", "--private-key-file", path}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	private, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, rest := pem.Decode(private)
	if block == nil || block.Type != "ECH KEYS" || len(rest) != 0 {
		t.Fatal("generated ECH file is not native sing-box ECH KEYS PEM")
	}
	keys, err := boxTLS.UnmarshalECHKeys(block.Bytes)
	if err != nil || len(keys) != 1 {
		t.Fatalf("upstream could not parse generated ECH keys: %v", err)
	}
	var metadata map[string]string
	if err := json.Unmarshal(output.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	config, err := base64.StdEncoding.DecodeString(metadata["ech_config"])
	if err != nil || len(config) < 2 || int(binary.BigEndian.Uint16(config[:2])) != len(config)-2 || !bytes.Equal(config[2:], keys[0].Config) {
		t.Fatal("published client ECHConfigList does not match private-key record")
	}
	if metadata["ech_query_server_name"] != "public.example.test" || len(metadata) != 2 {
		t.Fatal("ECH metadata is incomplete or exposes unexpected fields")
	}
	if bytes.Contains(output.Bytes(), private) || bytes.Contains(output.Bytes(), []byte(base64.StdEncoding.EncodeToString(keys[0].PrivateKey))) {
		t.Fatal("ECH private key appeared in stdout")
	}
	assertPrivateFile(t, path)
}

func TestKeyGenerationRejectsInvalidOrAmbiguousDestinations(t *testing.T) {
	for _, args := range [][]string{
		{"reality-keypair"},
		{"reality-keypair", "--out", "keys", "--private-key-file", "key"},
		{"ech-keypair", "--out", "keys"},
		{"ech-keypair", "--out", "keys", "--server-name", "https://private.example"},
		{"ech-keypair", "--out", "keys", "--server-name", "127.0.0.1"},
		{"ech-keypair", "--out", "keys", "--server-name", "*.example.test"},
	} {
		var output bytes.Buffer
		if err := generate(args, &output, io.Discard); err == nil || output.Len() != 0 {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
	}
}

func assertPrivateFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("private-key permissions = %o, want 600", info.Mode().Perm())
	}
}
