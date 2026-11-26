package providers

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Per-model capability overrides ("model editor").
//
// An operator can pin any of the eleven capability fields on one provider+model
// pair. The override is SPARSE: a field the operator left on Auto is absent, so
// detection still answers it. This mirrors upstream
// src/lib/userModelCaps.js + MODEL_CAPS_OVERRIDE_META (9router-pro v0.5.109).
//
// The package stays DB-free the same way the synced catalog does: the server
// pushes a loader in with InstallModelCapsOverrideLoader, and this file holds
// the 5s TTL cache. Fail-open: a missing loader, a failed read or a corrupt row
// means "no override", which is exactly detection.

// ModelCapsOverrideMaxTokens is the largest number any field may carry. Above
// it the value is a typo, not a model: publishing it would let a client budget
// history for a window that cannot exist.
const ModelCapsOverrideMaxTokens = 10_000_000

// ErrCapsOverrideNotObject is returned for a payload that is not a JSON object.
// Upstream answers null in that case, which the API surfaces as a 400 rather
// than a silent clear that would look like a successful save.
var ErrCapsOverrideNotObject = errors.New("capsOverride must be a JSON object")

// ModelCapsOverride is the sparse override. Every field is a pointer so
// "absent" (Auto) is distinguishable from "explicitly false".
type ModelCapsOverride struct {
	Vision      *bool `json:"vision,omitempty"`
	PDF         *bool `json:"pdf,omitempty"`
	AudioInput  *bool `json:"audioInput,omitempty"`
	VideoInput  *bool `json:"videoInput,omitempty"`
	ImageOutput *bool `json:"imageOutput,omitempty"`
	AudioOutput *bool `json:"audioOutput,omitempty"`
	Search      *bool `json:"search,omitempty"`
	Tools       *bool `json:"tools,omitempty"`
	Reasoning   *bool `json:"reasoning,omitempty"`
	// Limits are positive integers; 0 is never stored because "no opinion" and
	// "zero tokens" would be indistinguishable to a client.
	ContextWindow *int `json:"contextWindow,omitempty"`
	MaxOutput     *int `json:"maxOutput,omitempty"`
}

// Empty reports whether the override pins nothing, i.e. the row should be
// deleted instead of stored.
func (o ModelCapsOverride) Empty() bool {
	return o.Vision == nil && o.PDF == nil && o.AudioInput == nil && o.VideoInput == nil &&
		o.ImageOutput == nil && o.AudioOutput == nil && o.Search == nil && o.Tools == nil &&
		o.Reasoning == nil && o.ContextWindow == nil && o.MaxOutput == nil
}

// decodeJSONObject decodes one JSON object. An array, a scalar or malformed
// bytes is an error, which is what makes "capsOverride must be an object"
// reportable instead of silently clearing a stored override.
func decodeJSONObject(text string) (map[string]any, error) {
	var members map[string]any
	if err := json.Unmarshal([]byte(text), &members); err != nil {
		return nil, err
	}
	if members == nil {
		return nil, ErrCapsOverrideNotObject
	}
	return members, nil
}

// NormalizeModelCapsOverride enforces upstream's sanitizeModelCapsOverride
// rules exactly:
//
//   - unknown keys are dropped,
//   - booleans must be real booleans (a string "true" is dropped),
//   - numbers must be integers in 1..ModelCapsOverrideMaxTokens; a numeric
//     string from a form input is accepted, anything else is dropped,
//   - null means Auto and the key is dropped.
//
// Dropping rather than erroring is upstream's policy, and it matters: a legacy
// row or a hand-edited import can carry junk, and refusing the whole payload
// would hide the fields that are usable.
func NormalizeModelCapsOverride(raw map[string]any) (ModelCapsOverride, error) {
	if raw == nil {
		return ModelCapsOverride{}, ErrCapsOverrideNotObject
	}
	var out ModelCapsOverride
	setBool := func(dst **bool, key string) {
		if v, ok := raw[key].(bool); ok {
			b := v
			*dst = &b
		}
	}
	setBool(&out.Vision, "vision")
	setBool(&out.PDF, "pdf")
	setBool(&out.AudioInput, "audioInput")
	setBool(&out.VideoInput, "videoInput")
	setBool(&out.ImageOutput, "imageOutput")
	setBool(&out.AudioOutput, "audioOutput")
	setBool(&out.Search, "search")
	setBool(&out.Tools, "tools")
	setBool(&out.Reasoning, "reasoning")
	setNum := func(dst **int, key string) {
		if n, ok := capsOverrideNumber(raw[key]); ok {
			*dst = &n
		}
	}
	setNum(&out.ContextWindow, "contextWindow")
	setNum(&out.MaxOutput, "maxOutput")
	return out, nil
}

