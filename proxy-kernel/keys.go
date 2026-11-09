// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	boxTLS "github.com/sagernet/sing-box/common/tls"
)

func generate(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "reality-keypair" && args[0] != "ech-keypair") {
		return errors.New("use generate reality-keypair or generate ech-keypair")
	}
	fs := flag.NewFlagSet("generate "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	var directory, keyPath, name string
	fs.StringVar(&directory, "out", "", "directory for the new private-key file")
	fs.StringVar(&keyPath, "private-key-file", "", "new private-key file path, instead of --out")
	fs.StringVar(&name, "server-name", "", "public ECH cover name (required for ECH)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || (directory == "") == (keyPath == "") {
		return errors.New("provide exactly one of --out DIR or --private-key-file PATH")
	}
	if directory != "" {
		filename := "reality.key"
		if args[0] == "ech-keypair" {
			filename = "ech.pem"
		}
		keyPath = filepath.Join(directory, filename)
	}
	var private []byte
	public := map[string]string{}
	if args[0] == "reality-keypair" {
		if name != "" {
			return errors.New("--server-name applies to ECH generation only")
		}
		key, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		private = []byte(base64.RawURLEncoding.EncodeToString(key.Bytes()) + "\n")
		shortID := make([]byte, 8)
		if _, err := rand.Read(shortID); err != nil {
			return err
		}
		public["reality_public_key"] = base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
		public["reality_short_id"] = hex.EncodeToString(shortID)
	} else {
		if !validServerName(name) {
			return errors.New("--server-name must be a DNS name without a port or wildcard")
		}
		configPEM, keyPEM, err := boxTLS.ECHKeygenDefault(name)
		if err != nil {
			return fmt.Errorf("upstream ECH key generation: %w", err)
		}
		block, rest := pem.Decode([]byte(configPEM))
		if block == nil || block.Type != "ECH CONFIGS" || len(strings.TrimSpace(string(rest))) != 0 {
			return errors.New("upstream ECH generator returned an invalid public configuration")
		}
		private = []byte(keyPEM)
		public["ech_config"] = base64.StdEncoding.EncodeToString(block.Bytes)
		public["ech_query_server_name"] = name
	}
	if err := writePrivateKey(keyPath, private); err != nil {
		return err
	}
	// Deliberately never print private-key material, even on a terminal.
	return json.NewEncoder(stdout).Encode(public)
}

func validServerName(name string) bool {
	if name == "" || len(name) > 253 || net.ParseIP(name) != nil || strings.ContainsAny(name, " \t\r\n/:*_") {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}

func writePrivateKey(path string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create private-key directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create private-key file (existing keys are never overwritten): %w", err)
	}
	success := false
	defer func() {
		file.Close()
		if !success {
			os.Remove(path)
		}
	}()
	if _, err := file.Write(contents); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	success = true
	return nil
}
