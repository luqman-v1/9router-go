//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// report surfaces a failure via a native Windows warning dialog without ever
// printing a credential (C6): errors from actions carry a command and its
// output, never a token.
func report(err error) {
	if err == nil {
		return
	}
	titlePtr, titleErr := windows.UTF16PtrFromString(defaultTrayTitle)
	msgPtr, msgErr := windows.UTF16PtrFromString(err.Error())
	if titleErr != nil || msgErr != nil {
		fmt.Println(err)
		return
	}
	if _, boxErr := windows.MessageBox(0, msgPtr, titlePtr, windows.MB_OK|windows.MB_ICONWARNING|windows.MB_SETFOREGROUND); boxErr != nil {
		fmt.Println(err)
	}
}
