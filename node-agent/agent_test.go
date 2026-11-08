package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// The helper is a real dedicated child process with an echo socket and a h2c
// StatsService. Killing it must close an already-established connection.
func TestKernelHelper(t *testing.T) {
	if os.Getenv("NICECLOUD_TEST_KERNEL") != "1" {
		return
	}
	file := os.Args[len(os.Args)-1]
	raw, err := os.ReadFile(file)
	if err != nil {
		os.Exit(2)
	}
	var cfg struct {
		API struct {
			Listen string `json:"listen"`
		} `json:"api"`
		Inbounds []struct {
			Listen string `json:"listen"`
			Port   int    `json:"port"`
			Tag    string `json:"tag"`
		} `json:"inbounds"`
	}
	if json.Unmarshal(raw, &cfg) != nil || len(cfg.Inbounds) != 1 {
		os.Exit(3)
	}
	var up, down atomic.Int64
	inbound, err := net.Listen("tcp", net.JoinHostPort(cfg.Inbounds[0].Listen, fmt.Sprint(cfg.Inbounds[0].Port)))
	if err != nil {
		os.Exit(4)
	}
	go func() {
		for {
			conn, e := inbound.Accept()
			if e != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 4096)
				for {
					n, e := conn.Read(buf)
					if n > 0 {
						up.Add(int64(n))
						written, _ := conn.Write(buf[:n])
						down.Add(int64(written))
					}
					if e != nil {
						return
					}
				}
			}()
		}
	}()
	stats, err := net.Listen("tcp", cfg.API.Listen)
	if err != nil {
		os.Exit(5)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if len(b) != 5 || b[0] != 0 || binary.BigEndian.Uint32(b[1:]) != 0 {
			os.Exit(6)
		}
		var body []byte
		for _, pair := range []struct {
			name  string
			value int64
		}{{"uplink", up.Load()}, {"downlink", down.Load()}} {
			name := "user>>>" + cfg.Inbounds[0].Tag + ">>>traffic>>>" + pair.name
			stat := append([]byte{10}, binary.AppendUvarint(nil, uint64(len(name)))...)
			stat = append(stat, []byte(name)...)
			stat = append(stat, 16)
			stat = binary.AppendUvarint(stat, uint64(pair.value))
			body = append(body, 10)
			body = binary.AppendUvarint(body, uint64(len(stat)))
			body = append(body, stat...)
		}
		frame := make([]byte, 5)
		binary.BigEndian.PutUint32(frame[1:], uint32(len(body)))
		frame = append(frame, body...)
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		w.Write(frame)
		w.Header().Set("Grpc-Status", "0")
	})
	for {
		conn, e := stats.Accept()
		if e != nil {
			return
		}
		go (&http2.Server{}).ServeConn(conn, &http2.ServeConnOpts{Handler: handler})
	}
}
func helperCore(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fake-xray")
	// Executable paths are quoted as one POSIX shell word.
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\nNICECLOUD_TEST_KERNEL=1 exec " + quote(exe) + " -test.run=TestKernelHelper -- \"$@\"\n"
	if err = os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
func unusedPort(t *testing.T) int {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}
func testPolicy(t *testing.T) Policy {
	return Policy{Node: Node{ID: 1, Engine: "xray", Protocol: "vless", PortStart: 2000, PortEnd: 65535, MultiplierPPM: 1000000}, LeaseSeconds: 2, Users: []User{{ID: 2, Port: unusedPort(t), UUID: "00112233-4455-6677-8899-aabbccddeeff", StatsName: "user-2", CycleID: "cycle1", Version: "enabled-v1", Enabled: true, Reason: "active"}}}
}
func waitFor(t *testing.T, limit time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for expected state")
}
func newTestAgent(t *testing.T, serverURL string) *Agent {
	t.Helper()
	store, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	client, err := newPanelClient(serverURL, "secret-token")
	if err != nil {
		t.Fatal(err)
	}
	return &Agent{Config: Config{Engine: "xray", Core: helperCore(t), StateDir: store.dir, Bind: "127.0.0.1"}, Panel: client, Store: store}
}
func startTestAgent(t *testing.T, a *Agent) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.run(ctx, 100*time.Millisecond); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Error("agent shutdown timed out")
		}
	})
	return cancel
}
func TestQuotaClosesExistingConnectionAndDoesNotRestartDuringMetering(t *testing.T) {
	policy := testPolicy(t)
	var mu sync.Mutex
	var enabledAck, disabledAck bool
	var firstSession string
	var sessionChanged bool
	var maxTraffic int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Error("missing node authentication")
			w.WriteHeader(401)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/node/policy":
			json.NewEncoder(w).Encode(map[string]any{"data": policy})
		case "/api/node/traffic":
			var body struct {
				Samples []Sample `json:"samples"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			for _, s := range body.Samples {
				if firstSession == "" {
					firstSession = s.SessionID
				} else if firstSession != s.SessionID {
					sessionChanged = true
				}
				if s.Upload+s.Download > maxTraffic {
					maxTraffic = s.Upload + s.Download
				}
			}
			if maxTraffic >= 4096 {
				policy.Users[0].Enabled = false
				policy.Users[0].Version = "quota-v2"
				policy.Users[0].Reason = "quota_exceeded"
			}
			// The actual backend sends changing usage fields alongside stable Version.
			raw, _ := json.Marshal(policy)
			var dto map[string]any
			json.Unmarshal(raw, &dto)
			dto["users"].([]any)[0].(map[string]any)["traffic_used_bytes"] = maxTraffic
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"accepted": len(body.Samples), "policy": dto}})
		case "/api/node/ack":
			var body struct {
				Users []Ack `json:"users"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			for _, u := range body.Users {
				if u.Enabled && u.Error == "" {
					enabledAck = true
				}
				if !u.Enabled && u.Version == "quota-v2" && u.Error == "" {
					disabledAck = true
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ok": true}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	a := newTestAgent(t, server.URL)
	startTestAgent(t, a)
	waitFor(t, 4*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return enabledAck })
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", policy.Users[0].Port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	payload := strings.Repeat("a", 512)
	for i := 0; i < 2; i++ {
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err = conn.Write([]byte(payload)); err != nil {
			t.Fatal(err)
		}
		reply := make([]byte, len(payload))
		if _, err = io.ReadFull(conn, reply); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	mu.Lock()
	changed := sessionChanged
	mu.Unlock()
	if changed {
		t.Fatal("ordinary metering restarted the kernel")
	}
	conn.Write([]byte(strings.Repeat("b", 1024)))
	reply := make([]byte, 1024)
	io.ReadFull(conn, reply)
	waitFor(t, 4*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return disabledAck })
	requireConnectionClosed(t, conn, time.Second)
	a.Store.mu.Lock()
	nodeID := a.Store.state.NodeID
	a.Store.mu.Unlock()
	if nodeID != 1 {
		t.Fatal("node identity was not persisted")
	}
}
func TestWatchdogStopsConnectionsWhilePanelRequestIsBlocked(t *testing.T) {
	policy := testPolicy(t)
	var offline, enabledAck atomic.Bool
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		switch r.URL.Path {
		case "/api/node/traffic":
			var body struct {
				Samples []Sample `json:"samples"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"accepted": len(body.Samples), "policy": policy}})
		case "/api/node/ack":
			var body struct {
				Users []Ack `json:"users"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			for _, u := range body.Users {
				if u.Enabled && u.Error == "" {
					enabledAck.Store(true)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ok": true}})
		default:
			json.NewEncoder(w).Encode(map[string]any{"data": policy})
		}
	}))
	defer func() { close(release); server.Close() }()
	a := newTestAgent(t, server.URL)
	startTestAgent(t, a)
	waitFor(t, 4*time.Second, enabledAck.Load)
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", policy.Users[0].Port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	offline.Store(true)
	started := time.Now()
	requireConnectionClosed(t, conn, 2800*time.Millisecond)
	if time.Since(started) > 2600*time.Millisecond {
		t.Fatal("lease watchdog was blocked by network request")
	}
}

func requireConnectionClosed(t *testing.T, conn net.Conn, limit time.Duration) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(limit))
	_, err := conn.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("proxy left the established connection open")
	}
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("proxy did not close connection before deadline")
	}
}
func echoBytes(t *testing.T, conn net.Conn, size int) {
	t.Helper()
	conn.SetDeadline(time.Now().Add(time.Second))
	payload := strings.Repeat("m", size)
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, size)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != payload {
		t.Fatal("unexpected echo payload")
	}
	conn.SetDeadline(time.Time{})
}
func TestTrafficReportFailureStopsDespiteHealthyPolicy(t *testing.T) {
	policy := testPolicy(t)
	var failedReports, enabledAck atomic.Bool
	var healthyGETs, rejectedReports atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/node/policy":
			healthyGETs.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"data": policy})
		case "/api/node/traffic":
			if failedReports.Load() {
				rejectedReports.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			var body struct {
				Samples []Sample `json:"samples"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"accepted": len(body.Samples), "policy": policy}})
		case "/api/node/ack":
			var body struct {
				Users []Ack `json:"users"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			for _, u := range body.Users {
				if u.Enabled && u.Error == "" {
					enabledAck.Store(true)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ok": true}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	a := newTestAgent(t, server.URL)
	startTestAgent(t, a)
	waitFor(t, 4*time.Second, enabledAck.Load)
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", policy.Users[0].Port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	baselineGETs := healthyGETs.Load()
	failedReports.Store(true)
	echoBytes(t, conn, 1024)
	requireConnectionClosed(t, conn, 3500*time.Millisecond)
	if healthyGETs.Load() <= baselineGETs || rejectedReports.Load() < 2 {
		t.Fatal("test did not exercise repeated healthy policy and failed reports")
	}
	pending := a.Store.pending()
	var recorded int64
	for _, sample := range pending {
		recorded += sample.Upload + sample.Download
	}
	if recorded < 2048 {
		t.Fatalf("failed reports lost captured bytes: %d", recorded)
	}
}
func TestPolicyOutageStillPersistsTrafficOutbox(t *testing.T) {
	policy := testPolicy(t)
	var offline, enabledAck atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/api/node/traffic":
			var body struct {
				Samples []Sample `json:"samples"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"accepted": len(body.Samples), "policy": policy}})
		case "/api/node/ack":
			var body struct {
				Users []Ack `json:"users"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			for _, u := range body.Users {
				if u.Enabled && u.Error == "" {
					enabledAck.Store(true)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ok": true}})
		default:
			json.NewEncoder(w).Encode(map[string]any{"data": policy})
		}
	}))
	defer server.Close()
	a := newTestAgent(t, server.URL)
	startTestAgent(t, a)
	waitFor(t, 4*time.Second, enabledAck.Load)
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", policy.Users[0].Port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	offline.Store(true)
	echoBytes(t, conn, 2048)
	waitFor(t, 1500*time.Millisecond, func() bool {
		raw, err := os.ReadFile(filepath.Join(a.Store.dir, "state.json"))
		if err != nil {
			return false
		}
		var state State
		if json.Unmarshal(raw, &state) != nil {
			return false
		}
		var up, down int64
		for _, sample := range state.Outbox {
			if sample.UserID == 2 {
				up += sample.Upload
				down += sample.Download
			}
		}
		return up >= 2048 && down >= 2048
	})
	requireConnectionClosed(t, conn, 3*time.Second)
}
func TestPolicyAcceptsDisabledPortExhaustionWithoutDisablingOtherUsers(t *testing.T) {
	policy := testPolicy(t)
	for _, id := range []int64{3, 4} {
		policy.Users = append(policy.Users, User{ID: id, Port: 0, StatsName: fmt.Sprintf("user-%d", id), CycleID: "cycle1", Version: "no-port", Enabled: false, Reason: "port_exhausted"})
	}
	if err := policy.validate("xray"); err != nil {
		t.Fatalf("disabled unallocated users rejected entire node policy: %v", err)
	}
	policy.Users[1].Enabled = true
	if err := policy.validate("xray"); err == nil {
		t.Fatal("enabled user accepted with no allocated port")
	}
}
func TestDurableOutboxCoalescesAndKeepsNewerSamples(t *testing.T) {
	dir := t.TempDir()
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	sample := Sample{UserID: 2, CycleID: "cycle1", SessionID: "session1", Upload: 1, Download: 2}
	if err = s.capture(sample); err != nil {
		t.Fatal(err)
	}
	sent := s.pending()
	sample.Upload = 5
	s.capture(sample)
	s.accepted(sent)
	if got := s.pending(); len(got) != 1 || got[0].Upload != 5 {
		t.Fatal("ACK discarded a newer cumulative sample")
	}
	s.Close()
	s, err = openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.pending(); len(got) != 1 || got[0].Upload != 5 {
		t.Fatal("outbox did not survive restart")
	}
	if err = s.bindNode(1); err != nil {
		t.Fatal(err)
	}
	if err = s.bindNode(2); err == nil {
		t.Fatal("state accepted different node identity")
	}
}
func TestConfigRejectsUnsafeAndUnsupportedSettings(t *testing.T) {
	p := testPolicy(t)
	c := Config{Engine: "xray", Bind: "127.0.0.1"}
	if _, _, err := kernelConfig(c, p.Node, p.Users[0], "127.0.0.1:20000"); err != nil {
		t.Fatal(err)
	}
	p.Node.TLS = true
	if _, _, err := kernelConfig(c, p.Node, p.Users[0], "127.0.0.1:20000"); err == nil {
		t.Fatal("TLS policy started without certificate")
	}
	if _, err := newPanelClient("http://public.example", "token"); err == nil {
		t.Fatal("plaintext public panel accepted")
	}
	if _, err := newPanelClient("https://panel.example", ""); err == nil {
		t.Fatal("empty token accepted")
	}
}

func TestStopFailureRetainsWorkerAndNeverAcknowledgesStopped(t *testing.T) {
	policy := testPolicy(t)
	store, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := &Agent{Config: Config{Engine: "xray"}, Store: store}
	w := &Worker{User: policy.Users[0], Node: policy.Node, done: make(chan struct{}), stopFn: func() error { return errors.New("injected signal denied") }}
	// Simulate a capture already in progress; stopping must not wait for it.
	w.sampleMu.Lock()
	defer w.sampleMu.Unlock()
	defer close(w.done)
	a.workers.Store(w.User.ID, w)
	policy.Users[0].Enabled = false
	policy.Users[0].Reason = "quota_exceeded"
	policy.Users[0].Version = "stop-v2"
	if err := a.apply(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	got, ok := a.workers.Load(w.User.ID)
	if !ok || got != w {
		t.Fatal("failed stop discarded the live process handle")
	}
	ack := a.acks()
	if len(ack) != 1 || !ack[0].Enabled || ack[0].Error == "" {
		t.Fatalf("failed stop falsely acknowledged stopped: %+v", ack)
	}
	a.leaseUntil.Store(time.Now().Add(-time.Second).UnixNano())
	a.reportUntil.Store(time.Now().Add(-time.Second).UnixNano())
	ack = a.acks()
	if !ack[0].Enabled || ack[0].Error == "" {
		t.Fatalf("lease expiry hid a still-live process: %+v", ack)
	}
	// A subsequent replacement policy also must not lose the existing process.
	policy.Users[0].Enabled = true
	policy.Users[0].Reason = "active"
	policy.Users[0].Version = "restart-v3"
	if err := a.apply(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	got, ok = a.workers.Load(w.User.ID)
	if !ok || got != w {
		t.Fatal("replacement policy discarded process after failed stop")
	}
	// A live process from an earlier policy cannot confirm the replacement,
	// even if its desired enabled flag happens to match and no error is cached.
	a.status.Delete(w.User.ID)
	a.leaseUntil.Store(time.Now().Add(time.Second).UnixNano())
	a.reportUntil.Store(time.Now().Add(time.Second).UnixNano())
	ack = a.acks()
	if !ack[0].Enabled || ack[0].Error == "" {
		t.Fatalf("old process falsely confirmed replacement policy: %+v", ack)
	}
}
