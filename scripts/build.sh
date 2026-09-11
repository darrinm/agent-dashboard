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
codesign --force --deep --sign - dist/Agents.app
echo "Built $PWD/dist/Agents.app"
