package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/providers"
	json "encoding/json/v2"
)

func TestBuildWav(t *testing.T) {
	pcm := []byte{1, 2, 3, 4}
	wav := buildWav(pcm)
	if string(wav[0:4]) != "RIFF" || string(wav[8:16]) != "WAVEfmt " || string(wav[36:40]) != "data" {
		t.Fatalf("missing RIFF/WAVE markers: %x", wav[:48])
	}
	if got := binary.LittleEndian.Uint32(wav[4:]); got != uint32(len(wav)-8) {
		t.Errorf("RIFF size = %d, want %d", got, len(wav)-8)
	}
	if got := binary.LittleEndian.Uint32(wav[24:]); got != 24000 {
		t.Errorf("sample rate = %d, want 24000", got)
	}
	if got := binary.LittleEndian.Uint32(wav[40:]); got != uint32(len(pcm)) {
		t.Errorf("data size = %d, want %d", got, len(pcm))
	}
	if string(wav[44:]) != string(pcm) {
		t.Errorf("samples not copied")
	}
}

func TestParseCodexSTTForm(t *testing.T) {
	build := func(t *testing.T, fields map[string]string, file []byte, fileCount int) ([]byte, string) {
		t.Helper()
		buf := &bytes.Buffer{}
		w := multipart.NewWriter(buf)
		for i := 0; i < max(fileCount, 1); i++ {
			part, err := w.CreateFormFile("file", "a.wav")
			if err != nil {
				t.Fatalf("create form file: %v", err)
			}
			part.Write(file)
		}
		for k, v := range fields {
			if err := w.WriteField(k, v); err != nil {
				t.Fatalf("write field %s: %v", k, err)
			}
		}
		w.Close()
		return buf.Bytes(), w.FormDataContentType()
	}

	tests := []struct {
		name       string
		fields     map[string]string
		fileCount  int
		wantErr    string
		wantFormat string
	}{
		{name: "minimal body defaults to json", fileCount: 1, wantFormat: "json"},
		{name: "response_format text is kept", fields: map[string]string{"response_format": "text"}, fileCount: 1, wantFormat: "text"},
		{name: "empty language is tolerated", fields: map[string]string{"language": ""}, fileCount: 1, wantFormat: "json"},
		{name: "unknown field is refused", fields: map[string]string{"boost": "2"}, fileCount: 1, wantErr: "Unsupported or duplicate form field"},
		{name: "duplicate file is refused", fileCount: 2, wantErr: "Unsupported or duplicate form field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, contentType := build(t, tt.fields, []byte{1, 2, 3}, tt.fileCount)
			form, err := parseCodexSTTForm(body, contentType)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("expected error %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if form.responseFormat != tt.wantFormat {
				t.Fatalf("response format = %q, want %q", form.responseFormat, tt.wantFormat)
			}
		})
	}

	t.Run("non-multipart content type is refused", func(t *testing.T) {
		if _, err := parseCodexSTTForm([]byte{}, "application/json"); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestValidateCodexSTTForm(t *testing.T) {
	tests := []struct {
		name    string
		form    codexSTTForm
		wantErr string
	}{
		{"valid json", codexSTTForm{responseFormat: "json", file: []byte{1}}, ""},
		{"valid text", codexSTTForm{responseFormat: "text", file: []byte{1}}, ""},
		{"unsupported format", codexSTTForm{responseFormat: "verbose_json", file: []byte{1}}, "Only json and text response formats are supported"},
		{"language is unsupported", codexSTTForm{responseFormat: "json", file: []byte{1}, extras: map[string]string{"language": "eng"}}, "language is not supported by Codex transcription"},
		{"prompt is unsupported", codexSTTForm{responseFormat: "json", file: []byte{1}, extras: map[string]string{"prompt": "hello"}}, "prompt is not supported by Codex transcription"},
		{"temperature is unsupported", codexSTTForm{responseFormat: "json", file: []byte{1}, extras: map[string]string{"temperature": "0.5"}}, "temperature is not supported by Codex transcription"},
		{"empty file", codexSTTForm{responseFormat: "json"}, "A non-empty audio file is required"},
		{"oversized file", codexSTTForm{responseFormat: "json", file: make([]byte, codexMaxAudioBytes+1)}, "Audio exceeds 25 MiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCodexSTTForm(&tt.form)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("expected %q, got %v", tt.wantErr, err)
			}
			var stErr *codexSTTError
			if !errors.As(err, &stErr) {
				t.Fatalf("expected a typed codexSTTError, got %T", err)
			}
		})
	}
}

