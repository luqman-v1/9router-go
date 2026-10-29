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
//   - fetch downloads them concurrently on a shared, cached transport, in
//     waves sized to the budget that is still open;
//   - apply walks the results in document order and charges the budget per
//     inlined image, exactly as the sequential pass did.
//
// Charging at apply time (not fetch time) keeps the contract that a refusal
// never spends the budget: a URL that fails leaves room for the next one, and
// URLs after the budget is spent keep their remote form. The wave loop is what
// makes that hold for the fetch phase too — a fixed window over the first N
// candidates would fetch nothing behind a broken prefix, and inline zero
// images from a body whose budget was never touched.

const (
	// maxPrefetchWorkers bounds concurrent downloads per pass.
	maxPrefetchWorkers = 4
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

// fetchImagesParallel downloads the collected URLs concurrently, one wave at a
// time. Each wave asks for as many candidates as the budget can still use, so a
// body whose leading URLs all refuse still reaches the images behind them —
// the semantics the sequential walk had, where prefetchOne charged the budget
// only on success and the walk continued past a refusal.
//
// A wave that fills the budget ends the loop, so a healthy body costs one
// round of fetches — the same count the pre-parallel pass made. A wave that
// returns refusals starts the next one, which is the work the sequential walk
// did anyway: the bound on a document of dead links is the document itself,
// exactly as before.
func fetchImagesParallel(ctx context.Context, client *http.Client, opts PrefetchOptions, urls []string) []prefetchResult {
	results := make([]prefetchResult, 0, len(urls))
	for len(urls) > 0 {
		want := opts.MaxImages - countFetched(results)
		if want <= 0 {
			break
		}
		wave := urls[:min(len(urls), want)]
		urls = urls[len(wave):]
		results = append(results, fetchWave(ctx, client, opts, wave)...)
	}
	return results
}

// fetchWave downloads one batch of URLs concurrently.
func fetchWave(ctx context.Context, client *http.Client, opts PrefetchOptions, urls []string) []prefetchResult {
	results := make([]prefetchResult, len(urls))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxPrefetchWorkers)
	for i, raw := range urls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// A private counter starting above zero: the fetch must not be
			// short-circuited by a budget it does not own.
			budget := 1
			dataURL, ok := prefetchOne(ctx, client, opts, raw, &budget)
			results[i] = prefetchResult{dataURL: dataURL, ok: ok}
		}()
	}
	wg.Wait()
	return results
}

// countFetched reports how many results are usable, i.e. how much of the
// budget they would spend at apply time.
func countFetched(results []prefetchResult) int {
	n := 0
	for _, r := range results {
		if r.ok {
			n++
		}
	}
	return n
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
