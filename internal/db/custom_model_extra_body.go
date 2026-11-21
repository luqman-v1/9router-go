package db

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"

	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
)

// customModelsScope is the kv scope custom model rows live in.
const customModelsScope = "customModels"

// ExtraBodyProvider is the only canonical provider whose custom models carry an
// extraBody: OpenRouter sampling and reasoning knobs a client usually cannot
// set itself (see ExtraBodyAllowedKeys).
const ExtraBodyProvider = "openrouter"

// ExtraBodyMaxBytes caps a custom model's extraBody. The object is merged into
// every request for that model, so it stays a few provider knobs, not a payload.
const ExtraBodyMaxBytes = 8 << 10

var (
	ErrExtraBodyNotObject   = errors.New("extraBody must be a JSON object")
	ErrExtraBodyInvalidJSON = errors.New("extraBody is not valid JSON (duplicate keys or out-of-range number?)")
	ErrExtraBodyTooLarge    = fmt.Errorf("extraBody exceeds %d bytes", ExtraBodyMaxBytes)
	ErrExtraBodyProvider    = errors.New("extraBody is only supported for openrouter custom models")
)

// ExtraBodyProviderAllowed reports whether a custom model stored under alias
// may carry an extraBody, resolving the alias to its canonical provider id.
func ExtraBodyProviderAllowed(alias string) bool {
	return canonicalProvider(alias) == ExtraBodyProvider
}

func canonicalProvider(alias string) string {
	if canonical, ok := providers.ProviderAliasMap[alias]; ok {
		return canonical
	}
	return alias
}

// ParseExtraBody validates raw as a custom model's extraBody and returns its
// top-level members with their original bytes.
func ParseExtraBody(raw []byte) (map[string]jsontext.Value, error) {
	if len(raw) > ExtraBodyMaxBytes {
		return nil, ErrExtraBodyTooLarge
	}
	if !jsontext.Value(raw).IsValid() {
		return nil, ErrExtraBodyInvalidJSON
	}
	var members map[string]jsontext.Value
	if err := json.Unmarshal(raw, &members); err != nil || members == nil {
		return nil, ErrExtraBodyNotObject
	}
	if err := validateExtraBodyMembers(members); err != nil {
		return nil, err
	}
	return members, nil
}

// extraBodyIndex maps canonical provider id -> model id -> extraBody members.
type extraBodyIndex map[string]map[string]map[string]jsontext.Value

// extraBodySnapshot is an index plus the invalidation generation it was read
// under; a snapshot older than the current generation is never served.
type extraBodySnapshot struct {
	gen uint64
	idx extraBodyIndex
}

// afterExtraBodyRead is a test seam run between the rows read and the snapshot
// store, where a concurrent invalidation would otherwise be lost.
var afterExtraBodyRead func()

// CustomModelExtraBody returns the extraBody declared for provider/model, or
// nil. It reads an in-memory snapshot, so the request path never queries.
func (r *Repo) CustomModelExtraBody(provider, model string) map[string]jsontext.Value {
	if r == nil {
		return nil
	}
	return r.loadExtraBodyIndex()[provider][model]
}

// InvalidateCustomModelExtraBody retires the snapshot. Every customModels
// write that bypasses SetKV/SetKVMany/DeleteKV (a database import) must call it.
func (r *Repo) InvalidateCustomModelExtraBody() {
	r.extraBodyGen.Add(1)
}

func (r *Repo) loadExtraBodyIndex() extraBodyIndex {
	if s := r.extraBody.Load(); s != nil && s.gen == r.extraBodyGen.Load() {
		return s.idx
	}
	r.extraBodyBuild.Lock()
	defer r.extraBodyBuild.Unlock()
	gen := r.extraBodyGen.Load()
	if s := r.extraBody.Load(); s != nil && s.gen == gen {
		return s.idx
	}
	// gen is captured before the read: an invalidation landing during the
	// build bumps the generation, so this snapshot is rebuilt on next use.
	idx, err := r.buildExtraBodyIndex()
	if err != nil {
		// Not cached: the next request retries instead of serving an empty
		// index until some unrelated write happens to invalidate it.
		log.Warn("extra-body", "custom model read failed", "error", err)
		return idx
	}
	r.extraBody.Store(&extraBodySnapshot{gen: gen, idx: idx})
	return idx
}

// buildExtraBodyIndex reads every row once. A row that fails validation (an
// imported backup can carry anything) is skipped rather than half-applied.
func (r *Repo) buildExtraBodyIndex() (extraBodyIndex, error) {
	idx := extraBodyIndex{}
	customs, err := r.GetCustomModels()
	if afterExtraBodyRead != nil {
		afterExtraBodyRead()
	}
	if err != nil {
		return idx, err
	}
	for _, cm := range customs {
		if len(cm.ExtraBody) == 0 || string(cm.ExtraBody) == "null" {
			continue
		}
		var members map[string]jsontext.Value
		if err := json.Unmarshal(cm.ExtraBody, &members); err != nil || members == nil {
			log.Warn("extra-body", "skip custom model extraBody", "provider", cm.ProviderAlias, "model", cm.ID, "error", ErrExtraBodyNotObject)
			continue
		}
		// A legacy or imported row may carry keys the allowlist now refuses:
		// keep only the valid members rather than forwarding or half-applying.
		if kept := FilterExtraBody(members); len(kept) != len(members) {
			log.Warn("extra-body", "dropped disallowed extraBody keys", "provider", cm.ProviderAlias, "model", cm.ID, "error", validateExtraBodyMembers(members))
			members = kept
		}
		if len(members) == 0 {
			continue
		}
		provider := canonicalProvider(cm.ProviderAlias)
		if idx[provider] == nil {
			idx[provider] = map[string]map[string]jsontext.Value{}
		}
		idx[provider][cm.ID] = members
	}
	return idx, nil
}
