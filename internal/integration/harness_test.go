//go:build integration

package integration

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/app"
	"9router/proxy/internal/db"
)

// testAPIKey is the client key seeded into every Env. The engine routes are
// gated by middleware.RequireApiKey, which looks the key up in the apiKeys
// table, so a runnable integration test must own one.
const testAPIKey = "sk-integration-test-key"

// Env is a fully wired 9router-go instance: the production chi router from
// app.ProvideRouter (same middleware stack, same route table), a private
// on-disk SQLite database built by the real schema bootstrap, and a real HTTP
// listener on a random port.
//
// It deliberately does NOT boot app.AppModule. That path goes through
// db.InitGlobalDatabase, a process-wide singleton, so only one database could
// ever exist per test binary without resetting it between tests.
// Constructing the router directly keeps each Env's state isolated while still
// exercising the production wiring — app.ProvideRouter is the same function the
// binary uses, so a route registered in the wrong place fails here exactly as it
// would in production. See bootfx/ for the complementary test that boots the
// real fx graph end to end.
//
// Env holds no *testing.T on purpose. Every helper takes the test that is
// currently running: calling FailNow through a stored parent would abort the
// parent from a subtest's goroutine, which the testing package treats as a bug
// and reports on the wrong test.
type Env struct {
	BaseURL string
	Repo    *db.Repo
	APIKey  string

	client *http.Client
}

// newEnv starts an isolated gateway. Cleanup stops the HTTP server and closes
// the database; t.TempDir removes the SQLite file and its WAL siblings.
func newEnv(t *testing.T) *Env {
	t.Helper()

	// DATA_DIR keeps auth.CLIToken() and config.LoadConfig() from reading or
	// writing the developer's real ~/.9router. JWT_SECRET pins the HS256
	// session secret so a minted cookie verifies regardless of the machine.
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("JWT_SECRET", "integration-suite-jwt-secret")

	conn, err := db.OpenDatabase(filepath.Join(t.TempDir(), "data.sqlite"))
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	t.Cleanup(func() {
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("close integration database: %v", cerr)
		}
	})

	// The same two schema calls app.ProvideDatabase makes on a real boot, so a
	// column the gateway relies on but the test schema lacks is a failure here
	// rather than a mystery in production.
	if err := db.EnsureCoreSchema(conn); err != nil {
		t.Fatalf("ensure core schema: %v", err)
	}
	if err := db.EnsureUpstreamLeases(conn); err != nil {
		t.Fatalf("ensure upstream leases: %v", err)
	}

	repo := db.NewRepo(conn)
	if err := repo.CreateApiKey("integration-key", testAPIKey, "Integration Suite", "integration-machine"); err != nil {
		t.Fatalf("seed api key: %v", err)
	}

	// Token savers off with every *Set flag raised: the CLI values then win over
	// the persisted settings row, so RTK/caveman/ponytail never rewrite the
	// request body and upstream assertions can compare it byte for byte.
	ts := app.ProvideTokenSaverConfig(repo, app.CLIParams{
		RTK: false, RTKSet: true,
		Caveman: false, CavemanSet: true,
		Ponytail: false, PonytailSet: true,
	})

	srv := httptest.NewServer(app.ProvideRouter(repo, ts))
	t.Cleanup(srv.Close)

	return &Env{
		BaseURL: srv.URL,
		Repo:    repo,
		APIKey:  testAPIKey,
		client:  srv.Client(),
	}
}

// Result is a completed HTTP exchange with the body already drained.
type Result struct {
	Status int
	Header http.Header
	Body   []byte
}

// Decode unmarshals the response body into dst and fails the test on a
// malformed payload — a non-JSON body where JSON was expected is a finding, not
// something for each caller to re-check.
func (r *Result) Decode(t *testing.T, dst any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, dst); err != nil {
		t.Fatalf("decode response body %q: %v", truncate(r.Body), err)
	}
}

// ErrorMessage pulls error.message out of the gateway's OpenAI-style error
// envelope, failing the test when the body is not that envelope.
func (r *Result) ErrorMessage(t *testing.T) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	r.Decode(t, &envelope)
	if envelope.Error.Message == "" {
		t.Fatalf("expected an error envelope with a message, got %q", truncate(r.Body))
	}
	return envelope.Error.Message
}

// RequestOpt mutates a request before it is sent.
type RequestOpt func(*http.Request)

// WithAPIKey authenticates as the given client key. Pass an empty string to
// send the request unauthenticated.
func WithAPIKey(key string) RequestOpt {
	return func(r *http.Request) {
		if key == "" {
			r.Header.Del("Authorization")
			return
		}
		r.Header.Set("Authorization", "Bearer "+key)
	}
}

// WithoutAPIKey sends the request with no Authorization header at all.
func WithoutAPIKey() RequestOpt {
	return func(r *http.Request) { r.Header.Del("Authorization") }
}

// WithHeader sets an arbitrary request header.
func WithHeader(name, value string) RequestOpt {
	return func(r *http.Request) { r.Header.Set(name, value) }
}

// Do sends a request to the gateway and drains the response. body is
// JSON-encoded unless it is already a string or []byte, in which case it is
// sent verbatim; a nil body sends no payload.
func (e *Env) Do(t *testing.T, method, path string, body any, opts ...RequestOpt) *Result {
	t.Helper()

	var reader io.Reader
	switch v := body.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(v)
	case string:
		reader = strings.NewReader(v)
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("encode request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, e.BaseURL+path, reader)
	if err != nil {
		t.Fatalf("build request %s %s: %v", method, path, err)
	}
	if reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+e.APIKey)
	for _, opt := range opts {
		opt(req)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("send request %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body %s %s: %v", method, path, err)
	}
	return &Result{Status: resp.StatusCode, Header: resp.Header, Body: raw}
}

// Get performs an authenticated GET.
func (e *Env) Get(t *testing.T, path string, opts ...RequestOpt) *Result {
	return e.Do(t, http.MethodGet, path, nil, opts...)
}

// Post performs an authenticated POST with a JSON body.
func (e *Env) Post(t *testing.T, path string, body any, opts ...RequestOpt) *Result {
	return e.Do(t, http.MethodPost, path, body, opts...)
}

// Put performs an authenticated PUT with a JSON body.
func (e *Env) Put(t *testing.T, path string, body any, opts ...RequestOpt) *Result {
	return e.Do(t, http.MethodPut, path, body, opts...)
}

// Delete performs an authenticated DELETE.
func (e *Env) Delete(t *testing.T, path string, opts ...RequestOpt) *Result {
	return e.Do(t, http.MethodDelete, path, nil, opts...)
}

// ChatBody builds a minimal OpenAI chat request. The prompt is deliberately
// unremarkable: handleBypassRequest answers "Warmup", "count", "{", and
// "isNewTopic" synthetically without contacting upstream, so those strings
// would test the bypass path rather than the provider path.
func ChatBody(model string, stream bool) map[string]any {
	return map[string]any{
		"model":  model,
		"stream": stream,
		"messages": []map[string]any{
			{"role": "user", "content": "Summarize the Go memory model."},
		},
	}
}

// truncate shortens a body for a failure message without dumping a whole SSE
// stream into CI output.
func truncate(b []byte) string {
	const limit = 512
	if len(b) <= limit {
		return string(b)
	}
	return string(b[:limit]) + "…(truncated)"
}
