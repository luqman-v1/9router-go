// Package sso implements the dashboard Single Sign-On settings endpoints that
// the profile page calls: OIDC discovery + client-secret probe, SAML
// configuration check, and SP metadata download. These mirror the Next
// dashboard's /api/auth/oidc/test, /api/auth/saml/test and
// /api/auth/saml/metadata routes.
package sso

import (
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

const (
	discoveryPath    = "/.well-known/openid-configuration"
	oidcCallback     = "/api/auth/oidc/callback"
	samlACSPath      = "/api/auth/saml/acs"
	samlMetadataPth  = "/api/auth/saml/metadata"
	defaultScopes    = "openid profile email"
	testCode         = "__oidc_test_invalid_code__"
	testCodeVerifier = "__oidc_test_invalid_verifier__"
)

// Handler serves the dashboard SSO settings routes.
type Handler struct {
	Repo *db.Repo
}

// NewHandler creates an SSO settings handler bound to the repo.
func NewHandler(repo *db.Repo) *Handler {
	return &Handler{Repo: repo}
}

// HandleOidcTest handles POST /api/auth/oidc/test: load the issuer's OIDC
// discovery document and, when a client secret is known, probe the token
// endpoint with a deliberately invalid code to see whether the secret is
// accepted.
func (h *Handler) HandleOidcTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IssuerURL    string `json:"issuerUrl"`
		ClientID     string `json:"clientId"`
		Scopes       string `json:"scopes"`
		ClientSecret string `json:"clientSecret"`
	}
	// Next treats a missing/invalid body as {}.
	decodeJSONBody(r, &body)

	settings, _ := h.Repo.GetSettingsRaw()
	if settings == nil {
		settings = map[string]any{}
	}

	issuerURL := firstNonEmpty(body.IssuerURL, handlerutil.GetString(settings, "oidcIssuerUrl"))
	clientID := firstNonEmpty(body.ClientID, handlerutil.GetString(settings, "oidcClientId"))
	scopes := firstNonEmpty(body.Scopes, handlerutil.GetString(settings, "oidcScopes"), defaultScopes)
	clientSecret := body.ClientSecret
	if clientSecret == "" {
		clientSecret = handlerutil.GetString(settings, "oidcClientSecret")
	}

	if issuerURL == "" {
		writeError(w, http.StatusBadRequest, "Issuer URL is required")
		return
	}
	if clientID == "" {
		writeError(w, http.StatusBadRequest, "Client ID is required")
		return
	}

	discovery, err := fetchDiscovery(issuerURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	redirectURI := publicOrigin(r) + oidcCallback
	probe, err := probeClientSecret(discovery.TokenEndpoint, clientID, clientSecret, redirectURI)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	result := map[string]any{
		"discoveryOk":           true,
		"clientSecretTested":    probe.tested,
		"clientSecretValid":     probe.valid,
		"issuerUrl":             issuerURL,
		"clientId":              clientID,
		"scopes":                scopes,
		"redirectUri":           redirectURI,
		"authorizationEndpoint": discovery.AuthorizationEndpoint,
		"tokenEndpoint":         discovery.TokenEndpoint,
		"jwksUri":               discovery.JwksURI,
		"message":               probe.message,
		"ok":                    true,
	}
	if probe.tested && probe.valid == false {
		result["ok"] = false
		result["error"] = "Discovery loaded, but the client secret is not valid: " + probe.message
	}
	handlerutil.WriteJSON(w, http.StatusOK, result)
}

