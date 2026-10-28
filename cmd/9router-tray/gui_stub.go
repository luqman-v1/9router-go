//go:build !windows && !tray

// This build has no GUI toolkit linked in. It exists so that `go build ./...`,
// `go vet ./...`, `go test ./...` and the linter all work on a Linux machine
// that has never installed libayatana-appindicator3-dev — the same reason
// gui.go carries a build constraint at all.
//
// A binary produced this way cannot show an icon, so it fails at startup
// instead of running and doing nothing: a tray that silently never appears
// looks like a broken gateway, and the error names the flag that fixes it.

package main

import (
	"fmt"
	"runtime"

	"9router/proxy/internal/tray"
)

func runGUI(tray.Actions, int) error {
	return fmt.Errorf("this 9router-tray build has no GUI: rebuild with -tags tray (required on %s; Windows needs no tag)", runtime.GOOS)
}
