package qodercheckin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func claimHTTP(t *testing.T, handler http.HandlerFunc) Doer {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return func(req *http.Request) (*http.Response, error) {
		// Retarget the absolute upstream URL at the local fake while keeping
		// path, method, headers and body verbatim.
		target, err := url.Parse(server.URL)
		if err != nil {
			t.Fatalf("parse fake URL: %v", err)
		}
		clone := req.Clone(req.Context())
		clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
		clone.Host = ""
		return server.Client().Do(clone)
	}
}

func newTestChecker(t *testing.T, do Doer) (*Checker, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qoder-checkin.json")
	c, err := NewChecker(path, do)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	return c, path
}

func campaignsJSON(campaigns string) string {
	return `{"campaigns":[` + campaigns + `]}`
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func TestQoderCheckin(t *testing.T) {
	start := time.Now().Add(-time.Hour).Unix()
	end := time.Now().Add(time.Hour).Unix()
	claimable := campaignsJSON(`{"campaignId":"c1","actionType":"CLAIM_BENEFIT","claimStatus":"CLAIMABLE","startAt":` + itoa(start) + `,"endAt":` + itoa(end) + `,"benefit":{"amount":100}}`)
	tests := []struct {
		name      string
		list      string
		claim     string
		want      Outcome
		wantCred  float64
		wantCalls int
	}{
		{
			name:      "claimable daily credits are claimed",
			list:      claimable,
			claim:     `{"status":"CLAIMED","benefit":{"amount":100}}`,
			want:      OutcomeClaimed,
			wantCred:  100,
			wantCalls: 2,
		},
		{
			name:      "already claimed active campaign is done",
			list:      campaignsJSON(`{"campaignId":"c1","actionType":"CLAIM_BENEFIT","claimStatus":"CLAIMED","startAt":` + itoa(start) + `,"endAt":` + itoa(end) + `,"benefit":{"amount":100}}`),
			want:      OutcomeDone,
			wantCred:  100,
			wantCalls: 1,
		},
		{
			name:      "no daily campaign is inactive",
			list:      campaignsJSON(`{"campaignId":"c1","actionType":"OTHER","claimStatus":"CLAIMABLE"}`),
			want:      OutcomeInactive,
			wantCalls: 1,
		},
		{
			name:      "claim refusal is a failure",
			list:      claimable,
			claim:     `{"status":"REJECTED"}`,
			want:      OutcomeFailed,
			wantCalls: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			do := claimHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if got := r.Header.Get("Authorization"); got != "Bearer dt-test" {
					t.Errorf("Authorization = %q", got)
				}
				if !strings.HasSuffix(r.URL.Path, campaignsPath) && !strings.HasSuffix(r.URL.Path, "/claim") {
					t.Errorf("unexpected path %q", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(tt.list))
					return
				}
				if r.Method != http.MethodPost {
					t.Errorf("claim method = %s", r.Method)
				}
				_, _ = w.Write([]byte(tt.claim))
			})
			c, _ := newTestChecker(t, do)
			r := c.Check(context.Background(), Account{Provider: "qoder", ID: "conn-1", Name: "personal", Token: "dt-test"})
			if r.Outcome != tt.want {
				t.Fatalf("outcome = %q, want %q (msg=%q)", r.Outcome, tt.want, r.Message)
			}
			if r.Credit != tt.wantCred {
				t.Errorf("credit = %v, want %v", r.Credit, tt.wantCred)
			}
			if calls != tt.wantCalls {
				t.Errorf("calls = %d, want %d", calls, tt.wantCalls)
			}
			results := c.Results()
			if len(results) != 1 || results[0].Outcome != tt.want {
				t.Errorf("stored results = %+v", results)
			}
		})
	}
}

func TestQoderCheckinSiteIsolation(t *testing.T) {
	start, end := time.Now().Add(-time.Hour).Unix(), time.Now().Add(time.Hour).Unix()
	list := campaignsJSON(`{"campaignId":"c1","actionType":"CLAIM_BENEFIT","claimStatus":"CLAIMABLE","startAt":` + itoa(start) + `,"endAt":` + itoa(end) + `,"benefit":{"amount":50}}`)
	claims := 0
	do := claimHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			claims++
			_, _ = w.Write([]byte(`{"status":"CLAIMED","benefit":{"amount":50}}`))
			return
		}
		_, _ = w.Write([]byte(list))
	})
	c, _ := newTestChecker(t, do)
	for _, provider := range []string{"qoder", "qoder-cn"} {
		r := c.Check(context.Background(), Account{Provider: provider, ID: "id-" + provider, Token: "dt-" + provider})
		if r.Outcome != OutcomeClaimed {
			t.Errorf("%s outcome = %q, want claimed (msg=%q)", provider, r.Outcome, r.Message)
		}
	}
	if got := len(c.Results()); got != 2 {
		t.Errorf("results = %d, want 2 (one per site)", got)
	}
	if claims != 2 {
		t.Errorf("claims = %d, want 2 (one per site)", claims)
	}
}

func TestQoderCheckinExpiredSignIn(t *testing.T) {
	do := claimHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"expired"}}`))
	})
	c, _ := newTestChecker(t, do)
	r := c.Check(context.Background(), Account{Provider: "qoder", ID: "x", Token: "dead"})
	if r.Outcome != OutcomeFailed || !strings.Contains(r.Message, "sign-in has expired") {
		t.Fatalf("outcome=%q msg=%q", r.Outcome, r.Message)
	}
}

func TestQoderCheckinLedgerSurvivesRestart(t *testing.T) {
	start, end := time.Now().Add(-time.Hour).Unix(), time.Now().Add(time.Hour).Unix()
	do := claimHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(campaignsJSON(`{"campaignId":"c1","actionType":"CLAIM_BENEFIT","claimStatus":"CLAIMED","startAt":` + itoa(start) + `,"endAt":` + itoa(end) + `,"benefit":{"amount":100}}`)))
			return
		}
		t.Error("claim must not be sent for an already-claimed campaign")
	})
	path := filepath.Join(t.TempDir(), "qoder-checkin.json")
	c, err := NewChecker(path, do)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	c.Check(context.Background(), Account{Provider: "qoder", ID: "x", Name: "acct", Token: "token"})
	c2, err := NewChecker(path, do)
	if err != nil {
		t.Fatalf("NewChecker reopen: %v", err)
	}
	if got := c2.Results(); len(got) != 1 || got[0].Outcome != OutcomeDone {
		t.Fatalf("reopened results = %+v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat ledger: %v", err)
	}
	if info.Mode().Perm() != 0600 && info.Mode().Perm() != 0644 {
		t.Errorf("ledger mode = %v", info.Mode().Perm())
	}
}
