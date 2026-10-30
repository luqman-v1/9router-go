package clientconfig

import (
	"errors"
	"strconv"
	"strings"
)

// Claude Code: ~/.claude/settings.json, JSONC, all settings under "env".
// docs/specs/client-schema-matrix.md §3.1 and connect-cli-spec.md §3.2.4.

// claudeEnvKeys is every key Apply manages. Reset removes exactly these.
//
// hasCompletedOnboarding is written but deliberately absent here: it records
// that the user finished the onboarding flow, and a reset that cleared it would
// send them through it again for no reason connected to 9router.
var claudeEnvKeys = []string{
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"CLAUDE_CODE_AUTO_COMPACT_WINDOW",
}

// claudeTierEnv maps a --<tier> flag name to the env key it sets.
var claudeTierEnv = map[string]string{
	"fable":  "ANTHROPIC_DEFAULT_FABLE_MODEL",
	"opus":   "ANTHROPIC_DEFAULT_OPUS_MODEL",
	"sonnet": "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"haiku":  "ANTHROPIC_DEFAULT_HAIKU_MODEL",
}

type claudeAdapter struct{ baseAdapter }

func newClaudeAdapter() Adapter {
	return claudeAdapter{baseAdapter{id: "claude", paths: claudePaths}}
}

func (a claudeAdapter) Plan(opts Options) ([]FilePlan, error) {
	paths, err := a.Paths(opts)
	if err != nil {
		return nil, err
	}
	edits := claudeApplyEdits(paths.Files[0], opts)
	return []FilePlan{{File: paths.Files[0], Edits: edits}}, nil
}

func (a claudeAdapter) Apply(opts Options) (Result, error) {
	plans, err := a.Plan(opts)
	if err != nil {
		return Result{}, err
	}
	return applyPlans(plans, nil)
}

func (a claudeAdapter) Reset(opts Options) (Result, error) {
	paths, err := a.Paths(opts)
	if err != nil {
		return Result{}, err
	}
	edits := make([]Edit, 0, len(claudeEnvKeys)+2)
	for _, k := range claudeEnvKeys {
		edits = append(edits, Edit{KeyPath: []string{"env", k}, Delete: true})
	}
	// autoCompactWindow is adapter-owned, removed unconditionally.
	edits = append(edits, Edit{KeyPath: []string{"autoCompactWindow"}, Delete: true})

	// `model` is the user's own key once the env block is gone, so it is only
	// removed when Claude was configured to point at the gateway.
	if owned, err := claudePointsAtUs(paths.Files[0], opts.BaseURL); err == nil && owned {
		edits = append(edits, Edit{KeyPath: []string{"model"}, Delete: true})
	}
	return applyPlans([]FilePlan{{File: paths.Files[0], Edits: edits}}, nil)
}

func isClaudeGatewayURL(rawURL, baseURL string) bool {
	u := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if u == "" {
		return false
	}
	// The official Anthropic API or foreign providers are never our gateway
	if strings.Contains(u, "api.anthropic.com") {
		return false
	}
	if baseURL != "" {
		target := strings.TrimRight(strings.TrimSpace(baseURL), "/")
		targetV1 := strings.TrimRight(withV1(baseURL), "/")
		normU := strings.Replace(u, "127.0.0.1", "localhost", 1)
		normTarget := strings.Replace(target, "127.0.0.1", "localhost", 1)
		normTargetV1 := strings.Replace(targetV1, "127.0.0.1", "localhost", 1)
		return normU == normTarget || normU == normTargetV1
	}
	// Default ports when baseURL is not explicitly supplied in options
	return strings.Contains(u, "20130") || strings.Contains(u, "20128") || strings.Contains(u, "9router")
}

func claudePointsAtUs(file TargetFile, baseURL string) (bool, error) {
	g, err := resolveGuard(file.Dir, relFromDir(file.Dir, file.Path))
	if err != nil {
		if errors.Is(err, ErrBaseMissing) {
			return false, nil
		}
		return false, err
	}
	b, err := readThroughGuard(g)
	if err != nil || len(b) == 0 {
		return false, err
	}
	p, root, err := parseJSONC(b)
	if err != nil || root == nil {
		return false, err
	}
	if envMem := findJSONMember(root, "env"); envMem != nil && envMem.val != nil && envMem.val.kind == jLBrace {
		for _, m := range envMem.val.members {
			if m.key == "ANTHROPIC_BASE_URL" && m.val != nil && m.val.kind == jString && m.val.tok < len(p.toks) {
				tok := p.toks[m.val.tok]
				if tok.off+tok.len <= len(b) {
					if str, err := decodeJSONString(b[tok.off : tok.off+tok.len]); err == nil {
						return isClaudeGatewayURL(str, baseURL), nil
					}
				}
			}
		}
	}
	return false, nil
}

