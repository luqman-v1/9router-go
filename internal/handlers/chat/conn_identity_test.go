package chat

import (
	"testing"
)

// A multi-account user reads the console to find out which account answered.
// The name only helps if the request reports the account it actually used, and
// the lookup must not run twice for one request.
func TestConnIdentityKVOr(t *testing.T) {
	tests := []struct {
		name   string
		params forwardRequestParams
		connID string
		want   map[string]string
		absent []string
	}{
		{
			name:   "carried name is used as-is",
			params: forwardRequestParams{ConnName: "Work Account"},
			connID: "conn-1",
			want:   map[string]string{"conn": "conn-1", "connName": "Work Account"},
			// A repo lookup would add the email; the carried name is enough and
			// the success path runs on every request.
			absent: []string{"email", "projectId"},
		},
		{
			name:   "carried email is kept too",
			params: forwardRequestParams{ConnName: "Work Account", ConnEmail: "ops@example.com"},
			connID: "conn-1",
			want: map[string]string{
				"conn":     "conn-1",
				"connName": "Work Account",
				"email":    "ops@example.com",
			},
		},
		{
			name:   "a connection with no identity still reports its id",
			params: forwardRequestParams{},
			connID: "noauth",
			want:   map[string]string{"conn": "noauth"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kv := (&ChatHandler{}).connIdentityKVOr(tt.params, tt.connID)
			got := map[string]string{}
			for i := 0; i+1 < len(kv); i += 2 {
				key, ok := kv[i].(string)
				if !ok {
					t.Fatalf("kv[%d] is not a string key: %v", i, kv)
				}
				got[key] = kv[i+1].(string)
			}
			for k, want := range tt.want {
				if got[k] != want {
					t.Errorf("kv[%q] = %q, want %q (full kv: %v)", k, got[k], want, kv)
				}
			}
			for _, k := range tt.absent {
				if v, ok := got[k]; ok {
					t.Errorf("kv[%q] = %q, want it absent: the lookup must not run when the name is carried", k, v)
				}
			}
		})
	}
}

func TestIdentityNames(t *testing.T) {
	kv := []any{"conn", "conn-1", "connName", "Work Account", "email", "ops@example.com", "projectId", "p-1"}
	name, email := identityNames(kv)
	if name != "Work Account" || email != "ops@example.com" {
		t.Errorf("name=%q email=%q, want the connName/email pairs", name, email)
	}
	if n, e := identityNames([]any{"conn", "x"}); n != "" || e != "" {
		t.Errorf("identity with no name/email = (%q, %q), want both empty", n, e)
	}
}

// The virtual no-auth connection and a real row reach the log through the same
// helpers, so a nil name must not panic.
func TestConnObjAccessorsAreNilSafe(t *testing.T) {
	if got := connObjName(nil); got != "" {
		t.Errorf("connObjName(nil) = %q, want empty", got)
	}
	if got := connObjEmail(nil); got != "" {
		t.Errorf("connObjEmail(nil) = %q, want empty", got)
	}
	if got := connObjName(&ProviderConnection{ID: "noauth"}); got != "" {
		t.Errorf("a connection with no name = %q, want empty", got)
	}
	if got := connObjName(&ProviderConnection{Name: new("Public")}); got != "Public" {
		t.Errorf("connObjName = %q, want Public", got)
	}
}
