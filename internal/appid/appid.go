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
