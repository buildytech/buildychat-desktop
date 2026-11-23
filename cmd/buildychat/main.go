// BuildY Chat desktop: the buildytech/chat web app in a Wails 3 window, with its runtime (/runtime/v1/*) answered in
// process by Go. Build tags: none for the desktop app, `server` for the headless HTTP mode (tests, the exam runner).
//
//	buildychat              open the app
//	buildychat --set-key    read the xAI key from stdin and keep it in the OS keychain (never pass it in argv)
//	buildychat --forget-key remove it from the keychain
//
// The ZIP export is saved through the system's save dialog; the `server` build writes it to BUILDYCHAT_EXPORT_DIR.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"

	buildychat "github.com/buildytech/buildychat-desktop"
	"github.com/buildytech/buildychat-desktop/internal/appid"
	"github.com/buildytech/buildychat-desktop/internal/runtime"
	"github.com/buildytech/buildychat-desktop/internal/secrets"
	"github.com/buildytech/buildychat-desktop/internal/webapp"
)

func main() {
	setKey := flag.Bool("set-key", false, "read the xAI API key from stdin and store it in the OS keychain")
	forgetKey := flag.Bool("forget-key", false, "remove the xAI API key from the OS keychain")
	flag.Parse()
	switch {
	case *setKey:
		fmt.Fprintln(os.Stderr, "Paste the xAI API key and press Enter:")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			log.Fatal("no key read from stdin")
		}
		if err := secrets.Store(line); err != nil {
			log.Fatalf("keychain: %v", err)
		}
		fmt.Fprintln(os.Stderr, "Stored in the OS keychain.")
		return
	case *forgetKey:
		if err := secrets.Forget(); err != nil {
			log.Fatalf("keychain: %v", err)
		}
		fmt.Fprintln(os.Stderr, "Removed from the OS keychain.")
		return
	}

	constants, err := runtime.LoadConstants(buildychat.Contract())
	if err != nil {
		log.Fatal(err)
	}
	var app *application.App
	rt := runtime.New(runtime.Options{Constants: constants, Keys: secrets.Keys{}, BaseURL: os.Getenv("XAI_BASE_URL"), Saver: newSaver(func() *application.App { return app }), Version: appid.Version,
		// Sign in with OpenRouter (ADR 0067): the key in its own keychain item (OPENROUTER_API_KEY as the headless fallback),
		// the sign-in page in the system browser.
		OpenRouter: secrets.OpenRouter{}, OpenRouterBaseURL: os.Getenv("OPENROUTER_BASE_URL"), OpenURL: openURL(func() *application.App { return app })})
	defer rt.Close()

	app = application.New(application.Options{
		Name:        appid.Name,
		Description: "BuildY Chat desktop (buildytech/chat in a Wails window)",
		Mac:         application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
		// The WebView2 data (the web app's saved sessions) lives under the fixed app id, not the binary name.
		Windows: application.WindowsOptions{WebviewUserDataPath: webviewDataDir()},
		Server:  serverOptions(),
		Assets: application.AssetOptions{
			Handler:    webapp.Handler(buildychat.Frontend()),
			Middleware: rt.Middleware,
		},
	})
	var mainWindow *application.WebviewWindow
	if menu := appMenu(app, func() *application.WebviewWindow { return mainWindow }); menu != nil {
		app.Menu.Set(menu)
	}
	mainWindow = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  appid.Name,
		Width:  1440,
		Height: 900,
		URL:    "/",
		// Windows and Linux show the application menu (Settings…, ADR 0068) in the window.
		UseApplicationMenu: true,
	})
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// webviewDataDir is <UserConfigDir>/tw.buildy.chat/WebView2 (used on Windows only).
func webviewDataDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return ""
	}
	return filepath.Join(base, appid.ID, "WebView2")
}
