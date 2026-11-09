package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// runtimeOptions separates local deployment settings from centrally issued
// policy. A local file cannot set user credentials, quotas, or node identity.
type runtimeOptions struct {
	Config       Config
	Panel        string
	TokenFile    string
	PollInterval time.Duration
}

type fileOptions struct {
	Panel                 *string         `json:"panel"`
	Engine                *string         `json:"engine"`
	Core                  *string         `json:"core"`
	StateDir              *string         `json:"state_dir"`
	Bind                  *string         `json:"bind"`
	TokenFile             *string         `json:"token_file"`
	TLSCert               *string         `json:"tls_cert"`
	TLSKey                *string         `json:"tls_key"`
	RealityPrivateKeyFile *string         `json:"reality_private_key_file"`
	ECHKeyFile            *string         `json:"ech_key_file"`
	PollInterval          *string         `json:"poll_interval"`
	Token                 json.RawMessage `json:"token"`
}

func defaultRuntimeOptions() runtimeOptions {
	return runtimeOptions{
		Config:       Config{Engine: "xray", Bind: "0.0.0.0"},
		PollInterval: time.Second,
	}
}

// Precedence: explicitly supplied CLI flags > JSON file > built-in defaults.
// Parsing is isolated from the global FlagSet so tests and embedders can call it
// repeatedly. No files are created and no secrets are read during parsing.
func parseRuntimeOptions(args []string, output io.Writer) (runtimeOptions, error) {
	cli := defaultRuntimeOptions()
	fs := flag.NewFlagSet("node-agent", flag.ContinueOnError)
	fs.SetOutput(output)
	var configPath string
	fs.StringVar(&configPath, "config", "", "JSON deployment configuration file (explicit CLI flags override it)")
	fs.StringVar(&cli.Panel, "panel", "", "HTTPS panel root URL (HTTP only on loopback)")
	fs.StringVar(&cli.TokenFile, "token-file", "", "read node bearer token from this file instead of environment")
	fs.StringVar(&cli.Config.Engine, "engine", cli.Config.Engine, "xray or sing-box")
	fs.StringVar(&cli.Config.Core, "core", "", "absolute path to proxy kernel executable")
	fs.StringVar(&cli.Config.StateDir, "state", "", "private persistent state directory")
	fs.StringVar(&cli.Config.Bind, "bind", cli.Config.Bind, "public proxy bind IP")
	fs.StringVar(&cli.Config.TLSCert, "tls-cert", "", "TLS certificate PEM path (when TLS enabled)")
	fs.StringVar(&cli.Config.TLSKey, "tls-key", "", "TLS private key PEM path (when TLS enabled)")
	fs.StringVar(&cli.Config.RealityPrivateKeyFile, "reality-private-key-file", "", "local REALITY X25519 private key file")
	fs.StringVar(&cli.Config.ECHKeyFile, "ech-key-file", "", "local sing-box ECH KEYS PEM file")
	fs.DurationVar(&cli.PollInterval, "poll", cli.PollInterval, "policy and metering interval (100ms to 5s)")
	if err := fs.Parse(args); err != nil {
		return runtimeOptions{}, err
	}
	if fs.NArg() != 0 {
		return runtimeOptions{}, fmt.Errorf("unexpected positional arguments; use -config or named flags")
	}
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	options := defaultRuntimeOptions()
	if configPath != "" {
		if err := options.loadFile(configPath, explicit["poll"]); err != nil {
			return runtimeOptions{}, err
		}
	}
	for name := range explicit {
		switch name {
		case "panel":
			options.Panel = cli.Panel
		case "token-file":
			options.TokenFile = cli.TokenFile
		case "engine":
			options.Config.Engine = cli.Config.Engine
		case "core":
			options.Config.Core = cli.Config.Core
		case "state":
			options.Config.StateDir = cli.Config.StateDir
		case "bind":
			options.Config.Bind = cli.Config.Bind
		case "tls-cert":
			options.Config.TLSCert = cli.Config.TLSCert
		case "tls-key":
			options.Config.TLSKey = cli.Config.TLSKey
		case "reality-private-key-file":
			options.Config.RealityPrivateKeyFile = cli.Config.RealityPrivateKeyFile
		case "ech-key-file":
			options.Config.ECHKeyFile = cli.Config.ECHKeyFile
		case "poll":
			options.PollInterval = cli.PollInterval
		}
	}
	if err := options.validate(); err != nil {
		return runtimeOptions{}, err
	}
	return options, nil
}

