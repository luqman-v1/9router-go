package dashboard

import (
	"net/http"
	"strings"
	"testing"
)

const probeTosBanBody = `{
  "error": {
    "code": 403,
    "message": "This service has been disabled in this account for violation of Terms of Service. Please submit an appeal to continue using this product.",
    "status": "PERMISSION_DENIED",
    "details": [
      {
        "@type": "type.googleapis.com/google.rpc.ErrorInfo",
        "reason": "TOS_VIOLATION",
        "domain": "cloudcode-pa.googleapis.com",
        "metadata": {
          "uiMessage": "true",
          "appeal_url": "https://forms.gle/hGzM9MEUv2azZsrb9",
          "appeal_url_link_text": "Submit Appeal"
        }
      }
    ]
  }
}`

func TestTestConnection_AntigravityTOSBanCapturesAppealURL(t *testing.T) {
	repo, cleanup := setupProbeTestDB(t)
	defer cleanup()

	const id = "conn-antigravity-tos"
	if err := repo.CreateProviderConnectionFull(id, "antigravity", "oauth", "antigravity", nil,
		`{"accessToken":"tok","expiresAt":"2099-01-01T00:00:00Z"}`); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	stubProbeDo(t, func(method, rawURL string) (int, []byte) {
		if rawURL != cloudCodeAssistProbeURL {
			t.Errorf("probed %q, want the cloudcode assist endpoint", rawURL)
		}
		return http.StatusForbidden, []byte(probeTosBanBody)
	})

	status, body := postTestConnection(t, repo, id)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["valid"] != false {
		t.Fatalf("expected valid=false, got %v", body)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "[verification-url]") && !strings.Contains(msg, "disabled in this account") {
		t.Errorf("expected ban text in error, got %q", msg)
	}
	if strings.Contains(msg, "forms.gle") {
		t.Errorf("error must not contain the raw appeal link: %q", msg)
	}

	updated := readConnectionData(t, repo, id)
	if updated["testStatus"] != "error" {
		t.Errorf("testStatus = %v, want error", updated["testStatus"])
	}
	psd, _ := updated["providerSpecificData"].(map[string]any)
	if psd == nil {
		t.Fatalf("providerSpecificData missing: %v", updated)
	}
	if got, _ := psd["verificationUrl"].(string); got != "https://forms.gle/hGzM9MEUv2azZsrb9" {
		t.Errorf("verificationUrl = %q, want the appeal link", got)
	}
	lastErr, _ := updated["lastError"].(string)
	if strings.Contains(lastErr, "forms.gle") {
		t.Errorf("lastError must not contain the raw appeal link: %q", lastErr)
	}
}

func TestTestConnection_AgyTOSBanCapturesAppealURL(t *testing.T) {
	repo, cleanup := setupProbeTestDB(t)
	defer cleanup()

	const id = "conn-agy-tos"
	if err := repo.CreateProviderConnectionFull(id, "agy", "oauth", "agy", nil,
		`{"accessToken":"tok","expiresAt":"2099-01-01T00:00:00Z"}`); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	stubProbeDo(t, func(method, rawURL string) (int, []byte) {
		if rawURL != cloudCodeAssistProbeURL {
			t.Errorf("probed %q, want the cloudcode assist endpoint", rawURL)
		}
		return http.StatusForbidden, []byte(probeTosBanBody)
	})

	status, body := postTestConnection(t, repo, id)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["valid"] != false {
		t.Fatalf("expected valid=false, got %v", body)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "[verification-url]") && !strings.Contains(msg, "disabled in this account") {
		t.Errorf("expected ban text in error, got %q", msg)
	}
	if strings.Contains(msg, "forms.gle") {
		t.Errorf("error must not contain the raw appeal link: %q", msg)
	}

	updated := readConnectionData(t, repo, id)
	if updated["testStatus"] != "error" {
		t.Errorf("testStatus = %v, want error", updated["testStatus"])
	}
	psd, _ := updated["providerSpecificData"].(map[string]any)
	if psd == nil {
		t.Fatalf("providerSpecificData missing: %v", updated)
	}
	if got, _ := psd["verificationUrl"].(string); got != "https://forms.gle/hGzM9MEUv2azZsrb9" {
		t.Errorf("verificationUrl = %q, want the appeal link", got)
	}
	lastErr, _ := updated["lastError"].(string)
	if strings.Contains(lastErr, "forms.gle") {
		t.Errorf("lastError must not contain the raw appeal link: %q", lastErr)
	}
}
