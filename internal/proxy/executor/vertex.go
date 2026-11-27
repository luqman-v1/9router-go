package executor

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// Vertex AI.
//
// The registry entries for vertex and vertex-partner carried a base URL and
// nothing else — no executor, no format. Every request POSTed an OpenAI body at
// https://aiplatform.googleapis.com/v1, which serves neither lane.
//
// Two auth shapes reach this host, and they put the credential in different
// places:
//
//   - a Service Account JSON key (the `apiKey` field holds the JSON) mints a
//     Bearer token by signing a JWT assertion with the SA's RS256 key;
//   - a raw API key rides in the query string (`?key=`) and never in a header.
//
// The URL differs too: Gemini models need the project-scoped
// /publishers/google/models path, while partner models (Llama, Mistral, GLM,
// DeepSeek, Qwen) use the OpenAI-compatible
// /endpoints/openapi/chat/completions path and require a project.

const (
	vertexGoogleTokenURL = "https://oauth2.googleapis.com/token"
	vertexCloudScope     = "https://www.googleapis.com/auth/cloud-platform"

	// vertexDefaultAPIVersion is the Vertex Gemini path version every existing
	// connection expects. A connection may opt into v1beta1 through its data
	// (`vertexApiVersion`) for surfaces Vertex only serves there — Imagen,
	// newer Gemini features. Upstream: DefaultAPIVersion in
	// relay/channel/vertex/url_builder.go.
	vertexDefaultAPIVersion = "v1"

	// vertexTokenSkew re-mints slightly before expiry so a request is never
	// sent with a token that dies in flight.
	vertexTokenSkew = 5 * time.Minute
)

// vertexServiceAccount is the subset of a GCP service-account key needed to
// mint a token.
type vertexServiceAccount struct {
	Type        string `json:"type"`
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
}

