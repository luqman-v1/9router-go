//go:build windows

package service

import (
	"fmt"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

func TestDefaultWindowsRunRegistryRoundTrip(t *testing.T) {
	prevKeyPath := windowsRunKeyPath
	tempKey := fmt.Sprintf(`Software\9router-go-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	windowsRunKeyPath = tempKey
	t.Cleanup(func() {
		_ = registry.DeleteKey(registry.CURRENT_USER, tempKey)
		windowsRunKeyPath = prevKeyPath
	})

	if val, ok, err := defaultWindowsRunRead(windowsTaskName); err != nil || ok || val != "" {
		t.Fatalf("initial read = (%q, %v, %v), want empty absent", val, ok, err)
	}

	const wantCmd = `"C:\Program Files\9router-go\9router-go.exe" start`
	if err := defaultWindowsRunWrite(windowsTaskName, wantCmd); err != nil {
		t.Fatalf("defaultWindowsRunWrite: %v", err)
	}

	val, ok, err := defaultWindowsRunRead(windowsTaskName)
	if err != nil || !ok || val != wantCmd {
		t.Fatalf("read after write = (%q, %v, %v), want (%q, true, nil)", val, ok, err, wantCmd)
	}

	deleted, err := defaultWindowsRunDelete(windowsTaskName)
	if err != nil || !deleted {
		t.Fatalf("first delete = (%v, %v), want (true, nil)", deleted, err)
	}

	deleted, err = defaultWindowsRunDelete(windowsTaskName)
	if err != nil || deleted {
		t.Fatalf("second delete = (%v, %v), want (false, nil)", deleted, err)
	}

	_ = defaultWindowsIsElevated()
}
