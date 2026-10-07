package providers

import "testing"

func TestIsModelDeprecation(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "410 with ModelDeprecated type is a deprecation",
			status: 410,
			body:   `{"error":{"type":"ModelDeprecated","message":"mimo-v2.5-free is retired","code":"model_deprecated"}}`,
			want:   true,
		},
		{
			name:   "410 naming a replacement only in the message is a deprecation",
			status: 410,
			body:   `{"error":{"message":"model has been deprecated, use mimo-v2.6-flash-free instead"}}`,
			want:   true,
		},
		{
			name:   "410 with no payload at all is still a Gone",
			status: 410,
			body:   "",
			want:   true,
		},
		{
			name:   "410 with a non-JSON body is a Gone",
			status: 410,
			body:   "Gone",
			want:   true,
		},
		{
			// The 410 that matters most: an expired Freebuff session or OAuth
			// device code is a 410 too, and badging the model for it would
			// blacklist a model that is serving fine.
			name:   "410 for an expired session is not a deprecation",
			status: 410,
			body:   `{"status":"session_expired","message":"session expired, re-claim it"}`,
			want:   false,
		},
		{
			name:   "410 for an expired device code is not a deprecation",
			status: 410,
			body:   `{"error":"expired_token","message":"authorization code expired"}`,
			want:   false,
		},
		{
			// The same words on a different status must not badge the model:
			// a 404 is a wrong model id in the request, not a retirement.
			name:   "404 ModelDeprecated is not a deprecation",
			status: 404,
			body:   `{"error":{"type":"ModelDeprecated","message":"gone"}}`,
			want:   false,
		},
		{
			name:   "400 deprecation wording is not a deprecation",
			status: 400,
			body:   `{"error":{"message":"model is deprecated"}}`,
			want:   false,
		},
		{
			name:   "410 with an unrelated error object is not a deprecation",
			status: 410,
			body:   `{"error":{"type":"permission_error","message":"forbidden"}}`,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsModelDeprecation(tt.status, []byte(tt.body)); got != tt.want {
				t.Errorf("IsModelDeprecation(%d, %q) = %v, want %v", tt.status, tt.body, got, tt.want)
			}
		})
	}
}

func TestParseModelDeprecation(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantMessage   string
		wantSuccessor string
	}{
		{
			name:          "explicit successor field wins",
			body:          `{"error":{"type":"ModelDeprecated","message":"gone","successor":"mimo-v2.6-flash-free"}}`,
			wantMessage:   "gone",
			wantSuccessor: "mimo-v2.6-flash-free",
		},
		{
			name:          "snake_case replaced_by is read",
			body:          `{"error":{"type":"ModelDeprecated","replaced_by":"mimo-v2.6-flash-free"}}`,
			wantSuccessor: "mimo-v2.6-flash-free",
		},
		{
			name:          "successor recovered from the message prose",
			body:          `{"error":{"message":"mimo-v2.5-free is deprecated, use mimo-v2.6-flash-free instead."}}`,
			wantMessage:   "mimo-v2.5-free is deprecated, use mimo-v2.6-flash-free instead.",
			wantSuccessor: "mimo-v2.6-flash-free",
		},
		{
			// A message that merely names another model must not invent a
			// successor, or the UI would send the operator to the wrong model.
			name:        "no successor invented from a bare mention",
			body:        `{"error":{"message":"mimo-v2.5-free was retired alongside mimo-v2.5-pro"}}`,
			wantMessage: "mimo-v2.5-free was retired alongside mimo-v2.5-pro",
		},
		{
			name: "unreadable payload yields no detail",
			body: "not json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseModelDeprecation([]byte(tt.body))
			if got.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", got.Message, tt.wantMessage)
			}
			if got.Successor != tt.wantSuccessor {
				t.Errorf("Successor = %q, want %q", got.Successor, tt.wantSuccessor)
			}
		})
	}
}

func TestDeprecationKeys(t *testing.T) {
	// The key is what a combo entry, a connection row and a dashboard query
	// all have to agree on, so case must not split one model into three rows.
	if got, want := DeprecationKey("TokenHarbor", "MiMo-V2.5:Free"), "tokenharbor/mimo-v2.5:free"; got != want {
		t.Errorf("DeprecationKey = %q, want %q", got, want)
	}

	provider, model, ok := SplitDeprecationKey("tokenharbor/mimo-v2.5")
	if !ok || provider != "tokenharbor" || model != "mimo-v2.5" {
		t.Errorf("SplitDeprecationKey = (%q, %q, %v), want (tokenharbor, mimo-v2.5, true)", provider, model, ok)
	}

	if _, _, ok := SplitDeprecationKey("noslash"); ok {
		t.Error("SplitDeprecationKey(\"noslash\") reported ok, want false")
	}

	if got, ok := DeprecationKeyForEntry("th/mimo-v2.5:free"); !ok || got != "th/mimo-v2.5:free" {
		t.Errorf("DeprecationKeyForEntry = (%q, %v), want (th/mimo-v2.5:free, true)", got, ok)
	}

	// A bare model names no provider, so there is nothing to file it under.
	if _, ok := DeprecationKeyForEntry("mimo-v2.5"); ok {
		t.Error("DeprecationKeyForEntry for a bare model reported ok, want false")
	}
}