// vertexADC is a `gcloud auth application-default login` credential.
type vertexADC struct {
	Type         string `json:"type"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
	QuotaProject string `json:"quota_project_id"`
}

type vertexCachedToken struct {
	token     string
	expiresAt time.Time
}

var (
	vertexTokenMu    sync.Mutex
	vertexTokenCache = map[string]vertexCachedToken{}
)

// parseVertexServiceAccount returns the SA key when the credential is one.
func parseVertexServiceAccount(apiKey string) *vertexServiceAccount {
	if !strings.HasPrefix(strings.TrimSpace(apiKey), "{") {
		return nil
	}
	var sa vertexServiceAccount
	if json.Unmarshal([]byte(apiKey), &sa) != nil {
		return nil
	}
	if sa.Type != "service_account" || sa.ClientEmail == "" || sa.PrivateKey == "" || sa.ProjectID == "" {
		return nil
	}
	return &sa
}

// parseVertexADC returns the ADC credential when the key is one.
func parseVertexADC(apiKey string) *vertexADC {
	if !strings.HasPrefix(strings.TrimSpace(apiKey), "{") {
		return nil
	}
	var adc vertexADC
	if json.Unmarshal([]byte(apiKey), &adc) != nil {
		return nil
	}
	if adc.Type != "authorized_user" || adc.RefreshToken == "" || adc.ClientID == "" {
		return nil
	}
	return &adc
}

// vertexTokenFor mints (or reuses) a Bearer token for a service account.
func vertexTokenFor(ctx context.Context, client *http.Client, sa *vertexServiceAccount) (string, error) {
	key := sa.ClientEmail
	vertexTokenMu.Lock()
	cached, ok := vertexTokenCache[key]
	vertexTokenMu.Unlock()
	if ok && time.Now().Add(vertexTokenSkew).Before(cached.expiresAt) {
		return cached.token, nil
	}

	assertion, err := vertexJWTAssertion(sa)
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	tok, expiresIn, err := vertexPostToken(ctx, client, form)
	if err != nil {
		return "", err
	}
	vertexTokenMu.Lock()
	vertexTokenCache[key] = vertexCachedToken{token: tok, expiresAt: time.Now().Add(time.Duration(expiresIn) * time.Second)}
	vertexTokenMu.Unlock()
	return tok, nil
}

// vertexADCRefresh exchanges an ADC refresh token for an access token.
func vertexADCRefresh(ctx context.Context, client *http.Client, adc *vertexADC) (string, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {adc.RefreshToken},
		"client_id":     {adc.ClientID},
	}
	if adc.ClientSecret != "" {
		form.Set("client_secret", adc.ClientSecret)
	}
	tok, _, err := vertexPostToken(ctx, client, form)
	return tok, err
}

func vertexPostToken(ctx context.Context, client *http.Client, form url.Values) (string, int, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, vertexGoogleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("vertex token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
	}
	if err := json.UnmarshalRead(resp.Body, &payload); err != nil {
		return "", 0, fmt.Errorf("vertex token: parse response: %w", err)
	}
	if payload.AccessToken == "" {
		return "", 0, fmt.Errorf("vertex token: %s", payload.Error)
	}
	if payload.ExpiresIn <= 0 {
		payload.ExpiresIn = 3600
	}
	return payload.AccessToken, payload.ExpiresIn, nil
}

// vertexJWTAssertion signs the RS256 assertion Google exchanges for a token.
func vertexJWTAssertion(sa *vertexServiceAccount) (string, error) {
	// The key arrives with literal \n from a JSON-in-JSON paste.
	pemText := strings.ReplaceAll(sa.PrivateKey, `\n`, "\n")
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return "", fmt.Errorf("vertex: service account private_key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// Older keys are PKCS#1; accept both rather than telling the operator
		// to re-export.
		if rsaKey, rsaErr := x509.ParsePKCS1PrivateKey(block.Bytes); rsaErr == nil {
			parsed = rsaKey
		} else {
			return "", fmt.Errorf("vertex: parse private_key: %w", err)
		}
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return "", fmt.Errorf("vertex: private_key is not an RSA key")
	}

	now := time.Now().Unix()
	sign := func(s string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(s))
	}
	header := sign(`{"alg":"RS256","typ":"JWT"}`)
	claims := fmt.Sprintf(`{"iss":%q,"scope":%q,"aud":%q,"iat":%d,"exp":%d}`,
		sa.ClientEmail, vertexCloudScope, vertexGoogleTokenURL, now, now+3600)
	unsigned := header + "." + sign(claims)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsaKey.Sign(nil, sum[:], crypto.SHA256)
	if err != nil {
		return "", fmt.Errorf("vertex: sign assertion: %w", err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ForwardVertex serves both vertex and vertex-partner.
//
// vertexModelURL builds the Vertex Gemini path for one model call. The
// publisher-scoped form is used whenever a project is known — both the OAuth
// path (which requires it) and the API-key path with a project set — and the
// project-less form is the API-key-without-project case, where Vertex serves
// the model at the bare publisher root.
//
// The version is the path segment: `v1` for every existing connection, and
// `v1beta1` for the surfaces Vertex only serves there (Imagen, newer Gemini
// features). It comes from the connection's `vertexApiVersion` setting, not a
// request field, because which versions a project can serve is a property of
// the credential. It is not the `apiVersion` key, which belongs to Azure.
func vertexModelURL(apiVersion, projectID, location, model, action, streamSuffix string) string {
	if projectID == "" {
		return fmt.Sprintf("https://aiplatform.googleapis.com/%s/publishers/google/models/%s:%s%s",
			apiVersion, model, action, streamSuffix)
	}
	return fmt.Sprintf("https://aiplatform.googleapis.com/%s/projects/%s/locations/%s/publishers/google/models/%s:%s%s",
		apiVersion, projectID, location, model, action, streamSuffix)
}

// vertexConnectionSettings is the per-connection routing configuration a
// Vertex request resolves before it builds a URL.
type vertexConnectionSettings struct {
	ProjectID  string
	Location   string
	APIVersion string
}

// readVertexConnectionSettings reads the operator's per-connection overrides.
// `vertexApiVersion` is the camelCase key every other providerSpecificData
// field uses; it defaults to v1 so a connection that never set it keeps
// reaching the path it always did.
func readVertexConnectionSettings(connData map[string]any) vertexConnectionSettings {
	out := vertexConnectionSettings{APIVersion: vertexDefaultAPIVersion}
	if connData == nil {
		return out
	}
	if s, ok := connData["projectId"].(string); ok {
		out.ProjectID = strings.TrimSpace(s)
	}
	if s, ok := connData["location"].(string); ok {
		out.Location = strings.TrimSpace(s)
	}
	if s, ok := connData["vertexApiVersion"].(string); ok && strings.TrimSpace(s) != "" {
		out.APIVersion = strings.TrimSpace(s)
	}
	return out
}

func ForwardVertex(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	sa := parseVertexServiceAccount(req.APIKey)
	adc := parseVertexADC(req.APIKey)

	settings := readVertexConnectionSettings(req.ConnData)
	projectID := settings.ProjectID
	location := settings.Location
	apiVersion := settings.APIVersion
	if sa != nil && projectID == "" {
		projectID = sa.ProjectID
	}
	if adc != nil && projectID == "" {
		projectID = adc.QuotaProject
	}

	bearer := ""
	rawKey := req.APIKey
	switch {
	case sa != nil:
		tok, err := vertexTokenFor(ctx, req.Client, sa)
		if err != nil {
			return fmt.Errorf("ForwardVertex: %w", err)
		}
		bearer, rawKey = tok, ""
	case adc != nil:
		tok, err := vertexADCRefresh(ctx, req.Client, adc)
		if err != nil {
			return fmt.Errorf("ForwardVertex: %w", err)
		}
		bearer, rawKey = tok, ""
	}

	model := vertexModel(req.ModelName)
	streamSuffix := ""
	if req.IsStream {
		streamSuffix = "?alt=sse"
	}

	var targetURL string
	var sendBody []byte
	if req.Provider == "vertex-partner" {
		// Partner models are the OpenAI-compatible surface, so the body needs
		// no translation and the project is mandatory. The version stays the
		// OpenAI-compatible endpoint's own: it is not the Gemini path this
		// setting exists to switch.
		if projectID == "" {
			return fmt.Errorf("ForwardVertex: vertex-partner requires a project id")
		}
		targetURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/endpoints/openapi/chat/completions", projectID)
		if rawKey != "" {
			targetURL += "?key=" + url.QueryEscape(rawKey)
		}
		sendBody = req.Body
	} else {
		geminiBody, err := translator.TranslateOpenAIToGeminiWithSession(req.Body, req.SessionID, req.APIKeyID)
		if err != nil {
			return fmt.Errorf("ForwardVertex: translate to Gemini: %w", err)
		}
		sendBody = geminiBody
		action := "generateContent"
		if req.IsStream {
			action = "streamGenerateContent"
		}
		if bearer != "" {
			// OAuth/SA tokens must use the project-scoped path or Vertex
			// answers RESOURCE_PROJECT_INVALID.
			if projectID == "" {
				return fmt.Errorf("ForwardVertex: an OAuth or service-account credential requires a project id")
			}
			if location == "" {
				location = "us-central1"
			}
		} else if location == "" {
			location = "global"
		}
		targetURL = vertexModelURL(apiVersion, projectID, location, model, action, streamSuffix)
		if bearer == "" && rawKey != "" {
			if strings.Contains(targetURL, "?") {
				targetURL += "&key=" + url.QueryEscape(rawKey)
			} else {
				targetURL += "?key=" + url.QueryEscape(rawKey)
			}
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader(string(sendBody)))
	if err != nil {
		return fmt.Errorf("ForwardVertex: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		httpReq.Header.Set("Authorization", "Bearer "+bearer)
	}
	if req.IsStream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	if req.IsStream && httpReq.Header.Get("Accept-Encoding") == "" {
		httpReq.Header.Set("Accept-Encoding", "identity")
	}
	// Operator Custom Headers are applied last so they win over the defaults
	// above, matching ForwardOpenAI's order.
	for k, v := range providers.ApplyHeaderOverrides(nil, req.Config.HeaderOverrides) {
		httpReq.Header.Set(k, v)
	}
	client := req.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("ForwardVertex: upstream request: %w", err)
	}
	proxy.UnwrapIdentityGzip(httpReq, resp)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, constants.UpstreamErrLimit))
		log.Warn("vertex", "upstream refused", "status", resp.StatusCode, "model", model)
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: body, Header: resp.Header}
	}
	// The response is Gemini-shaped for the Gemini lanes and OpenAI-shaped for
	// the partner lane; the caller's translation layer reads it accordingly.
	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, constants.UpstreamErrLimit))
	if err != nil {
		return fmt.Errorf("ForwardVertex: read response: %w", err)
	}
	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(body)
	return err
}

// vertexModel strips 9router's provider prefix and effort suffixes.
func vertexModel(model string) string {
	if _, after, ok := strings.Cut(model, "/"); ok {
		model = after
	}
	for _, suffix := range []string{"-thinking", "-agentic"} {
		model = strings.TrimSuffix(model, suffix)
	}
	if idx := strings.IndexByte(model, '('); idx > 0 {
		model = model[:idx]
	}
	return model
}
