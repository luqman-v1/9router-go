package main

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSetAutostartDelegatesToGatewayCLI(t *testing.T) {
	orig := execGatewayCLI
	t.Cleanup(func() { execGatewayCLI = orig })

	var calls [][]string
	var stubErr error
	execGatewayCLI = func(args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return stubErr
	}

	if err := setAutostart(true); err != nil {
		t.Fatalf("setAutostart(true) unexpected error: %v", err)
	}
	if err := setAutostart(false); err != nil {
		t.Fatalf("setAutostart(false) unexpected error: %v", err)
	}

	want := [][]string{
		{"service", "install"},
		{"service", "uninstall"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("execGatewayCLI calls = %v, want %v", calls, want)
	}

	wantErr := errors.New("access denied")
	stubErr = wantErr
	if err := setAutostart(true); !errors.Is(err, wantErr) {
		t.Fatalf("setAutostart(true) error = %v, want %v", err, wantErr)
	}
}

func TestStartGatewayDelegatesToGatewayCLI(t *testing.T) {
	orig := execGatewayCLI
	t.Cleanup(func() { execGatewayCLI = orig })

	var calls [][]string
	var stubErr error
	execGatewayCLI = func(args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return stubErr
	}

	if err := startGateway(); err != nil {
		t.Fatalf("startGateway() unexpected error: %v", err)
	}

	want := [][]string{{"start"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("execGatewayCLI calls = %v, want %v", calls, want)
	}

	wantErr := errors.New("already running")
	stubErr = wantErr
	if err := startGateway(); !errors.Is(err, wantErr) {
		t.Fatalf("startGateway() error = %v, want %v", err, wantErr)
	}
}

func TestGatewayBinaryNameFromArg0(t *testing.T) {
	tests := []struct {
		arg0 string
		want string
	}{
		{"opencode-go-tray", "opencode-go"},
		{"opencode-go-tray.exe", "opencode-go"},
		{"opencode-go-tray.EXE", "opencode-go"},
		{filepath.Join("dir", "custom-tray.exe"), "custom"},
		{"9router-go-tray.exe", "9router-go"},
		{"-tray", "9router-go"},
		{"-tray.exe", "9router-go"},
		{"other.exe", "9router-go"},
		{"", "9router-go"},
	}
	for _, tt := range tests {
		if got := gatewayBinaryNameFromArg0(tt.arg0); got != tt.want {
			t.Errorf("gatewayBinaryNameFromArg0(%q) = %q, want %q", tt.arg0, got, tt.want)
		}
	}
}
