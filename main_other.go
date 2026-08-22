//go:build !windows

package main

import (
	"fmt"
	"os"
)

// The GUI is Windows-only (github.com/lxn/walk). Non-Windows builds get a
// clear message so `go test ./...` and module consumers still work elsewhere.
func main() {
	fmt.Fprintln(os.Stderr, "LLM Provider Checker GUI is Windows-only.")
	fmt.Fprintln(os.Stderr, "Cross-compile from Linux/macOS with:")
	fmt.Fprintln(os.Stderr, "  ./build.sh")
	fmt.Fprintln(os.Stderr, "or:")
	fmt.Fprintln(os.Stderr, `  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H windowsgui" -o provider-checker.exe .`)
	os.Exit(1)
}