func (a claudeAdapter) Current(opts Options) (map[string]any, error) {
	paths, err := a.Paths(opts)
	if err != nil || len(paths.Files) == 0 {
		return nil, err
	}
	file := paths.Files[0]
	g, err := resolveGuard(file.Dir, relFromDir(file.Dir, file.Path))
	if err != nil {
		if errors.Is(err, ErrBaseMissing) {
			return nil, nil
		}
		return nil, err
	}
	b, err := readThroughGuard(g)
	if err != nil || len(b) == 0 {
		return nil, err
	}
	p, root, err := parseJSONC(b)
	if err != nil || root == nil {
		return nil, err
	}
	res := make(map[string]any)
	tiers := make(map[string]string)
	if envMem := findJSONMember(root, "env"); envMem != nil && envMem.val != nil && envMem.val.kind == jLBrace {
		for _, m := range envMem.val.members {
			if m.val != nil && m.val.kind == jString && m.val.tok < len(p.toks) {
				tok := p.toks[m.val.tok]
				if tok.off+tok.len <= len(b) {
					str, err := decodeJSONString(b[tok.off : tok.off+tok.len])
					if err == nil {
						switch m.key {
						case "ANTHROPIC_BASE_URL":
							res["baseUrl"] = str
						case "ANTHROPIC_AUTH_TOKEN":
							if str != "" {
								res["hasApiKey"] = true
							}
						case "ANTHROPIC_DEFAULT_SONNET_MODEL":
							tiers["sonnet"] = str
						case "ANTHROPIC_DEFAULT_OPUS_MODEL":
							tiers["opus"] = str
						case "ANTHROPIC_DEFAULT_HAIKU_MODEL":
							tiers["haiku"] = str
						case "ANTHROPIC_DEFAULT_FABLE_MODEL":
							tiers["fable"] = str
						}
					}
				}
			}
		}
	}
	if len(tiers) > 0 {
		res["tiers"] = tiers
	}
	if modelMem := findJSONMember(root, "model"); modelMem != nil && modelMem.val != nil && modelMem.val.kind == jString && modelMem.val.tok < len(p.toks) {
		tok := p.toks[modelMem.val.tok]
		if tok.off+tok.len <= len(b) {
			if str, err := decodeJSONString(b[tok.off : tok.off+tok.len]); err == nil {
				res["model"] = str
			}
		}
	}
	if compactMem := findJSONMember(root, "autoCompactWindow"); compactMem != nil && compactMem.val != nil && compactMem.val.tok < len(p.toks) {
		tok := p.toks[compactMem.val.tok]
		if tok.off+tok.len <= len(b) {
			rawStr := strings.TrimSpace(string(b[tok.off : tok.off+tok.len]))
			if n, err := strconv.Atoi(rawStr); err == nil {
				res["autoCompactWindow"] = n
			} else {
				res["autoCompactWindow"] = rawStr
			}
		}
	}
	return res, nil
}

