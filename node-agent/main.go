package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	options, err := parseRuntimeOptions(os.Args[1:], os.Stdout)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Fatal(err)
	}
	token, err := options.nodeToken(os.Getenv("NICECLOUD_NODE_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}
	client, err := newPanelClient(options.Panel, token)
	if err != nil {
		log.Fatal(err)
	}
	store, err := openStore(options.Config.StateDir)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	agent := &Agent{Config: options.Config, Panel: client, Store: store}
	log.Printf("node agent starting engine=%s; metering every %s; lease expires within 15s", options.Config.Engine, options.PollInterval)
	if err = agent.run(ctx, options.PollInterval); err != nil {
		log.Fatal(err)
	}
}
