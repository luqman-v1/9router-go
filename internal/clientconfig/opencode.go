package clientconfig

import (
	"errors"
	"strconv"
	"strings"

	"9router/proxy/internal/providers"
)

// OpenCode: ~/.config/opencode/opencode.json, JSONC.
// docs/specs/client-schema-matrix.md §3.3.
//
// The one shape worth knowing: every model this writes carries a "9router/"
// prefix, and Reset only touches keys that still carry it. A model the user
// pointed somewhere else is theirs.

const opencodePrefix = "9router/"

type opencodeAdapter struct{ baseAdapter }

func newOpenCodeAdapter() Adapter {
	return opencodeAdapter{baseAdapter{id: "opencode", paths: opencodePaths}}
}

func (a opencodeAdapter) Plan(opts Options) ([]FilePlan, error) {
	paths, err := a.Paths(opts)
	if err != nil {
		return nil, err
	}
	return []FilePlan{{File: paths.Files[0], Edits: opencodeApplyEdits(paths.Files[0], opts)}}, nil
}

// Current reports the model and base URL opencode is pointed at, mirroring
// what the dashboard's OpenCode card reads back (config.model, provider
// options.baseURL). The models map is skipped: the write side owns it.
func (a opencodeAdapter) Current(opts Options) (map[string]any, error) {
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
	if modelMem := findJSONMember(root, "model"); modelMem != nil && modelMem.val != nil && modelMem.val.kind == jString && modelMem.val.tok < len(p.toks) {
		tok := p.toks[modelMem.val.tok]
		if tok.off+tok.len <= len(b) {
			if str, err := decodeJSONString(b[tok.off : tok.off+tok.len]); err == nil {
				res["model"] = str
			}
		}
	}
	if providerMem := findJSONMember(root, "provider"); providerMem != nil && providerMem.val != nil && providerMem.val.kind == jLBrace {
		if own := findJSONMember(providerMem.val, opencodeProviderKey); own != nil && own.val != nil && own.val.kind == jLBrace {
			if optMem := findJSONMember(own.val, "options"); optMem != nil && optMem.val != nil && optMem.val.kind == jLBrace {
				if urlMem := findJSONMember(optMem.val, "baseURL"); urlMem != nil && urlMem.val != nil && urlMem.val.kind == jString && urlMem.val.tok < len(p.toks) {
					tok := p.toks[urlMem.val.tok]
					if tok.off+tok.len <= len(b) {
						if str, err := decodeJSONString(b[tok.off : tok.off+tok.len]); err == nil {
							res["baseUrl"] = str
						}
					}
				}
			}
		}
	}
	return res, nil
}

func (a opencodeAdapter) Apply(opts Options) (Result, error) {
	plans, err := a.Plan(opts)
	if err != nil {
		return Result{}, err
	}
	return applyPlans(plans, nil)
}

func (a opencodeAdapter) Reset(opts Options) (Result, error) {
	paths, err := a.Paths(opts)
	if err != nil {
		return Result{}, err
	}
	edits := []Edit{
		{KeyPath: []string{"provider", opencodeProviderKey}, Delete: true},
	}
	// `model` and `agent.explorer.model` are only ours while they point at us.
	owned := opencodeOwnedModelKeys(paths.Files[0])
	if owned.model {
		edits = append(edits, Edit{KeyPath: []string{"model"}, Delete: true})
	}
	if owned.explorer {
		edits = append(edits, Edit{KeyPath: []string{"agent", "explorer", "model"}, Delete: true})
		edits = append(edits, Edit{KeyPath: []string{"agent", "explorer", "description"}, Delete: true})
		edits = append(edits, Edit{KeyPath: []string{"agent", "explorer", "mode"}, Delete: true})
	}
	edits = append(edits, Edit{KeyPath: []string{"agent", "explorer"}, Delete: true})
	return applyPlans([]FilePlan{{File: paths.Files[0], Edits: edits}}, nil)
}

const opencodeProviderKey = "9router"

// opencodeApplyEdits writes the provider block, the default model, and the
// explorer subagent.
//
// The provider object is one edit with ValueJSON rather than several key edits:
// its shape is fixed (npm package, baseURL, apiKey, one models entry) and
// writing it whole keeps the file's other providers untouched by construction.
//
// Effort variants follow upstream opencode-settings (7abc190d): a model entry
// gains a `variants` object — one `{reasoningEffort: level}` per selectable
// thinking level, keyed by the level — so opencode's model picker offers the
// same effort set the dashboard shows. Variants the user already tuned win:
// they are read back from the file and only generated when absent.
func opencodeApplyEdits(file TargetFile, opts Options) []Edit {
	// The prefix is stripped here and re-added at each write, so a caller that
	// passes it already (a user copying a model id out of opencode) does not end
	// up with "9router/9router/…".
	model := strings.TrimPrefix(opts.Model, opencodePrefix)
	// The limit is the whole point of writing the model entry: a model unknown
	// to opencode's models.dev snapshot has no context percentage and cannot
	// auto-compact at all (their #10759), while an explicit limit.context
	// gives both. A model id that carries no limits is written bare, exactly
	// as before.
	modelEntry := `{"name": ` + jsonString(model)
	if variants := opencodeEffortVariants(file, opts); variants != "" {
		modelEntry += `, "variants": ` + variants
	}
	if limits, ok := opts.ModelLimits[opts.Model]; ok && limits.ContextWindow > 0 {
		modelEntry += `, "limit": {"context": ` + strconv.Itoa(limits.ContextWindow)
		if limits.MaxOutput > 0 {
			modelEntry += `, "output": ` + strconv.Itoa(limits.MaxOutput)
		}
		modelEntry += `}`
	}
	modelEntry += `}`
	provider := `{"npm": "@ai-sdk/openai-compatible", "options": {"baseURL": ` +
		jsonString(withV1(opts.BaseURL)) + `, "apiKey": ` + jsonString(opts.APIKey) +
		`}, "models": {` + jsonString(model) + `: ` + modelEntry + `}}`
	edits := []Edit{
		{KeyPath: []string{"provider", opencodeProviderKey}, ValueJSON: provider, CreateIfMissing: true},
	}
	if opts.Model != "" {
		edits = append(edits, Edit{
			KeyPath:         []string{"model"},
			Value:           opencodePrefix + model,
			CreateIfMissing: true,
		})
		edits = append(edits,
			Edit{KeyPath: []string{"agent", "explorer", "model"}, Value: opencodePrefix + model, CreateIfMissing: true},
			Edit{KeyPath: []string{"agent", "explorer", "description"}, Value: "Explore the codebase", CreateIfMissing: true},
			Edit{KeyPath: []string{"agent", "explorer", "mode"}, Value: "subagent", CreateIfMissing: true},
		)
	}
	return edits
}

