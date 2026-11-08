package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type Worker struct {
	User                      User
	Node                      Node
	Sample                    Sample
	Prefix, StatsAddr, Engine string
	cmd                       *exec.Cmd
	done                      chan struct{}
	stopMu                    sync.Mutex
	sampleMu                  sync.Mutex
	readyForTraffic           atomic.Bool
	stopFn                    func() error // optional test injection; nil sends the actual process-group signal
}

func (w *Worker) alive() bool {
	select {
	case <-w.done:
		return false
	default:
		return true
	}
}
func (w *Worker) stopSignal() error {
	w.stopMu.Lock()
	defer w.stopMu.Unlock()
	if !w.alive() {
		return nil
	}
	if w.stopFn != nil {
		return w.stopFn()
	}
	if w.cmd == nil || w.cmd.Process == nil {
		return fmt.Errorf("kernel process handle missing")
	}
	err := syscall.Kill(-w.cmd.Process.Pid, syscall.SIGKILL)
	if err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}
func (w *Worker) stop() error {
	if err := w.stopSignal(); err != nil {
		return err
	}
	select {
	case <-w.done:
		return nil
	case <-time.After(2 * time.Second):
		return fmt.Errorf("kernel did not exit after SIGKILL")
	}
}
func (w *Worker) capture(ctx context.Context, s *Store) error {
	w.sampleMu.Lock()
	defer w.sampleMu.Unlock()
	return w.captureLocked(ctx, s)
}
func (w *Worker) tryCapture(ctx context.Context, s *Store) error {
	if !w.sampleMu.TryLock() {
		return nil
	}
	defer w.sampleMu.Unlock()
	return w.captureLocked(ctx, s)
}
func (w *Worker) captureLocked(ctx context.Context, s *Store) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !w.alive() {
		return fmt.Errorf("kernel exited")
	}
	up, down, err := readStats(ctx, w.StatsAddr, w.Engine, w.Prefix)
	if err != nil {
		return err
	}
	if up < w.Sample.Upload || down < w.Sample.Download {
		return fmt.Errorf("kernel cumulative counters decreased")
	}
	sample := w.Sample
	sample.Upload = up
	sample.Download = down
	if err = s.capture(sample); err != nil {
		return fmt.Errorf("cannot durably record traffic: %w", err)
	}
	w.Sample = sample
	return nil
}
func spawnWorker(c Config, n Node, u User, s *Store) (*Worker, error) {
	addr, err := loopbackPort()
	if err != nil {
		return nil, err
	}
	b, prefix, err := kernelConfig(c, n, u, addr)
	if err != nil {
		return nil, err
	}
	sample := Sample{UserID: u.ID, CycleID: u.CycleID, SessionID: newSession()}
	if err = s.capture(sample); err != nil {
		return nil, err
	}
	configPath := filepath.Join(c.StateDir, fmt.Sprintf("user-%d.json", u.ID))
	if err = os.WriteFile(configPath, b, 0600); err != nil {
		return nil, err
	}
	args := []string{"run", "-config", configPath}
	if c.Engine == "sing-box" {
		args = []string{"run", "-c", configPath}
	}
	logFile, err := os.OpenFile(filepath.Join(c.StateDir, fmt.Sprintf("user-%d.log", u.ID)), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(c.Core, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureChild(cmd)
	if err = cmd.Start(); err != nil {
		logFile.Close()
		return nil, err
	}
	w := &Worker{User: u, Node: n, Sample: sample, Prefix: prefix, StatsAddr: addr, Engine: c.Engine, cmd: cmd, done: make(chan struct{})}
	go func() { cmd.Wait(); logFile.Close(); close(w.done) }()
	return w, nil
}
func (w *Worker) ready(ctx context.Context, bind string, s *Store) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		if !w.alive() {
			return fmt.Errorf("kernel exited during startup; inspect user-%d.log", w.User.ID)
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		last = w.capture(attemptCtx, s)
		cancel()
		if last == nil {
			host := bind
			if host == "0.0.0.0" {
				host = "127.0.0.1"
			}
			if host == "::" {
				host = "::1"
			}
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprint(w.User.Port)), 200*time.Millisecond)
			if err == nil {
				conn.Close()
				w.readyForTraffic.Store(true)
				return nil
			}
			last = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("kernel not ready: %v: %w", last, ctx.Err())
		case <-ticker.C:
		}
	}
}
