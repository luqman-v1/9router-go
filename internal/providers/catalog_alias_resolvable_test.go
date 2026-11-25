package providers

import "testing"

// The dashboard's copy button hands out "<prefix>/<model>". That prefix has
// to be the one /v1/models publishes, or the operator pastes an id that the
// router does not resolve.
//
// The catalog carries it as ProviderMeta.ModelPrefix, so the value the
// dashboard copies and the value the gateway serves are read from the same
// place. This test holds the two to each other: for every provider the
// catalog publishes, ModelPrefix must be exactly GetProviderAlias(id).
func TestCatalogModelPrefixMatchesPublishedAlias(t *testing.T) {
	seen := map[string]bool{}
	for _, metas := range GetCatalogByCategory() {
		for _, meta := range metas {
			if meta.ID == "" || meta.ModelPrefix == "" {
				continue
			}
			if want := GetProviderAlias(meta.ID); meta.ModelPrefix != want {
				t.Errorf("provider %q publishes model ids as %q but the catalog says %q; "+
					"a copied model id would not match the id the gateway serves",
					meta.ID, want, meta.ModelPrefix)
			}
			// Two visible providers publishing the same prefix would make a
			// copied model id ambiguous. Upstream has one such pair
			// ("mimo-free" and "mmf" both carry the alias "mmf"), and both
			// entries are hidden there too, so the collision never reaches a
			// page. Only the visible ones have to be unique.
			if meta.Hidden {
				continue
			}
			if seen[meta.ModelPrefix] {
				t.Errorf("two providers publish the same model prefix %q; "+
					"a copied model id would be ambiguous", meta.ModelPrefix)
			}
			seen[meta.ModelPrefix] = true
		}
	}
	if len(seen) == 0 {
		t.Fatal("no provider carried a model prefix: the catalog changed shape, so this test proved nothing")
	}
}

// A published prefix must resolve back to its own provider, or a copied
// model id routes to a different provider entirely — the cross-provider
// aliasing AGENTS.md §3 forbids.
//
// `mmf` is the one deliberate exception: upstream publishes it as a second
// name for `mimo-free`, and the dashboard drops it from the catalog for that
// reason (web/src/lib/providers.ts drops an entry whose alias is another
// entry's alias), so it never reaches a provider page.
func TestCatalogAliasResolvesBackToItsOwnProvider(t *testing.T) {
	allowed := map[string]string{
		// upstream publishes mmf as an alias of mimo-free; the dashboard
		// drops it from the catalog, so no page ever copies it.
		"mmf": "mimo-free",
	}
	for id, meta := range ProviderRegistry {
		alias := meta.Alias
		if alias == "" || alias == id {
			continue
		}
		want, ok := allowed[id]
		if !ok {
			want = id
		}
		if got := ResolveAlias(alias); got != want {
			t.Errorf("catalog alias %q of provider %q resolves to %q, want %q; "+
				"a copied model id would route to the wrong provider", alias, id, got, want)
		}
	}
}
