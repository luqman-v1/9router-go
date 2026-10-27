package executor

import (
	"bytes"
	"testing"
)

// wireBodyRuns is how many identical calls each case makes. json/v2 randomizes
// map member order unless Deterministic(true) is asked for, so a seam that
// marshals a map without the option produces a different byte sequence within
// a couple of calls; twenty makes the assertion reliable without slowing the
// suite.
const wireBodyRuns = 20

// TestWireBodiesAreDeterministic pins the map-to-bytes request seams the
// thinking pass and the CommandCode rewrite own. Both upstreams key their
// prompt cache on the longest byte-identical prefix, so the same logical
// request has to serialize identically on every call — dropping the
// deterministic option at any case below fails this test.
func TestWireBodiesAreDeterministic(t *testing.T) {
	tests := []struct {
		name string
		// call performs one identical request and returns the wire body.
		call func(t *testing.T) ([]byte, error)
		// wantKey is a top-level member of the returned body, asserted so a
		// seam that silently returns something else cannot pass by accident.
		wantKey string
	}{
		{
			// The nested `params` path. The caller supplies everything the
			// function would otherwise mint — threadId and the config (whose
			// date is read from the clock) — so map ordering is the only
			// variable left.
			name:    "commandcode params body",
			wantKey: "params",
			call: func(t *testing.T) ([]byte, error) {
				return buildCommandcodeBody([]byte(`{
					"model": "m",
					"threadId": "thread-fixed",
					"memory": "",
					"config": {"workingDir": "/"},
					"params": {
						"messages": [{"role": "user", "content": "hi"}],
						"stream": true,
						"temperature": 0.5,
						"top_p": 0.9
					}
				}`), "m")
			},
		},
		{
			// The Responses-native muse-spark lane: input, model, stream,
			// store and the nested reasoning block all sit in one map.
			name:    "muse-spark responses body",
			wantKey: "input",
			call: func(t *testing.T) ([]byte, error) {
				return normalizeMuseSparkResponsesBody([]byte(`{
					"model": "muse-spark-1.3-contributor",
					"input": [{"role": "user", "content": "hi"}],
					"stream": true,
					"store": false,
					"reasoning_effort": "high"
				}`), "muse-spark-1.3-contributor")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, err := tt.call(t)
			if err != nil {
				t.Fatalf("call 1: %v", err)
			}
			if !bytes.Contains(first, []byte(`"`+tt.wantKey+`"`)) {
				t.Fatalf("body lacks %q: %s", tt.wantKey, first)
			}
			for i := 2; i <= wireBodyRuns; i++ {
				got, err := tt.call(t)
				if err != nil {
					t.Fatalf("call %d: %v", i, err)
				}
				if !bytes.Equal(first, got) {
					t.Fatalf("call %d serialized differently from call 1:\n call 1: %s\n call %d: %s",
						i, first, i, got)
				}
			}
		})
	}
}
