package clientconfig

import (
	"fmt"
	"os"
	"path/filepath"
)

// Path resolution for the nine clients in docs/specs/client-schema-matrix.md §3.
//
// Every function here takes the home directory as an argument rather than
// reading it from the environment. That is what lets the adapter tests build a
// fixture in t.TempDir() and never touch the real ~/.claude: §9 of the schema
// matrix forbids reading the developer's own configuration, and a resolver that
// calls os.UserHomeDir internally cannot be held to that.

// Paths is one client's resolved target files.
//
// Files is ordered: a client whose reset has to undo several writes depends on
// the order being the same one Apply used. Dir is the nearest directory the
// adapter is allowed to touch, which is what resolveGuard takes as its base —
// never $HOME itself, or containment would permit every path on the machine.
type Paths struct {
	Files []TargetFile
	Dir   string
}

// TargetFile is one file a client owns, with the base directory its guard is
// resolved against.
type TargetFile struct {
	// Path is absolute and fully resolved: no "~", no "$HOME", no unexpanded
	// variable. TestPathResolution asserts that for all nine on all three OSes.
	Path string
	// Dir is the nearest ancestor the adapter may touch, e.g. ~/.claude.
	Dir string
	// Format is how the file is parsed and spliced.
	Format Format
	// Purpose names what the file holds, for error messages that have to say
	// which of a two-file client failed.
	Purpose string
}

// homeDir resolves the operator's home directory.
//
// os.UserHomeDir reads $HOME on POSIX and %USERPROFILE% on Windows, which is
// exactly the rule the schema matrix §2 states. It is the one place this
// package reads the environment for a path.
func homeDir() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if h == "" {
		return "", fmt.Errorf("resolve home directory: empty result")
	}
	return h, nil
}

// xdgConfigHome returns $XDG_CONFIG_HOME when set, else ~/.config.
//
// The matrix §2 keeps the same layout on Windows (~/.config/<tool>) rather than
// switching to %APPDATA%, because the clients themselves do that. Following the
// client is the only way the file the user edits is the file this patches.
func xdgConfigHome(home string) string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(home, ".config")
}

// claudePaths is ~/.claude/settings.json on every platform (matrix §3.1).
func claudePaths(home string) Paths {
	dir := filepath.Join(home, ".claude")
	return Paths{
		Dir:   dir,
		Files: []TargetFile{{Path: filepath.Join(dir, "settings.json"), Dir: dir, Format: FormatJSONC, Purpose: "settings"}},
	}
}

// codexPaths is ~/.codex/config.toml. auth.json sits beside it and is
// deliberately not a target: matrix §3.2 records that a custom provider ignores
// it and authenticates through the static header instead, so patching it would
// change nothing and risk a credential the gateway never reads.
func codexPaths(home string) Paths {
	dir := filepath.Join(home, ".codex")
	return Paths{
		Dir:   dir,
		Files: []TargetFile{{Path: filepath.Join(dir, "config.toml"), Dir: dir, Format: FormatTOML, Purpose: "config"}},
	}
}

// opencodePaths is ~/.config/opencode/opencode.json on every platform.
func opencodePaths(home string) Paths {
	dir := filepath.Join(xdgConfigHome(home), "opencode")
	return Paths{
		Dir:   dir,
		Files: []TargetFile{{Path: filepath.Join(dir, "opencode.json"), Dir: dir, Format: FormatJSONC, Purpose: "config"}},
	}
}

// droidPaths is ~/.factory/settings.json.
func droidPaths(home string) Paths {
	dir := filepath.Join(home, ".factory")
	return Paths{
		Dir:   dir,
		Files: []TargetFile{{Path: filepath.Join(dir, "settings.json"), Dir: dir, Format: FormatJSONC, Purpose: "settings"}},
	}
}

// crushPaths honours $XDG_CONFIG_HOME like the client does, falling back to
// ~/.config (matrix §3.5).
func crushPaths(home string) Paths {
	dir := filepath.Join(xdgConfigHome(home), "crush")
	return Paths{
		Dir:   dir,
		Files: []TargetFile{{Path: filepath.Join(dir, "crush.json"), Dir: dir, Format: FormatJSONC, Purpose: "config"}},
	}
}

// kiloPaths is ~/.local/share/kilo/auth.json. The dashboard also patches the
// VS Code settings.json; D7 keeps that out of `connect`, so it is not here.
func kiloPaths(home string) Paths {
	dir := filepath.Join(home, ".local", "share", "kilo")
	return Paths{
		Dir:   dir,
		Files: []TargetFile{{Path: filepath.Join(dir, "auth.json"), Dir: dir, Format: FormatJSONC, Purpose: "auth"}},
	}
}

// clinePaths is two files under ~/.cline/data. They are ordered state first
// because that is the file Apply writes first; a reset that ran the other way
// would clear the secret while the provider still pointed at it.
func clinePaths(home string) Paths {
	dir := filepath.Join(home, ".cline", "data")
	return Paths{
		Dir: dir,
		Files: []TargetFile{
			{Path: filepath.Join(dir, "globalState.json"), Dir: dir, Format: FormatJSONC, Purpose: "state"},
			{Path: filepath.Join(dir, "secrets.json"), Dir: dir, Format: FormatJSONC, Purpose: "secrets"},
		},
	}
}

// piPaths prefers ~/.pi/agent/models.json and falls back to the older
// ~/.pi/models.json when only that exists (matrix §3.8).
func piPaths(home string) Paths {
	dir := filepath.Join(home, ".pi")
	agentDir := filepath.Join(dir, "agent")
	preferred := filepath.Join(agentDir, "models.json")
	if _, err := os.Stat(preferred); err != nil {
		legacy := filepath.Join(dir, "models.json")
		if _, legacyErr := os.Stat(legacy); legacyErr == nil {
			return Paths{Dir: dir, Files: []TargetFile{{Path: legacy, Dir: dir, Format: FormatJSONC, Purpose: "models"}}}
		}
	}
	return Paths{
		Dir:   agentDir,
		Files: []TargetFile{{Path: preferred, Dir: agentDir, Format: FormatJSONC, Purpose: "models"}},
	}
}

// ompPaths is ~/.omp/agent/models.yml. The SQLite agent.db beside it is the
// dashboard's business, not connect's (matrix §3.9).
func ompPaths(home string) Paths {
	dir := filepath.Join(home, ".omp", "agent")
	return Paths{
		Dir:   dir,
		Files: []TargetFile{{Path: filepath.Join(dir, "models.yml"), Dir: dir, Format: FormatYAML, Purpose: "models"}},
	}
}
