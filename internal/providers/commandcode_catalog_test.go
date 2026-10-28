package providers

import "testing"

// TestCommandCodeLiveCatalog pins the live-catalogue wiring for Command Code.
//
// Its registry catalog is a fixed 22-model snapshot, so every model Command
// Code released after that snapshot exists only in the Provider API's
// /v1/models feed: the free tiers (reflection/beam:free,
// stealth/glyph-cluster:free, poolside/laguna-s-2.1-free) and the paid ones
// (minimax/minimax-m3.1-flash, z-ai/glm-5.3-flash, moonshotai/Kimi-K3, …).
// Without the endpoint below, the dashboard's "Check Latest Models", "Import
// from /models" and "Sync Models" all answer 400 for the provider and the
// catalogue stays frozen at the snapshot.
func TestCommandCodeLiveCatalog(t *testing.T) {
	const want = "https://api.commandcode.ai/provider/v1/models"
	if got := ModelsListURL("commandcode"); got != want {
		t.Errorf("ModelsListURL(commandcode) = %q, want %q", got, want)
	}
	// The endpoint answers keyless, so the catalogue is reachable even for a
	// connection whose credential was revoked — a discovery path that needed a
	// live key would report a broken sync instead of a stale catalogue.
	if got := ModelsListHeaders("commandcode"); got != nil {
		t.Errorf("ModelsListHeaders(commandcode) = %v, want nil (no extra headers)", got)
	}
}

// TestPublicLiveCatalogs pins the five providers whose catalogue is reachable
// without a valid credential — the same class of fix as commandcode, and
// distinct from the keyed aggregators where a revoked key means no discovery
// at all.
//
// "Public" is the precise word, and the distinction the table below cannot
// show is worth stating: probed live, all five answer 200 with no Authorization
// header, but only nvidia, ollama and openrouter still answer 200 once the
// handler sends the bearer it always sends. vercel-ai-gateway and chutes 401 on
// any bearer, so a revoked key there reports a failed fetch rather than a stale
// catalogue. Either way a connection must exist first — writeConnectionModels
// refuses a provider with no stored key before it dials anything.
//
// The stakes differ per provider and are the reason this is a table rather
// than a comment:
//
//   - vercel-ai-gateway and chutes ship no static catalog at all — absent from
//     ProviderModels and from web/src/lib/models.ts — so before this entry the
//     dashboard could not list one model for either.
//   - nvidia and ollama carry 11 and 8 static rows against 84 and 23 live ones.
//   - openrouter already had a web modelsFetcher for its free tier but no
//     backend entry, so its "Check Latest Models" button never rendered.
//
// ollama is the odd shape: {models:[{name, model, …}]} rather than OpenAI's
// {data:[{id, …}]}. writeConnectionModels accepts either key and the dashboard
// falls back to `model` when `id` is absent, so it needs no special case — this
// test is what stops someone "fixing" that by adding one.
func TestPublicLiveCatalogs(t *testing.T) {
	tests := []struct {
		id  string
		url string
	}{
		{"vercel-ai-gateway", "https://ai-gateway.vercel.sh/v1/models"},
		{"chutes", "https://llm.chutes.ai/v1/models"},
		{"nvidia", "https://integrate.api.nvidia.com/v1/models"},
		{"ollama", "https://ollama.com/api/tags"},
		{"openrouter", "https://openrouter.ai/api/v1/models"},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if got := ModelsListURL(tt.id); got != tt.url {
				t.Errorf("ModelsListURL(%q) = %q, want %q", tt.id, got, tt.url)
			}
			// None of the five needs a header beyond the bearer token the
			// connection already carries.
			if got := ModelsListHeaders(tt.id); got != nil {
				t.Errorf("ModelsListHeaders(%q) = %v, want nil", tt.id, got)
			}
		})
	}
}

// TestKeylessCatalogsExcludeLocalOllama guards the one provider that must NOT
// join the table above. ollama-local is a loopback endpoint by design
// (LocalByDesign), and pointing the shared discovery path at it would make
// "Check Latest Models" dial the operator's own machine — a different failure
// from a dead upstream, and one the operator cannot act on.
func TestKeylessCatalogsExcludeLocalOllama(t *testing.T) {
	if got := ModelsListURL("ollama-local"); got != "" {
		t.Errorf("ModelsListURL(ollama-local) = %q, want \"\" (loopback stays out of the shared discovery path)", got)
	}
}
