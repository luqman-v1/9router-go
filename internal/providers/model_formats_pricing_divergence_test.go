package providers

import (
	"sort"
	"strings"
	"testing"

	"9router/proxy/internal/pricing"
)

// Price and lane are two different questions that share a vocabulary, and
// nothing used to force them to agree (#410):
//
//   - pricing.KnownFreeModels answers "what does this id cost" — an id there is
//     billed at zero although it carries no "-free" suffix.
//   - openCodeFreeTierModels answers "which gate serves this id" — an id there is
//     forwarded to the keyless /inference/v1 gate instead of the keyed /zen/v1
//     lane.
//
// An id may legitimately land in one and not the other, but only as a recorded
// decision. openCodePricingDivergences is that record: every id whose two answers
// disagree names which way it disagrees and why. The tests below fail when a new
// id starts disagreeing silently, and when a recorded entry stops being true.
//
// The direction matters. big-pickle is billed at zero and served on the paid
// lane; deepseek-v4-flash-free is the mirror — served keyless while the price
// table still resolves a paid row. A record that only said "divergent" would
// let one silently turn into the other, so both sides are stored.
type divergence struct {
	pricedFree bool   // pricing.IsFreeModel(id)
	lanedFree  bool   // IsOpencodeFreeTierModel("opencode-zen", id)
	why        string // which side is deliberate, and what is still undecided
}

var openCodePricingDivergences = map[string]divergence{
	"big-pickle": {
		pricedFree: true,
		lanedFree:  false,
		why: "price: zero, mirroring upstream's suggested-models filter. lane: keyed /zen/v1, paid — #328 pinned it " +
			"after a 200 from the keyless gate failed to separate \"this id is free\" from \"the gate served a " +
			"zero-priced id\", and #366 declined to move it. Billing zero for a paid-lane call is the open " +
			"question in #410; these two lists cannot settle it.",
	},
	"deepseek-v4-flash-free": {
		pricedFree: false,
		lanedFree:  true,
		why: "price: paid — resolves to 0.14/0.28 per 1M through the deepseek-v* glob, not through a row naming " +
			"this id. lane: keyless /inference/v1, i.e. served by the free gate. The mirror of big-pickle: here " +
			"the name declares free, the lane treats it free, and only the price table disagrees. " +
			"handlers.deadFreeOpencodeModels also lists it, so three lists already call this id free and one " +
			"charges for it. Whether the paid row should apply is the same maintainer call #410 leaves open for " +
			"big-pickle — this entry records the state, it does not decide it.",
	},
}

// declaresFree reports whether an id states its own free status in its name.
// The suffix is not by itself an exemption from the price check: the lane rule
// matches the suffix, but pricing is resolved independently, and a -free id can
// still land on a paid row (deepseek-v4-flash-free does, via the deepseek-v*
// glob). The tests below therefore exempt a -free id only when the price table
// has no row for it at all.
func declaresFree(id string) bool {
	return strings.HasSuffix(strings.ToLower(id), "-free")
}

// TestZeroPricedIdWithoutFreeLaneIsDocumented covers the price→lane direction: an
// id billed at zero that is not routed to the free gate must say so in the record,
// and one that is routed there must not still carry an entry.
func TestZeroPricedIdWithoutFreeLaneIsDocumented(t *testing.T) {
	ids := append([]string(nil), pricing.KnownFreeModels...)
	sort.Strings(ids)
	if len(ids) == 0 {
		t.Skip("no zero-priced ids outside the -free convention; nothing to reconcile")
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			if !pricing.IsFreeModel(id) {
				t.Fatalf("%q is listed in KnownFreeModels but IsFreeModel is false", id)
			}
			laned := IsOpencodeFreeTierModel("opencode-zen", id)
			rec, documented := openCodePricingDivergences[id]
			if !documented {
				if !laned {
					t.Errorf("IsOpencodeFreeTierModel(opencode-zen, %q) = false while the id is billed at zero: "+
						"record the divergence in openCodePricingDivergences", id)
				}
				return
			}
			if !rec.pricedFree || rec.lanedFree != laned {
				t.Errorf("recorded %q as pricedFree=%v lanedFree=%v, but it is pricedFree=true lanedFree=%v: "+
					"the entry no longer describes this id", id, rec.pricedFree, rec.lanedFree, laned)
			}
		})
	}
}

// TestFreeLaneIdWithoutZeroPriceIsDocumented covers the lane→price direction: an
// id routed to the free gate that is not priced at zero must be recorded.
//
// A "-free" name used to be an unconditional exemption here, justified by "the
// price table carries no row for it". That is true for nine of the ten ids in the
// lane list and false for the tenth: deepseek-v4-flash-free resolves to a paid
// row through the deepseek-v* glob. The exemption is now the narrow claim it
// always described — no row means no cost — so the paid-row case reaches the
// record instead of skipping the check.
func TestFreeLaneIdWithoutZeroPriceIsDocumented(t *testing.T) {
	for id := range openCodeFreeTierModels {
		t.Run(id, func(t *testing.T) {
			if pricing.IsFreeModel(id) {
				if rec, ok := openCodePricingDivergences[id]; ok && (!rec.pricedFree || !rec.lanedFree) {
					t.Errorf("recorded %q as pricedFree=%v lanedFree=%v, but it is priced at zero on the free lane: "+
						"drop the entry", id, rec.pricedFree, rec.lanedFree)
				}
				return
			}
			if declaresFree(id) {
				// Free by construction: no row in the price table, so every
				// caller records a cost of zero regardless of the ok flag.
				if _, ok := pricing.GetPricingForModel("opencode-zen", id); !ok {
					return
				}
			}
			rec, ok := openCodePricingDivergences[id]
			if !ok {
				t.Errorf("%q is routed to the free gate but not billed at zero; record the reason in "+
					"openCodePricingDivergences", id)
				return
			}
			if rec.pricedFree || !rec.lanedFree {
				t.Errorf("recorded %q as pricedFree=%v lanedFree=%v, but it is pricedFree=false lanedFree=true: "+
					"the entry no longer describes this id", id, rec.pricedFree, rec.lanedFree)
			}
		})
	}
}

// TestRecordedDivergencesStillHold keeps the record honest: an entry that no
// longer describes a disagreement is stale and must be dropped, and an entry
// that describes a different disagreement than the one in the tree must be
// updated. That is what stops the record from rotting into a list of things
// nobody re-checked.
func TestRecordedDivergencesStillHold(t *testing.T) {
	ids := make([]string, 0, len(openCodePricingDivergences))
	for id := range openCodePricingDivergences {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	if len(ids) == 0 {
		t.Fatal("the record is empty, so no divergence is covered — remove this test only together with the map")
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			rec := openCodePricingDivergences[id]
			if strings.TrimSpace(rec.why) == "" {
				t.Errorf("%q is recorded without a reason", id)
			}
			pricedFree := pricing.IsFreeModel(id)
			lanedFree := IsOpencodeFreeTierModel("opencode-zen", id)
			if pricedFree == lanedFree {
				t.Errorf("%q is recorded as a divergence but price-free=%v and lane-free=%v now agree; "+
					"drop the entry", id, pricedFree, lanedFree)
			}
			if rec.pricedFree != pricedFree || rec.lanedFree != lanedFree {
				t.Errorf("%q is recorded as pricedFree=%v lanedFree=%v but is now pricedFree=%v lanedFree=%v; "+
					"update the record", id, rec.pricedFree, rec.lanedFree, pricedFree, lanedFree)
			}
		})
	}
}
