#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
export GOCACHE="$PWD/.cache/go-build"
export GOMODCACHE="$PWD/.cache/gomod"
mkdir -p dist/Agents.app/Contents/MacOS dist/Agents.app/Contents/Resources .cache/swift
go build -trimpath -ldflags='-s -w' -o dist/Agents.app/Contents/Resources/agents-collector ./cmd/agents-collector
SDK="$(xcrun --sdk macosx --show-sdk-path)"
DRIVER="$(xcode-select -p)/Toolchains/XcodeDefault.xctoolchain/usr/bin/swift-driver"
"$DRIVER" --driver-mode=swiftc -swift-version 5 -parse-as-library -O -sdk "$SDK" -module-cache-path "$PWD/.cache/swift" app/*.swift -o dist/Agents.app/Contents/MacOS/Agents
cp app/Info.plist dist/Agents.app/Contents/Info.plist
cp app/Resources/setup-guide.json dist/Agents.app/Contents/Resources/setup-guide.json
# A stable identity keeps Keychain access, login items and notification permission
# across rebuilds; ad-hoc signing makes every build a different app to macOS.
IDENTITY="${AGENTS_SIGN_IDENTITY:-$(security find-identity -v -p codesigning 2>/dev/null | awk -F'"' '/Developer ID Application|Apple Development/ {print $2; exit}')}"
if [ -z "$IDENTITY" ]; then
  echo "No code-signing identity found; signing ad hoc. Keychain pairing won't survive rebuilds." >&2
  IDENTITY="-"
fi
codesign --force --deep --sign "$IDENTITY" dist/Agents.app
echo "Built $PWD/dist/Agents.app"
