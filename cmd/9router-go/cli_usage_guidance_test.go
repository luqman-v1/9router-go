package main

import (
	"strings"
	"testing"
)

// TestGuideMisplacedRootFlag covers the message an operator sees when a global
// flag is typed after the sub-command.
//
// `9router-go start --rtk=false` is the common form of this mistake: urfave/cli
// reports "flag provided but not defined: -rtk" about a flag the binary does
// define, which reads as a missing feature and sends the reader to the release
// notes instead of to their own command line.
func TestGuideMisplacedRootFlag(t *testing.T) {
	tests := []struct {
		name string
		// isSubcommand mirrors urfave's third argument: true when the failing
		// parse happened inside a sub-command rather than at the root.
		isSubcommand  bool
		errText       string
		wantRewritten bool
	}{
		{
			name:          "root flag after subcommand is rewritten",
			isSubcommand:  true,
			errText:       "flag provided but not defined: -rtk",
			wantRewritten: true,
		},
		{
			name:          "background flag is rewritten too",
			isSubcommand:  true,
			errText:       "flag provided but not defined: -background",
			wantRewritten: true,
		},
		{
			name:          "a real typo keeps the original message",
			isSubcommand:  true,
			errText:       "flag provided but not defined: -rtkk",
			wantRewritten: false,
		},
		{
			name:          "a genuine subcommand typo is untouched",
			isSubcommand:  true,
			errText:       "flag provided but not defined: -lins",
			wantRewritten: false,
		},
		{
			name:          "root level failure keeps the original message",
			isSubcommand:  false,
			errText:       "flag provided but not defined: -rtk",
			wantRewritten: false,
		},
		{
			name:          "an unrelated usage error is untouched",
			isSubcommand:  true,
			errText:       "flag needs an argument: -lines",
			wantRewritten: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := guideMisplacedRootFlag(nil, errString(tt.errText), tt.isSubcommand)
			got := err.Error()
			if !tt.wantRewritten {
				if got != tt.errText {
					t.Fatalf("message = %q, want the original %q unchanged", got, tt.errText)
				}
				return
			}
			if got == tt.errText {
				t.Fatalf("message was not rewritten: %q", got)
			}
			if !strings.Contains(got, "before the sub-command") {
				t.Errorf("message %q does not tell the operator where the flag belongs", got)
			}
			if !strings.Contains(got, "9router-go --") {
				t.Errorf("message %q does not show the corrected form", got)
			}
		})
	}
}

// TestInstallUsageGuidanceCoversSubcommands proves the hook reaches nested
// commands, not only the top level: `service start --rtk=false` failed with the
// raw message before the walk existed.
func TestInstallUsageGuidanceCoversSubcommands(t *testing.T) {
	app := newApp("9router-go")
	seen := 0
	for _, c := range app.Commands {
		if c.OnUsageError == nil {
			t.Errorf("command %q has no usage-error hook", c.Name)
		} else {
			seen++
		}
		for _, sub := range c.Subcommands {
			if sub.OnUsageError == nil {
				t.Errorf("sub-command %q %q has no usage-error hook", c.Name, sub.Name)
			} else {
				seen++
			}
		}
	}
	if seen == 0 {
		t.Fatal("no command received the hook")
	}
}

// errString adapts a plain message to the error the hook is handed, without
// pulling a sentinel or an error constructor into the test.
type errString string

func (e errString) Error() string { return string(e) }
