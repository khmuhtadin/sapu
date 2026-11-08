package rulesupdate

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
)

// PublicKeys are the base64 ed25519 public keys whose signatures Sapu
// trusts. Any one of them may sign a bundle.
//
// PLACEHOLDER: no signing key is configured in this build. The owner runs
// `sapu-rules keygen` and puts the public key it prints here in place of
// placeholderKey (see docs/rules-updates.md). While only the placeholder is
// here, Install refuses every bundle and Active never uses a stored one.
// Tests set their own key.
var PublicKeys = []string{placeholderKey}

// placeholderKey marks "no key yet"; it is never used as a key.
const placeholderKey = "PLACEHOLDER-no-signing-key-configured-in-this-build"

var (
	ErrNoKey        = errors.New("no signing key configured in this build")
	ErrBadSignature = errors.New("the signature does not match the bundle")
)

// KeyConfigured reports whether this build trusts any signing key.
func KeyConfigured() bool {
	keys, err := trustedKeys()
	return err == nil && len(keys) > 0
}

// trustedKeys parses PublicKeys, leaving out the placeholder.
func trustedKeys() ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	for _, s := range PublicKeys {
		if s == placeholderKey {
			continue
		}
		k, err := ParsePublicKey(s)
		if err != nil {
			return nil, fmt.Errorf("public key in this build: %w", err)
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// ParsePublicKey reads a base64 ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace([]byte(s))))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%q is not a base64 ed25519 public key", s)
	}
	return ed25519.PublicKey(b), nil
}

// ParsePrivateKey reads the key file sapu-rules keygen writes: the base64
// ed25519 private key.
func ParsePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	b, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(data)))
	if err != nil || len(b) != ed25519.PrivateKeySize {
		return nil, errors.New("not a sapu-rules private key file")
	}
	return ed25519.PrivateKey(b), nil
}

// EncodeKey writes a key as base64, the form PublicKeys and key files use.
func EncodeKey(k []byte) string { return base64.StdEncoding.EncodeToString(k) }

// Sign returns the detached signature of data: the base64 ed25519 signature
// of the exact bytes, and a newline.
func Sign(priv ed25519.PrivateKey, data []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data)) + "\n")
}

// Verify checks sig against data with the keys this build trusts.
func Verify(data, sig []byte) error {
	keys, err := trustedKeys()
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return ErrNoKey
	}
	return verify(keys, data, sig)
}

// VerifyKey checks sig against data with one public key.
func VerifyKey(pub ed25519.PublicKey, data, sig []byte) error {
	return verify([]ed25519.PublicKey{pub}, data, sig)
}

func verify(keys []ed25519.PublicKey, data, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return errors.New("the signature file is not a base64 ed25519 signature")
	}
	if slices.ContainsFunc(keys, func(k ed25519.PublicKey) bool { return ed25519.Verify(k, data, raw) }) {
		return nil
	}
	return ErrBadSignature
}
