package clientconfig

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// PasswordEnvVar is the non-interactive fallback. The name matches upstream so
// CI scripts ported from the Node CLI keep working; the risk it carries is
// printed on every use (§6.3).
const PasswordEnvVar = "NINE_ROUTER_PASSWORD"

// maxPasswordLen is where a password is refused outright. 256 is generous and
// still catches a pasted key, which is the mistake this limit is really about.
const maxPasswordLen = 256

// bcryptMaxPasswordLen is bcrypt's own limit, and the one that matters.
//
// The blowfish key schedule uses the first 72 bytes of the password and
// ignores the rest. bcrypt.GenerateFromPassword refuses a longer input, but
// bcrypt.CompareHashAndPassword — the call the dashboard makes — silently
// authenticates a 100-byte password whose first 72 bytes match the stored
// one. The user is then logging in with a password that is not the one they
// think they typed, and the trailing 28 bytes are decoration.
//
// The limit is in bytes, not characters: 30 Vietnamese characters are already
// 60–90 bytes.
const bcryptMaxPasswordLen = 72

// EnvPasswordWarning is printed when the fallback is used. Not a debug line:
// the variable is readable by any process running as the same user.
const EnvPasswordWarning = "Cảnh báo: đang dùng " + PasswordEnvVar + " từ biến môi trường.\n" +
	"Biến môi trường có thể bị đọc bởi tiến trình khác cùng người dùng\n" +
	"(/proc/<pid>/environ trên Linux, danh sách tiến trình trên các hệ khác).\n" +
	"Ưu tiên: chạy tương tác để được nhắc mật khẩu (không echo)."

// PasswordOptions carries the seams a test needs. Every field is optional and
// has no package-level counterpart: a package variable here would race with
// t.Parallel() and leak a stub into the next test (safe-file-operations.md
// §7.1, lesson M2).
type PasswordOptions struct {
	// Env reads an environment variable. nil means os.Getenv.
	Env func(string) string
	// Stdin is the terminal to read from. nil means os.Stdin.
	Stdin *os.File
	// IsTTY reports whether Stdin is a terminal. nil means ask the platform.
	IsTTY func() bool
	// Prompt is written before reading. Empty means a default.
	Prompt string
	// Warn receives the env-fallback warning and the prompt. nil means
	// os.Stderr, so stdout stays available for machine-readable output.
	Warn io.Writer
}

// ReadConnectPassword returns the dashboard password without ever taking it
// from argv.
//
// On a terminal it prompts and reads with echo disabled. Off a terminal it uses
// PasswordEnvVar and says why that is risky. It never reads the password from a
// pipe: a pipe can hold anything, and consuming it silently is exactly the
// hidden behaviour §6.2.3 rules out.
func ReadConnectPassword(opts PasswordOptions) (string, error) {
	env := opts.Env
	if env == nil {
		env = os.Getenv
	}
	stdin := opts.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	isTTY := opts.IsTTY
	if isTTY == nil {
		isTTY = func() bool { return isTerminal(stdin) }
	}
	warn := opts.Warn
	if warn == nil {
		warn = os.Stderr
	}
	prompt := opts.Prompt
	if prompt == "" {
		prompt = "Mật khẩu dashboard: "
	}

	if isTTY() {
		_, _ = fmt.Fprint(warn, prompt)
		line, err := readPasswordFromConsole(stdin)
		_, _ = fmt.Fprintln(warn) // the user's Enter was not echoed either
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		return validatePassword(string(line))
	}

	if v := env(PasswordEnvVar); v != "" {
		_, _ = fmt.Fprintln(warn, EnvPasswordWarning)
		return validatePassword(v)
	}
	return "", fmt.Errorf("%w: đặt %s nếu chạy không tương tác, hoặc chạy lệnh trong terminal thật",
		ErrNoTTY, PasswordEnvVar)
}

