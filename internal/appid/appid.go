// Package appid is the desktop app's fixed identity. The WebView keeps the web app's sessions (localStorage) per app
// identity: the macOS WKWebView data store follows the bundle id, the Windows WebView2 user-data folder is set from ID
// (cmd/buildychat), so these never change between releases or with a renamed binary. The packaging files under build/
// carry the same values (appid_test.go checks them).
package appid

const (
	// ID is the reverse-DNS application id (macOS CFBundleIdentifier, the Windows WebView2 data folder name).
	ID = "tw.buildy.chat"
	// Name is the product name shown to people.
	Name = "BuildY Chat"
	// Binary is the executable name (Linux package and .desktop entry, the Windows .exe, the macOS CFBundleExecutable).
	Binary = "buildychat"
	// Company is the publisher shown by installers.
	Company = "BuildY"
)

// Version is the app version the build was stamped with: CI and scripts/package.sh set it with
// -ldflags "-X github.com/buildytech/buildychat-desktop/internal/appid.Version=<version>". An unstamped build is "dev"
// (the contract's constants.host.devVersion); GET /runtime/v1/status reports it as host.version.
var Version = "dev"
