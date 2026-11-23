package main

import (
	"testing"
	"time"
)

// The headless server must not cut a synchronous OpenRouter draw (up to 150 s) with the Wails 30 s default.
func TestServerOptionsWriteTimeout(t *testing.T) {
	t.Setenv("BUILDYCHAT_PORT", "")
	o := serverOptions()
	if o.WriteTimeout < 150*time.Second+30*time.Second {
		t.Fatalf("WriteTimeout %v is shorter than the image draw", o.WriteTimeout)
	}
	if o.Host != "127.0.0.1" || o.Port != 18110 {
		t.Fatalf("loopback default changed: %s:%d", o.Host, o.Port)
	}
}
