#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p .build .cache/swift
SDK="$(xcrun --sdk macosx --show-sdk-path)"
DRIVER="$(xcode-select -p)/Toolchains/XcodeDefault.xctoolchain/usr/bin/swift-driver"
"$DRIVER" --driver-mode=swiftc -swift-version 5 -parse-as-library -sdk "$SDK" -module-cache-path "$PWD/.cache/swift" app/MarkdownText.swift tests/MarkdownTests.swift -o .build/markdown-tests
.build/markdown-tests
"$DRIVER" --driver-mode=swiftc -swift-version 5 -parse-as-library -sdk "$SDK" -module-cache-path "$PWD/.cache/swift" app/Models.swift tests/AcknowledgmentTests.swift -o .build/acknowledgment-tests
.build/acknowledgment-tests
