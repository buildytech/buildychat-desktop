package secrets

import (
	"testing"

	"github.com/zalando/go-keyring"
)

// The OpenRouter key has its own keychain item: storing and forgetting it leaves the xAI item alone, the environment
// is the fallback, and forgetting twice is no error.
func TestOpenRouterItem(t *testing.T) {
	keyring.MockInit()
	if err := keyring.Set(Service, User, "xai-item"); err != nil {
		t.Fatal(err)
	}
	none := OpenRouter{Getenv: func(string) string { return "" }}
	if key, source := none.Key(); key != "" || source != "" {
		t.Fatalf("no key expected, got source %q", source)
	}
	if err := none.Store("  "); err == nil {
		t.Fatal("an empty key must not be stored")
	}
	if err := none.Store(" or-item "); err != nil {
		t.Fatal(err)
	}
	if key, source := none.Key(); key != "or-item" || source != "keychain" {
		t.Fatalf("got source %q (key matches: %v)", source, key == "or-item")
	}
	if err := none.Forget(); err != nil {
		t.Fatal(err)
	}
	if err := none.Forget(); err != nil {
		t.Fatal(err)
	}
	if v, err := keyring.Get(Service, User); err != nil || v != "xai-item" {
		t.Fatal("the xAI item must stay")
	}
	env := OpenRouter{Getenv: func(name string) string {
		if name == "OPENROUTER_API_KEY" {
			return " from-env "
		}
		return ""
	}}
	if key, source := env.Key(); key != "from-env" || source != "env" {
		t.Fatalf("got source %q", source)
	}
}
