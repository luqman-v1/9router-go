package executor

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Qoder model_config cache — port of open-sse/services/qoderModels.js
// (fetchQoderCatalogRaw, getQoderModelConfig, resolveQoderModels, cacheKey).
//
// model_config is not derivable from the request: Qoder publishes it in the
// live catalogue and expects it echoed back inside the chat payload. Without it
// there is no valid request to make, so chat has to be able to fetch the
// catalogue itself rather than depend on someone having pressed "import models"
// in the dashboard first.

const (
	// qoderCatalogTTL matches upstream's CACHE_TTL_MS (1h). Deliberately kept
	// at upstream's hour rather than the 5-minute snapshot some sibling
	// gateways use: AGENTS.md §1 makes decolua/9router the behavioral spec,
	// and a 12x tighter refresh loop buys nothing on a per-credential cache
	// that chat already force-refreshes when a model is missing. The short
	// retry window below covers the unfetched-catalog case instead.
	qoderCatalogTTL = time.Hour

	// qoderCatalogRetryWindow holds off non-forced fetches after a failed
	// attempt on a credential that has no live snapshot. Without it every
	// request on an install whose catalog is unreachable queues behind a
	// fresh fetch (and its timeout). Forced fetches — chat's last resort and
	// the post-login fill — ignore the window.
	qoderCatalogRetryWindow = 30 * time.Second

	// qoderCatalogTimeout bounds one catalogue fetch, including a PAT
	// exchange. Upstream uses the same connect timeout for the chat POST.
	qoderCatalogTimeout = 30 * time.Second

	// qoderMaxCatalogBytes caps the model-list read. The real answer is a few
	// kilobytes; this only bounds a broken or hostile endpoint.
	qoderMaxCatalogBytes = 2 << 20
)

// qoderCatalogCache holds the raw per-model configs, keyed by credential.
type qoderCatalog struct {
	// rawConfigs is keyed by model id. It carries every entry the account
	// published, including `enable:false` ones: chat needs model_config for
	// a model the IDE hides just as much as for a visible one.
	rawConfigs map[string]map[string]any
	// names maps every advertised id (raw keys and sanitized display names)
	// to the raw key a request must carry. Built once per snapshot so a
	// refresh cannot reshuffle friendly-name resolution.
	names map[string]string
	// attemptedAt records the last fetch start, successful or not. Only a
	// credential without a live snapshot obeys it (qoderCatalogRetryWindow).
	attemptedAt time.Time
	expiresAt   time.Time
}

var (
	qoderCatalogMu   sync.RWMutex
	qoderCatalogs    = map[string]*qoderCatalog{}
	qoderCatalogSfly singleflight.Group
)

// qoderCatalogKey identifies one credential's catalogue. Keying on the user id
// plus the token kind keeps two accounts, and a PAT-versus-device-token pair
// on the same account, from sharing a cache entry that one of them could not
// fetch.
func qoderCatalogKey(creds QoderCosyCreds, region QoderRegion) string {
	return string(region) + "\x00" + creds.UserID + "\x00" + creds.AuthToken
}

// qoderCachedModelConfig returns a cached config for modelKey without touching
// the network.
func qoderCachedModelConfig(creds QoderCosyCreds, region QoderRegion, modelKey string) map[string]any {
	qoderCatalogMu.RLock()
	defer qoderCatalogMu.RUnlock()
	entry, ok := qoderCatalogs[qoderCatalogKey(creds, region)]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil
	}
	return qoderDefensiveCopy(entry.rawConfigs[modelKey], modelKey)
}

// qoderDefensiveCopy returns a copy of a catalog config stamped with the model
// key. The caller mutates model_config when applying a context tier, so a
// shared cached map must never be handed out directly.
func qoderDefensiveCopy(config map[string]any, modelKey string) map[string]any {
	if config == nil {
		return nil
	}
	copied := make(map[string]any, len(config)+1)
	for k, v := range config {
		copied[k] = v
	}
	copied["key"] = modelKey
	return copied
}

