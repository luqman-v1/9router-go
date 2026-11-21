package db

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCustomModelExtraBodyRoundTripAndInvalidation(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)

	row := `{"providerAlias":"openrouter","id":"anthropic/claude-x:free","type":"llm",` +
		`"extraBody":{"top_p":0.5,"seed":12345678901234567}}`
	if err := repo.SetKV("customModels", "openrouter|anthropic/claude-x:free|llm", row); err != nil {
		t.Fatalf("SetKV: %v", err)
	}
	customs, err := repo.GetCustomModels()
	if err != nil || len(customs) != 1 {
		t.Fatalf("GetCustomModels = %v, %v", customs, err)
	}
	if got := string(customs[0].ExtraBody); !strings.Contains(got, `"seed":12345678901234567`) {
		t.Fatalf("ExtraBody = %s, want the stored bytes", got)
	}
	extra := repo.CustomModelExtraBody("openrouter", "anthropic/claude-x:free")
	if string(extra["seed"]) != "12345678901234567" {
		t.Fatalf("CustomModelExtraBody = %v", extra)
	}

	// A save through SetKV must be visible without a restart.
	cleared := `{"providerAlias":"openrouter","id":"anthropic/claude-x:free","type":"llm"}`
	if err := repo.SetKV("customModels", "openrouter|anthropic/claude-x:free|llm", cleared); err != nil {
		t.Fatalf("SetKV clear: %v", err)
	}
	if extra := repo.CustomModelExtraBody("openrouter", "anthropic/claude-x:free"); extra != nil {
		t.Fatalf("after clear CustomModelExtraBody = %v, want nil", extra)
	}

	// A row that never went through save validation (an import) keeps only the
	// allowed members.
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`,
		"or|bad|llm", `{"providerAlias":"or","id":"bad","extraBody":{"model":"other","provider":{"order":["x"]},"top_k":4}}`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	repo.InvalidateCustomModelExtraBody()
	extra = repo.CustomModelExtraBody("openrouter", "bad")
	if _, ok := extra["model"]; ok || extra["provider"] != nil || string(extra["top_k"]) != "4" {
		t.Fatalf("legacy row indexed with disallowed keys: %v", extra)
	}
}

func TestParseExtraBody(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr error
		wantMsg string // substring of the error
	}{
		{"object", `{"temperature":0.7,"reasoning":{"effort":"high"}}`, nil, ""},
		{"empty object", `{}`, nil, ""},
		{"array", `[1]`, ErrExtraBodyNotObject, ""},
		{"null", `null`, ErrExtraBodyNotObject, ""},
		{"duplicate key", `{"top_p":0.1,"top_p":0.2}`, ErrExtraBodyInvalidJSON, ""},
		{"syntax error", `{"top_p":`, ErrExtraBodyInvalidJSON, ""},
		{"oversize", `{"stop":"` + strings.Repeat("a", ExtraBodyMaxBytes) + `"}`, ErrExtraBodyTooLarge, ""},
		// every allowed key, valid
		{"temperature", `{"temperature":0.5}`, nil, ""},
		{"top_p", `{"top_p":0.9}`, nil, ""},
		{"top_k", `{"top_k":40}`, nil, ""},
		{"min_p", `{"min_p":0.1}`, nil, ""},
		{"top_a", `{"top_a":0.1}`, nil, ""},
		{"repetition_penalty", `{"repetition_penalty":1.1}`, nil, ""},
		{"frequency_penalty", `{"frequency_penalty":-1}`, nil, ""},
		{"presence_penalty", `{"presence_penalty":1}`, nil, ""},
		{"seed big int", `{"seed":12345678901234567}`, nil, ""},
		{"seed 5.0", `{"seed":5.0}`, nil, ""},
		{"stop string", `{"stop":"x"}`, nil, ""},
		{"stop array", `{"stop":["a","b"]}`, nil, ""},
		{"logit_bias", `{"logit_bias":{"12":-5,"7":1.5}}`, nil, ""},
		{"logprobs", `{"logprobs":true}`, nil, ""},
		{"top_logprobs", `{"top_logprobs":5}`, nil, ""},
		{"max_completion_tokens", `{"max_completion_tokens":512}`, nil, ""},
		{"reasoning_effort", `{"reasoning_effort":"xhigh"}`, nil, ""},
		{"null values", `{"temperature":null,"stop":null,"reasoning":null,"reasoning_effort":null}`, nil, ""},
		{"reasoning full", `{"reasoning":{"effort":"max","enabled":true,"exclude":false,"summary":"auto","context":"all_turns","mode":"pro"}}`, nil, ""},
		{"reasoning max_tokens", `{"reasoning":{"max_tokens":100,"effort":null}}`, nil, ""},
		// wrong types
		{"temperature string", `{"temperature":"hot"}`, ErrExtraBodyKey, `"temperature"`},
		{"top_p string", `{"top_p":"1"}`, ErrExtraBodyKey, `"top_p"`},
		{"top_k float", `{"top_k":1.5}`, ErrExtraBodyKey, `"top_k"`},
		{"min_p bool", `{"min_p":true}`, ErrExtraBodyKey, `"min_p"`},
		{"top_a array", `{"top_a":[1]}`, ErrExtraBodyKey, `"top_a"`},
		{"repetition_penalty str", `{"repetition_penalty":"1"}`, ErrExtraBodyKey, `"repetition_penalty"`},
		{"frequency_penalty str", `{"frequency_penalty":"1"}`, ErrExtraBodyKey, `"frequency_penalty"`},
		{"presence_penalty str", `{"presence_penalty":"1"}`, ErrExtraBodyKey, `"presence_penalty"`},
		{"seed float", `{"seed":1.5}`, ErrExtraBodyKey, `"seed"`},
		{"stop number", `{"stop":1}`, ErrExtraBodyKey, `"stop"`},
		{"stop mixed array", `{"stop":["a",1]}`, ErrExtraBodyKey, `"stop"`},
		{"logit_bias array", `{"logit_bias":[1]}`, ErrExtraBodyKey, `"logit_bias"`},
		{"logit_bias str value", `{"logit_bias":{"1":"x"}}`, ErrExtraBodyKey, `"logit_bias"`},
		{"logprobs number", `{"logprobs":1}`, ErrExtraBodyKey, `"logprobs"`},
		{"top_logprobs str", `{"top_logprobs":"5"}`, ErrExtraBodyKey, `"top_logprobs"`},
		{"max_completion_tokens str", `{"max_completion_tokens":"9"}`, ErrExtraBodyKey, `"max_completion_tokens"`},
		{"reasoning_effort bad", `{"reasoning_effort":"extreme"}`, ErrExtraBodyKey, `"reasoning_effort"`},
		{"reasoning string", `{"reasoning":"high"}`, ErrExtraBodyKey, `"reasoning"`},
		// unknown keys, incl. case variants
		{"provider", `{"provider":{"order":["a"]}}`, ErrExtraBodyKey, `"provider"`},
		{"plugins", `{"plugins":[]}`, ErrExtraBodyKey, `"plugins"`},
		{"model", `{"model":"x"}`, ErrExtraBodyKey, `"model"`},
		{"messages", `{"messages":[]}`, ErrExtraBodyKey, `"messages"`},
		{"max_tokens", `{"max_tokens":5}`, ErrExtraBodyKey, `"max_tokens"`},
		{"Temperature", `{"Temperature":1}`, ErrExtraBodyKey, `"Temperature"`},
		{"STREAM", `{"STREAM":true}`, ErrExtraBodyKey, `"STREAM"`},
		// reasoning sub-keys and conflicts
		{"reasoning unknown sub", `{"reasoning":{"foo":1}}`, ErrExtraBodyKey, `"reasoning.foo"`},
		{"reasoning effort bad", `{"reasoning":{"effort":"x"}}`, ErrExtraBodyKey, `"reasoning.effort"`},
		{"reasoning summary bad", `{"reasoning":{"summary":"long"}}`, ErrExtraBodyKey, `"reasoning.summary"`},
		{"reasoning context bad", `{"reasoning":{"context":"x"}}`, ErrExtraBodyKey, `"reasoning.context"`},
		{"reasoning mode bad", `{"reasoning":{"mode":"x"}}`, ErrExtraBodyKey, `"reasoning.mode"`},
		{"reasoning max_tokens float", `{"reasoning":{"max_tokens":1.5}}`, ErrExtraBodyKey, `"reasoning.max_tokens"`},
		{"reasoning enabled str", `{"reasoning":{"enabled":"yes"}}`, ErrExtraBodyKey, `"reasoning.enabled"`},
		{"reasoning exclude str", `{"reasoning":{"exclude":1}}`, ErrExtraBodyKey, `"reasoning.exclude"`},
		{"max_tokens+effort", `{"reasoning":{"max_tokens":5,"effort":"low"}}`, ErrExtraBodyKey, "cannot be combined"},
		{"max_tokens+reasoning_effort", `{"reasoning":{"max_tokens":5},"reasoning_effort":"low"}`, ErrExtraBodyKey, "cannot be combined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseExtraBody([]byte(tt.raw))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParseExtraBody(%s) err = %v, want %v", tt.raw, err, tt.wantErr)
			}
			if tt.wantMsg != "" && !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("err = %q, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestParseExtraBodyUnknownKeyListsAllowed(t *testing.T) {
	_, err := ParseExtraBody([]byte(`{"provider":{}}`))
	if err == nil || !strings.Contains(err.Error(), strings.Join(ExtraBodyAllowedKeys, ", ")) {
		t.Fatalf("err = %v, want the allowed key list", err)
	}
}

const extraBodyTestKey = "openrouter|vendor/m|llm"

func extraBodyTestRow(topK int) string {
	return fmt.Sprintf(`{"providerAlias":"openrouter","id":"vendor/m","extraBody":{"top_k":%d}}`, topK)
}

// An invalidation that lands while a snapshot is being built must win: the
// in-flight build may serve its own caller, but it must not be cached.
func TestCustomModelExtraBodyInvalidationDuringBuild(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)
	if err := repo.SetKV("customModels", extraBodyTestKey, extraBodyTestRow(1)); err != nil {
		t.Fatalf("SetKV: %v", err)
	}
	afterExtraBodyRead = func() {
		afterExtraBodyRead = nil
		if err := repo.SetKV("customModels", extraBodyTestKey, extraBodyTestRow(2)); err != nil {
			t.Errorf("concurrent SetKV: %v", err)
		}
	}
	t.Cleanup(func() { afterExtraBodyRead = nil })

	repo.CustomModelExtraBody("openrouter", "vendor/m") // build races the write
	if got := string(repo.CustomModelExtraBody("openrouter", "vendor/m")["top_k"]); got != "2" {
		t.Fatalf("top_k = %s after the racing write, want 2 (stale snapshot cached)", got)
	}
}

// A failed read must not cache an empty index: the next call retries.
func TestCustomModelExtraBodyReadFailureNotCached(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)
	if err := repo.SetKV("customModels", extraBodyTestKey, extraBodyTestRow(3)); err != nil {
		t.Fatalf("SetKV: %v", err)
	}
	if _, err := database.Exec(`ALTER TABLE kv RENAME TO kv_hidden`); err != nil {
		t.Fatalf("hide kv: %v", err)
	}
	if extra := repo.CustomModelExtraBody("openrouter", "vendor/m"); extra != nil {
		t.Fatalf("failed read returned %v", extra)
	}
	if _, err := database.Exec(`ALTER TABLE kv_hidden RENAME TO kv`); err != nil {
		t.Fatalf("restore kv: %v", err)
	}
	if got := string(repo.CustomModelExtraBody("openrouter", "vendor/m")["top_k"]); got != "3" {
		t.Fatalf("top_k = %q after recovery, want 3 (empty index was cached)", got)
	}
}

func TestExtraBodyProviderAllowed(t *testing.T) {
	for alias, want := range map[string]bool{"openrouter": true, "or": true, "openai": false, "opencode": false, "": false} {
		if got := ExtraBodyProviderAllowed(alias); got != want {
			t.Errorf("ExtraBodyProviderAllowed(%q) = %v, want %v", alias, got, want)
		}
	}
}
