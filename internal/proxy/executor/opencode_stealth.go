package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	fhttp "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/http2"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	tls "github.com/bogdanfinn/utls"
)

// opencodeStealthClient dials opencode.ai with the Bun/BoringSSL TLS fingerprint
// and preserved header order, rather than Go's standard net/http stack.
//
// Why this exists:
// Upstream OpenCode CLI compiles as a Bun binary (built on BoringSSL). The
// free-tier inference gate verifies the TLS ClientHello fingerprint (17 cipher
// suites, 12 extensions in exact order) and checks header ordering. Requests
// from Go's net/http (or Node.js) are rejected with 403 FreeTierError.
type opencodeStealthClient struct {
	client tls_client.HttpClient
}

var (
	opencodeClientMu sync.Mutex
	opencodeClients  = make(map[string]*opencodeStealthClient)
)

var opencodePseudoHeaderOrder = []string{":method", ":path", ":authority", ":scheme"}

var opencodeHeaderOrder = []string{
	"content-type",
	"authorization",
	"user-agent",
	"x-opencode-client",
	"x-opencode-session",
	"x-opencode-session-id",
	"x-session-affinity",
	"x-session-id",
	"x-opencode-project",
	"accept",
	"traceparent",
	"b3",
}

// bunBoringSSLSpec returns a ClientHelloSpec matching Bun's BoringSSL handshake
// (17 cipher suites and 12 extensions in exact baseline order).
func bunBoringSSLSpec() (tls.ClientHelloSpec, error) {
	ciphers := []uint16{
		tls.TLS_AES_128_GCM_SHA256,
		tls.TLS_AES_256_GCM_SHA384,
		tls.TLS_CHACHA20_POLY1305_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
		tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
		tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
		tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
		tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_RSA_WITH_AES_128_CBC_SHA,
		tls.TLS_RSA_WITH_AES_256_CBC_SHA,
	}

	extensions := []tls.TLSExtension{
		&tls.SNIExtension{},
		&tls.ExtendedMasterSecretExtension{},
		&tls.RenegotiationInfoExtension{Renegotiation: tls.RenegotiateOnceAsClient},
		&tls.SupportedCurvesExtension{Curves: []tls.CurveID{
			tls.X25519,
			tls.CurveP256,
			tls.CurveP384,
			tls.CurveP521,
		}},
		&tls.SupportedPointsExtension{SupportedPoints: []byte{0}},
		&tls.SessionTicketExtension{},
		&tls.ALPNExtension{AlpnProtocols: []string{"h2", "http/1.1"}},
		&tls.StatusRequestExtension{},
		&tls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []tls.SignatureScheme{
			tls.ECDSAWithP256AndSHA256,
			tls.PSSWithSHA256,
			tls.PKCS1WithSHA256,
			tls.ECDSAWithP384AndSHA384,
			tls.PSSWithSHA384,
			tls.PKCS1WithSHA384,
			tls.PSSWithSHA512,
			tls.PKCS1WithSHA512,
		}},
		&tls.SCTExtension{},
		&tls.KeyShareExtension{KeyShares: []tls.KeyShare{
			{Group: tls.X25519},
		}},
		&tls.PSKKeyExchangeModesExtension{Modes: []uint8{tls.PskModeDHE}},
		&tls.SupportedVersionsExtension{Versions: []uint16{
			tls.VersionTLS13,
			tls.VersionTLS12,
		}},
	}

	return tls.ClientHelloSpec{
		CipherSuites:       ciphers,
		CompressionMethods: []uint8{0},
		Extensions:         extensions,
	}, nil
}

// newOpencodeClientProfile builds a ClientProfile configured with Bun's BoringSSL
// TLS ClientHello and HTTP/2 settings.
func newOpencodeClientProfile() profiles.ClientProfile {
	return profiles.NewClientProfile(
		tls.ClientHelloID{
			Client:      "bun",
			Version:     "1.4.2",
			SpecFactory: bunBoringSSLSpec,
		},
		map[http2.SettingID]uint32{
			http2.SettingHeaderTableSize:      65536,
			http2.SettingEnablePush:           0,
			http2.SettingMaxConcurrentStreams: 1000,
			http2.SettingInitialWindowSize:    6291456,
			http2.SettingMaxFrameSize:         16384,
			http2.SettingMaxHeaderListSize:    262144,
		},
		[]http2.SettingID{
			http2.SettingHeaderTableSize,
			http2.SettingEnablePush,
			http2.SettingMaxConcurrentStreams,
			http2.SettingInitialWindowSize,
			http2.SettingMaxFrameSize,
			http2.SettingMaxHeaderListSize,
		},
		opencodePseudoHeaderOrder,
		15663105,
		nil,
		nil,
		0,
		false,
		nil,
		nil,
		0,
		nil,
		false,
	)
}

// newOpencodeStealthClient constructs an HttpClient with proxy support and no redirects.
func newOpencodeStealthClient(reference *http.Client) (*opencodeStealthClient, error) {
	options := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(120),
		tls_client.WithClientProfile(newOpencodeClientProfile()),
		tls_client.WithNotFollowRedirects(),
	}
	if proxyURL := opencodeProxyFrom(reference); proxyURL != "" {
		options = append(options, tls_client.WithProxyUrl(strings.TrimRight(proxyURL, "/")))
	}
	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), options...)
	if err != nil {
		return nil, fmt.Errorf("opencode: build stealth TLS client: %w", err)
	}
	return &opencodeStealthClient{client: client}, nil
}

