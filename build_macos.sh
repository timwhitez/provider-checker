#!/usr/bin/env bash
# Build the LLM Provider Checker macOS app (native AppKit GUI via cgo).
# Produces "dist/Provider Checker.app" (universal arm64 + x86_64 by default)
# and a zip of it. Requires macOS with the Xcode Command Line Tools.
#
# Environment overrides:
#   ARCHS="arm64"                     build a single architecture
#   MACOSX_DEPLOYMENT_TARGET=13.0     minimum macOS version
#   VERSION=1.2.3                     CFBundleShortVersionString
set -euo pipefail

cd "$(dirname "$0")"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "build_macos.sh must run on macOS (the AppKit GUI needs cgo and the Xcode Command Line Tools)." >&2
  exit 1
fi
if ! xcrun --find clang >/dev/null 2>&1; then
  echo "clang not found: install the Xcode Command Line Tools with 'xcode-select --install'." >&2
  exit 1
fi

APP_NAME="Provider Checker"
BUNDLE_ID="io.github.timwhitez.provider-checker"
EXE="provider-checker"
ARCHS="${ARCHS:-arm64 amd64}"
VERSION="${VERSION:-1.0.0}"
BUILD="$(git rev-parse --short HEAD 2>/dev/null || echo 0)"
# Recent Go runtimes require macOS 13; pin every C/ObjC object to the same
# target (CGO_* flags are part of Go's build cache key, the env var is not).
export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-13.0}"
export CGO_ENABLED=1
export CGO_CFLAGS="-O2 -g -mmacosx-version-min=${MACOSX_DEPLOYMENT_TARGET}"
export CGO_LDFLAGS="-mmacosx-version-min=${MACOSX_DEPLOYMENT_TARGET}"

OUT="dist"
WORK="$OUT/.build"
APP="$OUT/$APP_NAME.app"

echo "==> go test ./..."
go test ./...

mkdir -p "$WORK"
bins=()
for arch in $ARCHS; do
  case "$arch" in
    arm64) carch=arm64 ;;
    amd64) carch=x86_64 ;;
    *) echo "unsupported arch: $arch (use arm64 and/or amd64)" >&2; exit 1 ;;
  esac
  echo "==> Building darwin/$arch (cgo, AppKit)..."
  GOOS=darwin GOARCH="$arch" CC="clang -arch $carch" go build \
    -trimpath \
    -ldflags "-s -w" \
    -o "$WORK/$EXE-$arch" \
    .
  bins+=("$WORK/$EXE-$arch")
done

echo "==> Assembling $APP"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
if [[ ${#bins[@]} -gt 1 ]]; then
  lipo -create -output "$APP/Contents/MacOS/$EXE" "${bins[@]}"
else
  cp "${bins[0]}" "$APP/Contents/MacOS/$EXE"
fi

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDevelopmentRegion</key><string>en</string>
  <key>CFBundleDisplayName</key><string>$APP_NAME</string>
  <key>CFBundleExecutable</key><string>$EXE</string>
  <key>CFBundleIdentifier</key><string>$BUNDLE_ID</string>
  <key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
  <key>CFBundleName</key><string>$APP_NAME</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundleVersion</key><string>$BUILD</string>
  <key>LSApplicationCategoryType</key><string>public.app-category.developer-tools</string>
  <key>LSMinimumSystemVersion</key><string>$MACOSX_DEPLOYMENT_TARGET</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>NSPrincipalClass</key><string>NSApplication</string>
  <key>NSSupportsAutomaticGraphicsSwitching</key><true/>
</dict>
</plist>
PLIST

# Ad-hoc signature: required to launch on Apple Silicon. Not notarized.
codesign --force --sign - "$APP"

ZIP="$OUT/provider-checker-macos.zip"
rm -f "$ZIP"
ditto -c -k --keepParent "$APP" "$ZIP"

echo "==> Done."
lipo -info "$APP/Contents/MacOS/$EXE"
ls -la "$ZIP"
