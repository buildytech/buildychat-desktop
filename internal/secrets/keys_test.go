package secrets

import (
	"errors"
	"testing"
)

func TestKeychainFirstThenEnv(t *testing.T) {
	env := func(v string) func(string) string {
		return func(name string) string {
			if name == "XAI_API_KEY" {
				return v
			}
			return ""
		}
	}
	none := func(string, string) (string, error) { return "", errors.New("not found") }
	stored := func(string, string) (string, error) { return " from-keychain ", nil }
	cases := []struct {
		keys        Keys
		key, source string
	}{
		{Keys{Keyring: stored, Getenv: env("from-env")}, "from-keychain", "keychain"},
		{Keys{Keyring: none, Getenv: env(" from-env ")}, "from-env", "env"},
		{Keys{Keyring: none, Getenv: env("  ")}, "", ""},
	}
	for i, c := range cases {
		key, source := c.keys.Key()
		if key != c.key || source != c.source {
			t.Errorf("case %d: got source %q (key matches: %v)", i, source, key == c.key)
		}
	}
}