// validatePassword enforces E11. The length is in bytes on purpose: bcrypt's
// own limit is a byte limit, so counting runes would let a multi-byte password
// through to the truncation this check exists to prevent.
//
// The two limits are separate sentinels because they are separate faults, and
// the order matters: the 256-byte check runs first so a 257-character password
// still reports ErrPasswordTooLong (case P4), and the 72-byte check catches the
// range in between — the passwords that would authenticate against the wrong
// prefix, which is the fault the spec's E11 rationale describes.
func validatePassword(pw string) (string, error) {
	if len(pw) > maxPasswordLen {
		return "", fmt.Errorf("%w: %d bytes", ErrPasswordTooLong, len(pw))
	}
	if len(pw) > bcryptMaxPasswordLen {
		return "", fmt.Errorf("%w: %d bytes (bcrypt chỉ dùng %d byte đầu, phần còn lại bị bỏ qua)",
			ErrPasswordTruncatedByBcrypt, len(pw), bcryptMaxPasswordLen)
	}
	return pw, nil
}

// readLine reads until the newline, without the terminal's line editing being
// needed. Echo is already off when this runs.
//
// Only a CR that terminates the line is dropped. A CR in the middle of the
// password is a byte the user typed — dropping it turns "pa\rss" into "pass",
// and the user then authenticates with a password they never typed. CRLF is
// the Windows console's terminator, so the CR there is a line ending, not
// content.
func readLine(r io.Reader) ([]byte, error) {
	br := bufio.NewReader(r)
	var out []byte
	for {
		b, err := br.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
		if b == '\n' {
			// CRLF: the CR was the terminator, not content.
			if n := len(out); n > 0 && out[n-1] == '\r' {
				out = out[:n-1]
			}
			return out, nil
		}
		out = append(out, b)
		if len(out) > maxPasswordLen {
			// Stop reading rather than buffering an unbounded paste; the
			// length check reports it as E11.
			return out, nil
		}
	}
}

// passwordFlagNames are the long spellings §6.1 forbids. They are matched as a
// separate argument or as "--name=value"; the value is never read back.
var passwordFlagNames = []string{"--password", "--pass", "--pw"}

// passwordShortFlag is the short spelling §6.1 forbids.
//
// A short flag takes its value attached ("-pSECRET"), which is the one
// spelling no amount of grepping the shell history finds. Splitting the long
// form on "=" alone therefore misses it, and it is the worst of the leak
// channels in the §6.1 table.
//
// It is matched as a prefix, so "-p" and "-pSECRET" are both caught. The one
// exception is "-proxy", which is a different flag and not a password — see
// bannedPasswordFlag.
const passwordShortFlag = "-p"

// RejectPasswordFlag reports E12 when the command line carries a password flag.
//
// It is a diagnosis, not a parser: the error names the flag but never includes
// what came after "=" — echoing it would put the secret in the terminal and in
// any log that captured the error (case P1). The same holds for an attached
// short-flag value: "-pSECRET" is reported as "-p", never as the argument.
func RejectPasswordFlag(args []string) error {
	for _, a := range args {
		if a == "--" {
			return nil
		}
		if flag, ok := bannedPasswordFlag(a); ok {
			return fmt.Errorf("%w: bỏ %s để tránh lộ mật khẩu qua lịch sử shell; hãy chạy tương tác hoặc dùng %s",
				ErrPasswordFlagRemoved, flag, PasswordEnvVar)
		}
	}
	return nil
}

// bannedPasswordFlag reports whether an argument is one §6.1 forbids, and
// returns the flag name to put in the error message.
//
// The returned name is the flag, never the value attached to it: for "-pSECRET"
// it is "-p", because printing the argument would print the password (case P1).
//
// It distinguishes a short flag's attached value from a legitimate
// multi-character flag. "-proxy" is not a password flag and must not be
// rejected as "-p"; the exemption is on the exact name, so "-proxySECRET" is
// still caught — that is not a flag anyone defines.
func bannedPasswordFlag(arg string) (string, bool) {
	name, _, _ := strings.Cut(arg, "=")
	for _, banned := range passwordFlagNames {
		if name == banned {
			return banned, true
		}
	}
	if name == "-proxy" {
		return "", false
	}
	if name == passwordShortFlag || (strings.HasPrefix(name, passwordShortFlag) && !strings.HasPrefix(name, "--")) {
		return passwordShortFlag, true
	}
	return "", false
}
