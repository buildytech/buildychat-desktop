#!/usr/bin/env bash
# Unsigned installers of BuildY Chat for the system this runs on (CI: one job per OS), into dist/:
#   linux    buildychat_<version>_amd64.deb (wails3 tool package, nfpm) and buildychat-x86_64.AppImage (best effort)
#   windows  buildychat-amd64-installer.exe (NSIS, per-machine) and buildychat-windows-amd64.zip (the bare .exe)
#   darwin   BuildY Chat.app (ad-hoc signed only) inside "BuildY Chat.dmg"
# Needs: Go, the wails3 CLI (WAILS3, default wails3), frontend/dist built (scripts/build-frontend.sh), and per OS:
# GTK 4 + WebKitGTK 6 dev files (Linux), makensis (Windows), Xcode command line tools (macOS).
# Identity: internal/appid (fixed: tw.buildy.chat). Nothing here signs or notarizes; see README "Signing".
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
WAILS3="${WAILS3:-wails3}"
VERSION="${BUILDYCHAT_VERSION:-0.1.0}"
export BUILDYCHAT_VERSION="$VERSION"
TAGS="nodynamic production"
LDFLAGS="-s -w"
[ -f frontend/dist/index.html ] || { echo "package: frontend/dist missing (scripts/build-frontend.sh)" >&2; exit 1; }
rm -rf bin dist && mkdir -p bin dist
case "$(uname -s)" in
Linux)
	export GOARCH=amd64
	go build -tags "$TAGS" -trimpath -buildvcs=false -ldflags "$LDFLAGS" -o bin/buildychat ./cmd/buildychat
	"$WAILS3" tool package -name buildychat -format deb -config build/linux/nfpm/nfpm.yaml -out dist
	mv dist/buildychat.deb "dist/buildychat_${VERSION}_amd64.deb"
	if [ "${APPIMAGE:-1}" = "1" ]; then
		export APPIMAGE_EXTRACT_AND_RUN=1 # linuxdeploy without FUSE
		work="$(mktemp -d)"
		cp bin/buildychat "$work/buildychat"
		cp build/appicon.png "$work/buildychat.png"
		cp build/linux/buildychat.desktop "$work/buildychat.desktop"
		if (cd "$work" && "$WAILS3" generate appimage -binary buildychat -icon buildychat.png -desktopfile buildychat.desktop -outputdir "$OLDPWD/dist" -builddir "$work/build"); then
			echo "package: AppImage ok"
		else
			echo "package: AppImage failed (the .deb stands)" >&2
		fi
		rm -rf "$work"
	fi
	;;
MINGW* | MSYS* | CYGWIN*)
	export GOOS=windows GOARCH=amd64 CGO_ENABLED=0
	"$WAILS3" generate syso -arch amd64 -icon build/windows/icon.ico -manifest build/windows/wails.exe.manifest -info build/windows/info.json -out cmd/buildychat/wails_windows_amd64.syso
	go build -tags "$TAGS" -trimpath -buildvcs=false -ldflags "$LDFLAGS -H windowsgui" -o bin/buildychat.exe ./cmd/buildychat
	rm -f cmd/buildychat/wails_windows_amd64.syso
	(cd bin && 7z a -tzip ../dist/buildychat-windows-amd64.zip buildychat.exe >/dev/null)
	"$WAILS3" generate webview2bootstrapper -dir build/windows/nsis
	(cd build/windows/nsis && makensis -V2 -DINFO_PRODUCTVERSION="$VERSION" -DARG_WAILS_AMD64_BINARY="$(cygpath -w "$PWD/../../../bin/buildychat.exe")" project.nsi)
	mv bin/buildychat-amd64-installer.exe dist/
	rm -f build/windows/nsis/MicrosoftEdgeWebview2Setup.exe
	;;
Darwin)
	export CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=12.0
	go build -tags "$TAGS" -trimpath -buildvcs=false -ldflags "$LDFLAGS" -o bin/buildychat ./cmd/buildychat
	app="bin/BuildY Chat.app"
	mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
	cp bin/buildychat "$app/Contents/MacOS/buildychat"
	cp build/darwin/Info.plist "$app/Contents/Info.plist"
	/usr/libexec/PlistBuddy -c "Set :CFBundleVersion $VERSION" -c "Set :CFBundleShortVersionString $VERSION" "$app/Contents/Info.plist"
	cp build/darwin/icons.icns build/darwin/Assets.car "$app/Contents/Resources/"
	xattr -cr "$app"
	codesign --force --deep --sign - "$app"
	codesign --verify --deep --strict "$app"
	"$WAILS3" tool package -format dmg -name "BuildY Chat" -out bin -background build/darwin/dmg-background.png -volume-icon build/darwin/icons.icns -file-icon build/darwin/dmg-file-icon.icns
	mv "bin/BuildY Chat.dmg" dist/
	(cd bin && ditto -c -k --keepParent "BuildY Chat.app" "../dist/BuildY-Chat-macos-$(uname -m).app.zip")
	;;
*)
	echo "package: unsupported system $(uname -s)" >&2
	exit 1
	;;
esac
ls -l dist
