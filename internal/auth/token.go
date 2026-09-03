// Package auth implements agent token load/generate and request verification.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Header is the HTTP header carrying the agent token.
const Header = "X-Agent-Token"

// DefaultFileName is the conventional token filename in the data directory.
const DefaultFileName = ".agent-token"

// Token holds the expected secret bytes (hex-decoded comparison uses the
// raw file contents trimmed of whitespace).
type Token struct {
	raw string // exact file contents trimmed
}

// LoadOrCreate reads path or generates a 32-byte hex token with mode 0600.
func LoadOrCreate(path string) (*Token, error) {
	if path == "" {
		return nil, fmt.Errorf("token path is empty")
	}
	data, err := os.ReadFile(path)
	if err == nil {
		s := strings.TrimSpace(string(data))
		if s == "" {
			return nil, fmt.Errorf("token file %q is empty", path)
		}
		return &Token{raw: s}, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read token: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil && filepath.Dir(path) != "." && filepath.Dir(path) != "" {
		// Dir may be "."; MkdirAll(".") is fine / no-op issues on some FS
		if filepath.Dir(path) != "." {
			return nil, fmt.Errorf("create token dir: %w", err)
		}
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}
	s := hex.EncodeToString(b[:])
	// Write with 0600 exclusively.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("write token: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(s + "\n"); err != nil {
		return nil, fmt.Errorf("write token: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	// Re-assert mode in case umask interfered on some platforms.
	_ = os.Chmod(path, 0o600)
	return &Token{raw: s}, nil
}

// Valid reports whether provided matches the stored token (constant-time).
func (t *Token) Valid(provided string) bool {
	if t == nil {
		return false
	}
	a := []byte(t.raw)
	b := []byte(strings.TrimSpace(provided))
	if len(a) != len(b) {
		// still do a compare of equal-length dummy to reduce trivial timing
		subtle.ConstantTimeCompare(a, a)
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}

// String returns the token for tests only; do not log in production paths.
func (t *Token) String() string {
	if t == nil {
		return ""
	}
	return t.raw
}
