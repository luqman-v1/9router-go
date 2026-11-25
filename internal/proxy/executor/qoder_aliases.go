package executor

import (
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/samber/lo"
)

// Qoder friendly-name resolution — local compatibility fix.
//
// Clients send the marketing name of a model ("qwen3.8-flash", "glm-5.2",
// "kimi-k2.7") while the gateway's model/list publishes opaque keys
// (qfmodel, gm51model, kmodel). Two layers bridge them, in this order:
//
//  1. the live catalog: every confirmed key is also reachable through its
//     sanitized display_name, so the account's own model list decides how a
//     friendly name resolves once it has landed;
//  2. a deterministic static alias table for the window before the first
//     fetch (or for a name upstream never published).
//
// An id neither layer knows passes through unchanged, so a brand-new raw key
// still reaches the catalog lookup instead of failing locally.

// qoderModelAliases maps friendly model names to the raw keys Qoder serves.
// Keys are lowercase; lookup folds case. The targets are keys observed in
// /algo/api/v2/model/list answers — this table only short-circuits the name,
// it never invents a model_config (the live catalog or the static blocks in
// qoder_catalog.go still have to supply one).
var qoderModelAliases = map[string]string{
	"qwen3.8-flash":     "qfmodel",
	"qwen3.8-max":       "qmodel_38max",
	"qwen3.7-max":       "qmodel_latest",
	"qwen3.7-plus":      "qmodel",
	"deepseek-v4-pro":   "dmodel",
	"deepseek-v4-flash": "dfmodel",
	"glm-5.2":           "gm51model",
	"kimi-k2.7":         "kmodel",
	"minimax-m3":        "mmodel",
}

// qoderMaxAdvertisedIDLen caps a display-name-derived id. Upstream names are
// short; the cap keeps a runaway display name out of a route path.
const qoderMaxAdvertisedIDLen = 64

// QoderAdvertisedID transcribes an upstream display_name into an id the
// gateway may advertise alongside the raw key, or returns "" when the name
// cannot be carried: a character outside the model-id set, an over-long name,
// or one that says no more than the key already says ends the search instead
// of a transliteration. Whitespace and dash runs collapse into one separator,
// so " Qwen -- 3.8 " still yields "qwen-3.8".
func QoderAdvertisedID(displayName, key string) string {
	parts := strings.FieldsFunc(strings.ToLower(displayName), func(r rune) bool {
		return unicode.IsSpace(r) || r == '-'
	})
	id := strings.Join(parts, "-")
	if id == "" || len(id) > qoderMaxAdvertisedIDLen {
		return ""
	}
	hasAlnum := false
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			hasAlnum = true
		case r == '.' || r == '_' || r == '-':
		default:
			return ""
		}
	}
	if !hasAlnum || strings.EqualFold(id, key) {
		return ""
	}
	return id
}

// qoderCatalogNames builds the advertised-id → raw-key map of one snapshot.
// Every key maps to itself; a display name is accepted only when no key and
// no earlier name already holds it. Names are weighed in raw-key order, so
// two models sharing one name cannot reshuffle the mapping between fetches.
//
// Unlike the dashboard advertisement (enabled entries only), this map covers
// every cached key: chat resolves model_config for account-hidden models too,
// so their friendly names must resolve as well.
func qoderCatalogNames(rawConfigs map[string]map[string]any) map[string]string {
	keys := lo.Keys(rawConfigs)
	sort.Strings(keys)
	names := make(map[string]string, len(keys)*2)
	for _, key := range keys {
		names[key] = key
	}
	for _, key := range keys {
		display, _ := rawConfigs[key]["display_name"].(string)
		id := QoderAdvertisedID(display, key)
		if id == "" {
			continue
		}
		if _, held := names[id]; !held {
			names[id] = key
		}
	}
	return names
}

// qoderCatalogKeyFromNames looks an advertised id up in a snapshot's name
// map: exact first, then lowercase (ids are advertised lowercase while a
// client may send any case).
func qoderCatalogKeyFromNames(names map[string]string, id string) (string, bool) {
	if key, ok := names[id]; ok {
		return key, true
	}
	key, ok := names[strings.ToLower(id)]
	return key, ok
}

// qoderResolveModelKey reduces a wire model id to the raw key Qoder knows:
// qualifier stripped, then the live catalog's names, then the static alias
// table, then pass-through. The catalog wins over the static table so an
// account whose display names differ from the marketing names still resolves
// the way its own model list says.
func qoderResolveModelKey(creds QoderCosyCreds, region QoderRegion, model string) string {
	key := qoderModelKey(model)
	if key == "" {
		return ""
	}
	if canonical, ok := qoderCatalogKeyForID(creds, region, key); ok {
		return canonical
	}
	if alias, ok := qoderModelAliases[strings.ToLower(key)]; ok {
		return alias
	}
	return key
}

// qoderCatalogKeyForID is the cached-catalog half of qoderResolveModelKey.
// It never touches the network: an unfetched or expired snapshot answers
// "unknown" and the caller falls through to the static table.
func qoderCatalogKeyForID(creds QoderCosyCreds, region QoderRegion, id string) (string, bool) {
	qoderCatalogMu.RLock()
	defer qoderCatalogMu.RUnlock()
	entry, ok := qoderCatalogs[qoderCatalogKey(creds, region)]
	if !ok || time.Now().After(entry.expiresAt) {
		return "", false
	}
	return qoderCatalogKeyFromNames(entry.names, id)
}