func (options *runtimeOptions) loadFile(path string, cliPoll bool) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read node config: %w", err)
	}
	if len(raw) > 1<<20 {
		return fmt.Errorf("node config exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var file *fileOptions
	if err := decoder.Decode(&file); err != nil {
		return fmt.Errorf("invalid node config JSON: %w", err)
	}
	if file == nil {
		return fmt.Errorf("node config must be a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("node config must contain exactly one JSON object")
	}
	if len(file.Token) != 0 {
		return fmt.Errorf("node config must not contain token; use token_file or NICECLOUD_NODE_TOKEN")
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("resolve node config directory: %w", err)
	}
	set := func(target *string, value *string, isPath bool) {
		if value == nil {
			return
		}
		*target = *value
		if isPath && *target != "" && !filepath.IsAbs(*target) {
			*target = filepath.Join(base, *target)
		}
	}
	set(&options.Panel, file.Panel, false)
	set(&options.Config.Engine, file.Engine, false)
	set(&options.Config.Core, file.Core, true)
	set(&options.Config.StateDir, file.StateDir, true)
	set(&options.Config.Bind, file.Bind, false)
	set(&options.TokenFile, file.TokenFile, true)
	set(&options.Config.TLSCert, file.TLSCert, true)
	set(&options.Config.TLSKey, file.TLSKey, true)
	set(&options.Config.RealityPrivateKeyFile, file.RealityPrivateKeyFile, true)
	set(&options.Config.ECHKeyFile, file.ECHKeyFile, true)
	if file.PollInterval != nil && !cliPoll {
		poll, err := time.ParseDuration(*file.PollInterval)
		if err != nil {
			return fmt.Errorf("node config poll_interval must be a duration such as 1s or 250ms")
		}
		options.PollInterval = poll
	}
	return nil
}

func (options runtimeOptions) validate() error {
	if options.Config.Core == "" || !filepath.IsAbs(options.Config.Core) {
		return fmt.Errorf("core must be an absolute executable path (-core or config core)")
	}
	if options.Config.StateDir == "" {
		return fmt.Errorf("state directory is required (-state or config state_dir)")
	}
	if options.Config.Engine != "xray" && options.Config.Engine != "sing-box" {
		return fmt.Errorf("engine must be xray or sing-box")
	}
	if net.ParseIP(options.Config.Bind) == nil {
		return fmt.Errorf("bind must be an IP address")
	}
	if options.PollInterval < 100*time.Millisecond || options.PollInterval > 5*time.Second {
		return fmt.Errorf("poll interval must be between 100ms and 5s")
	}
	if (options.Config.TLSCert == "") != (options.Config.TLSKey == "") {
		return fmt.Errorf("provide both tls_cert and tls_key, or omit both")
	}
	// Reuse URL validation without reading credentials or making a request.
	if _, err := newPanelClient(options.Panel, "configuration-validation"); err != nil {
		return err
	}
	return nil
}

func (options runtimeOptions) nodeToken(environmentToken string) (string, error) {
	token := strings.TrimSpace(environmentToken)
	if options.TokenFile != "" {
		raw, err := os.ReadFile(options.TokenFile)
		if err != nil {
			return "", fmt.Errorf("read node token file: %w", err)
		}
		token = strings.TrimSpace(string(raw))
		if token == "" {
			return "", fmt.Errorf("node token file is empty")
		}
	}
	if token == "" {
		return "", fmt.Errorf("NICECLOUD_NODE_TOKEN or token_file/-token-file is required")
	}
	return token, nil
}
