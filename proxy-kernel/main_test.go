// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionReportsPinnedUpstreamAndCompiledCapabilities(t *testing.T) {
	var output bytes.Buffer
	if err := execute([]string{"version"}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(output.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["upstream"] != "github.com/sagernet/sing-box" || metadata["upstream_version"] != "1.12.12" || metadata["with_utls"] != hasUTLS || metadata["with_v2ray_api"] != hasV2RayAPI {
		t.Fatal("incorrect upstream version or capability metadata")
	}
}

func TestCheckBuildsActualUpstreamUserAndStatisticsConfiguration(t *testing.T) {
	if !hasUTLS || !hasV2RayAPI {
		t.Skip("requires production build tags with_utls,with_v2ray_api")
	}
	path := filepath.Join(t.TempDir(), "server.json")
	config := `{"log":{"disabled":true},"inbounds":[{"type":"vless","tag":"user-2","listen":"127.0.0.1","listen_port":20000,"users":[{"name":"user-2","uuid":"00112233-4455-6677-8899-aabbccddeeff"}]}],"outbounds":[{"type":"direct","tag":"direct"}],"experimental":{"v2ray_api":{"listen":"127.0.0.1:10085","stats":{"enabled":true,"users":["user-2"]}}}}`
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := execute([]string{"check", "-c", path}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	invalid := strings.Replace(config, `"type":"vless"`, `"type":"unsupported-reference-protocol"`, 1)
	if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
		t.Fatal(err)
	}
	if err := service(context.Background(), path, true); err == nil {
		t.Fatal("unsupported upstream protocol was accepted")
	}
}
