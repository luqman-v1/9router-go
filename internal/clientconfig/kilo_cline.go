package clientconfig

// Kilo Code and Cline CLI. docs/specs/client-schema-matrix.md §3.6, §3.7.
//
// Cline is the one client that takes the bare origin rather than the /v1 form,
// and it is the only one of the nine with two files whose reset has to happen
// together. Both facts are tested rather than commented, because both are the
// kind that a reader would assume the other way round.

// kiloProviderKey is the auth entry Kilo Code reads.
const kiloProviderKey = "openai-compatible"

type kiloAdapter struct{ baseAdapter }

func newKiloAdapter() Adapter {
	return kiloAdapter{baseAdapter{id: "kilo", paths: kiloPaths}}
}

func (a kiloAdapter) Plan(opts Options) ([]FilePlan, error) {
	paths, err := a.Paths(opts)
	if err != nil {
		return nil, err
	}
	obj := `{"type": "api-key", "apiKey": ` + jsonString(opts.APIKey) +
		`, "baseUrl": ` + jsonString(withV1(opts.BaseURL)) +
		`, "model": ` + jsonString(opts.Model) + `}`
	return []FilePlan{{File: paths.Files[0], Edits: []Edit{{
		KeyPath: []string{"auth", kiloProviderKey}, ValueJSON: obj, CreateIfMissing: true,
	}}}}, nil
}

func (a kiloAdapter) Apply(opts Options) (Result, error) {
	plans, err := a.Plan(opts)
	if err != nil {
		return Result{}, err
	}
	return applyPlans(plans, nil)
}

func (a kiloAdapter) Reset(opts Options) (Result, error) {
	paths, err := a.Paths(opts)
	if err != nil {
		return Result{}, err
	}
	// auth["9router"] is a leftover from an earlier schema this adapter never
	// wrote; §3.6 records it as part of what reset removes.
	return applyPlans([]FilePlan{{File: paths.Files[0], Edits: []Edit{
		{KeyPath: []string{"auth", kiloProviderKey}, Delete: true},
		{KeyPath: []string{"auth", "9router"}, Delete: true},
	}}}, nil)
}

// clineProviderKey is what the provider fields are set to and back from.
const clineProviderKey = "openai"

// clineNativeProvider is what Reset restores; Cline's own provider id.
const clineNativeProvider = "cline"

type clineAdapter struct{ baseAdapter }

func newClineAdapter() Adapter {
	return clineAdapter{baseAdapter{id: "cline", paths: clinePaths}}
}

func (a clineAdapter) Plan(opts Options) ([]FilePlan, error) {
	paths, err := a.Paths(opts)
	if err != nil {
		return nil, err
	}
	state, secrets := paths.Files[0], paths.Files[1]
	// The bare origin here, not withV1: Cline is the one client that wants it,
	// and it is the difference §7.1 lists first because getting it wrong gives a
	// client that authenticates and then 404s every request.
	base := originOnly(opts.BaseURL)
	return []FilePlan{
		{File: state, Edits: []Edit{
			{KeyPath: []string{"actModeApiProvider"}, Value: clineProviderKey, CreateIfMissing: true},
			{KeyPath: []string{"planModeApiProvider"}, Value: clineProviderKey, CreateIfMissing: true},
			{KeyPath: []string{"openAiBaseUrl"}, Value: base, CreateIfMissing: true},
			{KeyPath: []string{"openAiModelId"}, Value: opts.Model, CreateIfMissing: true},
			{KeyPath: []string{"planModeOpenAiModelId"}, Value: opts.Model, CreateIfMissing: true},
		}},
		{File: secrets, Edits: []Edit{
			{KeyPath: []string{"openAiApiKey"}, Value: opts.APIKey, CreateIfMissing: true},
		}},
	}, nil
}

func (a clineAdapter) Apply(opts Options) (Result, error) {
	plans, err := a.Plan(opts)
	if err != nil {
		return Result{}, err
	}
	// The order of plans is the order of writes: state first, then the secret.
	// If the second fails the first is already changed, and applyPlans says so
	// with both backup paths rather than pretending the write was atomic across
	// two files.
	return applyPlans(plans, nil)
}

func (a clineAdapter) Reset(opts Options) (Result, error) {
	paths, err := a.Paths(opts)
	if err != nil {
		return Result{}, err
	}
	state, secrets := paths.Files[0], paths.Files[1]
	// §3.7: only undo when the provider is still ours. A user who switched
	// Cline back to its own provider keeps that decision, and their URL and
	// model with it.
	edits := []Edit{}
	if clineIsOurs(state) {
		edits = append(edits,
			Edit{KeyPath: []string{"openAiBaseUrl"}, Delete: true},
			Edit{KeyPath: []string{"openAiModelId"}, Delete: true},
			Edit{KeyPath: []string{"planModeOpenAiModelId"}, Delete: true},
			Edit{KeyPath: []string{"actModeApiProvider"}, Value: clineNativeProvider},
			Edit{KeyPath: []string{"planModeApiProvider"}, Value: clineNativeProvider},
		)
	}
	return applyPlans([]FilePlan{
		{File: state, Edits: edits},
		{File: secrets, Edits: []Edit{{KeyPath: []string{"openAiApiKey"}, Delete: true}}},
	}, nil)
}

// clineIsOurs reports whether globalState.json still selects the openai
// provider this adapter writes.
func clineIsOurs(file TargetFile) bool {
	g, err := resolveGuard(file.Dir, relFromDir(file.Dir, file.Path))
	if err != nil {
		return false
	}
	b, err := readThroughGuard(g)
	if err != nil || len(b) == 0 {
		return false
	}
	p, root, err := parseJSONC(b)
	if err != nil {
		return false
	}
	m := findJSONMember(root, "actModeApiProvider")
	if m == nil || m.val.isContainer() {
		return false
	}
	t := p.toks[m.val.tok]
	v, ok := jsonStringValue(b[t.off:t.end()])
	return ok && v == clineProviderKey
}
