//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"

	"9router/proxy/internal/modelhealth"
)

// The two invariants the maintainer's review of #687 called out, driven through
// the real router rather than the package internals:
//
//  1. A disabled-models write reports the operator's own decisions to the
//     model-health loop, so a model the operator re-enables stops being
//     scheduler-owned.
//  2. Toggling the feature on in the dashboard does not need a restart.
//
// (2) is covered by the unit tables in internal/modelhealth — a restart cannot
// be observed inside one process. This drives (1), which is the wiring the
// review found missing.
func TestDisabledModelsWriteReachesModelHealthAudit(t *testing.T) {
	env := newEnv(t)
	// The app publishes the loop when it starts the scheduler, and the router
	// reads it back into the dashboard handler. The integration harness mounts
	// the real router without booting the app, so this publishes the loop the
	// way the boot does — otherwise the handler would (correctly) find none and
	// record nothing, which is the other test's case.
	modelhealth.PublishLoop(modelhealth.NewLoop(
		modelhealth.RepoStore{Repo: env.Repo},
		nil, nil, nil,
	))
	t.Cleanup(modelhealth.ResetLoopForTest)
	if _, err := env.Repo.CreateProviderNode("node-vsllm", "openai-compatible", "VSLLM",
		`{"prefix":"vsllm","apiType":"openai-compatible"}`); err != nil {
		t.Fatalf("create node: %v", err)
	}
	// A custom model on the node, which is what a pass would consider.
	if err := env.Repo.SetKV("customModels", "node-vsllm|m1|llm",
		`{"providerAlias":"node-vsllm","id":"m1","type":"llm"}`); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}
	// The scheduler disabled it, so the mark is set and the model is in the
	// stored disabled list.
	if err := env.Repo.SetKV("modelHealth.failures", "node-vsllm/m1",
		`{"count":3,"disabledByScheduler":true,"everProbed":true}`); err != nil {
		t.Fatalf("seed failure state: %v", err)
	}
	if err := env.Repo.SetKV("disabledModels", "node-vsllm", `["m1"]`); err != nil {
		t.Fatalf("seed disabled list: %v", err)
	}

	// The operator re-enables it: the list goes from ["m1"] to [].
	res := env.Put(t, "/api/models/disabled/node-vsllm", map[string]any{"models": []string{}})
	if res.Status != http.StatusOK {
		t.Fatalf("PUT disabled = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	// The audit log records the operator's decision, attributed to them.
	auditRes := env.Get(t, "/api/models/health")
	if auditRes.Status != http.StatusOK {
		t.Fatalf("GET health = %d, want 200 (body: %s)", auditRes.Status, truncate(auditRes.Body))
	}
	var health struct {
		Audit []struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
			Action   string `json:"action"`
			Actor    string `json:"actor"`
		} `json:"audit"`
	}
	if err := json.Unmarshal(auditRes.Body, &health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	var found bool
	for _, e := range health.Audit {
		if e.Model == "m1" && e.Action == "enabled" {
			found = true
			if e.Actor != "operator" {
				t.Errorf("actor = %q, want operator", e.Actor)
			}
		}
	}
	if !found {
		t.Fatalf("audit = %+v, want an operator 'enabled' entry for m1", health.Audit)
	}

	// And the scheduler mark is gone, so the loop will not silently re-enable
	// or refuse to re-disable it.
	kv, err := env.Repo.GetKVScope("modelHealth.failures")
	if err != nil {
		t.Fatalf("read failure scope: %v", err)
	}
	raw := kv["node-vsllm/m1"]
	var f struct {
		DisabledByScheduler bool `json:"disabledByScheduler"`
	}
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		t.Fatalf("decode failure state %q: %v", raw, err)
	}
	if f.DisabledByScheduler {
		t.Error("disabledByScheduler still set after the operator re-enabled the model")
	}
}
