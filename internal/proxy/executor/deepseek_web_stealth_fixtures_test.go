package executor

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	json "encoding/json/v2"
)

// Attestation stub: a unit test that drives a whole turn must not reach the
// public attestation hosts.
//
// The two URLs are process-wide, so the stub is installed once and left in
// place: every executor test wants the same substitution, and a test that
// restored the real hosts would be the one that accidentally calls the internet.
var stubAttestationOnce sync.Once

// stubDeepSeekAttestation points both attestation hosts at one stub server and
// clears the cache, so a value an earlier test fetched cannot satisfy this one.
func stubDeepSeekAttestation(t *testing.T) {
	t.Helper()
	stubAttestationOnce.Do(func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("x-hif-ttl", "600")
			// The real hosts answer with different values; what matters here is
			// that each value is recognisable and reaches the request.
			value := "attestation-leim-value"
			if strings.HasSuffix(r.URL.Path, "/dliq") {
				value = "attestation-dliq-value"
			}
			_ = json.MarshalWrite(w, map[string]any{
				"code": 0,
				"data": map[string]any{"biz_data": map[string]any{"value": value}},
			})
		}))
		deepSeekHifLeimURL = server.URL + "/leim"
		deepSeekHifDliqURL = server.URL + "/dliq"
	})

	deepSeekHif.mu.Lock()
	deepSeekHif.values = map[string]deepSeekHifEntry{}
	deepSeekHif.mu.Unlock()

	deepSeekTokens.drop("user-token-1")
}

// deepSeekTurnFrames builds a completion answer carrying the upstream's own
// message id and a FINISHED status, which is what a persisted session needs in
// order to be continued by the next turn.
func deepSeekTurnFrames(messageID int64, text string) string {
	return fmt.Sprintf(`data: {"v":{"response":{"message_id":%d,"fragments":[{"type":"RESPONSE","content":%s}]}}}`+"\n\n",
		messageID, mustJSONString(text)) +
		`data: {"p":"response/status","v":"FINISHED"}` + "\n\n"
}

// deepSeekMuteBody is the account-level enforcement a muted account gets: HTTP
// 200 and a plain JSON document with no stream at all.
func deepSeekMuteBody() string {
	return fmt.Sprintf(`{"code":0,"data":{"biz_code":5,"biz_msg":"user is muted",`+
		`"biz_data":{"is_muted":1,"mute_until":%d}}}`, time.Now().Add(72*time.Hour).Unix())
}

// deepSeekRateLimitHintFrame is the in-stream burst-limit refusal: an error
// payload with no fragments and no FINISHED status.
func deepSeekRateLimitHintFrame() string {
	return `data: {"type":"error","content":"消息发送过于频繁，请稍后重试","finish_reason":"rate_limit_reached"}` + "\n\n"
}

// deepSeekClientRejectedToastFrame is the version-refusal toast: the same error
// shape as a rate limit, but the account is not at fault.
func deepSeekClientRejectedToastFrame() string {
	return `data: {"type":"error","content":"update to the latest version","finish_reason":"unsupported_client_by_model"}` + "\n\n"
}