// qoderStaticModelConfigs are static model_config blocks for models the
// per-account catalog does not reliably publish.
//
// /algo/api/v2/model/list is per-account, and the frontier pair Sonus
// (smodel) / Cantus (cmodel) is frequently absent from it even for accounts
// that can use them. Without a model_config the executor refuses the request
// before it is ever sent, so the model looks unavailable when upstream would
// have served it. The chat endpoint does not validate model_config.key
// against the catalog, so supplying the block makes these routable.
//
// This is a FALLBACK only — a live catalog entry always wins, so a future
// catalog that publishes its own block for these keys takes precedence.
var qoderStaticModelConfigs = map[string]map[string]any{
	"smodel": {
		"model": "", "format": "openai", "source": "system", "url": "",
		"key": "smodel", "display_name": "Sonus",
		"is_vl": true, "is_reasoning": true, "max_input_tokens": 180000,
	},
	"cmodel": {
		"model": "", "format": "openai", "source": "system", "url": "",
		"key": "cmodel", "display_name": "Cantus",
		"is_vl": true, "is_reasoning": true, "max_input_tokens": 180000,
	},
}

// qoderStaticModelConfig returns a fresh copy of the static block for a
// canonical Qoder key, or nil when there is none (so genuinely unknown keys
// still fail loudly).
func qoderStaticModelConfig(key string) map[string]any {
	base, ok := qoderStaticModelConfigs[key]
	if !ok {
		return nil
	}
	copied := make(map[string]any, len(base)+1)
	for k, v := range base {
		copied[k] = v
	}
	copied["key"] = key
	return copied
}

// qoderModelConfig resolves model_config for a model key, fetching the live
// catalogue when the cache cannot answer. The key may be a friendly name:
// the cached catalog's display names resolve first, the static alias table
// second, and a freshly fetched catalog re-resolves the original id so a
// name only the live list knows still lands on its raw key.
//
// It force-refreshes once before giving up: a first-ever request for a
// credential has an empty cache by definition, and a stale entry for a newly
// published model is the other way this fails. Failing here — rather than
// sending a request Qoder will reject — is what turns "400 flow nodes found"
// into a message naming the actual problem. The static blocks are the last
// resort: Sonus/Cantus are accepted by chat even when /model/list never
// mentions them, so refusing here would make an available model look broken.
func qoderModelConfig(ctx context.Context, do QoderDoer, creds QoderCosyCreds, region QoderRegion, modelKey string) (map[string]any, error) {
	resolved := qoderResolveModelKey(creds, region, modelKey)
	if config := qoderCachedModelConfig(creds, region, resolved); config != nil {
		return config, nil
	}

	catalog, err := qoderFetchCatalog(ctx, do, creds, region, false)
	if err == nil && catalog != nil {
		if config := qoderConfigFromCatalog(catalog, modelKey); config != nil {
			return config, nil
		}
	}

	refreshed, refreshErr := qoderFetchCatalog(ctx, do, creds, region, true)
	if refreshErr != nil {
		return nil, refreshErr
	}
	if refreshed == nil {
		return nil, fmt.Errorf("qoder: model catalogue unavailable for %s", region)
	}
	if config := qoderConfigFromCatalog(refreshed, modelKey); config != nil {
		return config, nil
	}
	if config := qoderStaticModelConfig(resolved); config != nil {
		return config, nil
	}
	return nil, fmt.Errorf("qoder: model_config for %q not yet known (run a model list fetch or check upstream connectivity)", modelKey)
}

// qoderConfigFromCatalog reads one snapshot's config for an advertised id.
// The snapshot's own names win over the static alias table, so an account
// whose display names differ from the marketing names resolves the way its
// model list says. The returned copy is stamped with the raw key, never the
// friendly id: model_config.key is what Qoder routes the chat by.
func qoderConfigFromCatalog(catalog *qoderCatalog, id string) map[string]any {
	// Normalize exactly like the cache-hit path: a client may send the
	// qualified form ("qd/qwen3.8-flash") and the snapshot indexes bare ids.
	key := qoderModelKey(id)
	if canonical, ok := qoderCatalogKeyFromNames(catalog.names, key); ok {
		key = canonical
	} else if alias, ok := qoderModelAliases[strings.ToLower(key)]; ok {
		key = alias
	}
	return qoderDefensiveCopy(catalog.rawConfigs[key], key)
}

