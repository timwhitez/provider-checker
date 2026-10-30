//go:build !windows && !(darwin && cgo)

package main

import (
	"fmt"
	"os"
	"runtime"
)

// The GUI is available on Windows (github.com/lxn/walk) and macOS (AppKit via
// cgo). Other builds get a clear message so `go test ./...` and module
// consumers still work elsewhere.
func main() {
	if runtime.GOOS == "darwin" {
		fmt.Fprintln(os.Stderr, "This macOS build was compiled without cgo, so the AppKit GUI is unavailable.")
		fmt.Fprintln(os.Stderr, "Install the Xcode Command Line Tools (xcode-select --install) and build with:")
		fmt.Fprintln(os.Stderr, "  ./build_macos.sh")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "LLM Provider Checker GUI supports Windows and macOS.")
	fmt.Fprintln(os.Stderr, "Build the Windows executable with ./build.sh, or the macOS app on a Mac with ./build_macos.sh.")
	fmt.Fprintln(os.Stderr, "Windows cross-compile:")
	fmt.Fprintln(os.Stderr, `  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H windowsgui" -o provider-checker.exe .`)
	os.Exit(1)
}
