package tray

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// TestTray_QuitDecoupling is X3 of the acceptance matrix
// (docs/specs/tray-companion-architecture.md §8.2): quitting the tray must not
// stop the gateway (C4).
//
// The behaviour lives in the GUI's quit handler, which cannot be executed here —
// systray needs a display and a cgo build. What can be checked on any machine is
// the wiring: the goroutine that consumes quit.ClickedCh must not mention
// `actions`, because every action it could name is one that changes the
// gateway. Wiring Quit to Stop is the one edit that breaks C4 while leaving
// every other test green, so that is the edit this test exists to red.
//
// It reads the source rather than a copy of the behaviour on purpose. A test
// that called a helper named Quit would prove the helper does nothing; this one
// fails when somebody makes the menu item do something.
func TestTray_QuitDecoupling(t *testing.T) {
	path := filepath.Join("..", "..", "cmd", "9router-tray", "gui.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	// The anchor is the range over quit.ClickedCh. If the file is restructured
	// so that name no longer appears, this test must fail rather than pass on a
	// loop that never ran (§5.D.6: a loop that only errors on a match passes
	// vacuously when the collection is empty).
	const channel = "ClickedCh"
	var handler *ast.RangeStmt
	ast.Inspect(file, func(n ast.Node) bool {
		r, ok := n.(*ast.RangeStmt)
		if !ok || handler != nil {
			return true
		}
		sel, ok := r.X.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != channel {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "quit" {
			handler = r
		}
		return true
	})
	if handler == nil {
		t.Fatalf("%s: no goroutine ranges over quit.%s — C4 is then unwitnessed, not satisfied", path, channel)
	}

	// A handler may not name the Actions value at all. Reading a snapshot is
	// harmless today, but the rule that has to hold is the simple one: Quit
	// touches nothing the menu owns.
	var touched []string
	ast.Inspect(handler.Body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "actions" {
			touched = append(touched, fset.Position(id.Pos()).String())
		}
		return true
	})
	if len(touched) > 0 {
		t.Fatalf("quit handler references actions at %v: Quit Tray must release the icon only and leave the gateway running (C4)", touched)
	}
}
