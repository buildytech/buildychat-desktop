# BuildY Chat desktop

The [BuildY Chat](https://github.com/buildytech/chat) web app as a desktop app on [Wails 3](https://wails.io) (v3.1.0), for one user with their own xAI key.

This repository is **only the Go backend and the build**. The frontend, the agent, the prompts, the page contract and the runtime test cases live in `buildytech/chat` and come in as the git submodule `third_party/chat`, pinned by commit. Nothing in the submodule is edited here; a chat change reaches the desktop app by moving the pin ([chat ADR 0065](https://github.com/buildytech/chat/blob/main/.project/adr/0065-runtime-http-contract.md)).

## How it works

- The window loads the built `apps/chat-web` site, embedded in the binary (`frontend/dist`, made by `scripts/build-frontend.sh`).
- The web app calls its runtime with `fetch("/runtime/v1/...")` as on the web. Here the Wails asset-server middleware answers those paths in Go, in process, on the app's own origin: no network port, no Basic auth, no CORS proxy.
- Implemented so far: `GET /runtime/healthz`, `GET /runtime/v1/status` (key state, model discovery, contract fingerprint, `capabilities` with `loop: "client"`, and `host`: `kind: "desktop"`, the app version stamped with `-ldflags -X github.com/buildytech/buildychat-desktop/internal/appid.Version=<version>` (CI and `scripts/package.sh` use `BUILDYCHAT_VERSION`; unstamped builds say `dev`), `os` and `arch`; the chat stamps the session export `app.host: "desktop"` from it) and `POST /runtime/v1/xai/chat/completions`, the provider forward of the agent loop that runs in the webview (phase 2): the webview sends the placeholder key `host-held`, the forward sets the stored key, accepts only a chat model the key lists, caps the body at 256000 bytes, follows no redirect and redacts the key from the answer (`internal/runtime/forward.go`, contract `cases/xai-forward.json`). Media mode (phase 3, `internal/runtime/media.go`, contract `cases/media-*.json`): `POST /runtime/v1/media` generate / result / asset and `?action=encode`. A finished image request is downloaded at once into a content-addressed picture store, `<UserConfigDir>/BuildY/media/<sha256>.<ext>`, and answered as `/runtime/v1/files/<sha256>.<ext>` (the xAI URLs expire; a download that fails keeps its https URL); the store is served read-only and immutable, and the asset proxy and the encoder read it like an xAI picture (`capabilities.files: true`). The ZIP export's WebP sizes come from `internal/media` (Lanczos-3 resize, EXIF orientation, the grade and sharpen of `media-encode.ts`, libwebp through `gen2brain/webp`, no CGO) and match sharp's goldens of `contract/runtime-http/encode` by tolerance (size ±15 %, SSIM ≥ 0.97), never by bytes. AVIF input is not decoded here (`encode_unreadable`; the original stays in the ZIP). The ZIP export is saved natively (phase 4, `internal/runtime/export.go`, contract `cases/export.json`, `capabilities.saveFile`): the web app posts the archive to `POST /runtime/v1/export?name=<file>.zip`; the desktop app asks where to keep it with the system's save dialog, the `server` build writes it to `BUILDYCHAT_EXPORT_DIR` without overwriting (unset: no native save, the browser downloads). The answer names the saved file, never a path. The picture store has no size limit yet.
- **Sign in with OpenRouter** (ADR 0067 in buildytech/chat, contract `cases/openrouter-auth.json`, `cases/openrouter-forward.json`, `cases/media-openrouter.json`; `internal/runtime/auth.go`, `internal/runtime/openrouter.go`): `POST /runtime/v1/auth/openrouter/start` makes a PKCE verifier (S256 challenge) and a state, opens a one-shot listener on `127.0.0.1` (and `::1`) on a random port and opens the system browser (`app.Browser.OpenURL`) at `https://openrouter.ai/auth?callback_url=http://localhost:<port>/cb/<state>&…`. The callback is accepted once within 10 minutes; the code is exchanged at `POST https://openrouter.ai/api/v1/auth/keys` and the user's key goes to the keychain item `buildychat-desktop` / `OPENROUTER_API_KEY` (the xAI item is untouched). `POST /runtime/v1/auth/openrouter/signout` deletes it. Signed in, status says `provider: {id: "openrouter"}`, lists only `x-ai/grok-4.20` / `x-ai/grok-4.7` and `x-ai/grok-imagine-image-2.0` as OpenRouter lists them, the webview's loop calls `POST /runtime/v1/openrouter/chat/completions` (same rules as the xAI forward; `x-grok-conv-id` goes out as `x-session-id`), and Media draws with one synchronous base64 request stored in the picture store at once. Without a sign-in everything stays on the xAI key. No custom URL scheme. `OPENROUTER_API_KEY` in the environment is the headless/dev fallback (`OPENROUTER_BASE_URL` overrides the base for tests).
- The xAI key is kept in the OS keychain (`github.com/zalando/go-keyring`: macOS Keychain, Windows Credential Manager, Linux Secret Service). `XAI_API_KEY` in the environment is the development fallback. The key never reaches the webview and no answer contains it (checked by every contract case).

## Build

Requirements: Go 1.26+, Node 22 and npm (the chat build), and on Linux `pkg-config libgtk-4-dev libwebkitgtk-6.0-dev` (Debian/Ubuntu names). macOS needs the Xcode command line tools; Windows needs nothing beyond Go (WebView2 is part of Windows).

```bash
git clone --recurse-submodules https://github.com/buildytech/buildychat-desktop
cd buildychat-desktop
bash scripts/build-frontend.sh                 # npm ci + build in third_party/chat, copy to frontend/dist
go build -o bin/buildychat ./cmd/buildychat    # the desktop app
bin/buildychat --set-key                       # paste the xAI key on stdin, kept in the OS keychain
bin/buildychat
```

Headless mode for tests and the exam runner (no window; loopback only, port `BUILDYCHAT_PORT`, default 18110):

```bash
go build -tags server -o bin/buildychat-server ./cmd/buildychat
XAI_API_KEY=... bin/buildychat-server          # dev only: the env fallback
OPENROUTER_API_KEY=... bin/buildychat-server   # dev/exam: OpenRouter instead of xAI (set it from a secret, never in argv)
curl -s http://127.0.0.1:18110/runtime/v1/status
```

The server build has no auth: never expose it beyond loopback.

## Tests and the gate

```bash
bash scripts/gate.sh      # gofmt, go vet, go test (unit + contract), submodule clean, headless build
```

`internal/runtime/contract_test.go` runs every case of `third_party/chat/contract/runtime-http/cases/*.json` whose surfaces include `desktop`, embedded with `go:embed` from the pinned submodule, against the same middleware the app uses, with the xAI API stubbed from the case and an empty picture store per case (`store` and `$FILE:<picture>` of the contract README). `internal/media/golden_test.go` checks the encoder against the goldens. Builds and tests use `-tags nodynamic` (the embedded libwebp, never a system one). Case files of later phases are skipped by name until their handlers exist.

## Moving the chat pin

```bash
git -C third_party/chat fetch origin main && git -C third_party/chat checkout <commit>
bash scripts/build-frontend.sh && bash scripts/gate.sh
git add third_party/chat && git commit -m "chat: pin <commit>"
```

## Rules

Pushes go to `main` only, after `scripts/gate.sh`; no force pushes, no skipped hooks. No UI code lives here: UI changes are made in `buildytech/chat` under its UI8Kit / ui8px rules. Generated files (`frontend/dist`) are never committed.

License: Apache-2.0, as buildytech/chat.

## Installers (unsigned, Phase 5)

CI builds installers for the same three systems after the tests (`scripts/package.sh`, Wails **3.1.0** packaging tools),
and uploads workflow artifacts only. No GitHub Releases, no signing secrets. Installer artifacts (14-day retention):

| Artifact | Contents | Architecture / minimum |
|---|---|---|
| `buildychat-installer-windows-2025` | `buildychat-amd64-installer.exe` (NSIS; WebView2 bootstrapper if needed), bare `.exe` ZIP | Windows 10+ x64 |
| `buildychat-installer-macos-15` | `BuildY Chat.dmg`, `.app.zip` | CI runner's native CPU (currently arm64); macOS 12+ |
| `buildychat-installer-ubuntu-24.04` | `buildychat_0.1.0_amd64.deb`, AppImage when linuxdeploy succeeds | Linux x64; DEB needs GTK 4 + WebKitGTK 6 (Ubuntu 24.04+ / Debian 13+) |

The raw-binary artifacts stay available too. To reproduce: install the pinned wails3 CLI with `go install
 github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0`, build the frontend, then `bash scripts/package.sh` on the target OS.
`BUILDYCHAT_VERSION` changes the package version (default `0.1.0`); it never changes the app identity. Linux's .deb is
required; AppImage is best effort because linuxdeploy's GTK/WebKit dependency bundling can vary between runners.
Install/uninstall and GUI launch on a real user desktop still need manual smoke testing. The headless five-page exam
validates the app/runtime, not installer behaviour.

**Fixed identity:** `tw.buildy.chat` (macOS bundle id, Windows WebView2 data folder `<UserConfigDir>/tw.buildy.chat/WebView2`),
product `BuildY Chat`, executable `buildychat`. `internal/appid` and its test keep the packaging files aligned. Keep this
identity across upgrades to preserve sessions. Pre-installer development Windows builds used Wails' binary-name default;
export/import their sessions before moving to the fixed-id build. Sessions remain in the WebView's local storage;
user-selected ZIPs, keychain keys and media are not removed by installer uninstall. The generated Wails icon is temporary.

### Signing and notarization (not configured)

- **Windows:** an organisation-validated code-signing certificate (preferably hardware/cloud backed; EV or a trusted
  signing service), signing-tool access, SHA-256 Authenticode and a timestamp server. Sign the .exe **before** NSIS, then
  sign the installer; verify both. Until then SmartScreen shows an unknown publisher warning. No certificate/private key
  or provider credential should be committed.
- **macOS:** Apple Developer Program team, Developer ID Application certificate/private key in an ephemeral CI keychain,
  hardened runtime/entitlements appropriate for WebKit, secure timestamp; sign the app and DMG, submit to Apple's notary
  service (App Store Connect API key or a keychain notary profile), then staple and verify with `codesign` / `spctl`.
  Current builds have only an **ad-hoc** signature (`--sign -`), no Developer ID and no notarization. Gatekeeper may block
  a quarantined download. Do not disable Gatekeeper globally.
- **Linux:** packages are unsigned. A distribution channel needs a maintained GPG key and signed repository metadata (or
  detached checksums/signatures for direct downloads), plus an update/distribution policy. The CI artifacts alone are
  test builds, not a trusted update channel.

The xAI key still comes from the system keychain or dev env (not the installer). There is no native key-entry settings
screen yet (`--set-key` is the setup path), no auto-updater, no release publishing and no media-store size limit.
