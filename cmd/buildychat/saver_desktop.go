//go:build !server

package main

import (
	"context"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/buildytech/buildychat-desktop/internal/runtime"
)

// newSaver asks where to keep the ZIP export with the system's save dialog (capabilities.saveFile).
func newSaver(app func() *application.App) runtime.Saver {
	return runtime.SaverFunc(func(_ context.Context, name string, data []byte) (string, error) {
		a := app()
		if a == nil {
			return "", runtime.ErrCancelled
		}
		dialog := a.Dialog.SaveFile().SetFilename(name).AddFilter("ZIP archive", "*.zip").CanCreateDirectories(true)
		if w := a.Window.Current(); w != nil {
			dialog.AttachToWindow(w)
		}
		path, err := dialog.PromptForSingleSelection()
		if err != nil {
			return "", err
		}
		return runtime.WriteChosen(path, data)
	})
}

// openURL opens the OpenRouter sign-in page in the system browser.
func openURL(app func() *application.App) func(string) error {
	return func(url string) error {
		a := app()
		if a == nil {
			return runtime.ErrCancelled
		}
		return a.Browser.OpenURL(url)
	}
}

// openWindow opens the standalone preview in a second app window on the same asset server and webview storage
// (window.open cannot: a WebView2 popup has no handler for wails.localhost, WebKit gives none). The window is named
// after the session, so a second request raises it instead of piling up windows.
func openWindow(app func() *application.App) runtime.PreviewWindow {
	return func(name, path string) error {
		a := app()
		if a == nil {
			return runtime.ErrCancelled
		}
		// Off the request's goroutine: the window is created on the UI thread, which may be the one serving this request.
		go func() {
			if w, ok := a.Window.GetByName(name); ok {
				if w.IsMinimised() {
					w.UnMinimise()
				}
				w.Show()
				w.Focus()
				return
			}
			a.Window.NewWithOptions(application.WebviewWindowOptions{Name: name, Title: "Preview", Width: 1280, Height: 860, URL: path})
		}()
		return nil
	}
}
