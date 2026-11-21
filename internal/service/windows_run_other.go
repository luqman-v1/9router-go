//go:build !windows

package service

import "fmt"

func defaultWindowsIsElevated() bool {
	return false
}

func defaultWindowsRunRead(string) (string, bool, error) {
	return "", false, nil
}

func defaultWindowsRunWrite(string, string) error {
	return fmt.Errorf("windows-run backend only runs on windows")
}

func defaultWindowsRunDelete(string) (bool, error) {
	return false, nil
}