// HandleSamlTest handles POST /api/auth/saml/test: validate the SSO URL, SP
// issuer and IdP certificate before the settings are saved.
func (h *Handler) HandleSamlTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SamlEntryPoint string `json:"samlEntryPoint"`
		SamlIssuer     string `json:"samlIssuer"`
		SamlCert       string `json:"samlCert"`
	}
	decodeJSONBody(r, &body)

	settings, _ := h.Repo.GetSettingsRaw()
	if settings == nil {
		settings = map[string]any{}
	}

	entryPoint := strings.TrimSpace(firstNonEmpty(body.SamlEntryPoint, handlerutil.GetString(settings, "samlEntryPoint")))
	issuer := strings.TrimSpace(firstNonEmpty(body.SamlIssuer, handlerutil.GetString(settings, "samlIssuer"), "urn:9router:sp"))
	cert := strings.TrimSpace(firstNonEmpty(body.SamlCert, handlerutil.GetString(settings, "samlCert")))

	if entryPoint == "" {
		writeError(w, http.StatusBadRequest, "Single Sign-On Service URL (samlEntryPoint) is required")
		return
	}
	if _, err := url.ParseRequestURI(entryPoint); err != nil {
		writeError(w, http.StatusBadRequest, "Single Sign-On Service URL must be a valid URL")
		return
	}
	if issuer == "" {
		writeError(w, http.StatusBadRequest, "SP Entity ID / Issuer (samlIssuer) is required")
		return
	}
	if cert == "" {
		writeError(w, http.StatusBadRequest, "IdP X.509 Certificate (samlCert) is required")
		return
	}
	if !validCertificate(cert) {
		writeError(w, http.StatusBadRequest, "Invalid IdP X.509 Certificate format")
		return
	}

	origin := publicOrigin(r)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"samlEntryPoint": entryPoint,
		"samlIssuer":     issuer,
		"certValid":      true,
		"acsUrl":         origin + samlACSPath,
		"metadataUrl":    origin + samlMetadataPth,
		"message":        "SAML 2.0 configuration verified successfully.",
	})
}

// HandleSamlMetadata handles GET /api/auth/saml/metadata: serve the SAML
// Service Provider metadata XML for the configured issuer and ACS URL.
func (h *Handler) HandleSamlMetadata(w http.ResponseWriter, r *http.Request) {
	settings, err := h.Repo.GetSettingsRaw()
	if err != nil {
		writeMetadataError(w, err.Error())
		return
	}
	issuer := handlerutil.GetString(settings, "samlIssuer")
	if issuer == "" {
		issuer = "urn:9router:sp"
	}
	origin := publicOrigin(r)
	acsURL := origin + samlACSPath

	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, spMetadataXML(issuer, acsURL))
}

// spMetadataXML renders the Service Provider metadata document IdPs expect.
func spMetadataXML(issuer, acsURL string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>`+"\n"+
		`<md:EntityDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" entityID="%s">`+"\n"+
		`  <md:SPSSODescriptor AuthnRequestsSigned="false" WantAssertionsSigned="false" protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">`+"\n"+
		`    <md:NameIDFormat>urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress</md:NameIDFormat>`+"\n"+
		`    <md:AssertionConsumerService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST" Location="%s" index="0"/>`+"\n"+
		`  </md:SPSSODescriptor>`+"\n"+
		`</md:EntityDescriptor>`+"\n",
		xmlEscape(issuer), xmlEscape(acsURL))
}

// discoveryDocument holds the OIDC discovery fields the dashboard displays.
type discoveryDocument struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JwksURI               string `json:"jwks_uri"`
}

// probeOutcome is the client-secret probe result (valid is tri-state like Next).
type probeOutcome struct {
	tested  bool
	valid   any
	message string
}

// fetchDiscovery loads <issuer>/.well-known/openid-configuration.
func fetchDiscovery(issuerURL string) (*discoveryDocument, error) {
	discoveryURL := strings.TrimRight(strings.TrimSpace(issuerURL), "/") + discoveryPath
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(discoveryURL)
	if err != nil {
		return nil, fmt.Errorf("Failed to load OIDC discovery document from %s", discoveryURL)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Failed to load OIDC discovery document from %s", discoveryURL)
	}
	var doc discoveryDocument
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("Failed to read OIDC discovery document from %s", discoveryURL)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("Failed to parse OIDC discovery document from %s", discoveryURL)
	}
	return &doc, nil
}

var (
	clientInvalidRe = regexp.MustCompile(`(?i)client.*(invalid|failed|mismatch)`)
	grantCodeRe     = regexp.MustCompile(`(?i)grant|code`)
)

