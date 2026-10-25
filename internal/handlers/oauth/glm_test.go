package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The live init endpoint parses the bearer poll token as a hex flow
// identifier and answers anything else with 400 code 3004 "invalid_flow".
var glmHexPollToken = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestGLMDeviceFlow_StartAndPoll(t *testing.T) {
	const (
		planKey        = "key123.secret456"
		zcodeJWT       = "zcode-jwt"
		businessJWT    = "zai-business-jwt"
		zaiAccessToken = "zai-oauth-token"
		zaiRefresh     = "zai-refresh-token"
	)

	// Mock Z.ai / ZCode server
	var calls []string
	pollPending := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == "/api/v1/oauth/cli/init":
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || !glmHexPollToken.MatchString(token) {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 3004, "msg": "invalid_flow"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"authorize_url":     "https://chat.z.ai/api/oauth/authorize?x=1",
					"flow_id":           "flow-123",
					"poll_interval_sec": 2,
					"expires_at":        float64(time.Now().Add(5 * time.Minute).Unix()),
				},
			})

		case strings.HasPrefix(r.URL.Path, "/api/v1/oauth/cli/poll/"):
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				http.Error(w, `{"code":401,"msg":"missing poll token"}`, http.StatusUnauthorized)
				return
			}
			if pollPending {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{"status": "pending"},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"status":     "ready",
					"token":      zcodeJWT,
					"providerId": "zai",
					"user": map[string]any{
						"user_id": "u-1",
						"name":    "Feavy",
						"email":   "feavy@example.com",
					},
					"zai": map[string]any{
						"access_token":  zaiAccessToken,
						"refresh_token": zaiRefresh,
					},
				},
			})

		case r.URL.Path == "/api/auth/z/login":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["token"] != zaiAccessToken {
				http.Error(w, `{"code":400,"msg":"wrong token"}`, http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{"access_token": businessJWT},
			})

		case r.URL.Path == "/api/biz/customer/getCustomerInfo":
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+businessJWT {
				http.Error(w, `{"code":401,"msg":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{
					"organizations": []any{
						map[string]any{
							"organizationId":   "org-1",
							"organizationName": "默认机构",
							"projects": []any{
								map[string]any{
									"projectId":   "p-1",
									"projectName": "默认项目",
									"projectType": "1",
								},
							},
						},
					},
				},
			})

		case strings.HasSuffix(r.URL.Path, "/api_keys") && r.Method == http.MethodGet:
			// List keys: empty on first query
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": []any{},
			})

		case strings.HasSuffix(r.URL.Path, "/api_keys") && r.Method == http.MethodPost:
			// Create key
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{
					"apiKey": "key123",
					"name":   "zcode-api-key",
				},
			})

		case strings.HasSuffix(r.URL.Path, "/copy/key123"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{"secretKey": "secret456"},
			})

		default:
			http.Error(w, `{"code":500,"msg":"unexpected endpoint"}`, http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	// Redirect endpoints to the mock server
	oldInit := glmCLIInitURL
	oldPoll := glmCLIPollURL
	oldLogin := glmBusinessLoginURL
	oldBase := glmBizAPIBaseURL
	glmCLIInitURL = server.URL + "/api/v1/oauth/cli/init"
	glmCLIPollURL = server.URL + "/api/v1/oauth/cli/poll"
	glmBusinessLoginURL = server.URL + "/api/auth/z/login"
	glmBizAPIBaseURL = server.URL
	defer func() {
		glmCLIInitURL = oldInit
		glmCLIPollURL = oldPoll
		glmBusinessLoginURL = oldLogin
		glmBizAPIBaseURL = oldBase
	}()

	t.Run("StartReturnsAuthorizeURLAndSession", func(t *testing.T) {
		res, err := glmStart()
		if err != nil {
			t.Fatalf("glmStart failed: %v", err)
		}
		if res["device_code"] != "flow-123" {
			t.Errorf("device_code = %v, want flow-123", res["device_code"])
		}
		if res["verification_uri"] != "https://chat.z.ai/api/oauth/authorize?x=1" {
			t.Errorf("verification_uri = %v", res["verification_uri"])
		}
		session, _ := res["session"].(map[string]any)
		if strVal(session, "zcodePollToken") == "" {
			t.Errorf("session missing zcodePollToken")
		}
	})

	t.Run("PollTokenIsHexLikeTheCLI", func(t *testing.T) {
		res, err := glmStart()
		if err != nil {
			t.Fatalf("glmStart failed: %v", err)
		}
		session, _ := res["session"].(map[string]any)
		if token := strVal(session, "zcodePollToken"); !glmHexPollToken.MatchString(token) {
			t.Errorf("zcodePollToken = %q, want 64 lowercase hex chars", token)
		}
	})

	t.Run("PendingPollReturnsAuthorizationPending", func(t *testing.T) {
		pollPending = true
		session := map[string]any{"zcodePollToken": "dummy"}
		_, err := glmPoll(session, "flow-123")
		if err == nil || !strings.Contains(err.Error(), "authorization_pending") {
			t.Fatalf("expected authorization_pending, got %v", err)
		}
	})

	t.Run("ReadyDerivesCodingPlanKeyAndPsd", func(t *testing.T) {
		pollPending = false
		session := map[string]any{"zcodePollToken": "dummy"}
		toks, err := glmPoll(session, "flow-123")
		if err != nil {
			t.Fatalf("glmPoll failed: %v", err)
		}
		if toks.access != planKey {
			t.Errorf("access = %q, want %q", toks.access, planKey)
		}
		if toks.refresh != "" {
			t.Errorf("refresh = %q, want empty (no refresh grant upstream)", toks.refresh)
		}
		if toks.email != "feavy@example.com" {
			t.Errorf("email = %q, want feavy@example.com", toks.email)
		}
		if toks.name != "Feavy" {
			t.Errorf("name = %q, want Feavy", toks.name)
		}
		if toks.extra["authMethod"] != "cli_poll" {
			t.Errorf("authMethod = %v, want cli_poll", toks.extra["authMethod"])
		}
		if toks.extra["zcodeJwtToken"] != zcodeJWT {
			t.Errorf("zcodeJwtToken = %v", toks.extra["zcodeJwtToken"])
		}
		if toks.extra["zaiBusinessToken"] != businessJWT {
			t.Errorf("zaiBusinessToken = %v", toks.extra["zaiBusinessToken"])
		}
		if toks.extra["zaiRefreshToken"] != zaiRefresh {
			t.Errorf("zaiRefreshToken = %v", toks.extra["zaiRefreshToken"])
		}
	})

	t.Run("MissingPollTokenFailsCleanly", func(t *testing.T) {
		_, err := glmPoll(map[string]any{}, "flow-123")
		if err == nil || !strings.Contains(err.Error(), "access_denied") {
			t.Fatalf("expected access_denied, got %v", err)
		}
	})
}
