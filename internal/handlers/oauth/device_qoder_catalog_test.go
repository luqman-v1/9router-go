package oauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// After a successful Qoder device login the live model catalog must be
// filled immediately, so the operator's first chat request resolves
// model_config and friendly names from a warm snapshot. The fill is
// best-effort and must never fail the login.

type qoderCatalogCall struct {
	method  string
	url     string
	headers map[string]string
}

func TestRefreshQoderCatalogAfterDeviceLogin(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		wantURL  string
	}{
		{"intl", "qoder", "https://api3.qoder.sh/algo/api/v2/model/list"},
		{"cn", "qoder-cn", "https://gateway.qoder.com.cn/algo/api/v2/model/list"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int64
			var got qoderCatalogCall
			do := func(_ context.Context, method, rawURL string, headers map[string]string, _ []byte) (int, []byte, error) {
				calls.Add(1)
				got = qoderCatalogCall{method: method, url: rawURL, headers: headers}
				return http.StatusOK, []byte(`{"chat":[{"key":"qfmodel","enable":true,"display_name":"Qwen3.8-Flash"}]}`), nil
			}
			tokens := deviceTokens{
				access: "dt-fresh",
				extra:  map[string]any{"userId": "u1", "machineId": "m1"},
			}

			refreshQoderCatalogWithDoer(context.Background(), tt.provider, tokens, do)

			if calls.Load() != 1 {
				t.Fatalf("catalog fetch happened %d times, want 1", calls.Load())
			}
			if got.method != http.MethodGet {
				t.Errorf("method = %q, want GET", got.method)
			}
			if got.url != tt.wantURL {
				t.Errorf("url = %q, want %q (provider isolation)", got.url, tt.wantURL)
			}
			if got.headers["Cosy-User"] != "u1" {
				t.Errorf("Cosy-User = %q, want the login's user id", got.headers["Cosy-User"])
			}
			if !strings.HasPrefix(got.headers["Authorization"], "Bearer COSY.") {
				t.Errorf("Authorization = %q, want a COSY signature", got.headers["Authorization"])
			}
			if got.headers["Cosy-Machineid"] != "m1" {
				t.Errorf("Cosy-Machineid = %q, want the login's machine id", got.headers["Cosy-Machineid"])
			}
		})
	}
}

// The fill must be skipped — silently, without a network call — when there
// is nothing to sign with or the provider is not Qoder.
func TestRefreshQoderCatalogAfterDeviceLoginSkips(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		tokens   deviceTokens
	}{
		{"other provider", "github", deviceTokens{access: "dt-x", extra: map[string]any{"userId": "u1"}}},
		{"empty token", "qoder", deviceTokens{extra: map[string]any{"userId": "u1"}}},
		{"pat cannot sign", "qoder", deviceTokens{access: "pt-x", extra: map[string]any{"userId": "u1"}}},
		{"missing user id", "qoder", deviceTokens{access: "dt-x", extra: map[string]any{"machineId": "m1"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int64
			do := func(_ context.Context, _, _ string, _ map[string]string, _ []byte) (int, []byte, error) {
				calls.Add(1)
				return http.StatusOK, []byte(`{"chat":[]}`), nil
			}
			refreshQoderCatalogWithDoer(context.Background(), tt.provider, tt.tokens, do)
			if calls.Load() != 0 {
				t.Errorf("skip case made %d catalog calls, want 0", calls.Load())
			}
		})
	}
}

// A failed fill is logged, never fatal: the login has already succeeded.
func TestRefreshQoderCatalogAfterDeviceLoginFailureIsNotFatal(t *testing.T) {
	do := func(_ context.Context, _, _ string, _ map[string]string, _ []byte) (int, []byte, error) {
		return 0, nil, fmt.Errorf("upstream unreachable")
	}
	tokens := deviceTokens{access: "dt-fresh", extra: map[string]any{"userId": "u1"}}
	refreshQoderCatalogWithDoer(context.Background(), "qoder", tokens, do)
}

// The default transport is the device flow's own client, so the fill works
// from the same networks the login just succeeded on.
func TestRefreshQoderCatalogAfterDeviceLoginUsesDeviceClient(t *testing.T) {
	var sawCatalog atomic.Bool
	origTransport := deviceClient.Transport
	deviceClient.Transport = deviceRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/algo/api/v2/model/list") {
			sawCatalog.Store(true)
			rec := httptest.NewRecorder()
			rec.Header().Set("Content-Type", "application/json")
			rec.WriteHeader(http.StatusOK)
			_, _ = rec.WriteString(`{"chat":[{"key":"qfmodel","enable":true}]}`)
			return rec.Result(), nil
		}
		return nil, fmt.Errorf("unexpected request to %s", req.URL)
	})
	t.Cleanup(func() { deviceClient.Transport = origTransport })

	tokens := deviceTokens{access: "dt-fresh", extra: map[string]any{"userId": "u1", "machineId": "m1"}}
	refreshQoderCatalogAfterDeviceLogin(context.Background(), "qoder", tokens)

	if !sawCatalog.Load() {
		t.Error("post-login fill never reached the model list through deviceClient")
	}
}
