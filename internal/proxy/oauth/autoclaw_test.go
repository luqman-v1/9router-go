package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRefreshAutoclawRoundTrip(t *testing.T) {
	// Point the refresher at a fake upstream so no real credential or network
	// is needed.
	oldBase := autoclawBaseURL
	defer func() { autoclawBaseURL = oldBase }()

	var gotPath, gotAuth, gotSourceID, gotDeviceID, gotRefreshToken string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var in map[string]string
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Errorf("decode body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		gotSourceID = in["source_id"]
		gotDeviceID = in["device_id"]
		gotRefreshToken = in["refresh_token"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"at-2","refresh_token":"rt-2"}}`))
	}))
	defer srv.Close()
	autoclawBaseURL = srv.URL

	result, err := refreshAutoclaw(t.Context(), &Params{
		RefreshToken: "rt-1",
		AccessToken:  "at-1",
		ProviderSpecificData: map[string]string{
			"device_id": "did-1",
		},
	})
	if err != nil {
		t.Fatalf("refreshAutoclaw: %v", err)
	}
	if result.AccessToken != "at-2" {
		t.Errorf("AccessToken = %q, want %q", result.AccessToken, "at-2")
	}
	if result.RefreshToken != "rt-2" {
		t.Errorf("RefreshToken = %q, want %q", result.RefreshToken, "rt-2")
	}
	if gotPath != "/userapi/v1/refresh" {
		t.Errorf("path = %q, want %q", gotPath, "/userapi/v1/refresh")
	}
	if gotSourceID != "autoclaw" {
		t.Errorf("source_id = %q, want %q", gotSourceID, "autoclaw")
	}
	if gotDeviceID != "did-1" {
		t.Errorf("device_id = %q, want %q", gotDeviceID, "did-1")
	}
	if gotRefreshToken != "rt-1" {
		t.Errorf("refresh_token = %q, want %q", gotRefreshToken, "rt-1")
	}
	if gotAuth != "Bearer at-1" {
		t.Errorf("authorization = %q, want %q", gotAuth, "Bearer at-1")
	}
}

func TestRefreshAutoclawRejectsEmptyRefreshToken(t *testing.T) {
	_, err := refreshAutoclaw(t.Context(), &Params{})
	if err == nil {
		t.Fatal("expected error for empty refresh_token")
	}
	if !strings.Contains(err.Error(), "refresh_token is required") {
		t.Errorf("unexpected error: %v", err)
	}
}