func TestReadCodexSTTResponse(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    string
		wantErr string
	}{
		{"json text", `{"text":"hello there"}`, "hello there", ""},
		{"garbage", "not json", "", codexSTTInvalidResp},
		{"missing text", `{"segments":[]}`, "", codexSTTInvalidResp},
		{"text too long", fmt.Sprintf(`{"text":"%s"}`, strings.Repeat("a", codexSTTMaxText+1)), "", codexSTTInvalidResp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readCodexSTTResponse(strings.NewReader(tt.body))
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("expected %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}

	t.Run("body past the size cap is refused", func(t *testing.T) {
		if _, err := readCodexSTTResponse(strings.NewReader(strings.Repeat("a", codexSTTMaxResponse+10))); err == nil {
			t.Fatal("expected an error")
		}
	})
}

// newCodexSTTHandler seeds an active codex OAuth connection and returns a
// handler wired to it, with the upstream pointed at the fake server and the
// conversion stubbed to a canned WAV.
func newCodexSTTHandler(t *testing.T) *MediaHandler {
	t.Helper()
	database, cleanup := setupMultimodalTestDB(t)
	t.Cleanup(cleanup)
	connData, err := json.Marshal(map[string]any{
		"apiKey":               "eyJtest-access-token",
		"providerSpecificData": map[string]any{"chatgptAccountId": "acc-test-123"},
	})
	if err != nil {
		t.Fatalf("marshal conn data: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cx-stt', 'codex', 'oauth', 'Codex STT Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	return newTestMediaHandler(db.NewRepo(database))
}

func buildCodexSTTRequest(t *testing.T, h *MediaHandler, fields map[string]string, file []byte) *httptest.ResponseRecorder {
	t.Helper()
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	part, err := w.CreateFormFile("file", "a.mp3")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	part.Write(file)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}
	w.Close()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	modelInfo := &chat.ModelInfo{Provider: "codex", Model: codexSTTModel}
	if err := h.handleCodexSTT(rec, req, buf.Bytes(), modelInfo); err != nil {
		var stErr *codexSTTError
		if errors.As(err, &stErr) {
			rec.Code = stErr.status
			rec.Body.Reset()
			rec.Body.WriteString(stErr.message)
			return rec
		}
		t.Fatalf("handleCodexSTT: %v", err)
	}
	return rec
}

func TestHandleCodexSTTUpstreamContract(t *testing.T) {
	var gotHeader http.Header
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":"hello from codex"}`))
	}))
	defer upstream.Close()

	h := newCodexSTTHandler(t)
	previousURL, previousConvert := codexSTTUpstream, codexConvertAudio
	codexSTTUpstream = upstream.URL
	codexConvertAudio = func([]byte) ([]byte, error) { return buildWav([]byte{1, 2, 3, 4}), nil }
	t.Cleanup(func() { codexSTTUpstream, codexConvertAudio = previousURL, previousConvert })

	rec := buildCodexSTTRequest(t, h, nil, []byte{9, 9, 9})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotHeader.Get("Authorization") != "Bearer eyJtest-access-token" {
		t.Errorf("Authorization = %q", gotHeader.Get("Authorization"))
	}
	if gotHeader.Get("ChatGPT-Account-Id") != "acc-test-123" {
		t.Errorf("ChatGPT-Account-Id = %q", gotHeader.Get("ChatGPT-Account-Id"))
	}
	if gotHeader.Get("User-Agent") != providers.CodexCLIUserAgent {
		t.Errorf("User-Agent = %q", gotHeader.Get("User-Agent"))
	}
	if !strings.Contains(gotHeader.Get("Content-Type"), "multipart/form-data") {
		t.Errorf("Content-Type = %q", gotHeader.Get("Content-Type"))
	}
	if !bytes.Contains(gotBody, []byte("RIFF")) || !bytes.Contains(gotBody, []byte(`filename="audio.wav"`)) {
		t.Errorf("expected a WAV file part, got %d bytes", len(gotBody))
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Text != "hello from codex" {
		t.Errorf("client body = %s (%v)", rec.Body.String(), err)
	}
}

func TestHandleCodexSTTFormatsAndErrors(t *testing.T) {
	t.Run("text response format answers plain text", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"text":"plain answer"}`))
		}))
		defer upstream.Close()
		h := newCodexSTTHandler(t)
		withCodexSTTUpstream(t, upstream.URL)
		rec := buildCodexSTTRequest(t, h, map[string]string{"response_format": "text"}, []byte{1})
		if rec.Code != http.StatusOK || rec.Body.String() != "plain answer" {
			t.Fatalf("expected plain text, got %d %q", rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
			t.Errorf("Content-Type = %q", ct)
		}
	})

	t.Run("an upstream 400 stops before the fallback", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer upstream.Close()
		h := newCodexSTTHandler(t)
		withCodexSTTUpstream(t, upstream.URL)
		rec := buildCodexSTTRequest(t, h, nil, []byte{1})
		if rec.Code != http.StatusBadRequest || rec.Body.String() != codexSTTFailedMsg {
			t.Fatalf("expected 400 %q, got %d %q", codexSTTFailedMsg, rec.Code, rec.Body.String())
		}
	})

	t.Run("an unusable answer is a 502", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"no_text_here":true}`))
		}))
		defer upstream.Close()
		h := newCodexSTTHandler(t)
		withCodexSTTUpstream(t, upstream.URL)
		rec := buildCodexSTTRequest(t, h, nil, []byte{1})
		if rec.Code != http.StatusBadGateway || rec.Body.String() != codexSTTInvalidResp {
			t.Fatalf("expected 502 %q, got %d %q", codexSTTInvalidResp, rec.Code, rec.Body.String())
		}
	})

	t.Run("missing OAuth account data is a 401 without reaching upstream", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("upstream must not be reached without a valid account")
		}))
		defer upstream.Close()
		withCodexSTTUpstream(t, upstream.URL)
		database, cleanup := setupMultimodalTestDB(t)
		t.Cleanup(cleanup)
		connData, _ := json.Marshal(map[string]any{"apiKey": "eyJtest-access-token"})
		if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
			('conn-cx-stt', 'codex', 'oauth', 'Codex STT Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
			t.Fatalf("seed connection: %v", err)
		}
		h := newTestMediaHandler(db.NewRepo(database))
		rec := buildCodexSTTRequest(t, h, nil, []byte{1})
		if rec.Code != http.StatusUnauthorized || rec.Body.String() != "A valid Codex OAuth account is required" {
			t.Fatalf("expected 401, got %d %q", rec.Code, rec.Body.String())
		}
	})
}