// capsOverrideNumber accepts a JSON number or a numeric string and returns it
// only when it is a positive integer within the ceiling.
func capsOverrideNumber(v any) (int, bool) {
	var f float64
	switch value := v.(type) {
	case float64:
		f = value
	case float32:
		f = float64(value)
	case int:
		f = float64(value)
	case int64:
		f = float64(value)
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) {
		return 0, false
	}
	n := int(f)
	if n <= 0 || n > ModelCapsOverrideMaxTokens {
		return 0, false
	}
	return n, true
}

// CapsOverrideModelKey normalizes a model id to the key an override is stored
// under: the last path segment, lowercased, truncated at the first ":".
// "vendor/Claude-X:free" and "claude-x" therefore resolve to the same row.
func CapsOverrideModelKey(model string) string {
	base := model
	if idx := strings.LastIndex(base, "/"); idx != -1 {
		base = base[idx+1:]
	}
	base = strings.ToLower(base)
	if idx := strings.Index(base, ":"); idx != -1 {
		base = base[:idx]
	}
	return base
}

// capsOverrideProviderKeys returns every spelling that may own an override for
// provider. The row is written under the canonical id, but the capability path
// is called with whatever the caller has: an alias (`oc`), a node id, or the
// canonical id itself.
func capsOverrideProviderKeys(provider string) []string {
	keys := make([]string, 0, 2)
	add := func(id string) {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" {
			return
		}
		for _, k := range keys {
			if k == id {
				return
			}
		}
		keys = append(keys, id)
	}
	add(provider)
	add(ResolveAlias(provider))
	return keys
}

type modelCapsOverrideSnapshot struct {
	at  time.Time
	idx map[string]*ModelCapsOverride
}

const modelCapsOverrideTTL = 5 * time.Second

var (
	modelCapsOverrideSnap atomic.Pointer[modelCapsOverrideSnapshot]
	modelCapsOverrideMu   sync.Mutex
	modelCapsOverrideLoad func() (map[string]string, error)
	modelCapsOverrideBusy atomic.Bool
)

// InstallModelCapsOverrideLoader wires the reader and loads once, mirroring how
// the synced catalog source is installed at startup. Invalidating first means a
// reinstall picks up a fresh read instead of the previous snapshot.
func InstallModelCapsOverrideLoader(load func() (map[string]string, error)) {
	modelCapsOverrideMu.Lock()
	modelCapsOverrideLoad = load
	modelCapsOverrideMu.Unlock()
	modelCapsOverrideSnap.Store(nil)
	ReloadModelCapsOverrides()
	// The resolved block is cached keyed by provider+model, so a newly loaded
	// override would otherwise stay invisible until an unrelated invalidation.
	InvalidateCapabilitiesCache()
}

// ReloadModelCapsOverrides rebuilds the index synchronously. Exported for the
// boot path, where one read is cheap and the first request should not miss.
func ReloadModelCapsOverrides() {
	modelCapsOverrideMu.Lock()
	load := modelCapsOverrideLoad
	modelCapsOverrideMu.Unlock()
	if load == nil {
		return
	}
	rows, err := load()
	if err != nil {
		// Fail-open and not cached: the next lookup retries instead of serving
		// an empty index for the rest of the TTL window.
		return
	}
	idx := make(map[string]*ModelCapsOverride, len(rows))
	for key, raw := range rows {
		provider, model, ok := splitCapsOverrideRowKey(key)
		if !ok {
			continue
		}
		override, ok := parseStoredCapsOverride(raw)
		if !ok {
			continue
		}
		idx[capsOverrideIndexKey(provider, model)] = override
	}
	modelCapsOverrideSnap.Store(&modelCapsOverrideSnapshot{at: time.Now(), idx: idx})
}

// InvalidateModelCapsOverrideCache makes the next lookup reload. Called after
// every write so an edit takes effect on the next request.
func InvalidateModelCapsOverrideCache() {
	modelCapsOverrideSnap.Store(nil)
	InvalidateCapabilitiesCache()
	go ReloadModelCapsOverrides()
}

func capsOverrideIndexKey(provider, model string) string {
	return strings.ToLower(provider) + ":" + CapsOverrideModelKey(model)
}

// splitCapsOverrideRowKey reads the "provider|model" key the repo hands back.
func splitCapsOverrideRowKey(key string) (string, string, bool) {
	provider, model, ok := strings.Cut(key, "|")
	if !ok || provider == "" || model == "" {
		return "", "", false
	}
	return provider, model, true
}

func parseStoredCapsOverride(raw string) (*ModelCapsOverride, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return nil, false
	}
	members, err := decodeJSONObject(trimmed)
	if err != nil {
		return nil, false
	}
	override, err := NormalizeModelCapsOverride(members)
	if err != nil || override.Empty() {
		return nil, false
	}
	return &override, true
}

