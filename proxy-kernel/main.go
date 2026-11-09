// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singJSON "github.com/sagernet/sing/common/json"
)

const upstreamVersion = "1.12.12"

func main() {
	if err := execute(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "nicecloud-kernel:", err)
		os.Exit(1)
	}
}

// This adapter owns command-line parsing and process lifetime only. Protocols,
// REALITY, Vision, ECH and traffic statistics all come from the upstream module.
func execute(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("use run -c FILE, check -c FILE, version, or generate <key type>")
	}
	switch args[0] {
	case "version", "--version":
		if len(args) != 1 {
			return errors.New("version does not accept arguments")
		}
		return json.NewEncoder(stdout).Encode(map[string]any{
			"name": "nicecloud-kernel", "upstream": "github.com/sagernet/sing-box",
			"upstream_version": upstreamVersion, "with_utls": hasUTLS,
			"with_v2ray_api": hasV2RayAPI,
		})
	case "generate":
		return generate(args[1:], stdout, stderr)
	case "run", "check":
		if !hasUTLS || !hasV2RayAPI {
			return errors.New("build with -tags with_utls,with_v2ray_api for REALITY and safe user metering")
		}
		fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
		fs.SetOutput(stderr)
		var path string
		fs.StringVar(&path, "c", "", "sing-box JSON configuration path")
		fs.StringVar(&path, "config", "", "sing-box JSON configuration path")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if path == "" || fs.NArg() != 0 {
			return errors.New("provide one configuration using -c FILE")
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return service(ctx, path, args[0] == "check")
	case "-h", "--help", "help":
		_, err := fmt.Fprintln(stdout, "nicecloud-kernel run -c FILE | check -c FILE | version | generate reality-keypair|ech-keypair --out DIR [--server-name NAME]")
		return err
	default:
		return errors.New("unknown command; use run, check, version, or generate")
	}
}

func service(ctx context.Context, path string, checkOnly bool) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read configuration: %w", err)
	}
	if len(raw) > 8<<20 {
		return errors.New("kernel configuration exceeds 8 MiB")
	}
	ctx = include.Context(ctx)
	options, err := singJSON.UnmarshalExtendedContext[option.Options](ctx, raw)
	if err != nil {
		return fmt.Errorf("parse configuration: %w", err)
	}
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		return fmt.Errorf("initialize upstream kernel: %w", err)
	}
	if checkOnly {
		return instance.Close()
	}
	if err := instance.Start(); err != nil {
		return fmt.Errorf("start upstream kernel: %w", err)
	}
	<-ctx.Done()
	return instance.Close()
}
