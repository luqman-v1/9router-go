package executor

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/providers"
)

func TestDowngradeJsonSchemaToJSONObject(t *testing.T) {
	// 1. json_schema with full schema object is converted to {"type":"json_object"}
	input := []byte(`{"model":"deepseek-chat","response_format":{"type":"json_schema","json_schema":{"name":"test","schema":{"type":"object"}}}}`)
	out := DowngradeJsonSchemaToJSONObject(input)
	var m map[string]jsontext.Value
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("downgraded body does not parse: %v", err)
	}
	if string(m["response_format"]) != `{"type":"json_object"}` {
		t.Errorf("response_format = %s, want {\"type\":\"json_object\"}", m["response_format"])
	}

	// 2. json_object is preserved as-is
	inputObj := []byte(`{"response_format":{"type":"json_object"}}`)
	outObj := DowngradeJsonSchemaToJSONObject(inputObj)
	if string(outObj) != string(inputObj) {
		t.Errorf("json_object body changed: %s", outObj)
	}

	// 3. missing or non-json_schema response_format is preserved as-is
	inputMissing := []byte(`{"model":"deepseek-chat"}`)
	if got := DowngradeJsonSchemaToJSONObject(inputMissing); string(got) != string(inputMissing) {
		t.Errorf("body without response_format changed: %s", got)
	}

	inputOther := []byte(`{"response_format":{"type":"text"}}`)
	if got := DowngradeJsonSchemaToJSONObject(inputOther); string(got) != string(inputOther) {
		t.Errorf("body with non-json_schema response_format changed: %s", got)
	}

	// 4. non-JSON body returns unchanged
	if got := DowngradeJsonSchemaToJSONObject([]byte(`not json`)); string(got) != "not json" {
		t.Errorf("unparsable body changed: %s", got)
	}
}

func TestForwardOpenAI_DeepSeekAndCompatibleDowngrade(t *testing.T) {
	var receivedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		receivedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	schemaBody := []byte(`{"model":"m","response_format":{"type":"json_schema","json_schema":{"name":"test","schema":{"type":"object"}}}}`)

	// Case A: deepseek provider downgrades json_schema
	rec := httptest.NewRecorder()
	err := ForwardOpenAI(rec, &Request{
		Ctx:      t.Context(),
		Client:   srv.Client(),
		Config:   &providers.ProviderConfig{BaseURL: srv.URL + "/chat/completions"},
		APIKey:   "k",
		Provider: "deepseek",
		Body:     schemaBody,
	})
	if err != nil {
		t.Fatalf("ForwardOpenAI deepseek failed: %v", err)
	}
	var mDeepseek map[string]jsontext.Value
	if err := json.Unmarshal(receivedBody, &mDeepseek); err != nil {
		t.Fatalf("received body does not parse: %v", err)
	}
	if string(mDeepseek["response_format"]) != `{"type":"json_object"}` {
		t.Errorf("deepseek provider response_format = %s, want {\"type\":\"json_object\"}", mDeepseek["response_format"])
	}

	// Case B: openai-compatible- custom provider downgrades json_schema
	rec = httptest.NewRecorder()
	err = ForwardOpenAI(rec, &Request{
		Ctx:      t.Context(),
		Client:   srv.Client(),
		Config:   &providers.ProviderConfig{BaseURL: srv.URL + "/chat/completions"},
		APIKey:   "k",
		Provider: "openai-compatible-custom",
		Body:     schemaBody,
	})
	if err != nil {
		t.Fatalf("ForwardOpenAI openai-compatible- failed: %v", err)
	}
	var mCompat map[string]jsontext.Value
	if err := json.Unmarshal(receivedBody, &mCompat); err != nil {
		t.Fatalf("received body does not parse: %v", err)
	}
	if string(mCompat["response_format"]) != `{"type":"json_object"}` {
		t.Errorf("openai-compatible- provider response_format = %s, want {\"type\":\"json_object\"}", mCompat["response_format"])
	}

	// Case C: openai provider does NOT downgrade json_schema
	rec = httptest.NewRecorder()
	err = ForwardOpenAI(rec, &Request{
		Ctx:      t.Context(),
		Client:   srv.Client(),
		Config:   &providers.ProviderConfig{BaseURL: srv.URL + "/chat/completions"},
		APIKey:   "k",
		Provider: "openai",
		Body:     schemaBody,
	})
	if err != nil {
		t.Fatalf("ForwardOpenAI openai failed: %v", err)
	}
	var mOpenai map[string]jsontext.Value
	if err := json.Unmarshal(receivedBody, &mOpenai); err != nil {
		t.Fatalf("received body does not parse: %v", err)
	}
	var rf struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(mOpenai["response_format"], &rf); err != nil || rf.Type != "json_schema" {
		t.Errorf("openai provider response_format unexpectedly changed: %s", mOpenai["response_format"])
	}
}
