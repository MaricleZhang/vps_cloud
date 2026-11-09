package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"strings"
)

// Keys remain local to the node. Diagnostics deliberately exclude key bytes.
func readPrivateFile(path, kind string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("%s private key file is required", kind)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s private key file", kind)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s private key file must be a regular file", kind)
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("%s private key file must be readable only by its owner (0600 or 0400)", kind)
	}
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, fmt.Errorf("cannot read %s private key file within 1 MiB", kind)
	}
	return raw, nil
}

func loadRealityPrivateKey(path, publicKey, shortID string) (string, error) {
	if len(shortID) < 2 || len(shortID) > 16 || len(shortID)%2 != 0 {
		return "", fmt.Errorf("REALITY short ID must contain 2 to 16 hexadecimal characters in pairs")
	}
	if _, err := hex.DecodeString(shortID); err != nil {
		return "", fmt.Errorf("invalid REALITY short ID")
	}
	raw, err := readPrivateFile(path, "REALITY")
	if err != nil {
		return "", err
	}
	encoded := strings.TrimSpace(string(raw))
	privateBytes, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(privateBytes) != 32 {
		return "", fmt.Errorf("REALITY private key file must contain a 32-byte X25519 key in raw URL base64")
	}
	private, err := ecdh.X25519().NewPrivateKey(privateBytes)
	if err != nil {
		return "", fmt.Errorf("invalid REALITY private key")
	}
	expected, err := base64.RawURLEncoding.DecodeString(publicKey)
	if err != nil || len(expected) != 32 {
		return "", fmt.Errorf("invalid policy REALITY public key")
	}
	if subtle.ConstantTimeCompare(private.PublicKey().Bytes(), expected) != 1 {
		return "", fmt.Errorf("local REALITY private key does not match the policy public key")
	}
	return encoded, nil
}

// ECH KEYS is sing-box's documented PEM container: each record contains a
// uint16-length X25519 key and uint16-length public ECHConfig. We only validate
// this local key metadata; all ECH handshakes remain in the upstream TLS core.
func validateECHKeyFile(path, publicConfig string) error {
	raw, err := readPrivateFile(path, "ECH")
	if err != nil {
		return err
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "ECH KEYS" || len(bytes.TrimSpace(rest)) != 0 {
		return fmt.Errorf("ECH private key file must contain an ECH KEYS PEM block")
	}
	keys := map[string]bool{}
	for offset, count := 0, 0; offset < len(block.Bytes); count++ {
		if count >= 64 {
			return fmt.Errorf("ECH key file contains too many keys")
		}
		privateBytes, ok := takeVector16(block.Bytes, &offset)
		if !ok {
			return fmt.Errorf("invalid ECH private key container")
		}
		config, ok := takeVector16(block.Bytes, &offset)
		if !ok {
			return fmt.Errorf("invalid ECH key public configuration")
		}
		publicBytes, ok := echX25519PublicKey(config)
		if !ok {
			return fmt.Errorf("ECH requires valid draft-13 X25519 public configuration")
		}
		private, err := ecdh.X25519().NewPrivateKey(privateBytes)
		if err != nil || subtle.ConstantTimeCompare(private.PublicKey().Bytes(), publicBytes) != 1 {
			return fmt.Errorf("ECH private key does not match its public configuration")
		}
		keys[string(config)] = true
	}
	if len(keys) == 0 {
		return fmt.Errorf("ECH key file contains no keys")
	}
	list, err := base64.StdEncoding.DecodeString(publicConfig)
	if err != nil {
		list, err = base64.RawStdEncoding.DecodeString(publicConfig)
	}
	if err != nil || len(list) < 2 || len(list) > 65537 || int(binary.BigEndian.Uint16(list[:2])) != len(list)-2 {
		return fmt.Errorf("policy ECH configuration must be a base64 ECHConfigList")
	}
	for offset, count := 2, 0; ; count++ {
		if offset == len(list) {
			if count == 0 {
				return fmt.Errorf("policy ECH configuration contains no keys")
			}
			return nil
		}
		if count >= 64 || offset+4 > len(list) {
			return fmt.Errorf("invalid policy ECH configuration list")
		}
		length := int(binary.BigEndian.Uint16(list[offset+2 : offset+4]))
		end := offset + 4 + length
		if end > len(list) || !keys[string(list[offset:end])] {
			return fmt.Errorf("local ECH private keys do not match the policy public configuration")
		}
		offset = end
	}
}

func takeVector16(raw []byte, offset *int) ([]byte, bool) {
	if *offset+2 > len(raw) {
		return nil, false
	}
	length := int(binary.BigEndian.Uint16(raw[*offset : *offset+2]))
	*offset += 2
	if *offset+length > len(raw) {
		return nil, false
	}
	value := raw[*offset : *offset+length]
	*offset += length
	return value, true
}

func echX25519PublicKey(config []byte) ([]byte, bool) {
	if len(config) < 9 || binary.BigEndian.Uint16(config[:2]) != 0xfe0d || int(binary.BigEndian.Uint16(config[2:4])) != len(config)-4 {
		return nil, false
	}
	// config_id(1), kem_id(2), public_key<2-byte length>, cipher_suites<2>.
	if binary.BigEndian.Uint16(config[5:7]) != 0x0020 {
		return nil, false
	}
	offset := 7
	publicKey, ok := takeVector16(config, &offset)
	if !ok || len(publicKey) != 32 {
		return nil, false
	}
	suites, ok := takeVector16(config, &offset)
	if !ok || len(suites) == 0 || len(suites)%4 != 0 || offset+2 > len(config) {
		return nil, false
	}
	offset++ // maximum_name_length
	nameLength := int(config[offset])
	offset++
	if nameLength == 0 || offset+nameLength > len(config) {
		return nil, false
	}
	offset += nameLength
	_, ok = takeVector16(config, &offset) // extensions
	return publicKey, ok && offset == len(config)
}
