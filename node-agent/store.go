package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// A sample is cumulative for one process and cycle. Coalescing keeps retries
// bounded without discarding any bytes that the server has not yet accepted.
type State struct {
	NodeID    int64             `json:"node_id"`
	Outbox    map[string]Sample `json:"outbox"`
	Instances map[int64]Sample  `json:"instances"`
}
type Store struct {
	mu    sync.Mutex
	dir   string
	state State
	lock  *os.File
}

func openStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "agent.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another agent owns state directory: %w", err)
	}
	s := &Store{dir: dir, lock: f, state: State{Outbox: map[string]Sample{}, Instances: map[int64]Sample{}}}
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err == nil {
		err = json.Unmarshal(raw, &s.state)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.Close()
		return nil, err
	}
	if s.state.Outbox == nil {
		s.state.Outbox = map[string]Sample{}
	}
	if s.state.Instances == nil {
		s.state.Instances = map[int64]Sample{}
	}
	return s, nil
}
func (s *Store) Close() error { return s.lock.Close() }
func (s *Store) saveLocked() error {
	b, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".state-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(s.dir, "state.json")); err != nil {
		return err
	}
	d, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (s *Store) bindNode(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.NodeID != 0 && s.state.NodeID != id {
		return fmt.Errorf("state directory belongs to a different node; choose a new state directory")
	}
	s.state.NodeID = id
	return s.saveLocked()
}
func (s *Store) capture(sample Sample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.state.Outbox[sample.key()]
	if ok && (sample.Upload < old.Upload || sample.Download < old.Download) {
		return fmt.Errorf("kernel cumulative counters decreased")
	}
	// Persist both the session baseline and outbox before any network send.
	s.state.Instances[sample.UserID] = sample
	s.state.Outbox[sample.key()] = sample
	return s.saveLocked()
}
func (s *Store) pending() []Sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Sample{}
	for _, v := range s.state.Outbox {
		out = append(out, v)
		if len(out) >= 500 {
			break
		}
	}
	return out
}
func (s *Store) accepted(samples []Sample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sample := range samples {
		if s.state.Outbox[sample.key()] == sample {
			delete(s.state.Outbox, sample.key())
		}
	}
	return s.saveLocked()
}