// opencodeEffortVariants builds the `variants` JSON object for the model
// entry: every level GetThinkingLevels offers becomes one variant with the
// same shape opencode's providers expect ({reasoningEffort: level}). Empty
// when the model has no reasoning levels or when the file already carries a
// variants object for it — the user's tuned set is kept verbatim, which is
// what upstream does (existingProvider.models[m]?.variants || buildEffortVariants(m)).
func opencodeEffortVariants(file TargetFile, opts Options) string {
	if opts.Model == "" {
		return ""
	}
	model := strings.TrimPrefix(opts.Model, opencodePrefix)
	if hasExistingVariant(file, model) {
		return ""
	}
	alias, modelID, hasSlash := strings.Cut(model, "/")
	if !hasSlash || alias == "" || modelID == "" {
		return ""
	}
	levels := providers.GetThinkingLevels(alias, modelID)
	if len(levels) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, level := range levels {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(jsonString(level))
		b.WriteString(`: {"reasoningEffort": `)
		b.WriteString(jsonString(level))
		b.WriteByte('}')
	}
	b.WriteByte('}')
	return b.String()
}

// hasExistingVariant reports whether the target config already carries a
// variants object under provider."9router".models.<model>. Any parse or read
// failure returns false, so a file that cannot be inspected gets the generated
// set rather than a silent no-op.
func hasExistingVariant(file TargetFile, model string) bool {
	g, err := resolveGuard(file.Dir, relFromDir(file.Dir, file.Path))
	if err != nil {
		return false
	}
	b, err := readThroughGuard(g)
	if err != nil || len(b) == 0 {
		return false
	}
	_, root, err := parseJSONC(b)
	if err != nil || root == nil {
		return false
	}
	providerMem := findJSONMember(root, "provider")
	if providerMem == nil || providerMem.val == nil || providerMem.val.kind != jLBrace {
		return false
	}
	ownMem := findJSONMember(providerMem.val, opencodeProviderKey)
	if ownMem == nil || ownMem.val == nil || ownMem.val.kind != jLBrace {
		return false
	}
	modelsMem := findJSONMember(ownMem.val, "models")
	if modelsMem == nil || modelsMem.val == nil || modelsMem.val.kind != jLBrace {
		return false
	}
	entryMem := findJSONMember(modelsMem.val, model)
	if entryMem == nil || entryMem.val == nil || entryMem.val.kind != jLBrace {
		return false
	}
	return findJSONMember(entryMem.val, "variants") != nil
}

// jsonString renders a Go string as a JSON string literal.
func jsonString(s string) string { return jsoncQuote(s) }

// opencodeOwnedKeys reports which of the two model keys still point at us.
// It is read-only and returns false on any error, so a file that cannot be
// parsed leaves the keys alone rather than deleting them blind.
type opencodeOwned struct{ model, explorer bool }

func opencodeOwnedModelKeys(file TargetFile) opencodeOwned {
	g, err := resolveGuard(file.Dir, relFromDir(file.Dir, file.Path))
	if err != nil {
		return opencodeOwned{}
	}
	b, err := readThroughGuard(g)
	if err != nil || len(b) == 0 {
		return opencodeOwned{}
	}
	p, root, err := parseJSONC(b)
	if err != nil {
		return opencodeOwned{}
	}
	var out opencodeOwned
	if m := findJSONMember(root, "model"); m != nil && !m.val.isContainer() {
		t := p.toks[m.val.tok]
		if v, ok := jsonStringValue(b[t.off:t.end()]); ok {
			out.model = strings.HasPrefix(v, opencodePrefix)
		}
	}
	if agent := findJSONMember(root, "agent"); agent != nil && agent.val.kind == jLBrace {
		if explorer := findJSONMember(agent.val, "explorer"); explorer != nil && explorer.val.kind == jLBrace {
			if m := findJSONMember(explorer.val, "model"); m != nil && !m.val.isContainer() {
				t := p.toks[m.val.tok]
				if v, ok := jsonStringValue(b[t.off:t.end()]); ok {
					out.explorer = strings.HasPrefix(v, opencodePrefix)
				}
			}
		}
	}
	return out
}

// jsonStringValue unquotes a JSON string literal, reporting false when the
// bytes are not one.
func jsonStringValue(raw []byte) (string, bool) {
	if len(raw) < 2 || raw[0] != '"' {
		return "", false
	}
	v, err := decodeJSONString(raw)
	if err != nil {
		return "", false
	}
	return v, true
}
