package main

import (
	"errors"
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
