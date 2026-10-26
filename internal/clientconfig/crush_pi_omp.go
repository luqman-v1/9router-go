package clientconfig

import (
	"strconv"
	"strings"
)

// Three clients that each keep a providers["9router"] table and disagree on
// what an empty one means. docs/specs/client-schema-matrix.md §3.5, §3.8, §3.9.
//
// They share the write shape and nothing else, and the shared part is two
// lines, so this file holds all three rather than a helper module whose only
// content would be the differences.

// providerTableKey is the providers entry name crush, pi and omp all use.
const providerTableKey = "9router"

const crushContextWindow = 128000

type crushAdapter struct{ baseAdapter }

func newCrushAdapter() Adapter {
	return crushAdapter{baseAdapter{id: "crush", paths: crushPaths}}
}

func (a crushAdapter) Plan(opts Options) ([]FilePlan, error) {
	paths, err := a.Paths(opts)
	if err != nil {
		return nil, err
	}
	models := `[{"id": ` + jsonString(opts.Model) + `, "name": ` + jsonString(opts.Model) +
		`, "context_window": ` + strconv.Itoa(crushContextWindow) + `}]`
	obj := `{"type": "openai-compat", "base_url": ` + jsonString(withV1(opts.BaseURL)) +
		`, "api_key": ` + jsonString(opts.APIKey) + `, "models": ` + models + `}`
	return []FilePlan{{File: paths.Files[0], Edits: []Edit{{
		KeyPath: []string{"providers", providerTableKey}, ValueJSON: obj, CreateIfMissing: true,
	}}}}, nil
}

func (a crushAdapter) Apply(opts Options) (Result, error) {
	plans, err := a.Plan(opts)
	if err != nil {
		return Result{}, err
	}
	return applyPlans(plans, nil)
}

func (a crushAdapter) Reset(opts Options) (Result, error) {
	paths, err := a.Paths(opts)
	if err != nil {
		return Result{}, err
	}
	// crush empties `providers` but keeps the file: §7.1 records that reading
	// it as one of the delete-file clients is a misreading of §6.
	return applyPlans([]FilePlan{{File: paths.Files[0], Edits: []Edit{
		{KeyPath: []string{"providers", providerTableKey}, Delete: true},
	}}}, nil)
}

// pi merges into providers["9router"] rather than replacing it (§3.8): the
// user may have other fields on the same entry, and the matrix calls that out
// as the thing this adapter must not break.
const piContextWindow = 128000

const piMaxTokens = 16384

type piAdapter struct{ baseAdapter }

func newPiAdapter() Adapter {
	return piAdapter{baseAdapter{id: "pi", paths: piPaths}}
}

func (a piAdapter) Plan(opts Options) ([]FilePlan, error) {
	paths, err := a.Paths(opts)
	if err != nil {
		return nil, err
	}
	base := []string{"providers", providerTableKey}
	models := `[{"id": ` + jsonString(opts.Model) + `, "name": ` + jsonString(opts.Model) +
		`, "contextWindow": ` + strconv.Itoa(piContextWindow) + `, "maxTokens": ` + strconv.Itoa(piMaxTokens) + `}]`
	return []FilePlan{{File: paths.Files[0], Edits: []Edit{
		{KeyPath: append(base, "baseUrl"), Value: withV1(opts.BaseURL), CreateIfMissing: true},
		{KeyPath: append(base, "apiKey"), Value: opts.APIKey, CreateIfMissing: true},
		{KeyPath: append(base, "api"), Value: "openai-completions", CreateIfMissing: true},
		{KeyPath: append(base, "models"), ValueJSON: models, CreateIfMissing: true},
	}}}, nil
}

func (a piAdapter) Apply(opts Options) (Result, error) {
	plans, err := a.Plan(opts)
	if err != nil {
		return Result{}, err
	}
	return applyPlans(plans, nil)
}

func (a piAdapter) Reset(opts Options) (Result, error) {
	paths, err := a.Paths(opts)
	if err != nil {
		return Result{}, err
	}
	// Same as crush: providers drops, the file stays.
	return applyPlans([]FilePlan{{File: paths.Files[0], Edits: []Edit{
		{KeyPath: []string{"providers", providerTableKey}, Delete: true},
	}}}, nil)
}

// omp is the one of the three that deletes its file when nothing is left.
type ompAdapter struct{ baseAdapter }

func newOmpAdapter() Adapter {
	return ompAdapter{baseAdapter{id: "omp", paths: ompPaths}}
}

func (a ompAdapter) Plan(opts Options) ([]FilePlan, error) {
	paths, err := a.Paths(opts)
	if err != nil {
		return nil, err
	}
	// Flow mapping, because a block one spans lines and the YAML patcher
	// refuses to replace a multi-line collection (§3.9 gotcha (d)).
	obj := `{baseUrl: ` + yamlFlowScalar(withV1(opts.BaseURL)) +
		`, apiKey: ` + yamlFlowScalar(opts.APIKey) +
		`, api: openai-completions, authHeader: true, disableStrictTools: true, ` +
		`discovery: {type: proxy}}`
	return []FilePlan{{File: paths.Files[0], Edits: []Edit{{
		KeyPath: []string{"providers", providerTableKey}, ValueJSON: obj, CreateIfMissing: true,
	}}}}, nil
}

func (a ompAdapter) Apply(opts Options) (Result, error) {
	plans, err := a.Plan(opts)
	if err != nil {
		return Result{}, err
	}
	return applyPlans(plans, nil)
}

func (a ompAdapter) Reset(opts Options) (Result, error) {
	paths, err := a.Paths(opts)
	if err != nil {
		return Result{}, err
	}
	plans := []FilePlan{{File: paths.Files[0], Edits: []Edit{
		{KeyPath: []string{"providers", providerTableKey}, Delete: true},
	}}}
	// After removing our entry, a file left with nothing but a bare
	// `providers:` line is not a config — §3.9 says delete it. The judgement
	// needs the result, so it is made after the write.
	res, err := applyPlans(plans, nil)
	if err != nil {
		return res, err
	}
	if ompOnlyBareProviders(paths.Files[0]) {
		rm, err := removeFileIfExists(paths.Files[0])
		if err != nil {
			return res, err
		}
		res.Changed = res.Changed || rm
	}
	return res, nil
}

// ompOnlyBareProviders reports whether nothing but an empty `providers:` is
// left. An unreadable or missing file is not a case to delete.
func ompOnlyBareProviders(file TargetFile) bool {
	g, err := resolveGuard(file.Dir, relFromDir(file.Dir, file.Path))
	if err != nil {
		return false
	}
	b, err := readThroughGuard(g)
	if err != nil || len(b) == 0 {
		return false
	}
	trimmed := strings.TrimSpace(string(b))
	return trimmed == "providers:" || trimmed == "providers: {}" || trimmed == "{}"
}

// removeFileIfExists deletes a file an adapter has decided should not exist.
func removeFileIfExists(file TargetFile) (bool, error) {
	g, err := resolveGuard(file.Dir, relFromDir(file.Dir, file.Path))
	if err != nil {
		return false, err
	}
	changed, _, err := removeThroughGuard(g, g.existed)
	return changed, err
}

// yamlFlowScalar quotes a scalar for a flow mapping. Double quotes are what the
// matrix §4.1 D4 records for re-emitted YAML values.
func yamlFlowScalar(s string) string { return yamlQuote(s) }
