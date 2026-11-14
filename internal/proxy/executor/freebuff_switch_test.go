package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSwitchFreebuffModel_ReleasesHeldSessionThenAdmitsNewModel(t *testing.T) {
	// A seat held on another model must already be in the cache: the switch has
	// to drop it, or the proxy keeps sending the dead instance id.
	setFreebuffSession("tok", "model-a", &freebuffSession{
		InstanceID: "i-old",
		ExpiresAt:  time.Now().Add(time.Hour),
	})

	var releasedInstance, admittedModel string
	var calls []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Release stays on the session route; admission moved to its own.
		allowed := (r.Method == http.MethodDelete && r.URL.Path == freebuffSessionPath) ||
			(r.Method == http.MethodPost && r.URL.Path == freebuffAdmissionPath)
		if !allowed {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodDelete:
			calls = append(calls, "DELETE")
			releasedInstance = r.Header.Get(freebuffInstanceHeader)
			_, _ = w.Write([]byte(`{"status":"ended","freebucksRefund":3}`))
		case http.MethodPost:
			calls = append(calls, "POST")
			admittedModel = r.Header.Get("x-freebuff-model")
			_, _ = w.Write([]byte(`{"status":"active","instanceId":"i-new","currentModel":"model-b","expiresAt":"2030-01-01T00:00:00Z"}`))
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	result, err := SwitchFreebuffModel(context.Background(), server.Client(), server.URL, "tok", "i-old", "model-b", nil)
	if err != nil {
		t.Fatalf("SwitchFreebuffModel failed: %v", err)
	}

	// Release has to land before the admission POST, otherwise the server is
	// still holding the old seat and answers the POST with model_locked.
	if len(calls) != 2 || calls[0] != "DELETE" || calls[1] != "POST" {
		t.Fatalf("expected DELETE then POST, got %v", calls)
	}
	if releasedInstance != "i-old" {
		t.Errorf("expected released instance i-old, got %q", releasedInstance)
	}
	if admittedModel != "model-b" {
		t.Errorf("expected admitted model model-b, got %q", admittedModel)
	}
	if result.InstanceID != "i-new" || result.Model != "model-b" {
		t.Errorf("unexpected switch result: %+v", result)
	}
	if result.FreebucksRefund != 3 {
		t.Errorf("expected refund 3, got %d", result.FreebucksRefund)
	}

	if _, ok := getFreebuffSession("tok", "model-a"); ok {
		t.Error("expected the released model's session to be evicted from the cache")
	}
	if sess, ok := getFreebuffSession("tok", "model-b"); !ok || sess.InstanceID != "i-new" {
		t.Errorf("expected the new seat to be cached, got %+v (ok=%v)", sess, ok)
	}
}

func TestReleaseFreebuffSession_MissingRowIsNotAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	refund, err := releaseFreebuffSession(context.Background(), server.Client(), server.URL, "tok", "i-gone", nil)
	if err != nil {
		t.Fatalf("expected a missing session row to be tolerated, got %v", err)
	}
	if refund != 0 {
		t.Errorf("expected zero refund, got %d", refund)
	}
}

func TestReleaseFreebuffSession_RequiresInstanceID(t *testing.T) {
	if _, err := releaseFreebuffSession(context.Background(), http.DefaultClient, "https://example.invalid", "tok", "", nil); err == nil {
		t.Error("expected an error when no instance id is held")
	}
}