// qoderFetchCatalog reads the live model list, COSY-signed with the caller's
// identity. Concurrent misses for one credential collapse into a single
// upstream request; forceRefresh callers deliberately bypass the cache and get
// their own fetch.
func qoderFetchCatalog(ctx context.Context, do QoderDoer, creds QoderCosyCreds, region QoderRegion, forceRefresh bool) (*qoderCatalog, error) {
	if creds.UserID == "" {
		return nil, fmt.Errorf("qoder: model catalogue needs the account user id — re-authorize this connection")
	}
	if creds.AuthToken == "" {
		return nil, fmt.Errorf("qoder: model catalogue needs an auth token")
	}

	key := qoderCatalogKey(creds, region)
	if !forceRefresh {
		if qoderCatalogRetryHoldoff(creds, region) {
			return nil, errQoderCatalogRetryHoldoff
		}
		// A miss already in flight answers this request instead of issuing a
		// second one, so a burst of parallel chat windows fans out to exactly
		// one upstream call.
		result, err, _ := qoderCatalogSfly.Do(key, func() (any, error) {
			if cached := qoderCachedModelConfigAny(creds, region); cached != nil {
				return cached, nil
			}
			if qoderCatalogRetryHoldoff(creds, region) {
				return nil, errQoderCatalogRetryHoldoff
			}
			return qoderFetchCatalogOnce(ctx, do, creds, region)
		})
		if err != nil {
			return nil, err
		}
		return result.(*qoderCatalog), nil
	}
	return qoderFetchCatalogOnce(ctx, do, creds, region)
}

// qoderCachedModelConfigAny returns a live cache entry regardless of model key,
// so a singleflight leader that already paid for a fetch can serve followers.
func qoderCachedModelConfigAny(creds QoderCosyCreds, region QoderRegion) *qoderCatalog {
	qoderCatalogMu.RLock()
	defer qoderCatalogMu.RUnlock()
	entry, ok := qoderCatalogs[qoderCatalogKey(creds, region)]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil
	}
	return entry
}

// qoderProviderIDForRegion maps a region back to the provider id the endpoint
// table is keyed by. QoderEndpointsFor expects provider ids: handing it the
// raw region string "cn" silently falls through to the intl deployment, which
// would send CN credentials to the intl gateway (AGENTS.md §3.A).
func qoderProviderIDForRegion(region QoderRegion) string {
	if region == QoderRegionCN {
		return "qoder-cn"
	}
	return "qoder"
}

// qoderFetchCatalogOnce performs the COSY-signed GET of /algo/api/v2/model/list.
func qoderFetchCatalogOnce(ctx context.Context, do QoderDoer, creds QoderCosyCreds, region QoderRegion) (*qoderCatalog, error) {
	qoderRecordCatalogAttempt(creds, region)
	modelListURL := QoderEndpointsFor(qoderProviderIDForRegion(region)).ModelListURLForToken(creds.AuthToken)

	headers, err := buildQoderCosyHeaders(nil, modelListURL, creds)
	if err != nil {
		return nil, err
	}
	headers["Accept"] = "application/json"
	// gzip triggers signature validation on Qoder's CDN; force identity.
	headers["Accept-Encoding"] = "identity"

	status, body, err := do(ctx, http.MethodGet, modelListURL, headers, nil)
	if err != nil {
		return nil, fmt.Errorf("qoder model list: %w", err)
	}

	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("qoder model list returned %d", status)
	}
	var parsed struct {
		Chat []map[string]any `json:"chat"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("qoder model list was not JSON: %w", err)
	}

	rawConfigs := make(map[string]map[string]any, len(parsed.Chat))
	for _, entry := range parsed.Chat {
		key, _ := entry["key"].(string)
		if key == "" {
			continue
		}
		// Cache every key, not just the enabled ones: chat needs model_config
		// for a model the IDE hides (account policy) just as much as for a
		// visible one.
		rawConfigs[key] = entry
	}
	if len(rawConfigs) == 0 {
		return nil, fmt.Errorf("qoder model list carried no models")
	}

	catalog := &qoderCatalog{
		rawConfigs:  rawConfigs,
		names:       qoderCatalogNames(rawConfigs),
		attemptedAt: time.Now(),
		expiresAt:   time.Now().Add(qoderCatalogTTL),
	}
	qoderCatalogMu.Lock()
	defer qoderCatalogMu.Unlock()
	qoderCatalogs[qoderCatalogKey(creds, region)] = catalog
	return catalog, nil
}

// qoderPatPrefix marks a Personal Access Token, which cannot sign COSY
// requests and must be exchanged for a job token first.
const qoderPatPrefix = "pt-"

// IsQoderPAT reports whether a stored credential is a Personal Access Token.
func IsQoderPAT(token string) bool {
	return strings.HasPrefix(strings.TrimSpace(token), qoderPatPrefix)
}
