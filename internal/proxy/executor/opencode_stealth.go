package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

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

const maxOpencodeStealthClients = 128

var (
	opencodeClientMu sync.Mutex
	opencodeClients  = make(map[string]*opencodeStealthClient, maxOpencodeStealthClients)
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
	return bunBoringSSLSpecFor(false), nil
}

// bunBoringSSLSpecFor builds the Bun ClientHello.
//
// referenceFingerprint selects the layout opencode2api measured against Bun
// 1.4.3's fetch (JA4 t13d1713h1), which differs from the baseline in three
// places:
//
//   - ALPN is HTTP/1.1 only. Bun's fetch speaks HTTP/1.1, so a ClientHello
//     offering h2 is not what it sends.
//   - The post-quantum hybrid X25519MLKEM768 leads SupportedCurves and is sent
//     as a key share, with CurveP521 dropped. Verified: utls materializes the
//     MLKEM share (1216 bytes) even though the spec carries no explicit bytes,
//     so listing the group is enough — see
//     TestBunSpec_MLKEMKeyShareIsMaterialized.
//   - PKCS1WithSHA1 is appended after PKCS1WithSHA512.
//
// It stays opt-in because the baseline is the layout this repository has run in
// production, and neither could be validated against a live 200 while this was
// written: the gate refused the probe egress on quota. Turning the reference
// layout on unverified would replace a known-good handshake with a guess.
func bunBoringSSLSpecFor(referenceFingerprint bool) tls.ClientHelloSpec {
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

	curves := []tls.CurveID{
		tls.X25519,
		tls.CurveP256,
		tls.CurveP384,
		tls.CurveP521,
	}
	keyShares := []tls.KeyShare{
		{Group: tls.X25519},
	}
	alpn := []string{"h2", "http/1.1"}
	signatureAlgorithms := []tls.SignatureScheme{
		tls.ECDSAWithP256AndSHA256,
		tls.PSSWithSHA256,
		tls.PKCS1WithSHA256,
		tls.ECDSAWithP384AndSHA384,
		tls.PSSWithSHA384,
		tls.PKCS1WithSHA384,
		tls.PSSWithSHA512,
		tls.PKCS1WithSHA512,
	}
	if referenceFingerprint {
		curves = []tls.CurveID{
			tls.X25519MLKEM768,
			tls.X25519,
			tls.CurveP256,
			tls.CurveP384,
		}
		keyShares = []tls.KeyShare{
			{Group: tls.X25519MLKEM768},
			{Group: tls.X25519},
		}
		alpn = []string{"http/1.1"}
		signatureAlgorithms = append(signatureAlgorithms, tls.PKCS1WithSHA1)
	}

	extensions := []tls.TLSExtension{
		&tls.SNIExtension{},
		&tls.ExtendedMasterSecretExtension{},
		&tls.RenegotiationInfoExtension{Renegotiation: tls.RenegotiateOnceAsClient},
		&tls.SupportedCurvesExtension{Curves: curves},
		&tls.SupportedPointsExtension{SupportedPoints: []byte{0}},
		&tls.SessionTicketExtension{},
		&tls.ALPNExtension{AlpnProtocols: alpn},
		&tls.StatusRequestExtension{},
		&tls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: signatureAlgorithms},
		&tls.SCTExtension{},
		&tls.KeyShareExtension{KeyShares: keyShares},
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
	}
}

// opencodeReferenceFingerprint reports whether the reference (opencode2api)
// Bun fingerprint is enabled via OPENCODE_V2_REFERENCE_FINGERPRINT=1.
//
// It is opt-in because the baseline is the handshake this repository has run in
// production while the reference layout could not be validated against a live
// 200. One escape hatch, one reason: the transport spec, the Chat body key
// order and the extra sigalg are all the same "match the reference byte for
// byte" decision, so a single flag flips them together and a single flag
// reverts them.
func opencodeReferenceFingerprint() bool {
	return os.Getenv("OPENCODE_V2_REFERENCE_FINGERPRINT") == "1"
}

// newOpencodeClientProfile builds a ClientProfile configured with Bun's BoringSSL
// TLS ClientHello and HTTP/2 settings.
func newOpencodeClientProfile() profiles.ClientProfile {
	reference := opencodeReferenceFingerprint()
	specFactory := bunBoringSSLSpec
	if reference {
		specFactory = func() (tls.ClientHelloSpec, error) {
			return bunBoringSSLSpecFor(true), nil
		}
	}
	// The HTTP/2 settings are inert under the reference layout — it negotiates
	// HTTP/1.1 only — but the positional NewClientProfile signature means
	// dropping them would be churn for no behaviour change.
	return profiles.NewClientProfile(
		tls.ClientHelloID{
			Client:      "bun",
			Version:     "1.4.2",
			SpecFactory: specFactory,
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
		tls_client.WithDialer(net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}),
		tls_client.WithClientProfile(newOpencodeClientProfile()),
		tls_client.WithNotFollowRedirects(),
	}
	if opencodeReferenceFingerprint() {
		// The ALPN list is what a fingerprint is computed from; this stops the
		// transport from negotiating h2 anyway if a server ignored it.
		options = append(options, tls_client.WithForceHttp1())
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
// A plain pool client carries a *http.Transport; the poolless client wraps a
// safe transport in a *FallbackTransport, so its Base — the leg the request
// actually travels — is the one to inspect. Missing either means the dial is
// ambient: the stealth client then builds without a proxy and Go's ambient
// egress never applies to it (it does not use net/http), which silently
// bypassed an operator's HTTP_PROXY/ALL_PROXY on the free lane.
func opencodeProxyFrom(reference *http.Client) string {
	if reference == nil {
		return ""
	}
	target, err := url.Parse("https://opencode.ai")
	if err != nil {
		return ""
	}
	return strings.TrimRight(proxy.ProxyURLForTarget(reference, target), "/")
}

func getOpencodeStealthClient(reference *http.Client) (*opencodeStealthClient, error) {
	// The profile is flag-dependent, so the flag belongs in the cache key: a
	// key of proxy URL alone would hand back a client built under the other
	// fingerprint for the process lifetime.
	proxyURL := opencodeProxyFrom(reference)
	cacheKey := proxyURL
	if opencodeReferenceFingerprint() {
		cacheKey = "reference\x00" + proxyURL
	}
	opencodeClientMu.Lock()
	defer opencodeClientMu.Unlock()
	if client, ok := opencodeClients[cacheKey]; ok {
		return client, nil
	}
	client, err := newOpencodeStealthClient(reference)
	if err != nil {
		return nil, err
	}
	if len(opencodeClients) >= maxOpencodeStealthClients {
		for victimKey, victim := range opencodeClients {
			if victim != nil && victim.client != nil {
				victim.client.CloseIdleConnections()
			}
			delete(opencodeClients, victimKey)
			break
		}
	}
	opencodeClients[cacheKey] = client
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
		ck := http.CanonicalHeaderKey(k)
		httpResp.Header[ck] = append(httpResp.Header[ck], v...)
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
