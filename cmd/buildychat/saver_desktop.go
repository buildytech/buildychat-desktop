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
