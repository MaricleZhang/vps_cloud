package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// Delay a statistics failure until the caller has explicitly signalled its
// worker. This reproduces EOF/reset racing with cmd.Wait without timing guesses.
func blockedFailingStats(t *testing.T) (string, <-chan struct{}, chan struct{}) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		once.Do(func() { close(entered) })
		<-release
		w.WriteHeader(http.StatusServiceUnavailable)
	}), &http2.Server{}))
	t.Cleanup(func() { statsHTTP.CloseIdleConnections(); server.Close() })
	return server.Listener.Addr().String(), entered, release
}

func TestInFlightStatsFailureDistinguishesRequestedStop(t *testing.T) {
	for _, intentional := range []bool{true, false} {
		name := "real_statistics_failure_stays_blocked"
		if intentional {
			name = "requested_stop_can_restart_same_policy"
		}
		t.Run(name, func(t *testing.T) {
			policy := testPolicy(t)
			store, err := openStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			a := &Agent{Config: Config{Engine: "xray", Core: helperCore(t), StateDir: store.dir, Bind: "127.0.0.1"}, Store: store, policy: policy}
			defer a.stopAll()
			a.leaseUntil.Store(time.Now().Add(time.Minute).UnixNano())
			a.reportUntil.Store(time.Now().Add(time.Minute).UnixNano())
			addr, entered, release := blockedFailingStats(t)
			var processExit sync.Once
			w := &Worker{User: policy.Users[0], Node: policy.Node, StatsAddr: addr, Engine: "xray", Prefix: "user>>>user-2", done: make(chan struct{})}
			finish := func() { processExit.Do(func() { close(w.done) }) }
			defer finish()
			w.stopFn = func() error {
				if !intentional {
					finish()
				}
				return nil
			}
			a.workers.Store(w.User.ID, w)
			sampled := make(chan struct{})
			go func() { a.sampleWorker(context.Background(), w); close(sampled) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				close(release)
				t.Fatal("statistics RPC did not enter the controlled failure")
			}
			if intentional {
				// stopSignal marks intent before the RPC fails, while Wait has
				// deliberately not closed done and fresh leases are already valid.
				a.killAll()
				if !w.alive() || !w.stopRequested.Load() || !a.leased() {
					t.Fatal("test did not reproduce a signalled process before Wait")
				}
				ack := a.acks()[0]
				if !ack.Enabled || ack.Error == "" {
					t.Fatal("pending stop was falsely acknowledged online or stopped")
				}
			}
			close(release)
			select {
			case <-sampled:
			case <-time.After(time.Second):
				t.Fatal("statistics failure did not finish")
			}
			failure := a.failure(w.User.ID)
			if intentional && failure.Version != "" {
				t.Fatalf("requested stop permanently blocked current policy: %+v", failure)
			}
			if !intentional && (failure.Version != w.User.Version || failure.Message == "" || w.alive()) {
				t.Fatalf("real statistics failure did not stop and block policy: %+v", failure)
			}
			finish()
			if err := a.apply(context.Background(), policy); err != nil {
				t.Fatal(err)
			}
			if !intentional {
				if _, running := a.workers.Load(w.User.ID); running {
					t.Fatal("same policy restarted after a genuine statistics failure")
				}
				policy.Users[0].Version = "operator-recovery-v2"
				if err := a.apply(context.Background(), policy); err != nil {
					t.Fatal(err)
				}
			}
			restarted, ok := a.workers.Load(w.User.ID)
			if !ok || restarted == w || !restarted.(*Worker).alive() {
				t.Fatal("worker did not recover when policy allowed recovery")
			}
			ack := a.acks()[0]
			if !ack.Enabled || ack.Error != "" {
				t.Fatalf("recovered worker could not acknowledge policy: %+v", ack)
			}
		})
	}
}

func TestRequestedStopDuringStartupDoesNotLatchFailure(t *testing.T) {
	policy := testPolicy(t)
	store, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	core := helperCore(t)
	delayed := filepath.Join(t.TempDir(), "delayed-kernel")
	quotedCore := "'" + strings.ReplaceAll(core, "'", "'\"'\"'") + "'"
	if err := os.WriteFile(delayed, []byte("#!/bin/sh\nsleep 5\nexec "+quotedCore+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a := &Agent{Config: Config{Engine: "xray", Core: delayed, StateDir: store.dir, Bind: "127.0.0.1"}, Store: store}
	defer a.stopAll()
	a.reportUntil.Store(time.Now().Add(time.Minute).UnixNano())
	applied := make(chan error, 1)
	go func() { applied <- a.apply(context.Background(), policy) }()
	waitFor(t, time.Second, func() bool { _, ok := a.workers.Load(policy.Users[0].ID); return ok })
	worker, _ := a.workers.Load(policy.Users[0].ID)
	w := worker.(*Worker)
	if w.readyForTraffic.Load() {
		t.Fatal("delayed kernel unexpectedly finished startup")
	}
	a.reportUntil.Store(time.Now().Add(-time.Second).UnixNano())
	a.killAll()
	select {
	case err := <-applied:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("requested stop did not interrupt startup")
	}
	if !w.stopRequested.Load() || w.alive() || a.failure(w.User.ID).Version != "" {
		t.Fatalf("normal startup interruption became permanent failure: %+v", a.failure(w.User.ID))
	}
	a.Config.Core = core
	a.reportUntil.Store(time.Now().Add(time.Minute).UnixNano())
	if err := a.apply(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	if ack := a.acks()[0]; !ack.Enabled || ack.Error != "" {
		t.Fatalf("same policy did not recover after startup interruption: %+v", ack)
	}
}

func TestRequestedStopSignalFailureRemainsVisible(t *testing.T) {
	w := &Worker{User: User{ID: 2, Version: "v1"}, done: make(chan struct{}), stopFn: func() error { return errors.New("signal denied") }}
	defer close(w.done)
	a := &Agent{policy: Policy{Users: []User{{ID: 2, Version: "v1", Enabled: false}}}}
	a.leaseUntil.Store(time.Now().Add(time.Minute).UnixNano())
	a.reportUntil.Store(time.Now().Add(time.Minute).UnixNano())
	a.workers.Store(w.User.ID, w)
	a.killAll()
	if !w.stopRequested.Load() || !w.alive() {
		t.Fatal("failed signal altered the actual process state")
	}
	ack := a.acks()[0]
	if !ack.Enabled || !strings.Contains(ack.Error, "signal denied") {
		t.Fatalf("failed stop was hidden by the stop intent marker: %+v", ack)
	}
}
