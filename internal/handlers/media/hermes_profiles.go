package media

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Hermes profile discovery and path resolution. Port of upstream
// `src/app/api/cli-tools/hermes-settings/hermesProfiles.js`.
//
// A Hermes profile is a separate home directory (HERMES_HOME):
//
//	~/.hermes                     → the "default" profile (the install root)
//	~/.hermes/profiles/<name>     → a named profile
//
// Hermes only treats a directory under profiles/ as a profile when it carries
// one of the identity files below; a bare directory left behind by logging or
// a cron run is ignored. Everything here is derived from files on disk — no
// `hermes` subprocess is spawned, so there is no shell interpolation surface.

var (
	// profileNameRE keeps profile names inert: they become directory names
	// and command aliases, and both go through a shell.
	profileNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

	// hermesIdentityFiles are the files that make a directory a real profile.
	hermesIdentityFiles = []string{"config.yaml", ".env", "SOUL.md", "profile.yaml", "auth.json", "state.db"}
)

var (
	errInvalidProfileName = errors.New("invalid Hermes profile name (letters, digits, - and _ only, max 64 chars)")
	errInvalidProfilePath = errors.New("invalid Hermes profile path")
)

// hermesHome is a resolved profile directory plus the two files we edit.
type hermesHome struct {
	Name       string `json:"name"`
	Dir        string `json:"dir"`
	IsDefault  bool   `json:"isDefault"`
	ConfigPath string `json:"-"`
	EnvPath    string `json:"-"`
}

// hermesProfile is one entry of the dashboard's profile picker.
type hermesProfile struct {
	Name        string  `json:"name"`
	Dir         string  `json:"dir"`
	IsDefault   bool    `json:"isDefault"`
	DisplayName *string `json:"displayName"`
	Command     string  `json:"command"`
	Alias       *string `json:"alias"`
	Model       *string `json:"model"`
	BaseURL     *string `json:"baseUrl"`
	Has9Router  bool    `json:"has9Router"`
}

// hermesRoot is the install root. HERMES_HOME wins over the home directory so
// an operator (and every test) can point at a scratch tree instead of the real
// ~/.hermes.
func hermesRoot() string {
	if root := strings.TrimSpace(os.Getenv("HERMES_HOME")); root != "" {
		return root
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".hermes"
	}
	return filepath.Join(home, ".hermes")
}

func hermesProfilesRoot() string { return filepath.Join(hermesRoot(), "profiles") }

// isValidProfileName reports whether name may be used as a profile directory.
// "default" is reserved: it always means the install root.
func isValidProfileName(name string) bool {
	return profileNameRE.MatchString(name) && !strings.EqualFold(name, "default")
}

// resolveHermesHome maps a profile name to its home directory.
//
// Empty or "default" is the install root; anything else must pass the name
// check AND land inside <root>/profiles — the containment check is the
// backstop that keeps a crafted ?profile= from reaching ~/.ssh.
func resolveHermesHome(profile string) (hermesHome, error) {
	base := hermesRoot()
	name := strings.TrimSpace(profile)
	if name == "" || name == "default" {
		return hermesHome{
			Name:       "default",
			Dir:        base,
			IsDefault:  true,
			ConfigPath: filepath.Join(base, "config.yaml"),
			EnvPath:    filepath.Join(base, ".env"),
		}, nil
	}
	if !isValidProfileName(name) {
		return hermesHome{}, errInvalidProfileName
	}
	root, err := filepath.Abs(hermesProfilesRoot())
	if err != nil {
		return hermesHome{}, fmt.Errorf("resolveHermesHome: %w", err)
	}
	dir, err := filepath.Abs(filepath.Join(root, name))
	if err != nil {
		return hermesHome{}, fmt.Errorf("resolveHermesHome: %w", err)
	}
	if !strings.HasPrefix(dir, root+string(filepath.Separator)) {
		return hermesHome{}, errInvalidProfilePath
	}
	return hermesHome{
		Name:       name,
		Dir:        dir,
		IsDefault:  false,
		ConfigPath: filepath.Join(dir, "config.yaml"),
		EnvPath:    filepath.Join(dir, ".env"),
	}, nil
}

// dirExists reports whether path is an existing directory.
func dirExists(dir string) bool {
	st, err := os.Stat(dir)
	return err == nil && st.IsDir()
}

// isProfileHome reports whether dir carries at least one Hermes identity file.
func isProfileHome(dir string) bool {
	for _, file := range hermesIdentityFiles {
		if _, err := os.Stat(filepath.Join(dir, file)); err == nil {
			return true
		}
	}
	return false
}

// readHermesText reads a file, treating "not found" as empty. A fresh profile
// has no config.yaml and that is not an error.
func readHermesText(file string) (string, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("readHermesText %s: %w", file, err)
	}
	return string(b), nil
}

// buildHermesEntry assembles the picker entry for one profile home.
func buildHermesEntry(home hermesHome) (hermesProfile, error) {
	yaml, err := readHermesText(home.ConfigPath)
	if err != nil {
		return hermesProfile{}, err
	}
	profileYAML, err := readHermesText(filepath.Join(home.Dir, "profile.yaml"))
	if err != nil {
		return hermesProfile{}, err
	}

	model := parseHermesModelBlock(yaml)
	delegation := parseHermesDelegationBlock(yaml)

	entry := hermesProfile{
		Name:        home.Name,
		Dir:         home.Dir,
		IsDefault:   home.IsDefault,
		DisplayName: parseHermesDisplayName(profileYAML),
		Command:     "hermes",
		Model:       model.defaultModel(),
		BaseURL:     model.baseURL(),
		Has9Router:  has9RouterConfig(model.provider(), model.baseURL()) || has9RouterConfig(delegation.provider(), delegation.baseURL()),
	}
	if !home.IsDefault {
		// What the user types to run this profile; alias is the
		// ~/.local/bin/<name> wrapper Hermes installs.
		entry.Command = "hermes -p " + home.Name
		entry.Alias = new(home.Name)
	}
	for _, cfg := range parseHermesAuxRoles(yaml) {
		if has9RouterConfig(cfg.Provider, cfg.BaseURL) {
			entry.Has9Router = true
			break
		}
	}
	return entry, nil
}

// listHermesProfiles returns the default home plus every recognised directory
// under profiles/. A missing install is not an error: the result is just the
// default entry, so the dashboard can still render a profile picker.
func listHermesProfiles() ([]hermesProfile, error) {
	home, err := resolveHermesHome("")
	if err != nil {
		return nil, fmt.Errorf("listHermesProfiles: %w", err)
	}
	defaultEntry, err := buildHermesEntry(home)
	if err != nil {
		return nil, fmt.Errorf("listHermesProfiles: %w", err)
	}
	entries := []hermesProfile{defaultEntry}

	names, err := profileDirNames(hermesProfilesRoot())
	if err != nil {
		return entries, nil
	}
	for _, name := range names {
		profileHome, err := resolveHermesHome(name)
		if err != nil {
			continue
		}
		if !isProfileHome(profileHome.Dir) {
			continue // bare directory — Hermes ignores it too
		}
		entry, err := buildHermesEntry(profileHome)
		if err != nil {
			continue // one unreadable profile must not fail the whole list
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// profileDirNames lists the valid profile directory names under root, sorted.
// A missing root yields no names.
func profileDirNames(root string) ([]string, error) {
	items, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("profileDirNames %s: %w", root, err)
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		if !item.IsDir() || !isValidProfileName(item.Name()) {
			continue
		}
		names = append(names, item.Name())
	}
	slices.Sort(names)
	return names, nil
}
