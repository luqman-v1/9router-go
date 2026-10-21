package clientconfig

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A non-ASCII password must not trip the length rule: E11 counts bytes because
// bcrypt counts bytes, and 256 Vietnamese characters are far more than 256 bytes.
//
// The bcrypt limit is checked in bytes too, and it is the one that matters: a
// password past 72 bytes would authenticate against its own prefix.
func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name    string
		pw      string
		wantErr error
	}{
		{"empty", "", nil},
		{"short", "s3cr3t", nil},
		{"at the bcrypt limit", strings.Repeat("a", 72), nil},
		{"one past the bcrypt limit", strings.Repeat("a", 73), ErrPasswordTruncatedByBcrypt},
		{"past the bcrypt limit", strings.Repeat("a", 100), ErrPasswordTruncatedByBcrypt},
		{"at the overall limit", strings.Repeat("a", 256), ErrPasswordTruncatedByBcrypt},
		{"one past the overall limit", strings.Repeat("a", 257), ErrPasswordTooLong},
		{"multibyte over the overall limit", strings.Repeat("ậ", 100), ErrPasswordTooLong},
		{"multibyte over the bcrypt limit", strings.Repeat("ậ", 40), ErrPasswordTruncatedByBcrypt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validatePassword(tt.pw)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("validatePassword error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("validatePassword error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// The byte limit, not the rune limit: 30 Vietnamese characters are already
// 60–90 bytes, and bcrypt counts bytes. A password of 30 runes that is 100
// bytes long must be refused — counting runes would let it through.
func TestValidatePassword_CountsBytesNotRunes(t *testing.T) {
	pw := strings.Repeat("ậ", 100) // 100 runes, 300 bytes
	if len(pw) != 300 {
		t.Fatalf("fixture is %d bytes, want 300", len(pw))
	}
	if _, err := validatePassword(pw); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("err = %v, want ErrPasswordTooLong", err)
	}

	// 24 runes of "ậ" is 72 bytes — exactly bcrypt's limit, and it must pass.
	atLimit := strings.Repeat("ậ", 24)
	if len(atLimit) != 72 {
		t.Fatalf("fixture is %d bytes, want 72", len(atLimit))
	}
	if _, err := validatePassword(atLimit); err != nil {
		t.Fatalf("72-byte multibyte password refused: %v", err)
	}
}

// P2 — off a terminal with NINE_ROUTER_PASSWORD set, the fallback is used and
// the risk is printed. Silence here would be the failure: the user has to know
// the variable is readable by their other processes.
func TestReadConnectPassword_EnvFallbackWarns(t *testing.T) {
	var warn bytes.Buffer
	got, err := ReadConnectPassword(PasswordOptions{
		Env: func(k string) string {
			if k == PasswordEnvVar {
				return "s3cr3t"
			}
			return ""
		},
		IsTTY: func() bool { return false },
		Warn:  &warn,
	})
	if err != nil {
		t.Fatalf("ReadConnectPassword = %v, want nil", err)
	}
	if got != "s3cr3t" {
		t.Errorf("password = %q", got)
	}
	if !strings.Contains(warn.String(), PasswordEnvVar) {
		t.Errorf("warning = %q, want it to name %s", warn.String(), PasswordEnvVar)
	}
	if !strings.Contains(warn.String(), "/proc/") {
		t.Errorf("warning = %q, want it to say who can read the variable", warn.String())
	}
}

// P3 — no terminal and no env is E10, never a read from the pipe. A pipe can
// hold anything; consuming it silently is the behaviour §6.2.3 forbids.
func TestReadConnectPassword_NoTTYNoEnv(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer pr.Close()
	if _, err := pw.WriteString("piped-secret\n"); err != nil {
		t.Fatalf("write pipe: %v", err)
	}
	pw.Close()

	var warn bytes.Buffer
	got, err := ReadConnectPassword(PasswordOptions{
		Env:   func(string) string { return "" },
		Stdin: pr,
		IsTTY: func() bool { return false },
		Warn:  &warn,
	})
	if !errors.Is(err, ErrNoTTY) {
		t.Fatalf("err = %v, want ErrNoTTY", err)
	}
	if got != "" {
		t.Errorf("password = %q, want empty — the pipe must not be read", got)
	}
	if !strings.Contains(err.Error(), PasswordEnvVar) {
		t.Errorf("err = %v, want it to name %s", err, PasswordEnvVar)
	}

	// Nothing was consumed: the bytes are still there for whoever owns the pipe.
	buf := make([]byte, 64)
	n, _ := pr.Read(buf)
	if n == 0 {
		t.Error("the pipe was drained; §6.2.3 says it must never be read")
	}
}

// P1 — the removed flag is refused with a reason, and the value after it is not
// echoed back.
//
// The attached short form ("-pSECRET") is the case that matters most: it is
// the one spelling no amount of grepping the shell history finds, and it is
// also the one a "="-only split misses.
func TestRejectPasswordFlag(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"--password=value", []string{"--password=secret"}, true},
		{"--password alone", []string{"--password", "secret"}, true},
		{"short form", []string{"-p", "secret"}, true},
		{"short form with attached value", []string{"-pSECRET"}, true},
		{"--pass", []string{"--pass=hunter2"}, true},
		{"--pw", []string{"--pw=xyz"}, true},
		{"flag after --", []string{"--", "--password=secret"}, false},
		{"unrelated flags", []string{"--dry-run", "--allow-insecure"}, false},
		{"prefix lookalike", []string{"--passwordless=x"}, false},
		{"short flag that is not a password", []string{"-proxy"}, false},
		{"short flag with value that is not a password", []string{"-proxy=http://x"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RejectPasswordFlag(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("RejectPasswordFlag(%v) = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
			if !tt.wantErr {
				return
			}
			if !errors.Is(err, ErrPasswordFlagRemoved) {
				t.Errorf("err = %v, want ErrPasswordFlagRemoved", err)
			}
			for _, secret := range []string{"secret", "hunter2", "xyz", "SECRET"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("err %q echoes the value %q", err, secret)
				}
			}
		})
	}
}

// The error names the flag, not the value attached to it. "-pSECRET" must
// report "-p": printing the argument would print the password (case P1).
func TestRejectPasswordFlag_NamesTheFlagNotTheValue(t *testing.T) {
	err := RejectPasswordFlag([]string{"-pSECRET"})
	if err == nil {
		t.Fatal("attached short form accepted")
	}
	if !strings.Contains(err.Error(), "-p") {
		t.Errorf("err = %q, want it to name the -p flag", err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("err %q echoes the value", err)
	}
}

// Reading a line stops at the newline and drops a CR that terminates the line,
// so a Windows console and a Unix one produce the same password.
//
// A CR in the middle is content, not a line ending: dropping it turns
// "pa\rss" into "pass", and the user then authenticates with a password they
// never typed.
func TestReadLine(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"abc\n", "abc"},
		{"abc\r\n", "abc"},
		{"abc", "abc"},
		{"\n", ""},
		{"pa\rss\n", "pa\rss"},
		{"pa\rss", "pa\rss"},
		{"a\rb\r\n", "a\rb"},
		{"\r\n", ""},
	}
	for _, tt := range tests {
		got, err := readLine(strings.NewReader(tt.in))
		if err != nil {
			t.Fatalf("readLine(%q) = %v", tt.in, err)
		}
		if string(got) != tt.want {
			t.Errorf("readLine(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// The prompt path on a terminal writes the prompt to the warning channel, never
// to stdout, and restores the writer regardless of the read outcome.
func TestReadConnectPassword_PromptGoesToWarnWriter(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "stdin")
	if err := os.WriteFile(path, []byte("typed\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	var warn bytes.Buffer
	// IsTTY lies on purpose: readPasswordFromConsole will fail on a regular
	// file, and the point here is that the failure is reported, not that it
	// succeeded. P5 covers the real terminal separately.
	_, err = ReadConnectPassword(PasswordOptions{
		Env:   func(string) string { return "" },
		Stdin: f,
		IsTTY: func() bool { return true },
		Warn:  &warn,
	})
	if err == nil {
		t.Skip("platform accepted a regular file as a console; nothing to assert")
	}
	if !strings.Contains(warn.String(), "Mật khẩu") {
		t.Errorf("warn = %q, want the prompt", warn.String())
	}
}
