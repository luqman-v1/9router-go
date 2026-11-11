package executor

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/proxy"
)

// Measured live: a gated account receives the refusal as ordinary message
// content under a 200, with all-zero usage. Relaying it makes the client read
// upstream's upgrade notice as the model's answer.
const freePiGatedBody = `{"id":"chatcmpl-freepi-fbd657d7","object":"chat.completion","created":1794415030,"model":"deepseek-v4.1-flash","choices":[{"index":0,"message":{"role":"assistant","content":"free-pi is free because it shows ads, and this session hasn't loaded any. Requests stay paused until ads load in the official free-pi CLI. Update with ` + "`npx free-pi-cli@latest`" + ` and start a new session. Your free-pi-cli is out of date: update with ` + "`npx free-pi-cli@latest`" + `."},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`

func TestFreePiInbandGateError(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{
			name:    "the measured gate body is caught",
			body:    freePiGatedBody,
			wantErr: true,
		},
		{
			name: "paused wording alone is caught",
			body: `{"choices":[{"message":{"content":"Requests stay paused until ads load."}}],"usage":{"completion_tokens":0}}`,
			// "requests stay paused until ads load" is a marker.
			wantErr: true,
		},
		{
			// The guard that matters: a real answer about advertising must not be
			// mistaken for the gate.
			name:    "a genuine answer discussing ads is preserved",
			body:    `{"choices":[{"message":{"content":"free-pi is free because it shows ads to users."}}],"usage":{"prompt_tokens":12,"completion_tokens":9,"total_tokens":21}}`,
			wantErr: false,
		},
		{
			// Same wording, but tokens were spent producing it — not the gate.
			name:    "gate wording with real token spend is preserved",
			body:    `{"choices":[{"message":{"content":"free-pi is free because it shows ads"}}],"usage":{"completion_tokens":5}}`,
			wantErr: false,
		},
		{
			name:    "an ordinary completion is untouched",
			body:    `{"choices":[{"message":{"content":"hi"}}],"usage":{"completion_tokens":1}}`,
			wantErr: false,
		},
		{
			name:    "empty body",
			body:    "",
			wantErr: false,
		},
		{
			name:    "unparseable body",
			body:    `not json`,
			wantErr: false,
		},
		{
			// A gate arriving as an SSE chunk shape carries the text under delta.
			name:    "delta-shaped gate content is caught",
			body:    `{"choices":[{"delta":{"content":"Requests stay paused until ads load"}}],"usage":{"completion_tokens":0}}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := freePiInbandGateError([]byte(tt.body))
			if tt.wantErr != (err != nil) {
				t.Fatalf("freePiInbandGateError = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				return
			}
			var ue *proxy.UpstreamError
			if !errors.As(err, &ue) {
				t.Fatalf("error = %T, want an *UpstreamError", err)
			}
			if ue.StatusCode != http.StatusForbidden {
				t.Errorf("status = %d, want 403", ue.StatusCode)
			}
			// The client must be told what to do, not shown upstream's notice.
			if !strings.Contains(string(ue.Body), "free_pi_ad_gate") {
				t.Errorf("body does not name the condition: %s", ue.Body)
			}
		})
	}
}

// End to end through the executor: the gate must reach the client as a real
// error status, never as a 200 whose content is upstream's upgrade notice.
func TestFreePiForward_GateAs200BecomesAnError(t *testing.T) {
	upstream := quietGateServer(t, freePiGatedBody)
	token := freePiTestToken("user_gate_200")
	req := freePiTestRequest(t, upstream, token, nil, false)
	rec := httptest.NewRecorder()

	err := ForwardFreePi(rec, req)
	if err == nil {
		t.Fatalf("a 200 carrying the ad gate must not be treated as a completion (client got status %d)", rec.Code)
	}
	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %T, want an *UpstreamError", err)
	}
	if ue.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", ue.StatusCode)
	}
	if strings.Contains(string(ue.Body), "Requests stay paused") {
		t.Error("upstream's notice leaked into the error body verbatim")
	}
	if !strings.Contains(string(ue.Body), "free_pi_ad_gate") {
		t.Errorf("body does not name the condition: %s", ue.Body)
	}
}

// A genuine completion still passes through untouched.
func TestFreePiForward_RealCompletionIsNotAffected(t *testing.T) {
	upstream := quietGateServer(t, freePiJSONCompletion)
	token := freePiTestToken("user_gate_ok")
	req := freePiTestRequest(t, upstream, token, nil, false)
	rec := httptest.NewRecorder()

	if err := ForwardFreePi(rec, req); err != nil {
		t.Fatalf("ForwardFreePi: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"content":"hi"`) {
		t.Errorf("the real answer was not relayed: %s", rec.Body.String())
	}
}

// quietGateServer answers every chat request with body, suppressing the log
// noise a full fixture would add.
func quietGateServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A large legitimate completion must survive the peek. The gate check reads the
// body before jsonResponse sees it, so capping that read at the 1 MiB error
// limit would truncate a real answer into unparseable JSON — silently, because
// nothing downstream can tell a short body from a cut one.
func TestFreePiForward_LargeCompletionIsNotTruncated(t *testing.T) {
	// ~2 MiB of content: over UpstreamErrLimit, under MaxUpstreamBodyBytes.
	big := strings.Repeat("a", 2<<20)
	body := `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"m",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"` + big + `"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

	upstream := quietGateServer(t, body)
	req := freePiTestRequest(t, upstream, freePiTestToken("user_big"), nil, false)
	rec := httptest.NewRecorder()

	if err := ForwardFreePi(rec, req); err != nil {
		t.Fatalf("a 2 MiB completion must be relayed, got %v", err)
	}
	if got := rec.Body.Len(); got < 2<<20 {
		t.Errorf("relayed body = %d bytes, want at least 2 MiB: the peek truncated it", got)
	}
}
