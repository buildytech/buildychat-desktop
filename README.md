# BuildY Chat desktop

The [BuildY Chat](https://github.com/buildytech/chat) web app as a desktop app on [Wails 3](https://wails.io) (v3.1.0), for one user with their own xAI key.

This repository is **only the Go backend and the build**. The frontend, the agent, the prompts, the page contract and the runtime test cases live in `buildytech/chat` and come in as the git submodule `third_party/chat`, pinned by commit. Nothing in the submodule is edited here; a chat change reaches the desktop app by moving the pin ([chat ADR 0065](https://github.com/buildytech/chat/blob/main/.project/adr/0065-runtime-http-contract.md)).

## How it works

- The window loads the built `apps/chat-web` site, embedded in the binary (`frontend/dist`, made by `scripts/build-frontend.sh`).
- The web app calls its runtime with `fetch("/runtime/v1/...")` as on the web. Here the Wails asset-server middleware answers those paths in Go, in process, on the app's own origin: no network port, no Basic auth, no CORS proxy.
- Implemented so far: `GET /runtime/healthz`, `GET /runtime/v1/status` (key state, model discovery, contract fingerprint, `capabilities` with `loop: "client"`) and `POST /runtime/v1/xai/chat/completions`, the provider forward of the agent loop that runs in the webview (phase 2): the webview sends the placeholder key `host-held`, the forward sets the stored key, accepts only a chat model the key lists, caps the body at 256000 bytes, follows no redirect and redacts the key from the answer (`internal/runtime/forward.go`, contract `cases/xai-forward.json`). Next: Media with pictures kept on disk and WebP (phase 3), native ZIP save (phase 4).
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
curl -s http://127.0.0.1:18110/runtime/v1/status
```

The server build has no auth: never expose it beyond loopback.

## Tests and the gate

```bash
bash scripts/gate.sh      # gofmt, go vet, go test (unit + contract), submodule clean, headless build
```

`internal/runtime/contract_test.go` runs every case of `third_party/chat/contract/runtime-http/cases/*.json` whose surfaces include `desktop`, embedded with `go:embed` from the pinned submodule, against the same middleware the app uses, with the xAI API stubbed from the case. Case files of later phases are skipped by name until their handlers exist.

## Moving the chat pin

```bash
git -C third_party/chat fetch origin main && git -C third_party/chat checkout <commit>
bash scripts/build-frontend.sh && bash scripts/gate.sh
git add third_party/chat && git commit -m "chat: pin <commit>"
```

## Rules

Pushes go to `main` only, after `scripts/gate.sh`; no force pushes, no skipped hooks. No UI code lives here: UI changes are made in `buildytech/chat` under its UI8Kit / ui8px rules. Generated files (`frontend/dist`) are never committed.

License: Apache-2.0, as buildytech/chat.
