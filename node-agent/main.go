package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	var c Config
	panel := flag.String("panel", "", "HTTPS panel root URL (HTTP only on loopback)")
	tokenFile := flag.String("token-file", "", "read node bearer token from this file instead of environment")
	flag.StringVar(&c.Engine, "engine", "xray", "xray or sing-box")
	flag.StringVar(&c.Core, "core", "", "absolute path to proxy kernel executable")
	flag.StringVar(&c.StateDir, "state", "", "private persistent state directory")
	flag.StringVar(&c.Bind, "bind", "0.0.0.0", "public proxy bind IP")
	flag.StringVar(&c.TLSCert, "tls-cert", "", "TLS certificate PEM path (when TLS enabled)")
	flag.StringVar(&c.TLSKey, "tls-key", "", "TLS private key PEM path (when TLS enabled)")
	poll := flag.Duration("poll", time.Second, "policy and metering interval (100ms to 5s)")
	flag.Parse()
	if c.Core == "" || !filepath.IsAbs(c.Core) || c.StateDir == "" || (c.Engine != "xray" && c.Engine != "sing-box") || *poll < 100*time.Millisecond || *poll > 5*time.Second {
		log.Fatal("provide -core absolute executable, -state persistent directory, -engine xray|sing-box and -poll 100ms..5s")
	}
	token := os.Getenv("NICECLOUD_NODE_TOKEN")
	if *tokenFile != "" {
		raw, err := os.ReadFile(*tokenFile)
		if err != nil {
			log.Fatal(err)
		}
		token = strings.TrimSpace(string(raw))
	}
	client, err := newPanelClient(*panel, token)
	if err != nil {
		log.Fatal(err)
	}
	store, err := openStore(c.StateDir)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	agent := &Agent{Config: c, Panel: client, Store: store}
	log.Printf("node agent starting engine=%s; metering every %s; lease expires within 15s", c.Engine, *poll)
	if err = agent.run(ctx, *poll); err != nil {
		log.Fatal(err)
	}
}
