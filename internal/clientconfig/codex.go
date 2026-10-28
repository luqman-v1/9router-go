package clientconfig

import "errors"

// OpenAI Codex CLI: ~/.codex/config.toml, TOML.
// docs/specs/client-schema-matrix.md §3.2.
//
// auth.json sits beside config.toml and is deliberately not a target: a custom
// provider ignores it and authenticates through the static Authorization header
// written here. Touching auth.json would change nothing Codex reads and would
// put a credential somewhere the gateway never sees.

// codexProviderKey is the model_providers entry name; `model_provider` selects
// it and must hold the same string.
const codexProviderKey = "9router"

type codexAdapter struct{ baseAdapter }

func newCodexAdapter() Adapter {
	return codexAdapter{baseAdapter{id: "codex", paths: codexPaths}}
}

func (a codexAdapter) Plan(opts Options) ([]FilePlan, error) {
	paths, err := a.Paths(opts)
	if err != nil {
		return nil, err
	}
	return []FilePlan{{File: paths.Files[0], Edits: codexApplyEdits(opts)}}, nil
}

func (a codexAdapter) Apply(opts Options) (Result, error) {
	plans, err := a.Plan(opts)
	if err != nil {
		return Result{}, err
	}
	return applyPlans(plans, nil)
}

func (a codexAdapter) Reset(opts Options) (Result, error) {
	paths, err := a.Paths(opts)
	if err != nil {
		return Result{}, err
	}
	// Order matters in one place only: the provider table is removed after the
	// keys that reference it, so a half-finished reset never leaves
	// `model_provider = "9router"` pointing at a table that is already gone.
	edits := []Edit{
		{KeyPath: []string{"model_providers", codexProviderKey, "http_headers", "Authorization"}, Delete: true},
		{KeyPath: []string{"model_providers", codexProviderKey, "wire_api"}, Delete: true},
		{KeyPath: []string{"model_providers", codexProviderKey, "base_url"}, Delete: true},
		{KeyPath: []string{"model_providers", codexProviderKey, "name"}, Delete: true},
		{KeyPath: []string{"model_providers", codexProviderKey}, Delete: true},
		{KeyPath: []string{"agents", "default_subagent_model"}, Delete: true},
	}
	// `model` and `model_provider` are the user's own keys once the provider
	// table is gone, so they are only removed when they still name us. That
	// decision needs the file, which a plan list cannot express — Reset reads
	// first.
	if owned, err := codexPointsAtUs(paths.Files[0]); err == nil && owned {
		edits = append(edits,
			Edit{KeyPath: []string{"model_provider"}, Delete: true},
			Edit{KeyPath: []string{"model"}, Delete: true},
		)
	}
	return applyPlans([]FilePlan{{File: paths.Files[0], Edits: edits}}, nil)
}

// codexApplyEdits writes the provider table, the selection, and the subagent
// default.
//
// Every key is CreateIfMissing, which for TOML also appends the [table] header
// when the table does not exist yet — so the first edit creates
// [model_providers.9router] and the rest land inside it, in the order listed.
func codexApplyEdits(opts Options) []Edit {
	model := opts.Model
	// A codex model id carries no routing prefix on this path: the gateway is
	// selected by model_provider, not by the model string.
	edits := []Edit{
		{KeyPath: []string{"model_providers", codexProviderKey, "name"}, Value: "9Router", CreateIfMissing: true},
		{KeyPath: []string{"model_providers", codexProviderKey, "base_url"}, Value: withV1(opts.BaseURL), CreateIfMissing: true},
		// The wire protocol is pinned rather than guessed: the gateway serves
		// /v1/responses for codex and /v1/chat/completions for everything else,
		// and a mismatch here is a 404 on every request.
		{KeyPath: []string{"model_providers", codexProviderKey, "wire_api"}, Value: "responses", CreateIfMissing: true},
		// A custom provider never reads auth.json, so the credential has to be
		// a static header. §3.2 records a 401 from getting this wrong even when
		// the file on disk looks right.
		{KeyPath: []string{"model_providers", codexProviderKey, "http_headers", "Authorization"},
			Value: "Bearer " + opts.APIKey, CreateIfMissing: true},
		{KeyPath: []string{"model_provider"}, Value: codexProviderKey, CreateIfMissing: true},
	}
	if model != "" {
		edits = append(edits, Edit{KeyPath: []string{"model"}, Value: model, CreateIfMissing: true})
	}
	// The legacy `agents.subagent` key is replaced by default_subagent_model.
	// Deleting it is part of Apply, not Reset: leaving both means Codex reads
	// whichever it prefers and the user sees a config with two answers.
	return append(edits,
		Edit{KeyPath: []string{"agents", "subagent"}, Delete: true},
	)
}

// codexPointsAtUs reports whether the file's model_provider still names the
// provider this adapter writes.
func codexPointsAtUs(file TargetFile) (bool, error) {
	g, err := resolveGuard(file.Dir, relFromDir(file.Dir, file.Path))
	if err != nil {
		// Nothing to reset on a machine that never ran the client.
		if errors.Is(err, ErrBaseMissing) {
			return false, nil
		}
		return false, err
	}
	b, err := readThroughGuard(g)
	if err != nil || len(b) == 0 {
		return false, err
	}
	doc, err := scanTOML(parseableTOML(b), b)
	if err != nil {
		return false, err
	}
	for _, kv := range doc.kvs {
		if samePath(kv.fullPath, []string{"model_provider"}) {
			return string(b[kv.valStart:kv.valEnd]) == `"`+codexProviderKey+`"`, nil
		}
	}
	return false, nil
}
