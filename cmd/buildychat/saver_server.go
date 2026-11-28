//go:build server

package main

import (
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/buildytech/buildychat-desktop/internal/runtime"
)

// newSaver of the headless build: no dialog to show, so the ZIP export is written to BUILDYCHAT_EXPORT_DIR (never
// overwriting); without it the build has no native save and the browser downloads the archive.
func newSaver(func() *application.App) runtime.Saver {
	if dir := os.Getenv("BUILDYCHAT_EXPORT_DIR"); dir != "" {
		return runtime.DirSaver{Dir: dir}
	}
	return nil
}

// openURL of the headless build: no browser to open; the start answer carries the URL for the caller.
func openURL(func() *application.App) func(string) error { return nil }

// openWindow of the headless build: no window; the browser opens the preview address itself.
func openWindow(func() *application.App) runtime.PreviewWindow { return nil }
