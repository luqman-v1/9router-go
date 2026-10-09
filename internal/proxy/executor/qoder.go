package executor

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	json "encoding/json/v2"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/proxy"
)

const qoderRSAPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

func parseQoderPublicKey() (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(qoderRSAPublicKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("invalid PEM block")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not RSA public key")
	}
	return rsaPub, nil
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - (len(data) % blockSize)
	padText := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, padText...)
}

func aesEncryptCBCBase64(plaintext []byte, key []byte) (string, error) {
	if len(key) != 16 {
		return "", fmt.Errorf("aes key must be 16 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	padded := pkcs7Pad(plaintext, aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	mode := cipher.NewCBCEncrypter(block, key)
	mode.CryptBlocks(ciphertext, padded)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func rsaEncryptBase64(data []byte) (string, error) {
	pub, err := parseQoderPublicKey()
	if err != nil {
		return "", err
	}
	enc, err := rsa.EncryptPKCS1v15(rand.Reader, pub, data)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

func md5Hex(data []byte) string {
	h := md5.Sum(data)
	return hex.EncodeToString(h[:])
}

// qoderCosySigPath mirrors upstream shared/qoder/cosy.js computeSigPath: the
// request pathname with the leading "/algo" stripped.
func qoderCosySigPath(requestURL string) string {
	u, err := url.Parse(requestURL)
	if err != nil {
		return requestURL
	}
	pathname := u.Path
	if pathname == "" {
		pathname = requestURL
	}
	return strings.TrimPrefix(pathname, "/algo")
}

// QoderCosyCreds carries the identity a COSY signature is bound to. Mirrors
// upstream's `creds` argument (open-sse/shared/qoder/cosy.js buildCosyHeaders).
// UserID and MachineID come from the connection's providerSpecificData, not
// from the request: Qoder rejects a signature made with any other account.
type QoderCosyCreds struct {
	UserID    string
	AuthToken string
	Name      string
	Email     string
	MachineID string
}

// BuildQoderCosyHeaders signs a Qoder request for any COSY path (chat and the
// model list share the scheme). Exported for the dashboard key-validate probe.
func BuildQoderCosyHeaders(body []byte, requestURL string, creds QoderCosyCreds) (map[string]string, error) {
	return buildQoderCosyHeaders(body, requestURL, creds)
}

func buildQoderCosyHeaders(body []byte, requestURL string, creds QoderCosyCreds) (map[string]string, error) {
	// Upstream throws on a missing user id or token rather than inventing one:
	// a signature over a made-up account is rejected with
	// 403 {"code":"105","message":"Login expired"}, which reads as an expired
	// login instead of the real problem (a connection that never stored its
	// userId). Failing here names it.
	if creds.UserID == "" {
		return nil, fmt.Errorf("qoder: COSY signing needs the account user id — re-authorize this connection")
	}
	if creds.AuthToken == "" {
		return nil, fmt.Errorf("qoder: COSY signing needs an auth token")
	}
	userID, token := creds.UserID, creds.AuthToken

	aesKeyStr := uuid.New().String()[:16]
	aesKey := []byte(aesKeyStr)

	userInfoJSON, err := json.Marshal(map[string]string{
		"uid":                  userID,
		"security_oauth_token": token,
		"name":                 creds.Name,
		"aid":                  "",
		"email":                creds.Email,
	})
	if err != nil {
		return nil, err
	}

	infoB64, err := aesEncryptCBCBase64(userInfoJSON, aesKey)
	if err != nil {
		return nil, err
	}

	cosyKeyB64, err := rsaEncryptBase64(aesKey)
	if err != nil {
		return nil, err
	}

	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	reqID := uuid.New().String()

	payloadJSON, _ := json.Marshal(map[string]string{
		"version":     "v1",
		"requestId":   reqID,
		"info":        infoB64,
		"cosyVersion": "1.0.0",
		"ideVersion":  "",
	})
	payloadB64 := base64.StdEncoding.EncodeToString(payloadJSON)

	sigPath := qoderCosySigPath(requestURL)
	sigInput := fmt.Sprintf("%s\n%s\n%s\n%s\n%s", payloadB64, cosyKeyB64, timestamp, string(body), sigPath)
	sig := md5Hex([]byte(sigInput))

	// Upstream persists the machine UUID on the connection so every request
	// from one auth presents the same machine (cosy.js generateMachineId).
	machineID := creds.MachineID
	if machineID == "" {
		machineID = uuid.New().String()
	}
	bodyHash := md5Hex(body)
	bodyLength := fmt.Sprintf("%d", len(body))

	headers := map[string]string{
		"Authorization":          "Bearer COSY." + payloadB64 + "." + sig,
		"Cosy-Key":               cosyKeyB64,
		"Cosy-User":              userID,
		"Cosy-Date":              timestamp,
		"Cosy-Version":           "1.0.0",
		"Cosy-Machineid":         machineID,
		"Cosy-Machinetoken":      machineID,
		"Cosy-Machinetype":       "5",
		"Cosy-Machineos":         "x86_64_windows",
		"Cosy-Clienttype":        "5",
		"Cosy-Clientip":          "127.0.0.1",
		"Cosy-Bodyhash":          bodyHash,
		"Cosy-Bodylength":        bodyLength,
		"Cosy-Sigpath":           sigPath,
		"Cosy-Data-Policy":       "disagree",
		"Cosy-Organization-Id":   "",
		"Cosy-Organization-Tags": "",
		"Login-Version":          "v2",
		"X-Request-Id":           uuid.New().String(),
	}

	return headers, nil
}

// qoderCosyCreds reads the signing identity off the connection's
// providerSpecificData. Upstream does the same (qoderModels.js
// cosyCredsFromConnection), and the userId is not optional: it is the account
// the signature is verified against, so a connection without one cannot sign
// a usable request.
func qoderCosyCreds(psd map[string]any, token string) QoderCosyCreds {
	creds := QoderCosyCreds{AuthToken: token}
	if psd == nil {
		return creds
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if s, ok := psd[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		return ""
	}
	creds.UserID = pick("userId", "user_id", "id")
	creds.MachineID = pick("machineId", "machine_id")
	creds.Name = pick("name", "displayName")
	creds.Email = pick("email")
	return creds
}

// ForwardQoder handles requests for Qoder using COSY signing. The chat
// endpoint comes from the provider's own registry config, so Qoder and
// Qoder CN each talk to their own gateway (upstream registry transport.baseUrl)
// without either provider being aliased onto the other.
//
// The client's OpenAI body is not forwarded. Qoder's agent_chat_generation
// routes to an `agent_router` node that has no flow for an OpenAI request and
// answers 400 "flow nodes found for router agent_router" on every model, so
// the body has to be rewritten into Qoder's own payload shape, WAF-encoded,
// and signed — in that order, because the COSY signature covers the encoded
// bytes.
func ForwardQoder(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	region := qoderRegionForEndpoint(qoderEndpoint(req))
	creds := qoderCosyCreds(req.ConnData, req.APIKey)
	if err := qoderRejectMissingIdentity(creds); err != nil {
		return err
	}

	do := qoderHTTPDoer(req.Client)
	creds, err := qoderResolveCredential(ctx, do, creds, region)
	if err != nil {
		return err
	}
	built, err := qoderBuildRequest(ctx, do, req, creds, region)
	if err != nil {
		return err
	}

	plain, err := json.Marshal(built.Payload)
	if err != nil {
		return fmt.Errorf("ForwardQoder marshal payload: %w", err)
	}
	encoded := QoderEncodeBody(plain)
	targetURL := qoderChatURL(qoderEndpoint(req), region, creds.AuthToken)
	headers, err := buildQoderCosyHeaders(encoded, targetURL, creds)
	if err != nil {
		return fmt.Errorf("build Qoder COSY headers: %w", err)
	}
	qoderApplyChatHeaders(headers, built)

	resp, err := proxy.DoRequest(ctx, req.Client, http.MethodPost, targetURL, headers, encoded)
	if err != nil {
		return fmt.Errorf("ForwardQoder: %w", err)
	}
	defer resp.Body.Close()

	// Qoder wraps every chunk in a {statusCodeValue, body} envelope, so the
	// stream is peeled before it reaches the shared SSE folder — otherwise a
	// perfectly good upstream looks like "200 without a completion".
	rewoundR, rewoundW := io.Pipe()
	go func() {
		_ = rewoundW.CloseWithError(qoderSSERewrite(resp.Body, rewoundW, built.QoderKey))
	}()


	if req.IsStream {
		return execSSEStream(w, rewoundR, req)
	}
	return qoderNonStream(w, req, rewoundR)
}

// qoderEndpoint is the request's configured chat endpoint, falling back to the
// intl default when no provider config reached the executor.
func qoderEndpoint(req *Request) string {
	if req != nil && req.Config != nil && strings.TrimSpace(req.Config.BaseURL) != "" {
		return strings.TrimSpace(req.Config.BaseURL)
	}
	return ""
}

// qoderRegionForEndpoint derives the deployment from the endpoint host.
//
// The executor is registered for both qoder and qoder-cn, and the two must
// never be routed to each other's host (AGENTS.md section 3.A). The host is
// what actually decides: qoder-cn's gateway is gateway.qoder.com.cn, and a
// connection-level baseUrl override names its own host too. An override on
// either host is treated as a local test target and stays intl, which is
// harmless — only the endpoint and the model list host follow from it.
func qoderRegionForEndpoint(endpoint string) QoderRegion {
	if strings.Contains(endpoint, "qoder.com.cn") {
		return QoderRegionCN
	}
	return QoderRegionIntl
}

// qoderChatURL is the COSY-signed inference endpoint plus the Encode flag the
// server needs in order to decode the body we just obfuscated.
func qoderChatURL(endpoint string, region QoderRegion, token string) string {
	if endpoint == "" {
		endpoint = "https://" + qoderInferenceHost(region, token) + "/algo" + QoderChatSigPath
	}
	return endpoint + "?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
}

// qoderInferenceHost picks the chat host for a token kind. Job tokens (jt-…)
// are rejected by api3 with "Login expired" 403; the official qodercli serves
// them from api2. Qoder CN has no such split — one gateway serves every kind.
func qoderInferenceHost(region QoderRegion, token string) string {
	if region == QoderRegionCN {
		return "gateway.qoder.com.cn"
	}
	if strings.HasPrefix(token, qoderJobTokenPrefix) {
		return "api2.qoder.sh"
	}
	return "api3.qoder.sh"
}

// qoderRejectMissingIdentity names the two credential problems that used to
// surface as an opaque signing failure inside buildQoderCosyHeaders, and which
// upstream answers with a clean 401 so the dashboard nudges a re-auth instead
// of bubbling a 500.
func qoderRejectMissingIdentity(creds QoderCosyCreds) error {
	if creds.UserID == "" {
		return &proxy.UpstreamError{
			StatusCode: http.StatusUnauthorized,
			Body:       qoderErrorBody("qoder credential is missing userId; reconnect the account", http.StatusUnauthorized),
		}
	}
	if creds.AuthToken == "" {
		return &proxy.UpstreamError{
			StatusCode: http.StatusUnauthorized,
			Body:       qoderErrorBody("qoder credential is missing accessToken; reconnect the account", http.StatusUnauthorized),
		}
	}
	return nil
}

// qoderResolveCredential trades a Personal Access Token for a short-lived job
// token. A PAT cannot sign COSY requests, so chat must exchange it before
// either the model list or the POST will work. Device (dt-…) and job (jt-…)
// tokens pass through unchanged.
func qoderResolveCredential(ctx context.Context, do QoderDoer, creds QoderCosyCreds, region QoderRegion) (QoderCosyCreds, error) {
	if !IsQoderPAT(creds.AuthToken) {
		return creds, nil
	}
	endpoints := QoderEndpointsFor(string(region))
	token, err := ExchangeQoderJobToken(ctx, do, endpoints.JobTokenExchangeURL, creds.AuthToken)
	if err != nil {
		return creds, &proxy.UpstreamError{
			StatusCode: http.StatusUnauthorized,
			Body:       qoderErrorBody(fmt.Sprintf("qoder PAT exchange failed: %v", err), http.StatusUnauthorized),
		}
	}
	creds.AuthToken = token
	if creds.UserID == "" {
		creds.UserID = FetchQoderUserID(ctx, do, endpoints.UserInfoURL, token)
	}
	return creds, nil
}

// qoderHTTPDoer is the transport chat uses: the connection's proxy-bound
// client, through the shared retry/proxy-pool path.
func qoderHTTPDoer(client *http.Client) QoderDoer {
	if client == nil {
		client = http.DefaultClient
	}
	return func(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error) {
		ctx, cancel := context.WithTimeout(ctx, qoderCatalogTimeout)
		defer cancel()
		resp, err := proxy.DoRequest(ctx, client, method, rawURL, headers, body)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		out, readErr := io.ReadAll(io.LimitReader(resp.Body, qoderMaxCatalogBytes))
		if readErr != nil {
			return resp.StatusCode, nil, readErr
		}
		return resp.StatusCode, out, nil
	}
}

// qoderBuildRequest resolves model_config and maps the client's OpenAI body
// onto Qoder's payload. Splitting the model key out first keeps the error
// message naming the model when the catalogue cannot answer.
func qoderBuildRequest(ctx context.Context, do QoderDoer, req *Request, creds QoderCosyCreds, region QoderRegion) (qoderBuiltChat, error) {
	modelKey, err := qoderRequestModelKey(req.Body)
	if err != nil {
		return qoderBuiltChat{}, err
	}
	modelConfig, err := qoderModelConfig(ctx, do, creds, region, modelKey)
	if err != nil {
		return qoderBuiltChat{}, err
	}
	return buildQoderChatRequest(req.Body, modelConfig, creds, time.Now().UnixMilli())
}

// qoderRequestModelKey reads the wire model id and reduces it to the key Qoder
// knows the model by.
func qoderRequestModelKey(body []byte) (string, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return "", fmt.Errorf("qoder: parse request body: %w", err)
	}
	model, _ := request["model"].(string)
	if key := qoderModelKey(model); key != "" {
		return key, nil
	}
	return "", fmt.Errorf("qoder: request carries no model")
}

// qoderApplyChatHeaders adds the non-COSY headers Qoder expects. Accept
// matters because the endpoint only ever streams; identity encoding matters
// because gzip makes the CDN re-validate the signature.
func qoderApplyChatHeaders(headers map[string]string, built qoderBuiltChat) {
	headers["Content-Type"] = "application/json"
	headers["Accept"] = "text/event-stream"
	headers["Cache-Control"] = "no-cache"
	headers["X-Model-Key"] = built.QoderKey
	headers["X-Model-Source"] = qoderModelSource(built)
	headers["Accept-Encoding"] = "identity"
}

// qoderModelSource is the catalogue entry's `source`, defaulting to "system"
// the way the IDE does when the account published none.
func qoderModelSource(built qoderBuiltChat) string {
	if source, ok := built.ModelConfig["source"].(string); ok && source != "" {
		return source
	}
	return "system"
}

// maxQoderSSEBytes caps the buffered read. Qoder answers from an SSE endpoint
// whatever `stream` says, so a non-streaming request still has to hold the
// stream in memory; the cap keeps a slow or endless upstream from growing the
// heap without bound, matching the codex non-streaming path.
const maxQoderSSEBytes = 10 << 20

// qoderNonStream answers a `stream:false` request. jsonResponse already folds
// an event stream into one chat.completion; the step before it exists because
// Qoder reports failures *inside* that stream, and folding alone would turn a
// real upstream error into a successful empty completion (issue #41).
func qoderNonStream(w http.ResponseWriter, req *Request, upstream io.Reader) error {
	body, err := io.ReadAll(io.LimitReader(upstream, maxQoderSSEBytes))
	if err != nil {
		return fmt.Errorf("ForwardQoder read response: %w", err)
	}
	// The envelope is already unwrapped by this point, so a failure now
	// arrives as an error chunk the rewriter marked.
	if err := qoderRewoundStreamError(body); err != nil {
		return err
	}
	return jsonResponse(req.Ctx, w, bytes.NewReader(body), req.TranslateResp, req.ResponseBuf)
}

// qoderRewoundStreamError surfaces a failure Qoder reported inside its
// always-SSE response.
//
// A non-200 envelope becomes an error chunk carrying `qoder_error`, and the
// envelope is gone by the time this sees the stream — so the unwrapped chunk
// is what has to be recognized. Folding it as a completion would hand the
// client a 200 with the error text as the answer (issue #41).
func qoderRewoundStreamError(body []byte) error {
	for _, frame := range sseDataFrames(body) {
		var chunk map[string]any
		if err := json.Unmarshal(frame, &chunk); err != nil {
			continue
		}
		marker, ok := chunk["qoder_error"].(map[string]any)
		if !ok {
			continue
		}
		status, _ := marker["status"].(float64)
		message, _ := marker["message"].(string)
		if message == "" {
			message = "qoder upstream error"
		}
		return &proxy.UpstreamError{
			StatusCode: int(status),
			Body:       qoderErrorBody(message, int(status)),
		}
	}
	return nil
}

// qoderErrorBody wraps an upstream message in the OpenAI error envelope the
// gateway already speaks, so the client reads it instead of SSE text.
func qoderErrorBody(message string, status int) []byte {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "upstream_error",
			"code":    status,
		},
	})
	return body
}
