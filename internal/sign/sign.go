// Package sign implements Ed25519 key generation, detached signatures and
// public-key loading for update manifests and checksum files.
package sign

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	privatePEMType = "PRIVATE KEY"
	publicPEMType  = "PUBLIC KEY"
)

// Generate creates a new Ed25519 key pair.
func Generate() (ed25519.PrivateKey, ed25519.PublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate Ed25519 key: %w", err)
	}
	return priv, pub, nil
}

// EncodePrivatePEM returns a PKCS#8 PEM block.
func EncodePrivatePEM(key ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: privatePEMType, Bytes: der}), nil
}

// EncodePublicPEM returns a PKIX PEM block.
func EncodePublicPEM(key ed25519.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: publicPEMType, Bytes: der}), nil
}

// ParsePrivatePEM parses a PKCS#8 Ed25519 private key.
func ParsePrivatePEM(data []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(bytes.TrimSpace(data))
	if block == nil {
		return nil, errors.New("private key is not PEM encoded")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is %T, not Ed25519", key)
	}
	return priv, nil
}

// LoadPrivateKey reads a PEM private key from disk.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	key, err := ParsePrivatePEM(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return key, nil
}

// ParsePublicKey accepts a PKIX PEM public key or a standard-base64 encoded
// raw 32-byte Ed25519 public key. Base64 is convenient for JSON configuration.
func ParsePublicKey(data []byte) (ed25519.PublicKey, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, errors.New("public key is empty")
	}
	if block, _ := pem.Decode(trimmed); block != nil {
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse public key: %w", err)
		}
		pub, ok := key.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("public key is %T, not Ed25519", key)
		}
		return pub, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(trimmed)))
	if err != nil {
		return nil, fmt.Errorf("public key must be PEM or base64: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// LoadPublicKey loads a public key from an inline PEM/base64 value or a file.
// Exactly one of inline and path may be set.
func LoadPublicKey(inline, path string) (ed25519.PublicKey, error) {
	if inline != "" && path != "" {
		return nil, errors.New("set only one of security.public_key and security.public_key_file")
	}
	if inline != "" {
		return ParsePublicKey([]byte(inline))
	}
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read public key: %w", err)
	}
	return ParsePublicKey(data)
}

// Sign returns the standard-base64 detached Ed25519 signature of data.
func Sign(priv ed25519.PrivateKey, data []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data))
}

// Verify checks a standard-base64 (or raw 64-byte) detached signature.
func Verify(pub ed25519.PublicKey, data []byte, encodedSignature string) error {
	sig, err := DecodeSignature(encodedSignature)
	if err != nil {
		return err
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid public key length %d", len(pub))
	}
	if !ed25519.Verify(pub, data, sig) {
		return errors.New("signature verification failed")
	}
	return nil
}

// DecodeSignature accepts standard base64 text, base64 with whitespace, or a
// raw 64-byte signature. Raw support keeps interoperability with tools that
// write binary .sig files.
func DecodeSignature(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("signature is empty")
	}
	if sig, err := base64.StdEncoding.DecodeString(s); err == nil && len(sig) == ed25519.SignatureSize {
		return sig, nil
	}
	if len(s) == ed25519.SignatureSize {
		return []byte(s), nil
	}
	return nil, errors.New("signature must be base64 text or 64 raw bytes")
}
