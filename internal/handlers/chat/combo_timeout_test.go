package chat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	internalproxy "9router/proxy/internal/proxy"
)

func TestComboResponseTimeoutPrecommit(t *testing.T) {
	rec := httptest.NewRecorder()
	gate := newComboResponseTimeout(context.Background(), rec, time.Hour)
	defer gate.finish()
	peeker := internalproxy.NewStreamContentPeeker(gate)
	_, _ = peeker.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
	gate.expire()
	if gate.ctx.Err() != context.Canceled || !gate.timedOut() {
		t.Fatal("precommit timeout must cancel candidate")
	}
	_, _ = peeker.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"late\"}}]}\n\n"))
	if rec.Body.Len() != 0 {
		t.Fatalf("expired candidate leaked response: %s", rec.Body.String())
	}
}

func TestComboResponseTimeoutKeepsCommittedStreamAlive(t *testing.T) {
	rec := httptest.NewRecorder()
	gate := newComboResponseTimeout(context.Background(), rec, time.Hour)
	defer gate.finish()
	peeker := internalproxy.NewStreamContentPeeker(gate)
	_, _ = peeker.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n"))
	gate.expire()
	if gate.ctx.Err() != nil || gate.timedOut() {
		t.Fatal("committed stream must outlive response timeout")
	}
	_, _ = peeker.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"second\"}}]}\n\n"))
	if !strings.Contains(rec.Body.String(), "second") {
		t.Fatal("active stream lost later output")
	}
}

func TestComboResponseTimeoutParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	gate := newComboResponseTimeout(ctx, httptest.NewRecorder(), time.Hour)
	defer gate.finish()
	cancel()
	if gate.ctx.Err() != context.Canceled || gate.timedOut() {
		t.Fatal("parent cancellation must remain distinct from model timeout")
	}
}

func TestComboModelResponseTimeoutEndpoints(t *testing.T) {
	for _, messages := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			name := "chat"
			if messages {
				name = "messages"
			}
			if stream {
				name += "-stream"
			}
			t.Run(name, func(t *testing.T) {
				var stalledCalls atomic.Int32
				release := make(chan struct{})
				stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					stalledCalls.Add(1)
					// Stream headers and a role-only frame are not usable content.
					// Non-streaming requests exercise a stall before headers.
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
						w.(http.Flusher).Flush()
					}
					select {
					case <-r.Context().Done():
					case <-release:
					}
				}))
				defer stalled.Close()
				defer close(release)
				healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"healthy response\"}}]}\n\ndata: [DONE]\n\n"))
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"healthy response"}}]}`))
					}
				}))
				defer healthy.Close()
				repo := seedComboWithTwoUpstreams(t, "combo-timeout-"+name, stalled.URL, healthy.URL, "timeout-test")
				if err := repo.UpdateSettingsRaw(map[string]any{"comboStrategies": map[string]any{"timeout-test": map[string]any{"firstTokenTimeoutMs": 100}}}); err != nil {
					t.Fatal(err)
				}
				h := NewChatHandler(repo)
				body := `{"model":"timeout-test","messages":[{"role":"user","content":"hi"}],"stream":false}`
				if stream {
					body = strings.Replace(body, `"stream":false`, `"stream":true`, 1)
				}
				req := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(body))
				rec := httptest.NewRecorder()
				if messages {
					h.HandleMessages(rec, req)
				} else {
					h.HandleChatCompletions(rec, req)
				}
				if !strings.Contains(rec.Body.String(), "healthy response") {
					t.Fatalf("missing failover: %d %s", rec.Code, rec.Body.String())
				}
				if stalledCalls.Load() != 1 {
					t.Fatalf("timed-out model retried %d times", stalledCalls.Load())
				}
				if !repo.IsProviderAvailable("deepseek", "deepseek-chat") {
					t.Fatal("model timeout cooled provider")
				}
				if locked, _ := repo.IsConnectionModelLocked("mock-a", "deepseek-chat"); locked {
					t.Fatal("model timeout locked account")
				}
			})
		}
	}
}

// TestComboTimeoutSparesLastCandidate pins #744: the response timeout only
// advances a combo. The last candidate keeps the global deadline, so a combo
// whose every member is slower than the timeout still answers instead of
// failing sooner than it would with the setting off.
func TestComboTimeoutSparesLastCandidate(t *testing.T) {
	for _, messages := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			name := "chat"
			if messages {
				name = "messages"
			}
			if stream {
				name += "-stream"
			}
			t.Run(name, func(t *testing.T) {
				slow := func(answer string) http.HandlerFunc {
					return func(w http.ResponseWriter, r *http.Request) {
						select {
						case <-r.Context().Done():
							return
						case <-time.After(300 * time.Millisecond):
						}
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"" + answer + "\"}}]}\n\ndata: [DONE]\n\n"))
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"` + answer + `"}}]}`))
					}
				}
				first := httptest.NewServer(slow("first member"))
				defer first.Close()
				last := httptest.NewServer(slow("last member"))
				defer last.Close()
				repo := seedComboWithTwoUpstreams(t, "combo-spare-"+name, first.URL, last.URL, "spare-test")
				if err := repo.UpdateSettingsRaw(map[string]any{"comboStrategies": map[string]any{"spare-test": map[string]any{"firstTokenTimeoutMs": 100}}}); err != nil {
					t.Fatal(err)
				}
				h := NewChatHandler(repo)
				body := fmt.Sprintf(`{"model":"spare-test","messages":[{"role":"user","content":"hi"}],"stream":%t}`, stream)
				req := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(body))
				rec := httptest.NewRecorder()
				if messages {
					h.HandleMessages(rec, req)
				} else {
					h.HandleChatCompletions(rec, req)
				}
				out := rec.Body.String()
				if rec.Code != http.StatusOK || !strings.Contains(out, "last member") {
					t.Fatalf("last candidate was cut short: %d %s", rec.Code, out)
				}
				if strings.Contains(out, "first member") {
					t.Fatalf("timed-out candidate leaked output: %s", out)
				}
			})
		}
	}
}

