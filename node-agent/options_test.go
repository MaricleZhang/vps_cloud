package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeOptionsFile(t *testing.T, value map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "node.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validFileOptions() map[string]any {
	return map[string]any{
		"panel": "https://panel.example.test", "engine": "sing-box",
		"core": "/usr/local/bin/sing-box", "state_dir": "/var/lib/node-1",
		"bind": "127.0.0.1", "token_file": "node.token", "poll_interval": "250ms",
		"tls_cert": "tls/fullchain.pem", "tls_key": "tls/privkey.pem",
	}
}

func TestRuntimeConfigFileAndExplicitCLIOverrides(t *testing.T) {
	path := writeOptionsFile(t, validFileOptions())
	options, err := parseRuntimeOptions([]string{"-config", path}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if options.Panel != "https://panel.example.test" || options.Config.Engine != "sing-box" || options.PollInterval != 250*time.Millisecond {
		t.Fatalf("file values not applied: %+v", options)
	}
	if options.Config.Bind != "127.0.0.1" || options.TokenFile != filepath.Join(filepath.Dir(path), "node.token") || options.Config.TLSCert != filepath.Join(filepath.Dir(path), "tls/fullchain.pem") {
		t.Fatalf("file paths must resolve relative to the configuration file: %+v", options)
	}
	options, err = parseRuntimeOptions([]string{
		"-config", path, "-engine=xray", "-core=/usr/local/bin/xray", "-poll=1s",
		"-bind=0.0.0.0", "-state=cli-state", "-panel=http://127.0.0.1:8080",
		"-token-file=", "-tls-cert=", "-tls-key=",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if options.Config.Engine != "xray" || options.Config.Core != "/usr/local/bin/xray" || options.PollInterval != time.Second || options.Config.Bind != "0.0.0.0" || options.Config.StateDir != "cli-state" || options.Panel != "http://127.0.0.1:8080" {
		t.Fatalf("explicit CLI flags did not override file: %+v", options)
	}
	if options.TokenFile != "" || options.Config.TLSCert != "" || options.Config.TLSKey != "" {
		t.Fatal("explicit empty flags did not clear optional file values")
	}
}

func TestRuntimeConfigPreservesLegacyCLIAndDefaults(t *testing.T) {
	args := []string{"-panel", "http://127.0.0.1:8080", "-core", "/usr/local/bin/xray", "-state", "local-state"}
	for i := 0; i < 2; i++ {
		options, err := parseRuntimeOptions(args, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if options.Config.Engine != "xray" || options.Config.Bind != "0.0.0.0" || options.PollInterval != time.Second || options.Config.StateDir != "local-state" {
			t.Fatalf("legacy defaults changed: %+v", options)
		}
	}
	if _, err := parseRuntimeOptions([]string{"-help"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help must work without deployment settings: %v", err)
	}
	path := writeOptionsFile(t, map[string]any{"panel": "https://panel.example.test", "core": "bin/xray", "state_dir": "state"})
	options, err := parseRuntimeOptions([]string{"-config=" + path}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if options.Config.Engine != "xray" || options.PollInterval != time.Second || options.Config.Core != filepath.Join(filepath.Dir(path), "bin/xray") || options.Config.StateDir != filepath.Join(filepath.Dir(path), "state") {
		t.Fatalf("partial config defaults/relative paths incorrect: %+v", options)
	}
}

func TestRuntimeConfigRejectsSecretsTyposAndUnsafeSettings(t *testing.T) {
	for _, test := range []struct {
		name, field string
		value       any
		want        string
	}{
		{"inline token", "token", "private-inline-token", "must not contain token"},
		{"null inline token", "token", nil, "must not contain token"},
		{"unknown field", "poll_intervel", "1s", "unknown field"},
		{"public plaintext panel", "panel", "http://panel.example.test", "requires HTTPS"},
		{"malformed polling", "poll_interval", "one second", "poll_interval"},
		{"polling too slow", "poll_interval", "6s", "between 100ms and 5s"},
		{"unsupported engine", "engine", "unknown", "engine"},
		{"invalid bind", "bind", "node.example.test", "IP address"},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := validFileOptions()
			data[test.field] = test.value
			path := writeOptionsFile(t, data)
			_, err := parseRuntimeOptions([]string{"-config", path}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("wanted %q error, got %v", test.want, err)
			}
			if strings.Contains(err.Error(), "private-inline-token") {
				t.Fatal("configuration error exposed a credential")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "node.json")
	for _, raw := range []string{"null", "{} {}", "{", "[]"} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := parseRuntimeOptions([]string{"-config", path}, io.Discard); err == nil {
			t.Fatalf("accepted malformed/non-object config: %s", raw)
		}
	}
	if _, err := parseRuntimeOptions([]string{"-core=/usr/local/bin/xray", "-state=state", "-panel=https://panel.example.test", "extra"}, io.Discard); err == nil {
		t.Fatal("accepted positional argument that could hide a configuration mistake")
	}
}

func TestRuntimeNodeTokenFilePriorityAndEnvironmentFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.token")
	if err := os.WriteFile(path, []byte("file-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	options := runtimeOptions{TokenFile: path}
	if token, err := options.nodeToken("environment-secret"); err != nil || token != "file-secret" {
		t.Fatalf("token file did not override environment: %v", err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := options.nodeToken("environment-secret"); err == nil {
		t.Fatal("empty explicitly selected token file silently fell back to environment")
	}
	options.TokenFile = ""
	if token, err := options.nodeToken(" environment-secret\n"); err != nil || token != "environment-secret" {
		t.Fatalf("environment fallback failed: %v", err)
	}
	if _, err := options.nodeToken(""); err == nil {
		t.Fatal("accepted missing node credentials")
	}
}
