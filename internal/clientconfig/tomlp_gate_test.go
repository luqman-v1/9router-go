package clientconfig

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

// The output gate is now the linear parser plus a read-back, not a second
// strict parse. That is a weaker check, and a weaker check has to be shown
// equivalent where it matters rather than asserted equivalent in a comment.
//
// This test is the evidence. It generates valid TOML, patches it, and asks the
// strict parser — the layer that was removed from the output path — whether the
// result is still a document TOML accepts. Any case where the new check passes
// and the old one would have failed is a document this package would write and
// a client would then refuse to read.
//
// The generator is deliberately biased toward the shapes the pre-splice
// conflict check is supposed to catch: redefined tables, a key that is also a
// table name, dotted keys, and creates into sections that already exist. A
// random byte soup would mostly produce parse failures on the input, which
// proves nothing about the output.
func TestPatchTOMLNeverWritesDocumentStrictParserRejects(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	paths := [][]string{
		{"root0"}, {"root1"},
		{"t", "k1"}, {"t", "zz"},
		{"s0", "k2"}, {"s0", "new"},
		{"a0", "b", "k3"}, {"a0", "c", "k4"},
		{"zzz", "deep", "key"},
		{"k0"}, {"k1"}, {"k2"},
		{"s0", "k0"}, {"t", "k0"},
	}

	var patched, rejected int
	for i := 0; i < 6000; i++ {
		src := randomValidTOML(rng)
		if src == "" {
			continue
		}
		edit := Edit{
			KeyPath:         paths[rng.Intn(len(paths))],
			Value:           "http://new/v1",
			CreateIfMissing: rng.Intn(2) == 0,
		}
		out, err := Patch([]byte(src), FormatTOML, []Edit{edit})
		if err != nil {
			rejected++
			continue
		}
		patched++
		var scratch any
		if err := toml.Unmarshal(out, &scratch); err != nil {
			rejected = 0 // keep the counters meaningful in the failure message
			t.Fatalf("a patch this package accepted is not valid TOML\nsrc:\n%s\nkey path %v, create %v\nout:\n%s\nstrict parse: %v",
				src, edit.KeyPath, edit.CreateIfMissing, out, err)
		}
	}
	if patched == 0 {
		t.Fatal("no document was patched; the generator is broken, not the patcher")
	}
	t.Logf("%d documents patched, %d refused before writing", patched, rejected)
}

// randomValidTOML returns a document the strict parser accepts, or "" when the
// draw produced a duplicate table. Biased toward the key/table overlaps the
// pre-splice check exists for.
func randomValidTOML(rng *rand.Rand) string {
	var b strings.Builder
	if rng.Intn(2) == 0 {
		fmt.Fprintf(&b, "root%d = \"r\"\n", rng.Intn(3))
	}
	seen := map[string]bool{}
	for j := 0; j < 1+rng.Intn(3); j++ {
		var header string
		switch rng.Intn(4) {
		case 0:
			header = fmt.Sprintf("[s%d]", j)
		case 1:
			header = fmt.Sprintf("[a%d.b]", rng.Intn(3))
		case 2:
			header = fmt.Sprintf("[[arr%d]]", rng.Intn(2))
		default:
			header = "[t]"
		}
		// A second identical [table] header is invalid TOML, and generating one
		// would test the generator rather than the patcher.
		if seen[header] && !strings.HasPrefix(header, "[[") {
			continue
		}
		seen[header] = true
		b.WriteString(header + "\n")
		for k := rng.Intn(3); k >= 0; k-- {
			// A dotted key is what makes "a.b = 1" then "a = ..." reachable: the
			// prefix is a table with no [header], which is the shape the strict
			// output parse used to reject and the pre-splice check now refuses.
			if rng.Intn(4) == 0 {
				fmt.Fprintf(&b, "k%d.sub = \"v%d\"\n", rng.Intn(50), rng.Intn(9))
				continue
			}
			fmt.Fprintf(&b, "k%d = \"v%d\"\n", rng.Intn(50), rng.Intn(9))
		}
	}
	src := b.String()
	var scratch any
	if err := toml.Unmarshal([]byte(src), &scratch); err != nil {
		return "" // the draw collided on a key; not interesting input
	}
	return src
}
