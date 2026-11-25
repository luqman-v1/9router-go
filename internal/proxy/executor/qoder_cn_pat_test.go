package executor

import (
	"context"
	"strings"
	"testing"
)

// A CN PAT exchange must reach the CN deployment, not intl. QoderEndpointsFor
// is keyed by provider id ("qoder-cn"); handing it the raw region string "cn"
// falls through to the intl gateway and sends CN credentials abroad
// (AGENTS.md §3.A). qoderResolveCredential therefore maps the region back to a
// provider id first.
func TestQoderResolveCredential_CNPATUsesCNEndpoints(t *testing.T) {
	var urls []string
	do := func(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error) {
		urls = append(urls, rawURL)
		if strings.Contains(rawURL, "jobToken/exchange") {
			return 200, []byte(`{"token":"jt-cn-job"}`), nil
		}
		return 200, []byte(`{"id":"cn-user-1"}`), nil
	}
	creds := QoderCosyCreds{AuthToken: "pt-cn-secret"}
	got, err := qoderResolveCredential(context.Background(), do, creds, QoderRegionCN)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.AuthToken != "jt-cn-job" {
		t.Fatalf("job token = %q, want jt-cn-job", got.AuthToken)
	}
	if got.UserID != "cn-user-1" {
		t.Fatalf("userID = %q, want cn-user-1", got.UserID)
	}
	if len(urls) != 2 {
		t.Fatalf("expected exchange + userinfo calls, got %d: %v", len(urls), urls)
	}
	for _, u := range urls {
		if !strings.Contains(u, "qoder.com.cn") {
			t.Fatalf("CN credential sent to non-CN endpoint %q (urls=%v)", u, urls)
		}
	}
}

// The intl path is unchanged: an intl PAT still reaches the .sh deployment.
func TestQoderResolveCredential_IntlPATUsesIntlEndpoints(t *testing.T) {
	var urls []string
	do := func(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error) {
		urls = append(urls, rawURL)
		if strings.Contains(rawURL, "jobToken/exchange") {
			return 200, []byte(`{"token":"jt-intl-job"}`), nil
		}
		return 200, []byte(`{"id":"intl-user-1"}`), nil
	}
	creds := QoderCosyCreds{AuthToken: "pt-intl-secret"}
	if _, err := qoderResolveCredential(context.Background(), do, creds, QoderRegionIntl); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, u := range urls {
		if !strings.Contains(u, "qoder.sh") {
			t.Fatalf("intl credential sent to non-intl endpoint %q (urls=%v)", u, urls)
		}
	}
}

// A device/job token passes through without any exchange call, on either region.
func TestQoderResolveCredential_DeviceTokenPassesThrough(t *testing.T) {
	called := false
	do := func(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error) {
		called = true
		return 200, nil, nil
	}
	creds := QoderCosyCreds{AuthToken: "dt-device", UserID: "u1"}
	got, err := qoderResolveCredential(context.Background(), do, creds, QoderRegionCN)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if called {
		t.Fatal("device token must not trigger a PAT exchange")
	}
	if got.AuthToken != "dt-device" || got.UserID != "u1" {
		t.Fatalf("creds mutated: %+v", got)
	}
}
