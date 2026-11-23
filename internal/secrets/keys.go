// Package secrets keeps the owner's xAI key in the OS keychain (macOS Keychain, Windows Credential Manager, Linux Secret
// Service) through github.com/zalando/go-keyring; XAI_API_KEY in the environment is the development fallback. The key
// is never logged, never put in argv and never handed to the webview.
package secrets

import (
	"errors"
	"os"
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	// Service and User name the keychain item.
	Service = "buildychat-desktop"
	User    = "XAI_API_KEY"
)

// Keys reads the key: the keychain first, then the environment.
type Keys struct {
	// Getenv is os.Getenv unless a test sets it.
	Getenv func(string) string
	// Keyring is the OS keychain unless a test sets it.
	Keyring func(service, user string) (string, error)
}

// Key answers the key and its source ("keychain" or "env"), or "", "" when none is set.
func (k Keys) Key() (string, string) {
	get := k.Keyring
	if get == nil {
		get = keyring.Get
	}
	if v, err := get(Service, User); err == nil && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v), "keychain"
	}
	env := k.Getenv
	if env == nil {
		env = os.Getenv
	}
	if v := strings.TrimSpace(env("XAI_API_KEY")); v != "" {
		return v, "env"
	}
	return "", ""
}

// Store saves the key in the OS keychain (Settings, ADR 0068: Keys is the runtime's writable xAI key store).
func (Keys) Store(key string) error { return Store(key) }

// Forget removes the keychain item.
func (Keys) Forget() error { return Forget() }

// Store saves the key in the OS keychain.
func Store(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("empty key")
	}
	return keyring.Set(Service, User, key)
}

// Forget removes the key from the OS keychain (no error when there was none).
func Forget() error {
	if err := keyring.Delete(Service, User); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}
