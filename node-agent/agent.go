package main

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

type workerFailure struct{ Version, Message string }
type Agent struct {
	Config      Config
	Panel       *PanelClient
	Store       *Store
	workers     sync.Map
	status      sync.Map // user ID -> workerFailure; metering errors block this version
	leaseUntil  atomic.Int64
	reportUntil atomic.Int64
	policy      Policy // owned by synchronization loop
}

func (a *Agent) leased() bool {
	at := time.Now().UnixNano()
	return at < a.leaseUntil.Load() && at < a.reportUntil.Load()
}
func (a *Agent) setFailure(w *Worker, err error) {
	a.status.Store(w.User.ID, workerFailure{Version: w.User.Version, Message: err.Error()})
}
func (a *Agent) failure(id int64) workerFailure {
	if v, ok := a.status.Load(id); ok {
		return v.(workerFailure)
	}
	return workerFailure{}
}
func (a *Agent) stopAll() {
	var wg sync.WaitGroup
	a.workers.Range(func(_, v any) bool {
		wg.Add(1)
		go func(w *Worker) {
			defer wg.Done()
			if err := w.stop(); err != nil {
				a.setFailure(w, err)
				log.Printf("stop user %d: %v", w.User.ID, err)
			}
		}(v.(*Worker))
		return true
	})
	wg.Wait()
}

