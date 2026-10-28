package translator

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

// Parallel image prefetch. The sequential walk this replaces downloaded every
// remote image one at a time on a throwaway transport, so a turn carrying four
// screenshots paid four full TCP+TLS handshakes back to back — with Antigravity
// and the other Gemini-family targets inlining, that was seconds of TTFT the
// native clients do not pay.
//
// The split is collect → fetch → apply:
//   - collect walks the wire shapes in document order and records, per remote
//     URL, a closure that writes the inlined data URL back into its block;
//   - fetch downloads them concurrently on a shared, cached transport;
//   - apply walks the results in document order and charges the budget per
//     inlined image, exactly as the sequential pass did.
//
// Charging at apply time (not fetch time) keeps the contract that a refusal
// never spends the budget: a URL that fails leaves room for the next one, and
// URLs after the budget is spent keep their remote form. The cost is that a
// turn whose budget closes early may have fetched images it then discards —
// bounded by maxPrefetchTargets, and invisible next to what it saves.

const (
	// maxPrefetchWorkers bounds concurrent downloads per pass.
	maxPrefetchWorkers = 4
	// maxPrefetchTargets caps how many remote URLs one pass will fetch, so a
	// pathological body cannot spawn unbounded work for a budget of 4.
	maxPrefetchTargets = 16
)

// imageWork is one remote URL found in the body, with the write-back into the
// block it came from.
type imageWork struct {
	url   string
	apply func(dataURL string)
}

// workURLs lists the collected URLs in document order, for the fetch phase.
func workURLs(works []imageWork) []string {
	urls := make([]string, len(works))
	for i, w := range works {
		urls[i] = w.url
	}
	return urls
}

// collectImageWork mirrors walkImageBlocks' shape coverage, collecting instead
// of mutating: image_url (object and bare string), input_image, Claude
// url-source images, and content arrays nested under tool_result.
func collectImageWork(node map[string]any, out *[]imageWork) {
	content, ok := node["content"]
	if !ok {
		return
	}
	blocks, ok := content.([]any)
	if !ok {
		return
	}
	for _, bRaw := range blocks {
		block, ok := bRaw.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "image_url":
			collectOpenAIImage(block, out)
		case "input_image":
			if raw, ok := block["image_url"].(string); ok && isRemoteImageURL(raw) {
				url := raw
				*out = append(*out, imageWork{url: url, apply: func(dataURL string) {
					block["image_url"] = dataURL
				}})
			}
		case "image":
			collectClaudeImage(block, out)
		case "tool_result":
			collectImageWork(block, out)
		}
	}
}

func collectOpenAIImage(block map[string]any, out *[]imageWork) {
	switch iu := block["image_url"].(type) {
	case string:
		if !isRemoteImageURL(iu) {
			return
		}
		url := iu
		*out = append(*out, imageWork{url: url, apply: func(dataURL string) {
			block["image_url"] = dataURL
		}})
	case map[string]any:
		raw, ok := iu["url"].(string)
		if !ok || !isRemoteImageURL(raw) {
			return
		}
		url := raw
		*out = append(*out, imageWork{url: url, apply: func(dataURL string) {
			iu["url"] = dataURL
		}})
	}
}

func collectClaudeImage(block map[string]any, out *[]imageWork) {
	src, ok := block["source"].(map[string]any)
	if !ok {
		return
	}
	srcType, _ := src["type"].(string)
	if srcType != "url" {
		return
	}
	raw, ok := src["url"].(string)
	if !ok || !isRemoteImageURL(raw) {
		return
	}
	url := raw
	*out = append(*out, imageWork{url: url, apply: func(dataURL string) {
		applyClaudeImage(block, dataURL)
	}})
}

// applyClaudeImage converts a "data:<mime>;base64,<data>" URL into Claude's
// base64 source shape.
func applyClaudeImage(block map[string]any, dataURL string) {
	media, b64, found := strings.Cut(strings.TrimPrefix(dataURL, "data:"), ";base64,")
	if !found {
		return
	}
	block["source"] = map[string]any{
		"type":       "base64",
		"media_type": media,
		"data":       b64,
	}
}

type prefetchResult struct {
	dataURL string
	ok      bool
}

// fetchImagesParallel downloads the collected URLs concurrently. Each fetch
// gets its own budget counter: prefetchOne charges the budget it is handed and
// refuses to run when it is empty, and the real budget is charged later, at
// apply time, so the fetch phase must never see it.
func fetchImagesParallel(ctx context.Context, client *http.Client, opts PrefetchOptions, urls []string) []prefetchResult {
	if len(urls) > maxPrefetchTargets {
		urls = urls[:maxPrefetchTargets]
	}
	results := make([]prefetchResult, len(urls))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxPrefetchWorkers)
	for i, raw := range urls {
		wg.Add(1)
		go func(i int, raw string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// A private counter starting above zero: the fetch must not be
			// short-circuited by a budget it does not own.
			budget := 1
			dataURL, ok := prefetchOne(ctx, client, opts, raw, &budget)
			results[i] = prefetchResult{dataURL: dataURL, ok: ok}
		}(i, raw)
	}
	wg.Wait()
	return results
}

// cached prefetch clients: the transport is safe for concurrent use and its
// idle-connection pool only pays off if it survives across passes, so the
// per-pass construction this replaces (a fresh transport per request) is built
// once per allowLoopback mode instead.
var (
	prefetchClientMu       sync.Mutex
	defaultPrefetchClient  *http.Client
	loopbackPrefetchClient *http.Client
)

// prefetchClientFor returns the HTTP client for one pass. opts.Client (tests)
// always wins; production shares the cached client for its allowLoopback mode.
func prefetchClientFor(opts PrefetchOptions) *http.Client {
	if opts.Client != nil {
		return opts.Client
	}
	prefetchClientMu.Lock()
	defer prefetchClientMu.Unlock()
	if opts.AllowLoopback {
		if loopbackPrefetchClient == nil {
			loopbackPrefetchClient = newPrefetchClient(true)
		}
		return loopbackPrefetchClient
	}
	if defaultPrefetchClient == nil {
		defaultPrefetchClient = newPrefetchClient(false)
	}
	return defaultPrefetchClient
}
