package main

import (
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// serverOptions bind the `server` build (headless HTTP, for tests and the exam runner) to loopback only: that mode
// has no window and no Basic auth, so it must never listen on a shared interface. BUILDYCHAT_PORT picks the port.
func serverOptions() application.ServerOptions {
	port := 18110
	if v := os.Getenv("BUILDYCHAT_PORT"); v != "" {
		var p int
		for _, ch := range v {
			if ch < '0' || ch > '9' {
				p = 0
				break
			}
			p = p*10 + int(ch-'0')
		}
		if p > 0 && p < 65536 {
			port = p
		}
	}
	return application.ServerOptions{Host: "127.0.0.1", Port: port}
}