func TestComboTimeoutGracePeriod(t *testing.T) {
	cases := []struct {
		name          string
		settings      map[string]any
		wantStalledOn int32 // stalled upstream hits after two requests
	}{
		{"default grace benches the model", map[string]any{"firstTokenTimeoutMs": 100}, 1},
		{"explicit grace benches the model", map[string]any{"firstTokenTimeoutMs": 100, "firstTokenGraceMs": 60000}, 1},
		{"zero grace re-includes at once", map[string]any{"firstTokenTimeoutMs": 100, "firstTokenGraceMs": 0}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stalledCalls atomic.Int32
			release := make(chan struct{})
			stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				stalledCalls.Add(1)
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer stalled.Close()
			defer close(release)
			healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"healthy response"}}]}`))
			}))
			defer healthy.Close()
			repo := seedComboWithTwoUpstreams(t, "grace-"+strings.ReplaceAll(tc.name, " ", "-"), stalled.URL, healthy.URL, "grace-test")
			if err := repo.UpdateSettingsRaw(map[string]any{"comboStrategies": map[string]any{"grace-test": tc.settings}}); err != nil {
				t.Fatal(err)
			}
			h := NewChatHandler(repo)
			for i := 0; i < 2; i++ {
				req := httptest.NewRequest(http.MethodPost, "/chat/completions",
					strings.NewReader(`{"model":"grace-test","messages":[{"role":"user","content":"hi"}],"stream":false}`))
				rec := httptest.NewRecorder()
				h.HandleChatCompletions(rec, req)
				if !strings.Contains(rec.Body.String(), "healthy response") {
					t.Fatalf("request %d not served: %d %s", i, rec.Code, rec.Body.String())
				}
			}
			if got := stalledCalls.Load(); got != tc.wantStalledOn {
				t.Fatalf("stalled model hit %d times, want %d", got, tc.wantStalledOn)
			}
		})
	}
}

func TestBenchedModelsReturnAfterGrace(t *testing.T) {
	h := &ChatHandler{}
	models := []string{"a/x", "b/y"}
	h.benchTimedOutModel("c", "a/x", time.Hour)
	if got := h.withoutBenchedModels("c", models, time.Hour); len(got) != 1 || got[0] != "b/y" {
		t.Fatalf("benched model not skipped: %v", got)
	}
	if got := h.withoutBenchedModels("other", models, time.Hour); len(got) != 2 {
		t.Fatalf("bench leaked to another combo: %v", got)
	}
	h.benchTimedOutModel("c", "b/y", time.Hour)
	if got := h.withoutBenchedModels("c", models, time.Hour); len(got) != 2 {
		t.Fatalf("fully benched chain must stay intact: %v", got)
	}
	h.timeoutBench[comboBenchKey("c", "a/x")] = time.Now().Add(-time.Second)
	h.timeoutBench[comboBenchKey("c", "b/y")] = time.Now().Add(-time.Second)
	if got := h.withoutBenchedModels("c", models, time.Hour); len(got) != 2 {
		t.Fatalf("expired grace must re-include: %v", got)
	}
}
