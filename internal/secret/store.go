// Package secret resolves API keys from keyring, 0600 file, env, or none.
// Errors never carry key material.
package secret

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

// ServiceName is the OS keyring service name.
const ServiceName = "sslug"

// Resolve returns the key material for a key_ref string:
// "keyring:<id>", "file:<id>", "env:VAR", or "none".
// The returned string is raw key material; callers must never log it.
func Resolve(ref string) (string, error) {
	if ref == "" || ref == "none" {
		return "", nil
	}
	scheme, id, ok := strings.Cut(ref, ":")
	if !ok {
		return "", fmt.Errorf("malformed key_ref %q", Redact(ref))
	}
	switch scheme {
	case "env":
		v := os.Getenv(id)
		if v == "" {
			return "", fmt.Errorf("env var %s not set", id)
		}
		return v, nil
	case "keyring":
		v, err := keyring.Get(ServiceName, id)
		if err != nil {
			return "", fmt.Errorf("keyring lookup %s: %w", id, redactErr(err))
		}
		return v, nil
	case "file":
		v, err := readFile(id)
		if err != nil {
			return "", fmt.Errorf("secret file %s: %w", id, redactErr(err))
		}
		return v, nil
	default:
		return "", fmt.Errorf("unknown key_ref scheme %q", Redact(scheme))
	}
}

// Store writes key material according to the scheme in ref.
// For "keyring:" it writes to the OS keyring. For "file:" it writes a 0600
// file under secretsDir. For "env:" and "none" it is a no-op (the caller
// owns the env var or the key is intentionally absent).
func Store(ref, value string) error {
	scheme, id, _ := strings.Cut(ref, ":")
	switch scheme {
	case "keyring":
		if err := keyring.Set(ServiceName, id, value); err != nil {
			return fmt.Errorf("keyring set %s: %w", id, redactErr(err))
		}
		return nil
	case "file":
		return writeFile(id, value)
	case "env", "none", "":
		return nil
	default:
		return fmt.Errorf("cannot store to scheme %q", Redact(scheme))
	}
}

// Delete removes key material for ref. Missing entries are not errors.
func Delete(ref string) error {
	scheme, id, _ := strings.Cut(ref, ":")
	switch scheme {
	case "keyring":
		if err := keyring.Delete(ServiceName, id); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return fmt.Errorf("keyring delete %s: %w", id, redactErr(err))
		}
	case "file":
		if err := os.Remove(filePath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("secret file delete %s: %w", id, redactErr(err))
		}
	}
	return nil
}

// KeyringAvailable probes the keyring with a no-op get.
func KeyringAvailable() bool {
	// A Get on a nonexistent key returns ErrNotFound if the backend works,
	// or a backend error if it does not.
	_, err := keyring.Get(ServiceName, "sslug-probe-nonexistent")
	return err == nil || errors.Is(err, keyring.ErrNotFound)
}

// Redact masks a string for safe inclusion in logs/errors.
func Redact(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:2] + strings.Repeat("*", len(s)-4) + s[len(s)-2:]
}

// redactErr wraps an error, scrubbing anything that looks like a key
// (long alphanumeric runs) before returning.
func redactErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	// Scrub any run of 12+ alphanumerics (plausible key material).
	return errors.New(scrubLong(msg))
}

func scrubLong(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		trimmed := strings.Trim(w, `"'.,;:()[]{}`)
		if len(trimmed) >= 12 && isAlnum(trimmed) {
			words[i] = strings.Replace(w, trimmed, Redact(trimmed), 1)
		}
	}
	return strings.Join(words, " ")
}

func isAlnum(s string) bool {
	for _, r := range s {
		if !('0' <= r && r <= '9' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// --- file backend ---

// filePath maps a file:<id> ref to its on-disk path.
// The id is sanitized to a bare filename to prevent path traversal.
func filePath(id string) string {
	safe := strings.Map(func(r rune) rune {
		if 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, id)
	return filepath.Join(secretsDir(), safe+".key")
}

// secretsDir returns $SSLUG_CONFIG_HOME/secrets or the default config dir.
func secretsDir() string {
	if d := os.Getenv("SSLUG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "secrets")
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "status-slug", "secrets")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "status-slug", "secrets")
}

func readFile(id string) (string, error) {
	data, err := os.ReadFile(filePath(id))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(data), "\n"), nil
}

func writeFile(id, value string) error {
	path := filePath(id)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(value); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
