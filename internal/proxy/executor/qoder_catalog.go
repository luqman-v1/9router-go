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
	// qoderCatalogTTL matches upstream's CACHE_TTL_MS (1h).
	qoderCatalogTTL = time.Hour

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
	expiresAt  time.Time
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

// qoderModelConfig resolves model_config for a model key, fetching the live
// catalogue when the cache cannot answer.
//
// It force-refreshes once before giving up: a first-ever request for a
// credential has an empty cache by definition, and a stale entry for a newly
// published model is the other way this fails. Failing here — rather than
// sending a request Qoder will reject — is what turns "400 flow nodes found"
// into a message naming the actual problem.
func qoderModelConfig(ctx context.Context, do QoderDoer, creds QoderCosyCreds, region QoderRegion, modelKey string) (map[string]any, error) {
	if config := qoderCachedModelConfig(creds, region, modelKey); config != nil {
		return config, nil
	}

	catalog, err := qoderFetchCatalog(ctx, do, creds, region, false)
	if err == nil && catalog != nil {
		if config := qoderDefensiveCopy(catalog.rawConfigs[modelKey], modelKey); config != nil {
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
	if config := qoderDefensiveCopy(refreshed.rawConfigs[modelKey], modelKey); config != nil {
		return config, nil
	}
	return nil, fmt.Errorf("qoder: model_config for %q not yet known (run a model list fetch or check upstream connectivity)", modelKey)
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
		// A miss already in flight answers this request instead of issuing a
		// second one, so a burst of parallel chat windows fans out to exactly
		// one upstream call.
		result, err, _ := qoderCatalogSfly.Do(key, func() (any, error) {
			if cached := qoderCachedModelConfigAny(creds, region); cached != nil {
				return cached, nil
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

// qoderFetchCatalogOnce performs the COSY-signed GET of /algo/api/v2/model/list.
func qoderFetchCatalogOnce(ctx context.Context, do QoderDoer, creds QoderCosyCreds, region QoderRegion) (*qoderCatalog, error) {
	modelListURL := QoderEndpointsFor(string(region)).ModelListURLForToken(creds.AuthToken)

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

	catalog := &qoderCatalog{rawConfigs: rawConfigs, expiresAt: time.Now().Add(qoderCatalogTTL)}
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
