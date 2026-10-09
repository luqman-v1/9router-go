package media

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// netlifyCall records one request the fake Netlify API received.
type netlifyCall struct {
	method string
	path   string
	query  string
	auth   string
	agent  string
	body   []byte
}

// netlifyFake is an httptest stand-in for api.netlify.com. Handlers register
// responses per "METHOD /path"; anything unmatched fails the test loudly,
// because a silent miss would let the digest-deploy flow look like it worked.
// The mutex guards calls and routes: the handler runs on the server's
// goroutine while the test reads the recorder after the response returns, and
// without it `go test -race` reports a data race rather than a failure.
type netlifyFake struct {
	mu     sync.Mutex
	srv    *httptest.Server
	calls  []netlifyCall
	routes map[string]func(w http.ResponseWriter, body []byte)
}

func newNetlifyFake(t *testing.T) *netlifyFake {
	t.Helper()
	f := &netlifyFake{routes: map[string]func(http.ResponseWriter, []byte){}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, netlifyCall{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			auth:   r.Header.Get("Authorization"),
			agent:  r.Header.Get("User-Agent"),
			body:   body,
		})
		h, ok := f.routes[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		if !ok {
			t.Errorf("unexpected Netlify call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		h(w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// useNetlifyFake points the relay helpers at the fake and shrinks the poll wait.
// Restored by cleanup, so no test can leak the override into another.
func useNetlifyFake(t *testing.T, f *netlifyFake) {
	t.Helper()
	oldAPI, oldInterval, oldAttempts := netlifyAPI, netlifyPollInterval, netlifyPollAttempts
	// The helpers build URLs as netlifyAPI + "/sites", so the fake root must
	// carry the /api/v1 prefix the production constant does — otherwise every
	// request lands on a path the test never registered.
	netlifyAPI = f.srv.URL + "/api/v1"
	netlifyPollInterval = time.Millisecond
	netlifyPollAttempts = 5
	t.Cleanup(func() {
		netlifyAPI, netlifyPollInterval, netlifyPollAttempts = oldAPI, oldInterval, oldAttempts
	})
}

// newNetlifyRepo builds a repo over the real core schema so the deploy handler
// inserts a pool through the same path production uses.
func newNetlifyRepo(t *testing.T) *db.Repo {
	t.Helper()
	conn, err := db.OpenDatabase(filepath.Join(t.TempDir(), "data.sqlite"))
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.EnsureCoreSchema(conn); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	return db.NewRepo(conn)
}

// ─── Bundle shape ───────────────────────────────────────────────────────

// TestNetlifyRelayBundleIsLambdaCompatibleCJS pins the two syntaxes a
// digest-deployed function cannot accept. Netlify runs no build step for a
// manual digest deploy, so the bundle is served verbatim by the
// Lambda-compatible runtime: `export default` is an ESM-only form and fails
// with Runtime.UserCodeSyntaxError (502). A returned stream object fails
// unmarshalling into the Lambda response struct (502), so the body must be a
// string — this is why the relay buffers instead of streaming like the
// Vercel/Cloudflare/Deno bundles do.
func TestNetlifyRelayBundleIsLambdaCompatibleCJS(t *testing.T) {
	if strings.Contains(netlifyRelayCode, "export ") {
		t.Error("relay bundle uses ESM syntax; a digest deploy serves it verbatim and Netlify rejects `export` with Runtime.UserCodeSyntaxError")
	}
	if !strings.Contains(netlifyRelayCode, "exports.handler") {
		t.Error("relay bundle must assign exports.handler; the Lambda runtime calls that export")
	}
	for _, want := range []string{"x-relay-target", "x-relay-path", "arrayBuffer", "isBase64Encoded"} {
		if !strings.Contains(netlifyRelayCode, want) {
			t.Errorf("relay bundle is missing %q", want)
		}
	}
}

// TestNetlifyRelayBundleRejectsMissingTargetHeader pins the one guard a caller
// can trip by accident: a request with no x-relay-target must answer 400
// rather than forwarding to `undefined/<path>`.
func TestNetlifyRelayBundleRejectsMissingTargetHeader(t *testing.T) {
	if !strings.Contains(netlifyRelayCode, `statusCode: 400`) {
		t.Error("relay bundle must return statusCode 400 when x-relay-target is absent")
	}
	if !strings.Contains(netlifyRelayCode, `statusCode: 400`) ||
		!strings.Contains(netlifyRelayCode, "Missing x-relay-target header") {
		t.Error("the 400 must name the missing header so the failure is diagnosable from the relay's own body")
	}
}

// TestBuildRelayFunctionZipIsAReadableZip pins that the hand-rolled stored zip
// is a real archive: archive/zip must parse it and extract relay.js with the
// exact bundle bytes. A malformed header would make Netlify answer 400 on the
// function upload, which is indistinguishable from a bad token at the UI.
func TestBuildRelayFunctionZipIsAReadableZip(t *testing.T) {
	zipBytes, sha := buildRelayFunctionZip()

	want := sha256.Sum256(zipBytes)
	if sha != hex.EncodeToString(want[:]) {
		t.Errorf("announced sha256 = %q, want the digest of the zip bytes %q", sha, hex.EncodeToString(want[:]))
	}
	if len(sha) != 64 {
		t.Errorf("sha256 length = %d, want 64 hex chars", len(sha))
	}

	reader, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatalf("archive/zip cannot parse the bundle: %v", err)
	}
	if len(reader.File) != 1 {
		t.Fatalf("bundle holds %d files, want exactly relay.js", len(reader.File))
	}
	f := reader.File[0]
	if f.Name != "relay.js" {
		t.Errorf("bundle entry name = %q, want relay.js", f.Name)
	}
	rc, err := f.Open()
	if err != nil {
		t.Fatalf("open bundle entry: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read bundle entry: %v", err)
	}
	if string(got) != netlifyRelayCode {
		t.Error("extracted bundle differs from the relay code that gets deployed")
	}
}

// TestBuildIndexFileDigest pins the index digest the deploy announces. Netlify
// rejects a deploy whose announced SHA does not match the uploaded bytes, and
// index.html is what makes the site root non-404.
func TestBuildIndexFileDigest(t *testing.T) {
	content, sha := buildIndexFile()
	if len(sha) != 40 {
		t.Errorf("index sha1 length = %d, want 40 hex chars", len(sha))
	}
	if !strings.Contains(string(content), netlifyFunctionPath) {
		t.Error("index.html must point operators at the function path, otherwise the site root is an unexplained blank page")
	}
}

// TestBuildRelayURL pins URL construction: a trailing slash on the site URL
// must not produce a double slash, which Netlify answers 404.
func TestBuildRelayURL(t *testing.T) {
	tests := []struct {
		name    string
		siteURL string
		want    string
	}{
		{name: "appends the function path", siteURL: "https://relay-x.netlify.app", want: "https://relay-x.netlify.app/.netlify/functions/relay"},
		{name: "trims a trailing slash", siteURL: "https://relay-x.netlify.app/", want: "https://relay-x.netlify.app/.netlify/functions/relay"},
		{name: "empty site url yields just the path", siteURL: "", want: "/.netlify/functions/relay"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildRelayURL(tt.siteURL); got != tt.want {
				t.Errorf("buildRelayURL(%q) = %q, want %q", tt.siteURL, got, tt.want)
			}
		})
	}
}

// ─── Site-name sanitisation ─────────────────────────────────────────────

// TestSanitizeNetlifySiteName pins the sanitiser, because a Netlify site name
// becomes a DNS subdomain: an underscore or an uppercase letter in the operator's
// input makes Netlify answer 422 with a message that reads like a quota problem
// rather than a naming problem.
func TestSanitizeNetlifySiteName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "lowercases and strips illegal characters", input: "Netlify-Relay!!", want: "netlify-relay"},
		{name: "collapses hyphen runs", input: "a---b", want: "a-b"},
		{name: "trims leading and trailing hyphens", input: "--x--", want: "x"},
		{name: "keeps digits", input: "relay-2024", want: "relay-2024"},
		{name: "spaces become hyphens", input: "my relay", want: "my-relay"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeNetlifySiteName(tt.input); got != tt.want {
				t.Errorf("sanitizeNetlifySiteName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestSanitizeNetlifySiteNameFallsBackToRandom covers the two inputs that
// sanitise away to nothing. Both must still yield a usable name, because the
// deploy cannot proceed with an empty one.
func TestSanitizeNetlifySiteNameFallsBackToRandom(t *testing.T) {
	for _, input := range []string{"", "   ", "!!!"} {
		got := sanitizeNetlifySiteName(input)
		if !strings.HasPrefix(got, "relay-") || len(got) <= len("relay-") {
			t.Errorf("sanitizeNetlifySiteName(%q) = %q, want a generated relay-* name", input, got)
		}
	}
}

// ─── Digest-deploy flow against the fake Netlify API ────────────────────

// TestNetlifyDeployRelayRoundTrip drives the whole flow against a fake API and
// pins the exact request sequence. The order is the contract Netlify's digest
// API imposes: announce digests, then upload only what it asked for, then wait
// for the deploy. The assertion also checks the announced digests match the
// real bundle bytes, so a digest drift between buildRelayFunctionZip and the
// upload cannot slip through.
func TestNetlifyDeployRelayRoundTrip(t *testing.T) {
	f := newNetlifyFake(t)
	useNetlifyFake(t, f)

	_, relaySHA := buildRelayFunctionZip()
	_, indexSHA := buildIndexFile()

	f.routes["POST /api/v1/sites"] = func(w http.ResponseWriter, body []byte) {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("site create body is not JSON: %v", err)
		}
		if req.Name != "netlify-relay" {
			t.Errorf("site name = %q, want the sanitised name", req.Name)
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": "site_1", "ssl_url": "https://relay-x.netlify.app"})
	}
	f.routes["POST /api/v1/sites/site_1/deploys"] = func(w http.ResponseWriter, body []byte) {
		var req struct {
			Files     map[string]string `json:"files"`
			Functions map[string]string `json:"functions"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("deploy body is not JSON: %v", err)
		}
		if req.Files["/index.html"] != indexSHA {
			t.Errorf("announced index digest = %q, want %q", req.Files["/index.html"], indexSHA)
		}
		if req.Functions["relay"] != relaySHA {
			t.Errorf("announced relay digest = %q, want %q", req.Functions["relay"], relaySHA)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":                 "deploy_1",
			"required":           []string{indexSHA},
			"required_functions": []string{relaySHA},
		})
	}
	f.routes["PUT /api/v1/deploys/deploy_1/files/index.html"] = func(w http.ResponseWriter, body []byte) {
		if string(body) != netlifyIndexHTML {
			t.Error("uploaded index.html differs from the bytes the digest was computed over")
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	}
	f.routes["PUT /api/v1/deploys/deploy_1/functions/relay"] = func(w http.ResponseWriter, body []byte) {
		if len(body) == 0 {
			t.Error("relay function uploaded empty")
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	}
	f.routes["GET /api/v1/deploys/deploy_1"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusOK, map[string]any{"id": "deploy_1", "state": "ready"})
	}

	got, err := netlifyDeployRelay(t.Context(), fakeHandlerClient(), "nfp_test", "netlify-relay")
	if err != nil {
		t.Fatalf("netlifyDeployRelay: %v", err)
	}
	if want := "https://relay-x.netlify.app/.netlify/functions/relay"; got != want {
		t.Errorf("relay URL = %q, want %q", got, want)
	}

	var seq []string
	for _, c := range f.calls {
		q := c.query
		if q != "" {
			q = "?" + q
		}
		seq = append(seq, c.method+" "+strings.TrimPrefix(c.path, "/api/v1")+q)
	}
	want := []string{
		"POST /sites",
		"POST /sites/site_1/deploys",
		"PUT /deploys/deploy_1/files/index.html",
		"PUT /deploys/deploy_1/functions/relay?runtime=js",
		"GET /deploys/deploy_1",
	}
	if !slices.Equal(seq, want) {
		t.Errorf("request sequence =\n%v\nwant\n%v", seq, want)
	}
	for _, c := range f.calls {
		if c.auth != "Bearer nfp_test" {
			t.Errorf("%s %s Authorization = %q, want the bearer token", c.method, c.path, c.auth)
		}
		if c.agent != netlifyRelayUserAgent {
			t.Errorf("%s %s User-Agent = %q, want %q", c.method, c.path, c.agent, netlifyRelayUserAgent)
		}
	}
}

// TestNetlifyDeployRelaySkipsAssetsNetlifyAlreadyHas pins the dedup path. A
// redeploy of an unchanged bundle must upload nothing: the whole point of the
// digest API is that an unchanged relay costs zero uploads.
func TestNetlifyDeployRelaySkipsAssetsNetlifyAlreadyHas(t *testing.T) {
	f := newNetlifyFake(t)
	useNetlifyFake(t, f)

	f.routes["POST /api/v1/sites"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusCreated, map[string]any{"id": "s1", "ssl_url": "https://a.netlify.app"})
	}
	f.routes["POST /api/v1/sites/s1/deploys"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusOK, map[string]any{"id": "d1", "required": []string{}, "required_functions": []string{}})
	}
	f.routes["GET /api/v1/deploys/d1"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusOK, map[string]any{"state": "ready"})
	}

	if _, err := netlifyDeployRelay(t.Context(), fakeHandlerClient(), "nfp_test", "a"); err != nil {
		t.Fatalf("netlifyDeployRelay: %v", err)
	}
	for _, c := range f.calls {
		if c.method == http.MethodPut {
			t.Errorf("uploaded %s %s even though Netlify required nothing", c.method, c.path)
		}
	}
}

// TestNetlifyDeployRelayPollsUntilReady covers the in-flight build: the relay
// must wait through a "building" state rather than reporting a relay URL for a
// deploy that is not serving yet. The first request carries no ready state, so
// a single-shot implementation would return early here.
func TestNetlifyDeployRelayPollsUntilReady(t *testing.T) {
	f := newNetlifyFake(t)
	useNetlifyFake(t, f)

	polls := 0
	f.routes["POST /api/v1/sites"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusCreated, map[string]any{"id": "s1", "ssl_url": "https://a.netlify.app"})
	}
	f.routes["POST /api/v1/sites/s1/deploys"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusOK, map[string]any{"id": "d1", "required": []string{}, "required_functions": []string{}})
	}
	f.routes["GET /api/v1/deploys/d1"] = func(w http.ResponseWriter, _ []byte) {
		polls++
		if polls < 3 {
			writeJSON(w, http.StatusOK, map[string]any{"state": "building"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"state": "ready"})
	}

	if _, err := netlifyDeployRelay(t.Context(), fakeHandlerClient(), "nfp_test", "a"); err != nil {
		t.Fatalf("netlifyDeployRelay: %v", err)
	}
	if polls < 3 {
		t.Errorf("polled %d times, want at least 3 before the deploy reported ready", polls)
	}
}

// TestNetlifyDeployRelaySurfacesFailedDeploy pins that a build Netlify rejected
// reports its own error_message. Surfacing the poll timeout instead would tell
// the operator to retry a deploy that can never succeed.
func TestNetlifyDeployRelaySurfacesFailedDeploy(t *testing.T) {
	f := newNetlifyFake(t)
	useNetlifyFake(t, f)

	f.routes["POST /api/v1/sites"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusCreated, map[string]any{"id": "s1", "ssl_url": "https://a.netlify.app"})
	}
	f.routes["POST /api/v1/sites/s1/deploys"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusOK, map[string]any{"id": "d1", "required": []string{}, "required_functions": []string{}})
	}
	f.routes["GET /api/v1/deploys/d1"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusOK, map[string]any{"state": "error", "error_message": "Function build failed"})
	}

	_, err := netlifyDeployRelay(t.Context(), fakeHandlerClient(), "nfp_test", "a")
	if err == nil {
		t.Fatal("a deploy in state error must fail, not return a relay URL")
	}
	if err.Error() != "Function build failed" {
		t.Errorf("error = %q, want Netlify's own message", err.Error())
	}
}

// TestNetlifyDeployRelayTimesOut pins the give-up path: a deploy stuck in
// "uploading" forever must not hang the dashboard request indefinitely.
func TestNetlifyDeployRelayTimesOut(t *testing.T) {
	f := newNetlifyFake(t)
	useNetlifyFake(t, f)
	netlifyPollAttempts = 2

	f.routes["POST /api/v1/sites"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusCreated, map[string]any{"id": "s1", "ssl_url": "https://a.netlify.app"})
	}
	f.routes["POST /api/v1/sites/s1/deploys"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusOK, map[string]any{"id": "d1", "required": []string{}, "required_functions": []string{}})
	}
	f.routes["GET /api/v1/deploys/d1"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusOK, map[string]any{"state": "uploading"})
	}

	_, err := netlifyDeployRelay(t.Context(), fakeHandlerClient(), "nfp_test", "a")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %v, want a deploy timeout", err)
	}
}

// TestNetlifyDeployRelayMapsTakenSiteName pins the 422 → 409 translation and
// its hint. Netlify answers 422 for a name that is already taken; passing that
// through raw gives the operator an unexplained "422" with no indication that
// the site name is the thing to change.
func TestNetlifyDeployRelayMapsTakenSiteName(t *testing.T) {
	f := newNetlifyFake(t)
	useNetlifyFake(t, f)

	f.routes["POST /api/v1/sites"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "name taken"})
	}

	_, err := netlifyDeployRelay(t.Context(), fakeHandlerClient(), "nfp_test", "taken")
	var apiErr *netlifyError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want a netlifyError carrying the upstream status", err)
	}
	if apiErr.status != http.StatusConflict {
		t.Errorf("status = %d, want 409 for a taken site name", apiErr.status)
	}
	if !strings.Contains(apiErr.message, "name taken") || !strings.Contains(apiErr.message, "different name") {
		t.Errorf("message = %q, want Netlify's reason plus the hint to pick a different name", apiErr.message)
	}
}

// TestNetlifyDeployRelayPropagatesUpstreamStatusAndMessage pins that a
// non-422 API rejection keeps Netlify's own status and message. Collapsing
// every upstream failure into one generic 500 would hide, say, an expired token.
func TestNetlifyDeployRelayPropagatesUpstreamStatusAndMessage(t *testing.T) {
	f := newNetlifyFake(t)
	useNetlifyFake(t, f)

	f.routes["POST /api/v1/sites"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "Access Denied"})
	}

	_, err := netlifyDeployRelay(t.Context(), fakeHandlerClient(), "nfp_test", "taken")
	var apiErr *netlifyError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want a netlifyError", err)
	}
	if apiErr.status != http.StatusUnauthorized || apiErr.message != "Access Denied" {
		t.Errorf("got status %d message %q, want 401 / \"Access Denied\"", apiErr.status, apiErr.message)
	}
}

// TestNetlifyDeployRelayReportsMissingSiteURL pins the malformed-success path:
// a 2xx with no id or no URL would otherwise build a pool row pointing at
// "/.netlify/functions/relay".
func TestNetlifyDeployRelayReportsMissingSiteURL(t *testing.T) {
	f := newNetlifyFake(t)
	useNetlifyFake(t, f)

	f.routes["POST /api/v1/sites"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusCreated, map[string]any{"id": "s1"})
	}

	_, err := netlifyDeployRelay(t.Context(), fakeHandlerClient(), "nfp_test", "a")
	var apiErr *netlifyError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want a netlifyError", err)
	}
	if apiErr.status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", apiErr.status)
	}
}

// TestNetlifyDeployRelaySurfacesUploadFailure pins that a rejected function
// upload fails the deploy instead of returning a relay URL for a deploy that
// never published the bundle. This is the exact failure mode upstream saw live
// (a bad zip gives 400 on the PUT).
func TestNetlifyDeployRelaySurfacesUploadFailure(t *testing.T) {
	f := newNetlifyFake(t)
	useNetlifyFake(t, f)

	f.routes["POST /api/v1/sites"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusCreated, map[string]any{"id": "s1", "ssl_url": "https://a.netlify.app"})
	}
	f.routes["POST /api/v1/sites/s1/deploys"] = func(w http.ResponseWriter, _ []byte) {
		_, relaySHA := buildRelayFunctionZip()
		writeJSON(w, http.StatusOK, map[string]any{
			"id":                 "d1",
			"required":           []string{},
			"required_functions": []string{relaySHA},
		})
	}
	f.routes["PUT /api/v1/deploys/d1/functions/relay"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Function zip is invalid"})
	}

	_, err := netlifyDeployRelay(t.Context(), fakeHandlerClient(), "nfp_test", "a")
	var apiErr *netlifyError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want a netlifyError", err)
	}
	if apiErr.status != http.StatusBadRequest || apiErr.message != "Function zip is invalid" {
		t.Errorf("got status %d message %q, want 400 / \"Function zip is invalid\"", apiErr.status, apiErr.message)
	}
}

// ─── HTTP handler ───────────────────────────────────────────────────────

// newNetlifyDeployServer mounts the handler on a real router and returns the
// recorder-backed request helper plus the repo it writes through.
func newNetlifyDeployServer(t *testing.T) (*db.Repo, func(body string) *httptest.ResponseRecorder) {
	t.Helper()
	repo := newNetlifyRepo(t)
	mux := http.NewServeMux()
	h := NewMediaHandler(repo, nil, nil)
	h.Client = fakeHandlerClient()
	mux.HandleFunc("/api/proxy-pools/netlify-deploy", h.HandleNetlifyDeploy)

	return repo, func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
			"/api/proxy-pools/netlify-deploy", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
}

// TestHandleNetlifyDeployRequiresToken pins the guard: a missing token must be
// rejected before any call to Netlify. The fake is left unrouted on purpose —
// reaching it would trip its "unexpected Netlify call" assertion.
func TestHandleNetlifyDeployRequiresToken(t *testing.T) {
	f := newNetlifyFake(t)
	useNetlifyFake(t, f)

	_, post := newNetlifyDeployServer(t)
	rec := post(`{"projectName":"x"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "token") {
		t.Errorf("body = %s, want a message naming the token requirement", rec.Body.String())
	}
}

// TestHandleNetlifyDeployCreatesPool is the create → test → deploy round trip
// the dashboard performs: the handler must persist a `netlify` pool whose
// proxyUrl is the relay URL, which is what the connection resolver later reads.
func TestHandleNetlifyDeployCreatesPool(t *testing.T) {
	f := newNetlifyFake(t)
	useNetlifyFake(t, f)

	f.routes["POST /api/v1/sites"] = func(w http.ResponseWriter, body []byte) {
		var req struct {
			Name string `json:"name"`
		}
		json.Unmarshal(body, &req)
		if req.Name != "netlify-relay" {
			t.Errorf("site name = %q, want the sanitised name", req.Name)
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": "site_1", "ssl_url": "https://relay-x.netlify.app/"})
	}
	_, relaySHA := buildRelayFunctionZip()
	_, indexSHA := buildIndexFile()
	f.routes["POST /api/v1/sites/site_1/deploys"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "deploy_1",
			"required": []string{indexSHA},
			"required_functions": []string{relaySHA},
		})
	}
	f.routes["PUT /api/v1/deploys/deploy_1/files/index.html"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusOK, map[string]any{})
	}
	f.routes["PUT /api/v1/deploys/deploy_1/functions/relay"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusOK, map[string]any{})
	}
	f.routes["GET /api/v1/deploys/deploy_1"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusOK, map[string]any{"state": "ready"})
	}

	repo, post := newNetlifyDeployServer(t)
	rec := post(`{"netlifyToken":"nfp_test","projectName":"Netlify-Relay!!"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}

	var resp struct {
		DeployURL string `json:"deployUrl"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if want := "https://relay-x.netlify.app/.netlify/functions/relay"; resp.DeployURL != want {
		t.Errorf("deployUrl = %q, want %q", resp.DeployURL, want)
	}

	pools, err := repo.ListProxyPools()
	if err != nil {
		t.Fatalf("ListProxyPools: %v", err)
	}
	if len(pools) != 1 {
		t.Fatalf("pool count = %d, want exactly the one the deploy created", len(pools))
	}
	id, _ := pools[0]["id"].(string)
	if id == "" {
		t.Fatalf("listed pool has no id: %v", pools[0])
	}
	got, err := repo.GetProxyPool(id)
	if err != nil {
		t.Fatalf("GetProxyPool: %v", err)
	}
	if got.Type != "netlify" {
		t.Errorf("pool type = %q, want netlify so IsEdgeRelay classifies it as a relay", got.Type)
	}
	if got.NextURL() != resp.DeployURL {
		t.Errorf("pool proxyUrl = %q, want the relay URL %q", got.NextURL(), resp.DeployURL)
	}
	if !got.IsEdgeRelay() {
		t.Error("a netlify pool must be an edge relay: dialling it with http.ProxyURL yields \"malformed HTTP status code\"")
	}
	if !got.IsActive {
		t.Error("the deploy must hand back an active pool; an inactive one is silently skipped by the resolver")
	}
	if got.Name != "netlify-relay" {
		t.Errorf("pool name = %q, want the sanitised site name", got.Name)
	}
}

// TestHandleNetlifyDeployMapsAPIFailure pins that a Netlify rejection reaches
// the dashboard with a usable status and message rather than a generic 500 —
// the operator can only fix a taken name or an expired token if told which.
func TestHandleNetlifyDeployMapsAPIFailure(t *testing.T) {
	f := newNetlifyFake(t)
	useNetlifyFake(t, f)

	f.routes["POST /api/v1/sites"] = func(w http.ResponseWriter, _ []byte) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "name taken"})
	}

	_, post := newNetlifyDeployServer(t)
	rec := post(`{"netlifyToken":"nfp_test","projectName":"taken"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "different name") {
		t.Errorf("body = %s, want the hint to choose a different site name", rec.Body.String())
	}
}

// TestHandleNetlifyDeployRejectsBadJSON pins the shape guard so a malformed
// SPA payload fails as a client error rather than as a 500 from the decode.
func TestHandleNetlifyDeployRejectsBadJSON(t *testing.T) {
	_, post := newNetlifyDeployServer(t)
	rec := post(`{not json`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestNetlifyErrorStatusFallsBackToGatewayFault pins the mapping default: an
// error carrying no upstream status (a dial failure, a cancelled context) is a
// gateway fault, not an API rejection the operator can fix.
func TestNetlifyErrorStatusFallsBackToGatewayFault(t *testing.T) {
	if got := netlifyErrorStatus(errors.New("dial tcp: refused")); got != http.StatusInternalServerError {
		t.Errorf("status for a transport error = %d, want 500", got)
	}
	if got := netlifyErrorStatus(newNetlifyError(http.StatusConflict, "taken")); got != http.StatusConflict {
		t.Errorf("status for an API error = %d, want 409", got)
	}
	if got := netlifyErrorMessage(newNetlifyError(http.StatusConflict, "taken")); got != "taken" {
		t.Errorf("message = %q, want the upstream message", got)
	}
}

// writeJSON renders a fake API response. The payloads are literals in these
// tests, so a marshal failure is a test bug rather than a runtime condition.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	raw, err := json.Marshal(payload)
	if err != nil {
		panic("netlify test fake: unmarshalable payload: " + err.Error())
	}
	w.Write(raw)
}

// fakeHandlerClient is the bounded client the handler tests run against, so a
// request to the real Netlify API fails fast instead of hanging the suite.
func fakeHandlerClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}