// LookupModelCapsOverride returns the pinned fields for provider+model, or nil.
// The hot path is a map read: a stale snapshot triggers a background reload and
// still serves the previous index, so no request ever hits the DB here.
func LookupModelCapsOverride(provider, model string) *ModelCapsOverride {
	if model == "" {
		return nil
	}
	snap := modelCapsOverrideSnap.Load()
	if snap == nil || time.Since(snap.at) > modelCapsOverrideTTL {
		if modelCapsOverrideBusy.CompareAndSwap(false, true) {
			go func() {
				defer modelCapsOverrideBusy.Store(false)
				ReloadModelCapsOverrides()
			}()
		}
	}
	if snap == nil {
		return nil
	}
	modelKey := CapsOverrideModelKey(model)
	if modelKey == "" {
		return nil
	}
	candidates := capsOverrideProviderKeys(provider)
	// A bare model arg may carry "provider/model" (combo seats call with no
	// provider); its first segment is a provider hint, so try it last.
	if len(candidates) == 0 {
		if idx := strings.Index(model, "/"); idx > 0 {
			candidates = capsOverrideProviderKeys(model[:idx])
		}
	}
	for _, p := range candidates {
		if hit, ok := snap.idx[p+":"+modelKey]; ok {
			return hit
		}
	}
	return nil
}

// applyToCapabilities overlays the pinned fields on a resolved block. Only the
// keys the operator set win, so an override that fixes vision for a
// pattern-missed model leaves the reasoning shape detection got right alone.
func (o *ModelCapsOverride) applyToCapabilities(c *Capabilities) {
	if o == nil || c == nil {
		return
	}
	set := func(dst *bool, src *bool) {
		if src != nil {
			*dst = *src
		}
	}
	set(&c.Vision, o.Vision)
	set(&c.PDF, o.PDF)
	set(&c.AudioInput, o.AudioInput)
	set(&c.VideoInput, o.VideoInput)
	set(&c.ImageOutput, o.ImageOutput)
	set(&c.AudioOutput, o.AudioOutput)
	set(&c.Search, o.Search)
	set(&c.Tools, o.Tools)
	set(&c.Reasoning, o.Reasoning)
	if o.ContextWindow != nil {
		c.ContextWindow = *o.ContextWindow
	}
	if o.MaxOutput != nil {
		c.MaxOutput = *o.MaxOutput
	}
}

// applyToDetail is the second application point. GetCapabilitiesDetailForModel
// fills the token limits from a longer chain (custom row, provider declaration,
// catalog, substring table) that runs after the capabilities block, so the
// operator's number has to be stamped on again to stay the winning one.
func (o *ModelCapsOverride) applyToDetail(d *CapabilitiesDetail) {
	if o == nil || d == nil {
		return
	}
	set := func(dst *bool, src *bool) {
		if src != nil {
			*dst = *src
		}
	}
	set(&d.Vision, o.Vision)
	set(&d.PDF, o.PDF)
	set(&d.AudioInput, o.AudioInput)
	set(&d.VideoInput, o.VideoInput)
	set(&d.ImageOutput, o.ImageOutput)
	set(&d.AudioOutput, o.AudioOutput)
	set(&d.Search, o.Search)
	set(&d.Tools, o.Tools)
	set(&d.Reasoning, o.Reasoning)
	if o.ContextWindow != nil {
		d.ContextWindow = *o.ContextWindow
	}
	if o.MaxOutput != nil {
		d.MaxOutput = *o.MaxOutput
	}
}

// CapsOverrideField is one editable field, so the dashboard renders the labels
// and the number ceiling from the server instead of keeping a second copy that
// can drift from the validator.
type CapsOverrideField struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Desc  string `json:"desc"`
	Type  string `json:"type"`
}

// CapsOverrideFields is upstream's MODEL_CAPS_OVERRIDE_META in wire form.
func CapsOverrideFields() []CapsOverrideField {
	return []CapsOverrideField{
		{"vision", "Vision", "Reads image input", "boolean"},
		{"pdf", "PDF", "Reads PDF / documents", "boolean"},
		{"audioInput", "Audio input", "Reads audio input", "boolean"},
		{"videoInput", "Video input", "Reads video input", "boolean"},
		{"imageOutput", "Image output", "Generates images", "boolean"},
		{"audioOutput", "Audio output", "Generates audio", "boolean"},
		{"search", "Web search", "Built-in web search / grounding", "boolean"},
		{"tools", "Tool calling", "Function / tool calling", "boolean"},
		{"reasoning", "Reasoning", "Thinking / reasoning", "boolean"},
		{"contextWindow", "Context window", "Max input tokens", "number"},
		{"maxOutput", "Max output", "Max output tokens", "number"},
	}
}

// DecodeCapsOverridePayload turns a raw JSON payload into the sparse override.
// Kept here so the HTTP layer and any future importer normalize identically.
func DecodeCapsOverridePayload(raw []byte) (ModelCapsOverride, error) {
	members, err := decodeJSONObject(string(raw))
	if err != nil {
		return ModelCapsOverride{}, fmt.Errorf("%w: %w", ErrCapsOverrideNotObject, err)
	}
	return NormalizeModelCapsOverride(members)
}
