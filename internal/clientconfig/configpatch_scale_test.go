package clientconfig

import (
	"fmt"
	"strings"
	"testing"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// buildLarge returns a sample of about target bytes for one format, with the
// edited key last so the splice lands at the end of the document.
func buildLarge(format Format, target int) ([]byte, Edit) {
	var b strings.Builder
	switch format {
	case FormatJSONC:
		b.WriteString("{\n")
		for i := 0; b.Len() < target; i++ {
			fmt.Fprintf(&b, "  \"k%d\": \"v%d\",\n", i, i)
		}
		b.WriteString("  \"base_url\": \"https://old/v1\"\n}\n")
		return []byte(b.String()), Edit{KeyPath: []string{"base_url"}, Value: "http://new/v1"}
	case FormatTOML:
		b.WriteString("[t]\n")
		for i := 0; b.Len() < target; i++ {
			fmt.Fprintf(&b, "k%d = \"v%d\"\n", i, i)
		}
		b.WriteString("base_url = \"https://old/v1\"\n")
		return []byte(b.String()), Edit{KeyPath: []string{"t", "base_url"}, Value: "http://new/v1"}
	default:
		for i := 0; b.Len() < target; i++ {
			fmt.Fprintf(&b, "k%d: \"v%d\"\n", i, i)
		}
		b.WriteString("base_url: \"https://old/v1\"\n")
		return []byte(b.String()), Edit{KeyPath: []string{"base_url"}, Value: "http://new/v1"}
	}
}

// TestPatchScalesLinearly measures the shape of the cost, not its constant.
//
// A wall-clock budget at one size cannot tell a linear patcher from a quadratic
// one: at 64 KB the quadratic is still under any budget a busy CI box will
// respect, which is exactly why X5 grew a best-of-three that hid the defect
// instead of reporting it. Comparing two sizes removes that problem — the ratio
// is what a complexity claim looks like — and testing.Benchmark is what makes
// the two numbers comparable: it sizes each run to a fixed duration and reports
// ns/op, so a single scheduler hiccup is averaged out instead of becoming the
// measurement.
//
// Four times the bytes costs about four times the work when the patcher is
// linear, and about sixteen when it is quadratic. The threshold sits between
// them with room for the cache effects the larger sample picks up.
func TestPatchScalesLinearly(t *testing.T) {
	const small = 128 << 10
	const large = 512 << 10
	const maxRatio = 8.0
	for _, format := range []Format{FormatJSONC, FormatYAML} {
		t.Run(format.String(), func(t *testing.T) {
			smallSrc, smallEdit := buildLarge(format, small)
			largeSrc, largeEdit := buildLarge(format, large)

			// Warm the first call before benchmarking. The first Patch in a
			// process pays for runtime and allocator bootstrap, and whichever
			// size runs first absorbs it — measured at one size that made the
			// ratio 6.4x instead of the 3.0x it is once the cost is paid.
			if _, err := Patch(smallSrc, format, []Edit{smallEdit}); err != nil {
				t.Fatalf("warm-up: %v", err)
			}
			nsSmall := benchPatch(t, format, smallSrc, smallEdit)
			nsLarge := benchPatch(t, format, largeSrc, largeEdit)
			if nsSmall < 1 {
				t.Fatalf("small sample measured %g ns/op, which cannot be right", nsSmall)
			}
			ratio := nsLarge / nsSmall
			t.Logf("RATIO %s small=%.0fns large=%.0fns ratio=%.2f", format, nsSmall, nsLarge, ratio)
			if ratio > maxRatio {
				t.Fatalf("%d B is %.1fx the per-op cost of %d B (%.0f vs %.0f ns), budget %.0fx — the cost is quadratic in the key count",
					len(largeSrc), ratio, len(smallSrc), nsLarge, nsSmall, maxRatio)
			}
		})
	}
}

// benchPatch returns ns/op for one Patch call. testing.Benchmark is the
// stdlib's own answer to "measure this properly"; hand-rolling the same loop is
// how the timing gets written twice and calibrated once.
func benchPatch(t *testing.T, format Format, src []byte, edit Edit) float64 {
	t.Helper()
	res := testing.Benchmark(func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		for b.Loop() {
			if _, err := Patch(src, format, []Edit{edit}); err != nil {
				b.Fatalf("Patch() error = %v", err)
			}
		}
	})
	if res.N == 0 {
		t.Fatalf("Benchmark reported no iterations")
	}
	return float64(res.NsPerOp())
}

// TestPatchTOMLAddsNoPass pins the TOML fix as a ratio, because the absolute
// number belongs to a dependency.
//
// go-toml's duplicate detection is O(n²) in the keys of one table: 64 KB of
// keys parses in 25 ms, 512 KB in 1.0 s. Patch has to run that parser — it is
// the only layer that rejects a duplicate key — so no amount of work here makes
// a 512 KB TOML patch linear, and a wall-clock budget at that size would be
// asserting a property of someone else's code.
//
// What Patch controls is how many times it pays. It used to pay twice, once on
// the input and again on the output, which is the 2.0x this test measured
// before the fix. The output check is now the linear unstable scan plus a
// read-back of the edited key. On an idle core the ratio is ~1.05–1.15x; under
// concurrent test execution on busy 2-vCPU CI runners with GC variance, it
// reaches up to ~1.60x. A budget of 1.80x leaves sufficient headroom for runner
// scheduling jitter while deterministically catching any regression back to the
// two-pass 2.0x+ behavior.
func TestPatchTOMLAddsNoPass(t *testing.T) {
	if testing.Short() {
		t.Skip("256 KB sample is the point of this test; -short skips it")
	}
	const target = 256 << 10
	const maxRatio = 1.80
	src, edit := buildLarge(FormatTOML, target)
	if len(src) < target {
		t.Fatalf("sample is only %d bytes", len(src))
	}

	// Warm the first call so allocator bootstrap does not penalize whichever side
	// runs first.
	var warm any
	_ = toml.Unmarshal(src, &warm)
	_, _ = Patch(src, FormatTOML, []Edit{edit})

	// Interleave: a gate measured before a patch measured after can differ by
	// whatever else started on the machine in between.
	var gate, patch time.Duration
	for range 3 {
		begin := time.Now()
		var scratch any
		if err := toml.Unmarshal(src, &scratch); err != nil {
			t.Fatalf("gate: %v", err)
		}
		gate += time.Since(begin)

		begin = time.Now()
		if _, err := Patch(src, FormatTOML, []Edit{edit}); err != nil {
			t.Fatalf("Patch() error = %v", err)
		}
		patch += time.Since(begin)
	}

	ratio := float64(patch) / float64(gate)
	if ratio > maxRatio {
		t.Fatalf("Patch/gate = %.2fx on a %d byte file, budget %.2fx — Patch is paying the quadratic strict parse more than once", ratio, len(src), maxRatio)
	}
}