// The hard watchdog only signals processes; it never waits for HTTP, stats,
// filesystem fsync, the sampler, or another user's process to exit.
func (a *Agent) killAll() {
	a.workers.Range(func(_, v any) bool {
		w := v.(*Worker)
		if err := w.stopSignal(); err != nil {
			a.setFailure(w, err)
			log.Printf("kill user %d: %v", w.User.ID, err)
		}
		return true
	})
}
func (a *Agent) watchdog(ctx context.Context) {
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			a.killAll()
			return
		case <-t.C:
			deadline := a.leaseUntil.Load()
			if report := a.reportUntil.Load(); report < deadline {
				deadline = report
			}
			remaining := time.Until(time.Unix(0, deadline))
			if remaining <= 0 {
				a.killAll()
				continue
			}
			if remaining <= 250*time.Millisecond {
				// Best effort final capture runs separately and cannot delay hard expiry.
				a.workers.Range(func(_, v any) bool {
					w := v.(*Worker)
					go func() {
						captureCtx, cancel := context.WithDeadline(ctx, time.Unix(0, deadline))
						defer cancel()
						w.tryCapture(captureCtx, a.Store)
					}()
					return true
				})
			}
		}
	}
}
func (a *Agent) sampler(ctx context.Context, poll time.Duration) {
	t := time.NewTicker(poll)
	defer t.Stop()
	var running sync.WaitGroup
	defer running.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.workers.Range(func(_, v any) bool {
				w := v.(*Worker)
				if !w.alive() || !w.readyForTraffic.Load() {
					return true
				}
				running.Add(1)
				go func() {
					defer running.Done()
					captureCtx, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
					defer cancel()
					if err := w.tryCapture(captureCtx, a.Store); err != nil && ctx.Err() == nil {
						a.setFailure(w, err)
						if stopErr := w.stop(); stopErr != nil {
							a.setFailure(w, stopErr)
						}
						log.Printf("user %d stopped because metering failed: %v", w.User.ID, err)
					}
				}()
				return true
			})
		}
	}
}
func (a *Agent) capture(w *Worker) error {
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	return w.capture(ctx, a.Store)
}
func (a *Agent) stopWorker(w *Worker) error {
	if w.alive() {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		err := w.tryCapture(ctx, a.Store)
		cancel()
		if err != nil {
			log.Printf("final stats user %d: %v", w.User.ID, err)
		}
	}
	if err := w.stop(); err != nil {
		a.setFailure(w, err)
		return err
	}
	return nil
}
func (a *Agent) apply(ctx context.Context, p Policy) error {
	if err := p.validate(a.Config.Engine); err != nil {
		return err
	}
	if err := a.Store.bindNode(p.Node.ID); err != nil {
		a.killAll()
		return err
	}
	a.leaseUntil.Store(time.Now().Add(time.Duration(p.LeaseSeconds) * time.Second).UnixNano())
	// The first process receives one reporting grace period. Thereafter GET
	// success alone cannot extend its right to run without delivering traffic.
	if a.reportUntil.Load() == 0 {
		a.reportUntil.Store(time.Now().Add(time.Duration(p.LeaseSeconds) * time.Second).UnixNano())
	}
	a.policy = p
	ids := map[int64]bool{}
	for _, u := range p.Users {
		ids[u.ID] = true
	}
	a.workers.Range(func(k, v any) bool {
		if !ids[k.(int64)] {
			if err := a.stopWorker(v.(*Worker)); err == nil {
				a.workers.Delete(k)
				a.status.Delete(k)
			}
		}
		return true
	})
	enabledUsers := false
	for _, u := range p.Users {
		var w *Worker
		if v, ok := a.workers.Load(u.ID); ok {
			w = v.(*Worker)
		}
		if !u.Enabled {
			if w != nil {
				if err := a.stopWorker(w); err != nil {
					continue
				}
			}
			a.status.Delete(u.ID)
			continue
		}
		enabledUsers = true
		if !a.leased() {
			continue
		}
		if w != nil && w.alive() && reflect.DeepEqual(w.User, u) && reflect.DeepEqual(w.Node, p.Node) {
			continue
		}
		if w != nil {
			if err := a.stopWorker(w); err != nil {
				continue
			}
			a.workers.Delete(u.ID)
		}
		if a.failure(u.ID).Version == u.Version {
			continue
		}
		a.status.Delete(u.ID)
		newWorker, err := spawnWorker(a.Config, p.Node, u, a.Store)
		if err == nil {
			a.workers.Store(u.ID, newWorker)
			readyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err = newWorker.ready(readyCtx, a.Config.Bind, a.Store)
			cancel()
			if err == nil && !a.leased() {
				err = fmt.Errorf("policy or traffic reporting lease expired during startup")
			}
			if err != nil {
				if stopErr := newWorker.stop(); stopErr != nil {
					err = fmt.Errorf("%v; stopping: %w", err, stopErr)
				}
			}
		}
		if err != nil {
			a.status.Store(u.ID, workerFailure{Version: u.Version, Message: err.Error()})
			log.Printf("user %d disabled: %v", u.ID, err)
		}
	}
	// Idle nodes do not need to submit synthetic traffic heartbeats. Preserve an
	// expired report deadline whenever unacknowledged samples still exist.
	if !enabledUsers && len(a.Store.pending()) == 0 {
		a.reportUntil.Store(0)
	}
	return nil
}
func (a *Agent) acks() []Ack {
	acks := make([]Ack, 0, len(a.policy.Users))
	for _, u := range a.policy.Users {
		enabled := false
		var running *Worker
		if v, ok := a.workers.Load(u.ID); ok {
			running = v.(*Worker)
			enabled = running.alive()
		}
		failure := a.failure(u.ID)
		message := ""
		if failure.Version == u.Version || (enabled && running.User.Version == failure.Version) {
			message = failure.Message
		}
		if enabled && running.User.Version != u.Version && message == "" {
			message = "running kernel still uses an earlier policy"
		}
		if enabled && !a.leased() {
			message = "policy or traffic reporting lease expired; kernel has not exited"
		}
		if enabled != u.Enabled && message == "" {
			message = "kernel state does not match policy"
		}
		acks = append(acks, Ack{UserID: u.ID, Version: u.Version, Enabled: enabled, Error: message})
	}
	return acks
}
func (a *Agent) step(ctx context.Context) error {
	p, err := a.Panel.policy(ctx)
	if err == nil {
		err = a.apply(ctx, p)
	}
	// A broken GET does not suppress local sampling or outbox retries; the
	// sampler runs independently and a traffic response itself includes policy.
	samples := a.Store.pending()
	if len(samples) > 0 {
		updated, reportErr := a.Panel.report(ctx, samples)
		if reportErr != nil {
			if err == nil {
				err = reportErr
			}
		} else {
			if validationErr := updated.validate(a.Config.Engine); validationErr != nil {
				a.killAll()
				return validationErr
			}
			if saveErr := a.Store.accepted(samples); saveErr != nil {
				a.killAll()
				return saveErr
			}
			a.reportUntil.Store(time.Now().Add(time.Duration(updated.LeaseSeconds) * time.Second).UnixNano())
			if applyErr := a.apply(ctx, updated); applyErr != nil {
				a.killAll()
				return applyErr
			}
		}
	}
	acks := a.acks()
	if len(acks) > 0 {
		if ackErr := a.Panel.ack(ctx, acks); ackErr != nil && err == nil {
			err = ackErr
		}
	}
	return err
}
func (a *Agent) run(ctx context.Context, poll time.Duration) error {
	watchCtx, cancelWatch := context.WithCancel(context.Background())
	defer cancelWatch()
	defer a.stopAll()
	sampleCtx, cancelSample := context.WithCancel(context.Background())
	sampleDone := make(chan struct{})
	go func() { a.sampler(sampleCtx, poll); close(sampleDone) }()
	defer func() { cancelSample(); <-sampleDone }()
	go a.watchdog(watchCtx)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			cancelSample()
			<-sampleDone
			a.workers.Range(func(_, v any) bool { a.stopWorker(v.(*Worker)); return true })
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
			if pending := a.Store.pending(); len(pending) > 0 {
				if _, err := a.Panel.report(shutdownCtx, pending); err == nil {
					a.Store.accepted(pending)
				}
			}
			shutdownCancel()
			return nil
		case <-timer.C:
			if err := a.step(ctx); err != nil {
				log.Printf("node synchronization: %v", err)
			}
			timer.Reset(poll)
		}
	}
}
