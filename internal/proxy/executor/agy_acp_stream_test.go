package executor

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEmitPacedSplitsText(t *testing.T) {
	text := "Halo 🌍 dunia, ini Potongan teks yang panjang."
	var pieces []string
	base := func(choices []map[string]any) map[string]any {
		return map[string]any{"choices": choices}
	}
	emit := func(chunk map[string]any) error {
		choices := chunk["choices"].([]map[string]any)
		delta := choices[0]["delta"].(map[string]any)
		pieces = append(pieces, delta["content"].(string))
		return nil
	}
	if err := emitPaced(context.Background(), emit, base, "content", text); err != nil {
		t.Fatalf("emitPaced: %v", err)
	}
	if got := strings.Join(pieces, ""); got != text {
		t.Fatalf("joined text = %q, want %q", got, text)
	}
	if len(pieces) < 2 {
		t.Fatalf("pieces = %d, want multiple deltas", len(pieces))
	}
	for i, p := range pieces {
		if !utf8.ValidString(p) {
			t.Fatalf("piece %d invalid UTF-8: %q", i, p)
		}
	}
}