// opencodeProxyFrom recovers the proxy configured in reference.Transport.
func opencodeProxyFrom(reference *http.Client) string {
	if reference == nil {
		return ""
	}
	transport, ok := reference.Transport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		return ""
	}
	target, err := url.Parse("https://opencode.ai")
	if err != nil {
		return ""
	}
	proxyURL, err := transport.Proxy(&http.Request{URL: target})
	if err != nil || proxyURL == nil {
		return ""
	}
	return strings.TrimRight(proxyURL.String(), "/")
}

func getOpencodeStealthClient(reference *http.Client) (*opencodeStealthClient, error) {
	proxyURL := opencodeProxyFrom(reference)
	opencodeClientMu.Lock()
	defer opencodeClientMu.Unlock()
	if client, ok := opencodeClients[proxyURL]; ok {
		return client, nil
	}
	client, err := newOpencodeStealthClient(reference)
	if err != nil {
		return nil, err
	}
	opencodeClients[proxyURL] = client
	return client, nil
}

// generateOpenCodeTraceHeaders generates random W3C traceparent and b3 headers
// matching the shape the official OpenCode CLI emits.
func generateOpenCodeTraceHeaders() (traceparent, b3 string) {
	var traceIDBytes [16]byte
	var spanIDBytes [8]byte
	_, _ = rand.Read(traceIDBytes[:])
	_, _ = rand.Read(spanIDBytes[:])
	traceID := hex.EncodeToString(traceIDBytes[:])
	spanID := hex.EncodeToString(spanIDBytes[:])
	return fmt.Sprintf("00-%s-%s-01", traceID, spanID), fmt.Sprintf("%s-%s-1", traceID, spanID)
}

// forward issues a POST request through the stealth client and converts the
// response to *http.Response so callers can consume it with standard interfaces.
func (s *opencodeStealthClient) forward(ctx context.Context, targetURL string, headers map[string]string, body []byte) (*http.Response, error) {
	fReq, err := fhttp.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for k, v := range headers {
		fReq.Header.Set(k, v)
	}
	if fReq.Header.Get("traceparent") == "" {
		tp, b3 := generateOpenCodeTraceHeaders()
		fReq.Header.Set("traceparent", tp)
		fReq.Header.Set("b3", b3)
	}
	fReq.Header[fhttp.HeaderOrderKey] = opencodeHeaderOrder

	resp, err := s.client.Do(fReq)
	if err != nil {
		return nil, err
	}
	httpResp := &http.Response{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Header:     make(http.Header, len(resp.Header)),
		Body:       resp.Body,
	}
	for k, v := range resp.Header {
		httpResp.Header[k] = v
	}
	return httpResp, nil
}

// forwardOpencodeRequest forwards an OpenCode request using the stealth TLS client
// when targeting https://opencode.ai directly, or falls back to plain HTTP when
// dialing a local mock server, an edge relay, or when OPENCODE_PLAIN_HTTP=1.
func forwardOpencodeRequest(ctx context.Context, client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool) (*http.Response, error) {
	if !opencodeUsesStealth(cfg) {
		return proxy.ForwardOpenAI(ctx, client, cfg, apiKey, body, isStream)
	}
	stealth, err := getOpencodeStealthClient(client)
	if err != nil {
		log.Warn("opencode", "stealth client error, falling back to plain HTTP", "error", err)
		return proxy.ForwardOpenAI(ctx, client, cfg, apiKey, body, isStream)
	}
	return stealth.forward(ctx, cfg.BaseURL, opencodeStealthHeaders(cfg, apiKey, isStream), body)
}

// forwardOpencodeRequestDirect replays a refused free-tier request over a
// direct egress (bypassing any assigned proxy): using plain HTTP when
// stealth TLS is disabled or targeting a local/mock URL, and stealth TLS without
// proxy when talking directly to opencode.ai.
func forwardOpencodeRequestDirect(ctx context.Context, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool) (*http.Response, error) {
	if !opencodeUsesStealth(cfg) {
		return proxy.ForwardOpenAI(ctx, nil, cfg, apiKey, body, isStream)
	}
	stealth, err := getOpencodeStealthClient(nil)
	if err != nil {
		return nil, err
	}
	return stealth.forward(ctx, cfg.BaseURL, opencodeStealthHeaders(cfg, apiKey, isStream), body)
}

// opencodeUsesStealth reports whether this lane talks to opencode.ai directly
// and so must use the stealth TLS stack.
func opencodeUsesStealth(cfg *providers.ProviderConfig) bool {
	return !opencodePlainHTTP() && strings.HasPrefix(cfg.BaseURL, "https://") && !isRelayConfig(cfg)
}

// opencodeStealthHeaders builds the request headers the stealth client sends.
// The direct replay reuses it so both attempts carry the same request.
func opencodeStealthHeaders(cfg *providers.ProviderConfig, apiKey string, isStream bool) map[string]string {
	headers := map[string]string{}
	if !cfg.NoAuth {
		headers["Authorization"] = "Bearer " + apiKey
	}
	for k, v := range cfg.StaticHeaders {
		headers[k] = v
	}
	if isStream {
		headers["Accept"] = "text/event-stream"
	} else {
		headers["Accept"] = "*/*"
	}
	return headers
}

// opencodePlainHTTP reports whether the stealth stack is turned off via environment variable.
func opencodePlainHTTP() bool {
	return os.Getenv("OPENCODE_PLAIN_HTTP") == "1"
}
