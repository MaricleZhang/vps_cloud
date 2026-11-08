package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

type Node struct {
	ID            int64  `json:"id"`
	Protocol      string `json:"protocol"`
	TLS           bool   `json:"tls"`
	ServerName    string `json:"server_name"`
	Engine        string `json:"agent_engine"`
	Cipher        string `json:"cipher"`
	PortStart     int    `json:"port_start"`
	PortEnd       int    `json:"port_end"`
	MultiplierPPM int64  `json:"multiplier_ppm"`
}
type User struct {
	ID        int64  `json:"user_id"`
	Port      int    `json:"port"`
	UUID      string `json:"uuid"`
	Password  string `json:"password"`
	StatsName string `json:"stats_name"`
	CycleID   string `json:"cycle_id"`
	Version   string `json:"policy_version"`
	Enabled   bool   `json:"enabled"`
	Reason    string `json:"reason"`
}
type Policy struct {
	Node         Node   `json:"node"`
	LeaseSeconds int    `json:"lease_seconds"`
	Users        []User `json:"users"`
}
type Sample struct {
	UserID    int64  `json:"user_id"`
	CycleID   string `json:"cycle_id"`
	SessionID string `json:"session_id"`
	Upload    int64  `json:"upload_bytes"`
	Download  int64  `json:"download_bytes"`
}

func (s Sample) key() string { return fmt.Sprintf("%d/%s/%s", s.UserID, s.CycleID, s.SessionID) }

type Ack struct {
	UserID  int64  `json:"user_id"`
	Version string `json:"policy_version"`
	Enabled bool   `json:"enabled"`
	Error   string `json:"error"`
}

func newSession() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (p Policy) validate(engine string) error {
	if p.Node.ID <= 0 || p.LeaseSeconds < 1 || p.LeaseSeconds > 15 {
		return fmt.Errorf("invalid node or policy lease (maximum 15s)")
	}
	if p.Node.Engine != "" && p.Node.Engine != engine {
		return fmt.Errorf("policy engine %q does not match %q", p.Node.Engine, engine)
	}
	if p.Node.PortStart < 1 || p.Node.PortEnd > 65535 || p.Node.PortStart > p.Node.PortEnd {
		return fmt.Errorf("invalid managed port range")
	}
	ids, ports := map[int64]bool{}, map[int]bool{}
	for _, u := range p.Users {
		if u.ID <= 0 || ids[u.ID] || u.CycleID == "" || u.Version == "" || u.StatsName == "" || strings.Contains(u.StatsName, ">>>") {
			return fmt.Errorf("invalid user policy for user %d", u.ID)
		}
		ids[u.ID] = true
		if !u.Enabled && u.Port == 0 {
			continue
		}
		if u.Port < p.Node.PortStart || u.Port > p.Node.PortEnd || ports[u.Port] {
			return fmt.Errorf("invalid or duplicate user port for user %d", u.ID)
		}
		ports[u.Port] = true
	}
	return nil
}
