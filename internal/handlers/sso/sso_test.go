package sso

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
)

func newTestRepo(t *testing.T) (*db.Repo, func()) {
	t.Helper()
	tmp, err := os.CreateTemp("", "sso_handlers_*.sqlite")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	tmp.Close()

	database, err := db.OpenDatabase(tmp.Name())
	if err != nil {
		os.Remove(tmp.Name())
		t.Fatalf("open database: %v", err)
	}
	cleanup := func() {
		database.Close()
		os.Remove(tmp.Name())
	}

	if _, err := database.Exec(`CREATE TABLE settings (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		data TEXT NOT NULL
	)`); err != nil {
		cleanup()
		t.Fatalf("create settings: %v", err)
	}
	return db.NewRepo(database), cleanup
}

func newTestRouter(repo *db.Repo) chi.Router {
	r := chi.NewRouter()
	h := NewHandler(repo)
	r.Post("/api/auth/oidc/test", h.HandleOidcTest)
	r.Post("/api/auth/saml/test", h.HandleSamlTest)
	r.Get("/api/auth/saml/metadata", h.HandleSamlMetadata)
	return r
}

// selfSignedPEM returns a PEM-encoded certificate the SAML validator accepts.
func selfSignedPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-idp"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestHandleSamlTest_ValidatesConfiguration(t *testing.T) {
	repo, cleanup := newTestRepo(t)
	defer cleanup()
	router := newTestRouter(repo)

	// Missing entry point → 400 with Next's flat error body.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/saml/test", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing entry point, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "samlEntryPoint") {
		t.Errorf("expected entry point error, got %s", rec.Body.String())
	}

	// Malformed URL → 400.
	req = httptest.NewRequest(http.MethodPost, "/api/auth/saml/test",
		strings.NewReader(`{"samlEntryPoint":"not-a-url","samlCert":"AAAA"}`))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for malformed URL, got %d: %s", rec.Code, rec.Body.String())
	}

	// Valid URL but garbage certificate → 400.
	req = httptest.NewRequest(http.MethodPost, "/api/auth/saml/test",
		strings.NewReader(`{"samlEntryPoint":"https://idp.example.com/sso","samlCert":"not-a-cert"}`))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid certificate, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Certificate") {
		t.Errorf("expected certificate error, got %s", rec.Body.String())
	}

	// Complete configuration passes.
	cert := selfSignedPEM(t)
	body := `{"samlEntryPoint":"https://idp.example.com/sso","samlIssuer":"urn:9router:sp","samlCert":` + toJSONString(cert) + `}`
	req = httptest.NewRequest(http.MethodPost, "/api/auth/saml/test", strings.NewReader(body))
	req.Header.Set("Host", "localhost:20130")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid config, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"certValid":true`) {
		t.Errorf("expected certValid=true, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/api/auth/saml/acs") {
		t.Errorf("expected ACS URL in response, got %s", rec.Body.String())
	}
}

func TestHandleSamlMetadata_ServesServiceProviderXML(t *testing.T) {
	repo, cleanup := newTestRepo(t)
	defer cleanup()
	router := newTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/saml/metadata", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/xml" {
		t.Errorf("expected application/xml, got %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `entityID="urn:9router:sp"`) {
		t.Errorf("expected default issuer entityID, got %s", body)
	}
	if !strings.Contains(body, "/api/auth/saml/acs") {
		t.Errorf("expected ACS location, got %s", body)
	}
	if !strings.Contains(body, "md:EntityDescriptor") {
		t.Errorf("expected SAML entity descriptor, got %s", body)
	}
}

func TestHandleOidcTest_RequiresIssuer(t *testing.T) {
	repo, cleanup := newTestRepo(t)
	defer cleanup()
	router := newTestRouter(repo)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/oidc/test", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without issuer, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Issuer URL is required") {
		t.Errorf("expected issuer error, got %s", rec.Body.String())
	}
}

func TestFormatCertificate_AcceptsRawBase64(t *testing.T) {
	pemBody := selfSignedPEM(t)
	der := formatCertificate(pemBody)
	if len(der) == 0 {
		t.Fatal("expected DER bytes from PEM input")
	}
	if _, err := x509.ParseCertificate(der); err != nil {
		t.Fatalf("parsed certificate invalid: %v", err)
	}

	// Raw base64 (PEM markers stripped) should work too.
	raw := strings.Trim(strings.ReplaceAll(strings.Trim(pemBody, "\n"), "-----END CERTIFICATE-----", ""), "\n")
	raw = strings.TrimPrefix(raw, "-----BEGIN CERTIFICATE-----")
	if len(formatCertificate(raw)) == 0 {
		t.Error("expected DER bytes from raw base64 input")
	}
	if formatCertificate("") != nil {
		t.Error("empty certificate should return nil")
	}
}

func TestSSOUtils(t *testing.T) {
	if got := firstNonEmpty("", "  ", "hello", "world"); got != "hello" {
		t.Errorf("firstNonEmpty = %q, want hello", got)
	}
	if got := firstNonEmpty("", "   "); got != "" {
		t.Errorf("firstNonEmpty all empty = %q, want empty", got)
	}
	if got := xmlEscape("<hello & 'world' \"test\">"); !strings.Contains(got, "&lt;") || !strings.Contains(got, "&amp;") {
		t.Errorf("xmlEscape failed: %q", got)
	}

	w := httptest.NewRecorder()
	writeMetadataError(w, "something went wrong")
	if w.Code != http.StatusInternalServerError {
		t.Errorf("writeMetadataError status = %d, want 500", w.Code)
	}
	if !strings.Contains(w.Body.String(), "<Error>something went wrong</Error>") {
		t.Errorf("writeMetadataError body = %q", w.Body.String())
	}
}

// toJSONString renders a string as a JSON literal for hand-built bodies.
func toJSONString(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + replacer.Replace(s) + `"`
}

func TestOIDC_FetchDiscoveryAndProbeHonorContextCancel(t *testing.T) {
	newBlockingServer := func(t *testing.T) *httptest.Server {
		t.Helper()
		unblock := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-unblock:
			}
		}))
		t.Cleanup(func() {
			close(unblock)
			server.Close()
		})
		return server
	}

	t.Run("fetchDiscovery honors canceled context", func(t *testing.T) {
		server := newBlockingServer(t)
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()

		start := time.Now()
		_, err := fetchDiscovery(ctx, server.URL)
		if err == nil {
			t.Fatal("expected error from fetchDiscovery on canceled context")
		}
		if elapsed := time.Since(start); elapsed >= time.Second {
			t.Fatalf("fetchDiscovery ignored context deadline, took %v", elapsed)
		}
	})

	t.Run("probeClientSecret honors canceled context", func(t *testing.T) {
		server := newBlockingServer(t)
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()

		_, err := probeClientSecret(ctx, server.URL, "cid", "csec", "https://app.example/cb")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected context.DeadlineExceeded, got %v", err)
		}
	})
}
