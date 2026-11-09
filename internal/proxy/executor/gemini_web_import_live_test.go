package executor

import (
	"context"
	"strings"
	"testing"

	"9router/proxy/internal/browsercookies"
)

// TestGeminiWebLiveImportedSession exercises a session that was read out of a
// browser that is already signed in.
//
// The imported session is the one the dashboard's import button produces, and
// the question it has to answer is whether the site serves it. A browser's
// own database holds the cookies, but a session that the site does not
// recognise is no use to the router, and the import would look like it
// worked.
//
// The test is env-gated like the other live tests: it needs a browser on the
// machine that is signed in to Google.
func TestGeminiWebLiveImportedSession(t *testing.T) {
	requireLiveUpstream(t)

	dbPath, err := browsercookies.FindCookieDB("")
	if err != nil {
		t.Skipf("live upstream test: no browser cookie database was found (%v)", err)
	}
	result, err := browsercookies.Read(context.Background(), dbPath)
	if err != nil {
		t.Skipf("live upstream test: the browser's cookies could not be read (%v)", err)
	}
	if result.Header == "" {
		t.Skip("live upstream test: the browser holds no Google session cookies")
	}
	if !GeminiWebHasSessionTimestamp(result.Header) {
		t.Fatalf("the imported session carries no __Secure-1PSIDTS, so it is answered as an anonymous visitor's")
	}

	session := geminiWebSessionForCredential(result.Header)
	models, err := session.availableModels(context.Background())
	if err != nil {
		t.Fatalf("availableModels() error = %v", err)
	}
	names := make([]string, 0, len(models))
	for _, model := range models {
		names = append(names, model.Category)
	}
	t.Logf("the imported session may be served: %s", strings.Join(names, ", "))
	if len(models) < 2 {
		t.Errorf("the imported session reaches %d model(s); a signed-in account is expected to reach more than one",
			len(models))
	}
}