// probeClientSecret exchanges a fake authorization code so the token endpoint
// validates the client credentials without consuming a real code.
func probeClientSecret(tokenEndpoint, clientID, clientSecret, redirectURI string) (*probeOutcome, error) {
	if clientSecret == "" {
		return &probeOutcome{
			tested:  false,
			valid:   nil,
			message: "No client secret was provided, so secret validation was skipped.",
		}, nil
	}
	if tokenEndpoint == "" {
		return &probeOutcome{
			tested:  true,
			valid:   nil,
			message: "Discovery document did not include a token endpoint.",
		}, nil
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("code", testCode)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", testCodeVerifier)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.PostForm(tokenEndpoint, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var data map[string]any
	_ = json.Unmarshal(mustRead(resp), &data)

	errorCode := strings.ToLower(handlerutil.GetString(data, "error"))
	errorDescription := firstNonEmpty(handlerutil.GetString(data, "error_description"), handlerutil.GetString(data, "error"))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return &probeOutcome{
			tested:  true,
			valid:   true,
			message: "Client secret was accepted by the token endpoint.",
		}, nil
	}
	switch {
	case errorCode == "invalid_client" || errorCode == "unauthorized_client" || clientInvalidRe.MatchString(errorDescription):
		message := errorDescription
		if message == "" {
			message = "Client secret is not valid."
		}
		return &probeOutcome{tested: true, valid: false, message: message}, nil
	case errorCode == "invalid_grant" || errorCode == "invalid_code" || grantCodeRe.MatchString(errorDescription):
		return &probeOutcome{
			tested:  true,
			valid:   true,
			message: "Client secret was accepted; the token exchange failed only because the test authorization code is invalid.",
		}, nil
	}

	message := errorDescription
	if message == "" {
		message = fmt.Sprintf("Token endpoint responded with %d", resp.StatusCode)
	}
	return &probeOutcome{tested: true, valid: nil, message: message}, nil
}

// validCertificate accepts a PEM block or raw base64 DER and reports whether it
// parses as an X.509 certificate.
func validCertificate(cert string) bool {
	der := formatCertificate(cert)
	if len(der) == 0 {
		return false
	}
	_, err := x509.ParseCertificate(der)
	return err == nil
}

// formatCertificate normalizes a certificate to DER bytes, mirroring Next's
// formatX509Certificate (strip headers, keep base64 body, then decode).
func formatCertificate(cert string) []byte {
	if strings.TrimSpace(cert) == "" {
		return nil
	}
	cleaned := strings.ReplaceAll(cert, "-----BEGIN CERTIFICATE-----", "")
	cleaned = strings.ReplaceAll(cleaned, "-----END CERTIFICATE-----", "")

	var b strings.Builder
	for _, r := range cleaned {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '+' || r == '/' || r == '=' {
			b.WriteRune(r)
		}
	}
	encoded := b.String()
	if encoded == "" {
		return nil
	}
	der, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// Tolerate PEM-style line breaks that survived the filter.
		der, err = base64.StdEncoding.DecodeString(strings.ReplaceAll(encoded, "\n", ""))
		if err != nil {
			return nil
		}
	}
	return der
}

// decodeJSONBody parses the request body, tolerating a missing or invalid body
// the way Next's body.json().catch(() => ({})) does.
func decodeJSONBody(r *http.Request, dst any) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return
	}
	_ = json.Unmarshal(body, dst)
}

// mustRead returns the raw response body bytes, empty on failure.
func mustRead(resp *http.Response) []byte {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	return body
}

// publicOrigin resolves the externally visible origin, honoring BASE_URL and
// forwarded headers the same way the Next dashboard does.
func publicOrigin(r *http.Request) string {
	if base := strings.TrimSpace(os.Getenv("BASE_URL")); base != "" {
		return strings.TrimRight(base, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		if p := strings.ToLower(strings.Split(proto, ",")[0]); p == "https" || p == "http" {
			scheme = p
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

// firstNonEmpty returns the first non-blank value.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// writeError answers with Next's flat { error: "message" } shape.
func writeError(w http.ResponseWriter, status int, message string) {
	handlerutil.WriteJSON(w, status, map[string]any{"error": message})
}

// writeMetadataError answers with Next's XML error document.
func writeMetadataError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprintf(w, `<?xml version="1.0"?><Error>%s</Error>`, xmlEscape(message))
}

// xmlEscape escapes XML text nodes/attributes.
func xmlEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
