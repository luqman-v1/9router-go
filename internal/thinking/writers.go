package thinking

import (
	"9router/proxy/internal/providers"
	"strings"
)

// applyFormat ports applyFormat: one dialect out. none on a model that
// cannot disable clamps to level minimal before the writer runs.
func applyFormat(fmt string, body map[string]any, cfg *Intent, caps providers.Capabilities, supported []string, display string) {
	none := cfg.Mode == ModeNone
	cd := caps.CanDisableThinking()
	eff := cfg
	if none && !cd {
		eff = &Intent{Mode: ModeLevel, Level: "minimal"}
	}
	writer, ok := formatWriters[fmt]
	if !ok {
		return
	}
	writer(&writerCtx{
		body:       body,
		eff:        eff,
		none:       none,
		canDisable: cd,
		supported:  supported,
		display:    display,
		caps:       caps,
	})
}

// knownLevel maps a level the client made up to medium and leaves a level the
// effort dialects know alone. It is the clamp for a format whose level set is
// wider than the registry's picker entries.
func knownLevel(level string) string {
	if KnownEffort(level) {
		return strings.ToLower(strings.TrimSpace(level))
	}
	return "medium"
}

// levelFor emits a level the target accepts. An unknown level becomes medium,
// and a level the model does not advertise falls back to the closest one it
// does — a provider answers a level outside its enum with a 400.
func levelFor(level string, supported []string) string {
	if level == "max" || level == "ultra" {
		return NormalizeOpenAILevel(level, supported)
	}
	if level == "auto" {
		// "auto" asks the upstream to decide; it is not a level to clamp.
		return level
	}
	if !KnownEffort(level) {
		level = "medium"
	}
	if len(supported) == 0 || containsLevel(supported, level) {
		return level
	}
	if level == "minimal" && containsLevel(supported, "low") {
		return "low"
	}
	want := effortRank(level)
	best, bestRank, lowest, lowestRank := "", -1, "", 1<<30
	for _, s := range supported {
		if s == "none" {
			continue
		}
		r := effortRank(s)
		if r < 0 {
			continue
		}
		if r <= want && r > bestRank {
			best, bestRank = s, r
		}
		if r < lowestRank {
			lowest, lowestRank = s, r
		}
	}
	if best != "" {
		return best
	}
	if lowest != "" {
		return lowest
	}
	return level
}

func withDisplay(thinking map[string]any, display string) map[string]any {
	if display != "" {
		thinking["display"] = display
	}
	return thinking
}

func containsLevel(levels []string, want string) bool {
	for _, l := range levels {
		if l == want {
			return true
		}
	}
	return false
}
