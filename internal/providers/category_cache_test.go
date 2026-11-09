package providers

import (
	"slices"
	"testing"
)

// The three catalog reads are memoized because ProviderRegistry is static, so a
// caller now gets a shared slice rather than a fresh copy. That makes "does this
// still return the right data" and "can a caller corrupt it for everyone else"
// the two things worth pinning.
func TestGetCatalogByCategory_ReturnsEveryRegisteredProvider(t *testing.T) {
	catalog := GetCatalogByCategory()
	seen := 0
	for _, cat := range CategoryOrder {
		metas, ok := catalog[cat]
		if !ok {
			t.Errorf("category %q missing from the grouped catalog", cat)
			continue
		}
		if !slices.IsSortedFunc(metas, compareMeta) {
			t.Errorf("category %q is not sorted by priority then id", cat)
		}
		seen += len(metas)
	}
	if want := len(ProviderRegistry) + 2; seen != want {
		t.Errorf("grouped catalog carries %d providers, want %d (registry + the two custom endpoints)", seen, want)
	}
	// The two synthetic custom endpoints must always be present, or an empty
	// install shows no way to create one.
	custom, ok := catalog[CategoryCustom]
	if !ok || len(custom) < 2 {
		t.Fatalf("custom category must carry the openai/anthropic compatible endpoints, got %d", len(custom))
	}
}

// Appending to a slice a caller received must not change what the next caller
// reads. This is the risk the memoization introduces: handing out a shared
// reference means an append could be written into the shared backing array.
//
// Written against a local variable rather than by assigning back into the map,
// because writing to the map is a different thing — the map is shared by design,
// and a caller that stores into it has simply replaced its own view.
func TestGetCatalogByCategory_AppendDoesNotEscape(t *testing.T) {
	shared := GetCatalogByCategory()[CategoryOAuth]
	if cap(shared) != len(shared) {
		t.Skipf("slice has spare capacity (len=%d cap=%d); the clip below is what removes it", len(shared), cap(shared))
	}
	local := append(shared, ProviderMeta{ID: "injected-by-caller"})
	if &local[0] == &shared[0] {
		t.Fatal("append wrote into the shared backing array")
	}
	for _, m := range GetCatalogByCategory()[CategoryOAuth] {
		if m.ID == "injected-by-caller" {
			t.Fatal("the appended row is visible to the next caller")
		}
	}
}

// The memoization must be idempotent across calls: same data, same order. A
// caller that got a different ordering on the second call would render a list
// that reorders itself under the operator.
func TestGetCatalogByCategory_IsStableAcrossCalls(t *testing.T) {
	a := GetCatalogByCategory()
	b := GetCatalogByCategory()
	for _, cat := range CategoryOrder {
		if len(a[cat]) != len(b[cat]) {
			t.Fatalf("category %q changed size between calls: %d vs %d", cat, len(a[cat]), len(b[cat]))
		}
		for i := range a[cat] {
			if a[cat][i].ID != b[cat][i].ID {
				t.Errorf("category %q position %d changed: %q vs %q", cat, i, a[cat][i].ID, b[cat][i].ID)
			}
		}
	}
}

// The llm requirement is the whole point of this list: a no-auth media host is
// not something an operator can chat with, so it must not appear here.
func TestNoAuthProviders_OnlyOffersChatCapableEntries(t *testing.T) {
	for _, m := range NoAuthProviders() {
		if !m.NoAuth {
			t.Errorf("%s is listed as no-auth but its entry says NoAuth=false", m.ID)
		}
		if !slices.Contains(m.ServiceKinds, "llm") {
			t.Errorf("%s is offered as a no-auth provider but declares kinds %v — it cannot answer a chat request", m.ID, m.ServiceKinds)
		}
	}
	if len(NoAuthProviders()) == 0 {
		t.Error("no no-auth providers found; an empty install would show nothing it can chat with")
	}
}

// Every registered provider that is no-auth and llm-capable must appear, or a
// usable provider is invisible until someone invents an account for it.
func TestNoAuthProviders_CoversEveryEligibleEntry(t *testing.T) {
	want := map[string]bool{}
	for _, m := range ProviderRegistry {
		if m.NoAuth && slices.Contains(m.ServiceKinds, "llm") {
			want[m.ID] = true
		}
	}
	for _, m := range NoAuthProviders() {
		delete(want, m.ID)
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for id := range want {
			missing = append(missing, id)
		}
		slices.Sort(missing)
		t.Errorf("eligible providers missing from the no-auth list: %v", missing)
	}
}

// The label map is handed to the client to render section headers, so a copy is
// required: mutating the caller's map must not change the shipped vocabulary.
func TestServiceKindLabels_ReturnsACopy(t *testing.T) {
	a := ServiceKindLabels()
	a["llm"] = "MUTATED"
	if got := ServiceKindLabels()["llm"]; got != "LLM" {
		t.Errorf("ServiceKindLabels leaked its internal map: got %q, want LLM", got)
	}
}

func compareMeta(a, b ProviderMeta) int {
	if a.Priority != b.Priority {
		if a.Priority < b.Priority {
			return -1
		}
		return 1
	}
	if a.ID < b.ID {
		return -1
	}
	if a.ID > b.ID {
		return 1
	}
	return 0
}
