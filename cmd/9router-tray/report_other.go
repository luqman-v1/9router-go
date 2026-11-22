//go:build !windows && tray

package main

import "fmt"

// report surfaces a failure without ever printing a credential (C6): errors
// from the actions carry a command and its output, never a token.
func report(err error) {
	if err == nil {
		return
	}
	fmt.Println(err)
}
