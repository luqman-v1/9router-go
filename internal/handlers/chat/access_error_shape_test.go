package chat

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The 403 a client receives when its key may not use the requested model is a
// WIRE CONTRACT, and upstream decolua/9router defines it in exactly one place:
// errorResponse(FORBIDDEN, keyAccessDeniedMessage(requested)) ->
// buildErrorBody(403, ...) -> ERROR_TYPES[403] = {permission_error,
// insufficient_quota}. The port wrote its own literal envelope for this branch
// and drifted from that pair (and from the message, which upstream writes so
// it names the model the client asked for).
//
// These cases pin the whole body — the type, the code AND the exact message
// string — so the divergence cannot come back silently. A client that matches
// on the type, the code or the message breaks on a change, so the change has
// to be deliberate. The message is compared in full, not by substring: any
// wording that merely mentions the model would pass a substring check and
// still be a different body from upstream's.

func TestWriteModelAccessError_ForbiddenBodyMatchesUpstream(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		{
			name:  "openai model",
			model: "gpt-4o",
			want:  `This API key is not allowed to use model "gpt-4o"`,
		},
		{
			name:  "provider qualified model",
			model: "openai/gpt-4o",
			want:  `This API key is not allowed to use model "openai/gpt-4o"`,
		},
		{
			name:  "combo name",
			model: "smart-mix",
			want:  `This API key is not allowed to use model "smart-mix"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeModelAccessError(rec, &ErrModelNotAllowed{Model: tt.model})

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}

			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal error body: %v", err)
			}
			if len(body) != 1 {
				t.Fatalf("body = %v, want only the error envelope", body)
			}
			errObj, ok := body["error"].(map[string]any)
			if !ok {
				t.Fatalf("body = %v, want an error object", body)
			}
			// The envelope carries exactly these three fields, as upstream's
			// buildErrorBody does.
			if len(errObj) != 3 {
				t.Fatalf("error = %v, want exactly message, type and code", errObj)
			}
			if errObj["type"] != "permission_error" {
				t.Errorf("error.type = %v, want permission_error", errObj["type"])
			}
			if errObj["code"] != "insufficient_quota" {
				t.Errorf("error.code = %v, want insufficient_quota", errObj["code"])
			}
			msg, _ := errObj["message"].(string)
			if msg != tt.want {
				t.Errorf("error.message = %q, want %q", msg, tt.want)
			}
		})
	}
}
