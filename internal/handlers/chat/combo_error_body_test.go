package chat

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// A combo whose every member fails with a non-upstreamError error (a
// connection whose baseUrl cannot even be parsed into a request) answers with
// the error text interpolated into the JSON body. Go's url.Parse error embeds
// the offending URL in double quotes and reports the invalid character in
// quotes too, so a baseUrl carrying a quote and a backslash produces
//
//	{"error":{"message":"upstream error: ... parse "http://a"\b": ...}}
//
// where the quote inside the message closes the JSON string early and the
// backslash that follows becomes a bare token. A JavaScript client reports that
// as `JSON Parse error: Unrecognized token '\'` and cannot read the status, the
// type or the code at all. The body must be marshalled, not formatted.
func TestComboErrorBodyIsParseableWhenUpstreamErrorCarriesQuotesAndBackslashes(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}

	// A baseUrl that makes http.NewRequestWithContext fail: it carries a double
	// quote immediately followed by a backslash, which is the exact shape that
	// turns the formatted body into an unparseable one.
	const badBaseURL = `http://a"\b`
	data, _ := json.Marshal(map[string]any{"apiKey": "sk-mock-key", "baseUrl": badBaseURL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-bad', 'deepseek', 'apikey', 'DeepSeek Bad URL', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(data)); err != nil {
		t.Fatalf("insert bad-url connection: %v", err)
	}

	comboModels, _ := json.Marshal([]string{"deepseek/deepseek-chat"})
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('combo-badurl', 'combo-badurl', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-10-23T00:00:00Z')`,
		string(comboModels)); err != nil {
		t.Fatalf("insert combo: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))
	rec := httptest.NewRecorder()
	handler.HandleChatCompletions(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"combo-badurl","messages":[{"role":"user","content":"hi"}]}`)))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", rec.Code, rec.Body.String())
	}

	// The client's own parse is the contract: a body it cannot decode carries no
	// status, no type and no code, so the failure is invisible to the caller.
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("client cannot parse the combo error body: %v\nbody: %s", err, rec.Body.String())
	}
	if envelope.Error.Type != "upstream_error" {
		t.Errorf("error.type = %q, want %q", envelope.Error.Type, "upstream_error")
	}
	if envelope.Error.Code != http.StatusBadGateway {
		t.Errorf("error.code = %d, want %d", envelope.Error.Code, http.StatusBadGateway)
	}
	if !strings.Contains(envelope.Error.Message, "upstream error:") {
		t.Errorf("error.message = %q, want it to name the upstream failure", envelope.Error.Message)
	}
}

// The Claude-format combo loop builds the same terminal error, so it has to
// survive the same text.
func TestMessagesComboErrorBodyIsParseableWhenUpstreamErrorCarriesQuotesAndBackslashes(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}

	const badBaseURL = `http://a"\b`
	data, _ := json.Marshal(map[string]any{"apiKey": "sk-mock-key", "baseUrl": badBaseURL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-bad', 'deepseek', 'apikey', 'DeepSeek Bad URL', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-10-23T00:00:00Z')`,
		string(data)); err != nil {
		t.Fatalf("insert bad-url connection: %v", err)
	}

	comboModels, _ := json.Marshal([]string{"deepseek/deepseek-chat"})
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('combo-badurl-msg', 'combo-badurl-msg', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-10-23T00:00:00Z')`,
		string(comboModels)); err != nil {
		t.Fatalf("insert combo: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))
	rec := httptest.NewRecorder()
	handler.HandleMessages(rec, httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"combo-badurl-msg","messages":[{"role":"user","content":"hi"}],"max_tokens":64}`)))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", rec.Code, rec.Body.String())
	}

	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("client cannot parse the combo error body: %v\nbody: %s", err, rec.Body.String())
	}
	if envelope.Error.Type != "upstream_error" || envelope.Error.Code != http.StatusBadGateway {
		t.Errorf("error = %+v, want type upstream_error and code 502", envelope.Error)
	}
}

// The helper itself: a message with a quote immediately followed by a backslash
// is the shape that used to close the JSON string early and leave a bare token.
func TestUpstreamErrorBodyEscapesQuotesAndBackslashes(t *testing.T) {
	const message = `upstream error: forward to http://a"\b: create request: parse "http://a"\b": invalid character "\" in host name`

	body := upstreamErrorBody(message)

	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("body is not valid JSON: %v\nbody: %s", err, body)
	}
	if envelope.Error.Message != message {
		t.Errorf("message round-trip mismatch:\n got %q\nwant %q", envelope.Error.Message, message)
	}
	if envelope.Error.Type != "upstream_error" || envelope.Error.Code != http.StatusBadGateway {
		t.Errorf("error = %+v, want type upstream_error and code 502", envelope.Error)
	}
}
