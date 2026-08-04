package media

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestPollDeployStatus verifies the deployment status polling loop against a
// fixture server: it must keep polling while "building" and stop on "succeeded".
func TestPollDeployStatus(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer tok" {
			t.Errorf("expected Bearer auth, got %q", auth)
		}
		calls++
		if calls < 3 {
			w.Write([]byte(`{"status":"building"}`))
			return
		}
		w.Write([]byte(`{"status":"succeeded","url":"x"}`))
	}))
	defer srv.Close()

	got, err := pollDeployStatus(t.Context(), &http.Client{Timeout: time.Second}, srv.URL, "tok", time.Millisecond, 10, func(data map[string]any) (bool, error) {
		return data["status"] == "succeeded", nil
	})
	if err != nil {
		t.Fatalf("pollDeployStatus errored: %v", err)
	}
	if got["url"] != "x" {
		t.Errorf("expected url x in result, got %v", got)
	}
	if calls < 3 {
		t.Errorf("expected multiple polls, got %d", calls)
	}

	// check returning an error surfaces immediately.
	if _, err := pollDeployStatus(t.Context(), &http.Client{Timeout: time.Second}, srv.URL, "tok", time.Millisecond, 3, func(data map[string]any) (bool, error) {
		return false, &pollErr{}
	}); err == nil || err.Error() != "boom" {
		t.Errorf("expected boom, got %v", err)
	}

	// Zero attempts: immediate timeout.
	if _, err := pollDeployStatus(t.Context(), &http.Client{}, "http://x", "tok", time.Millisecond, 0, func(map[string]any) (bool, error) { return false, nil }); err == nil || err.Error() != "deployment timed out" {
		t.Errorf("expected deployment timed out, got %v", err)
	}
}

type pollErr struct{}

func (*pollErr) Error() string { return "boom" }