// claudeApplyEdits builds the settings write.
//
// The tier rule is the whole subtlety (connect-cli-spec §3.2.4): a tier takes
// its value from the flag, else from the server's config, else the key is left
// exactly as the user had it. The third branch is why this is not a table of
// unconditional edits — the Go gateway does not yet serve the nine -settings
// routes, so "the server has no value" and "the server answered 404" are
// indistinguishable, and deleting on either would wipe a working setup on every
// run. Keeping a stale value fails softly and visibly; deleting a right one
// fails hard and silently.
//
// Every edit here is CreateIfMissing, so a first-ever connect creates "env" and
// fills it without a separate branch.
func claudeApplyEdits(file TargetFile, opts Options) []Edit {
	edits := []Edit{
		{KeyPath: []string{"env", "ANTHROPIC_BASE_URL"}, Value: withV1(opts.BaseURL), CreateIfMissing: true},
	}
	if apiKey := strings.TrimSpace(opts.APIKey); apiKey != "" {
		edits = append(edits, Edit{KeyPath: []string{"env", "ANTHROPIC_AUTH_TOKEN"}, Value: apiKey, CreateIfMissing: true})
	}
	// Deterministic order so a dry-run prints the same plan twice.
	for _, tier := range []string{"fable", "opus", "sonnet", "haiku"} {
		value, ok := claudeTierValue(opts, tier)
		if !ok {
			continue // neither source has one: leave whatever the user had
		}
		edits = append(edits, Edit{
			KeyPath:         []string{"env", claudeTierEnv[tier]},
			Value:           value,
			CreateIfMissing: true,
		})
	}
	// Onboarding is a boolean in the file, not a string, so it has no Value to
	// quote. It is written as a scalar literal.
	edits = append(edits, Edit{
		KeyPath:         []string{"hasCompletedOnboarding"},
		ValueJSON:       "true",
		CreateIfMissing: true,
	})
	if v := strings.TrimSpace(opts.AutoCompactWindow); v != "" {
		edits = append(edits, Edit{
			KeyPath:         []string{"autoCompactWindow"},
			ValueJSON:       v,
			CreateIfMissing: true,
		})
	} else {
		// Choosing "Default" unsets autoCompactWindow unconditionally
		edits = append(edits, Edit{
			KeyPath: []string{"autoCompactWindow"},
			Delete:  true,
		})
	}
	if m := strings.TrimSpace(opts.Model); m != "" {
		edits = append(edits, Edit{
			KeyPath:         []string{"model"},
			Value:           m,
			CreateIfMissing: true,
		})
	} else {
		// Choosing "Default" or clearing the model field unsets the key,
		// but only when Claude was pointing at our gateway.
		if owned, err := claudePointsAtUs(file, opts.BaseURL); err == nil && owned {
			edits = append(edits, Edit{
				KeyPath: []string{"model"},
				Delete:  true,
			})
		}
	}
	// CLAUDE_CODE_AUTO_COMPACT_WINDOW is the only way to tell Claude Code a
	// proxied model's real context window: window sizes are per-model-name
	// built into the CLI, and a custom model id via ANTHROPIC_DEFAULT_*_MODEL
	// gets the built-in assumption instead. The docs recommend exactly this
	// variable for a gateway that serves a different window than the name
	// implies — without it auto-compact fires at the wrong boundary or never.
	// An unknown window writes nothing, leaving Claude Code's own default.
	if w := claudeAutoCompactWindow(opts); w > 0 {
		edits = append(edits, Edit{
			KeyPath:         []string{"env", "CLAUDE_CODE_AUTO_COMPACT_WINDOW"},
			Value:           strconv.Itoa(w),
			CreateIfMissing: true,
		})
	}
	return edits
}

// claudeAutoCompactWindow narrows the resolved windows to the one compaction
// must respect. Claude Code applies the env var to every model, and a tier
// setup serves four at once, so the narrowest window of everything this
// connect names is the only value that never overshoots a real limit. The
// CLI accepts 100k–1M; anything outside clamps.
func claudeAutoCompactWindow(opts Options) int {
	best := 0
	consider := func(w int) {
		if w <= 0 {
			return
		}
		if best == 0 || w < best {
			best = w
		}
	}
	if limits, ok := opts.ModelLimits[opts.Model]; ok {
		consider(limits.ContextWindow)
	}
	for _, model := range opts.Tiers {
		if limits, ok := opts.ModelLimits[model]; ok {
			consider(limits.ContextWindow)
		}
	}
	if best == 0 {
		return 0 // nothing resolved: write nothing, keep Claude Code's default
	}
	if best > 1000000 {
		return 1000000
	}
	if best < 100000 {
		// Below the floor the variable cannot express the real window, so the
		// clamped value may fire compaction late; it is still the closest
		// legal guess, and closer than the CLI's built-in assumption.
		return 100000
	}
	return best
}

// claudeTierValue applies the two-source precedence for one tier.
func claudeTierValue(opts Options, tier string) (string, bool) {
	if v := strings.TrimSpace(opts.Tiers[tier]); v != "" {
		return v, true
	}
	if v := strings.TrimSpace(opts.ServerTiers[tier]); v != "" {
		return v, true
	}
	return "", false
}
