//go:build integration
package integration

import (
	"testing"
)

// TestCreateApiKeyBatchPersistsPerKey pins the contract the dashboard's bulk
// create relies on: one POST per planned line, each carrying its own name and
// machineId, with no batch endpoint and no shared state between the requests.
//
// The planner (web/src/lib/bulk-keys.ts) is unit-tested; what it cannot prove is
// that a line with no name still produces a usable row, or that two lines minted
// back to back do not bleed into each other. A unit test on the planner would
// pass while the second create overwrote the first.
func TestCreateApiKeyBatchPersistsPerKey(t *testing.T) {
	env := newEnv(t)

	type want struct {
		name, machine string
	}
	// The three shapes the planner emits: both halves, name only, machineId only.
	lines := []want{
		{"office", "m-abc-123"},
		{"laptop", ""},
		{"", "m-def-456"},
	}

	type minted struct {
		id, plaintext, name, machine string
	}
	created := make([]minted, 0, len(lines))
	for _, w := range lines {
		res := env.Post(t, "/api/keys", map[string]any{"name": w.name, "machineId": w.machine})
		if res.Status != 200 {
			t.Fatalf("create %+v: status %d: %s", w, res.Status, truncate(res.Body))
		}
		var out struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		}
		res.Decode(t, &out)
		// The secret is returned exactly once, at create. A batch that returned
		// an empty key would leave the operator with a credential they cannot
		// use and no way to recover it.
		if out.ID == "" || out.Key == "" {
			t.Fatalf("create %+v returned id=%q key=%q", w, out.ID, out.Key)
		}
		created = append(created, minted{out.ID, out.Key, w.name, w.machine})
	}

	// Every minted key authenticates. This is the property a client depends on:
	// a batch of three must be three working keys, not one key written three
	// times.
	for _, c := range created {
		if got := env.Get(t, "/v1/models", WithAPIKey(c.plaintext)); got.Status != 200 {
			t.Errorf("key for %q (machineId %q) status = %d, want 200", c.name, c.machine, got.Status)
		}
	}

	// Read the rows back: the name and machineId each line carried must be on
	// its own row. Two lines bleeding into one row is the failure a planner unit
	// test cannot see.
	keys, err := env.Repo.GetApiKeys()
	if err != nil {
		t.Fatalf("GetApiKeys: %v", err)
	}
	byID := make(map[string]struct{ name, machine string }, len(keys))
	for _, k := range keys {
		name, machine := "", ""
		if k.Name != nil {
			name = *k.Name
		}
		if k.MachineID != nil {
			machine = *k.MachineID
		}
		byID[k.ID] = struct{ name, machine string }{name, machine}
	}
	for _, c := range created {
		row, ok := byID[c.id]
		if !ok {
			t.Fatalf("key %s (%q) is not in the list", c.id, c.name)
		}
		if row.name != c.name {
			t.Errorf("key %s name = %q, want %q", c.id, row.name, c.name)
		}
		if row.machine != c.machine {
			t.Errorf("key %s machineId = %q, want %q", c.id, row.machine, c.machine)
		}
	}
}