// TestHandleCodexSTTRealConversion exercises the ffmpeg path end to end; it
// is skipped wherever ffmpeg is absent so the suite stays offline-green.
func TestHandleCodexSTTRealConversion(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	var gotWav bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotWav = bytes.Contains(body, []byte("RIFF"))
		w.Write([]byte(`{"text":"converted"}`))
	}))
	defer upstream.Close()

	h := newCodexSTTHandler(t)
	previousURL, previousConvert := codexSTTUpstream, codexConvertAudio
	codexSTTUpstream = upstream.URL
	codexConvertAudio = convertAudio
	t.Cleanup(func() { codexSTTUpstream, codexConvertAudio = previousURL, previousConvert })

	pcm := make([]byte, 24000/4*2)
	rec := buildCodexSTTRequest(t, h, nil, buildWav(pcm))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"text":"converted"}` {
		t.Fatalf("expected the converted upload to succeed, got %d %q", rec.Code, rec.Body.String())
	}
	if !gotWav {
		t.Fatal("upstream did not receive a WAV body")
	}
}

// withCodexSTTUpstream aims the fixed endpoint at a test server for the
// duration of the test.
func withCodexSTTUpstream(t *testing.T, url string) {
	t.Helper()
	previousURL, previousConvert := codexSTTUpstream, codexConvertAudio
	codexSTTUpstream = url
	codexConvertAudio = func([]byte) ([]byte, error) { return buildWav([]byte{1, 2, 3, 4}), nil }
	t.Cleanup(func() { codexSTTUpstream, codexConvertAudio = previousURL, previousConvert })
}
