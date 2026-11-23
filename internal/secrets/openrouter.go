package secrets

import (
	"errors"
	"os"
	"strings"

	"github.com/zalando/go-keyring"
)

// OpenRouterUser names the keychain item of the key the OpenRouter sign-in mints (ADR 0067), beside the xAI item
// (Service, User); signing out deletes only this one.
const OpenRouterUser = "OPENROUTER_API_KEY"

// OpenRouter keeps the user's OpenRouter key: the keychain first, then OPENROUTER_API_KEY in the environment (the
// headless build's tests and exam). Signing out deletes the keychain item; an environment key stays until unset.
type OpenRouter struct {
	// Getenv is os.Getenv unless a test sets it.
	Getenv func(string) string
}

// Key answers the key and its source ("keychain" or "env"), or "", "" when none is set.
func (o OpenRouter) Key() (string, string) {
	if v, err := keyring.Get(Service, OpenRouterUser); err == nil && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v), "keychain"
	}
	env := o.Getenv
	if env == nil {
		env = os.Getenv
	}
	if v := strings.TrimSpace(env(OpenRouterUser)); v != "" {
		return v, "env"
	}
	return "", ""
}

// Store keeps the key in the OS keychain.
func (OpenRouter) Store(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("empty key")
	}
	return keyring.Set(Service, OpenRouterUser, key)
}

// Forget deletes the keychain item (no error when there was none).
func (OpenRouter) Forget() error {
	if err := keyring.Delete(Service, OpenRouterUser); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}
