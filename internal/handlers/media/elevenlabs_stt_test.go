package media

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// elevenLabsUpstream is a fake Scribe endpoint that records the forwarded
// multipart form and replies with a canned JSON body.
type elevenLabsUpstream struct {
	server   *httptest.Server
	fields   map[string]string
	fileName string
	fileData []byte
	header   http.Header
	calls    int
}

// scribeResponse renders the fake upstream body. additionalFormats is the raw
// `additional_formats` JSON array the vendor would echo back, or "".
func scribeResponse(t *testing.T, transcriptJSON, additionalFormats string) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(transcriptJSON), &body); err != nil {
		t.Fatalf("parse transcript fixture: %v", err)
	}
	if additionalFormats != "" {
		var formats []any
		if err := json.Unmarshal([]byte(additionalFormats), &formats); err != nil {
			t.Fatalf("parse additional_formats fixture: %v", err)
		}
		body["additional_formats"] = formats
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode transcript fixture: %v", err)
	}
	return string(encoded)
}

func newElevenLabsUpstream(t *testing.T, responseJSON string, additionalFormats string) *elevenLabsUpstream {
	t.Helper()
	responseBody := scribeResponse(t, responseJSON, additionalFormats)
	up := &elevenLabsUpstream{fields: map[string]string{}, header: http.Header{}}
	up.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.calls++
		for k, v := range r.Header {
			if len(v) > 0 {
				up.header[k] = v
			}
		}
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("upstream content-type: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FormName() == "file" {
				up.fileName = p.FileName()
				up.fileData, _ = io.ReadAll(p)
				continue
			}
			b, _ := io.ReadAll(p)
			up.fields[p.FormName()] = string(b)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(up.server.Close)
	return up
}

// overrideElevenLabsProvider points the elevenlabs registry entry at the fake
// upstream for the duration of the test.
func overrideElevenLabsProvider(t *testing.T, cfg providers.ProviderConfig) {
	t.Helper()
	orig, existed := providers.KnownProviders["elevenlabs"]
	providers.KnownProviders["elevenlabs"] = cfg
	t.Cleanup(func() {
		if existed {
			providers.KnownProviders["elevenlabs"] = orig
		} else {
			delete(providers.KnownProviders, "elevenlabs")
		}
	})
}

// newElevenLabsSTTHandler seeds an active elevenlabs connection and returns a
// handler wired to it. The provider registry entry (which carries the STT
// endpoint) is overridden by the caller.
func newElevenLabsSTTHandler(t *testing.T) *MediaHandler {
	t.Helper()
	database, cleanup := setupMultimodalTestDB(t)
	t.Cleanup(cleanup)

	connData, err := json.Marshal(map[string]any{"apiKey": "sk-el-TEST"})
	if err != nil {
		t.Fatalf("marshal conn data: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-el-stt', 'elevenlabs', 'apikey', 'ElevenLabs STT Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	return newTestMediaHandler(db.NewRepo(database))
}

// buildSTTMultipart builds a caller multipart body.
func buildSTTMultipart(t *testing.T, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	part, err := w.CreateFormFile("file", "a.wav")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	part.Write([]byte{1, 2, 3, 4})
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}
	w.Close()
	return buf, w.FormDataContentType()
}

func TestHandleSTT_ElevenLabs_ForwardsScribeParams(t *testing.T) {
	const transcript = "{\"text\":\"hello from scribe\",\"language_code\":\"eng\",\"language_probability\":0.99,\"words\":[{\"text\":\"hello\"}]}"

	tests := []struct {
		name  string
		send  map[string]string
		want  map[string]string
		unset []string
	}{
		{
			name:  "bare request sends only file and model_id",
			send:  map[string]string{"model": "elevenlabs/scribe_v1", "response_format": "json"},
			want:  map[string]string{"model_id": "scribe_v1"},
			unset: []string{"language_code", "timestamps_granularity", "tag_audio_events", "diarize", "num_speakers", "additional_formats"},
		},
		{
			name: "blank language is omitted so the vendor auto-detects",
			send: map[string]string{"model": "elevenlabs/scribe_v1", "language": "   "},
			want: map[string]string{"model_id": "scribe_v1"},
		},
		{
			name: "language maps to language_code",
			send: map[string]string{"model": "elevenlabs/scribe_v1", "language": "vie"},
			want: map[string]string{"model_id": "scribe_v1", "language_code": "vie"},
		},
		{
			name: "scribe params ride the body when sent",
			send: map[string]string{"model": "elevenlabs/scribe_v2", "timestamps_granularity": "word", "tag_audio_events": "true", "num_speakers": "3"},
			want: map[string]string{"model_id": "scribe_v2", "timestamps_granularity": "word", "tag_audio_events": "true", "num_speakers": "3"},
		},
		{
			name:  "unsupported timestamps_granularity is dropped",
			send:  map[string]string{"model": "elevenlabs/scribe_v1", "timestamps_granularity": "sentence"},
			unset: []string{"timestamps_granularity"},
		},
		{
			name:  "tag_audio_events=false is dropped",
			send:  map[string]string{"model": "elevenlabs/scribe_v1", "tag_audio_events": "false"},
			unset: []string{"tag_audio_events"},
		},
		{
			name:  "num_speakers outside 1-32 is dropped",
			send:  map[string]string{"model": "elevenlabs/scribe_v1", "num_speakers": "99"},
			unset: []string{"num_speakers"},
		},
		{
			name:  "diarize wins over num_speakers",
			send:  map[string]string{"model": "elevenlabs/scribe_v1", "diarize": "true", "num_speakers": "3"},
			want:  map[string]string{"model_id": "scribe_v1", "diarize": "true"},
			unset: []string{"num_speakers"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			up := newElevenLabsUpstream(t, transcript, "")
			overrideElevenLabsProvider(t, providers.ProviderConfig{
				BaseURL:    "https://api.elevenlabs.io",
				AuthHeader: "xi-api-key",
				AuthScheme: "raw",
				STTURL:     up.server.URL + "/v1/speech-to-text",
			})
			handler := newElevenLabsSTTHandler(t)

			body, contentType := buildSTTMultipart(t, tc.send)
			req := httptest.NewRequest("POST", "/v1/audio/transcriptions", body)
			req.Header.Set("Content-Type", contentType)
			rec := httptest.NewRecorder()

			handler.HandleAudioTranscriptions(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if up.calls != 1 {
				t.Fatalf("expected 1 upstream call, got %d", up.calls)
			}
			if got := up.header.Get("xi-api-key"); got != "sk-el-TEST" {
				t.Errorf("expected xi-api-key sk-el-TEST, got %q", got)
			}
			if got := up.header.Get("Authorization"); got != "" {
				t.Errorf("expected no Authorization header, got %q", got)
			}
			if string(up.fileData) != string([]byte{1, 2, 3, 4}) || up.fileName != "a.wav" {
				t.Errorf("audio part not forwarded verbatim: %q %s", up.fileData, up.fileName)
			}
			for k, v := range tc.want {
				if got := up.fields[k]; got != v {
					t.Errorf("field %s = %q, want %q", k, got, v)
				}
			}
			for _, k := range tc.unset {
				if got, ok := up.fields[k]; ok {
					t.Errorf("field %s should be omitted, got %q", k, got)
				}
			}
		})
	}
}

func TestHandleSTT_ElevenLabs_ResponseFormats(t *testing.T) {
	const srtRender = "1\n00:00:00,100 --> 00:00:00,400\nhello\n"
	const vttRender = "WEBVTT\n\n00:00.100 --> 00:00.400\nhello\n"
	const transcript = "{\"text\":\"hello from scribe\",\"language_code\":\"eng\",\"language_probability\":0.99,\"words\":[{\"text\":\"hello\"}]}"
	segments := "[{\"id\":0,\"text\":\"hello from scribe\",\"start\":0.1,\"end\":1.2}]"

	tests := []struct {
		name           string
		responseFmt    string
		extraFormats   string
		wantContentCt  string
		wantBody       string
		wantSegments   bool
		wantAdditional string
	}{
		{
			name:           "srt serves the subtitle render verbatim",
			responseFmt:    "srt",
			extraFormats:   `[{"format":"srt","content":"1\n00:00:00,100 --> 00:00:00,400\nhello\n"}]`,
			wantContentCt:  "text/plain",
			wantBody:       srtRender,
			wantAdditional: "srt",
		},
		{
			name:           "vtt serves the caption render verbatim",
			responseFmt:    "vtt",
			extraFormats:   `[{"format":"vtt","content":"WEBVTT\n\n00:00.100 --> 00:00.400\nhello\n"}]`,
			wantContentCt:  "text/plain",
			wantBody:       vttRender,
			wantAdditional: "vtt",
		},
		{
			name:           "srt falls back to plain text when the render is absent",
			responseFmt:    "srt",
			extraFormats:   "",
			wantContentCt:  "text/plain",
			wantBody:       "hello from scribe",
			wantAdditional: "srt",
		},
		{
			name:           "verbose_json returns real segments when requested",
			responseFmt:    "verbose_json",
			extraFormats:   fmt.Sprintf("[{\"format\":\"seg_json\",\"content\":%q}]", segments),
			wantContentCt:  "application/json",
			wantBody:       transcript,
			wantSegments:   true,
			wantAdditional: "seg_json",
		},
		{
			name:           "verbose_json omits segments rather than fabricating them",
			responseFmt:    "verbose_json",
			extraFormats:   "",
			wantContentCt:  "application/json",
			wantBody:       transcript,
			wantSegments:   false,
			wantAdditional: "seg_json",
		},
		{
			name:           "text returns a plain-text body",
			responseFmt:    "text",
			extraFormats:   "",
			wantContentCt:  "text/plain",
			wantBody:       "hello from scribe",
			wantAdditional: "",
		},
		{
			name:           "json returns the plain envelope and asks for no render",
			responseFmt:    "json",
			extraFormats:   "",
			wantContentCt:  "application/json",
			wantBody:       "{\"text\":\"hello from scribe\"}",
			wantAdditional: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			up := newElevenLabsUpstream(t, transcript, tc.extraFormats)
			overrideElevenLabsProvider(t, providers.ProviderConfig{
				BaseURL:    "https://api.elevenlabs.io",
				AuthHeader: "xi-api-key",
				AuthScheme: "raw",
				STTURL:     up.server.URL + "/v1/speech-to-text",
			})
			handler := newElevenLabsSTTHandler(t)

			body, contentType := buildSTTMultipart(t, map[string]string{
				"model":           "elevenlabs/scribe_v1",
				"response_format": tc.responseFmt,
			})
			req := httptest.NewRequest("POST", "/v1/audio/transcriptions", body)
			req.Header.Set("Content-Type", contentType)
			rec := httptest.NewRecorder()

			handler.HandleAudioTranscriptions(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, tc.wantContentCt) {
				t.Errorf("content-type = %q, want it to contain %q", ct, tc.wantContentCt)
			}

			if tc.wantAdditional == "" {
				if got, ok := up.fields["additional_formats"]; ok {
					t.Errorf("additional_formats should be omitted, got %q", got)
				}
			} else {
				var got []map[string]string
				if err := json.Unmarshal([]byte(up.fields["additional_formats"]), &got); err != nil {
					t.Fatalf("parse additional_formats %q: %v", up.fields["additional_formats"], err)
				}
				if len(got) != 1 || got[0]["format"] != tc.wantAdditional {
					t.Errorf("additional_formats = %+v, want format %s", got, tc.wantAdditional)
				}
			}

			if !strings.Contains(tc.wantContentCt, "json") {
				if rec.Body.String() != tc.wantBody {
					t.Errorf("body = %q, want %q", rec.Body.String(), tc.wantBody)
				}
				return
			}

			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("parse response %q: %v", rec.Body.String(), err)
			}
			if resp["text"] != "hello from scribe" {
				t.Errorf("text = %v, want %q", resp["text"], "hello from scribe")
			}
			if _, ok := resp["segments"]; ok != tc.wantSegments {
				t.Errorf("segments present = %v, want %v (body %s)", ok, tc.wantSegments, rec.Body.String())
			}
			if tc.wantSegments {
				segs, _ := resp["segments"].([]any)
				if len(segs) != 1 {
					t.Fatalf("expected 1 real segment, got %v", resp["segments"])
				}
				seg, _ := segs[0].(map[string]any)
				if seg["text"] != "hello from scribe" {
					t.Errorf("segment text = %v", seg["text"])
				}
			}
		})
	}
}

func TestElevenLabsAdditionalFormatsMapping(t *testing.T) {
	tests := []struct {
		name           string
		responseFormat string
		wantField      string
		wantFormat     string
		wantRaw        bool
	}{
		{name: "json needs no render", responseFormat: "json"},
		{name: "empty needs no render", responseFormat: ""},
		{name: "unknown needs no render", responseFormat: "ass"},
		{name: "text needs no render", responseFormat: "text"},
		{name: "srt asks for the subtitles render", responseFormat: "srt", wantField: "srt", wantFormat: "srt", wantRaw: true},
		{name: "vtt asks for the captions render", responseFormat: "vtt", wantField: "vtt", wantFormat: "vtt", wantRaw: true},
		{name: "response_format is case-insensitive", responseFormat: "SRT", wantField: "srt", wantFormat: "srt", wantRaw: true},
		{name: "verbose_json asks for the segments render", responseFormat: "verbose_json", wantField: "verbose_json", wantFormat: "seg_json"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			field := elevenLabsSTTAdditionalFormats(tc.responseFormat)
			if tc.wantFormat == "" {
				if field != "" {
					t.Fatalf("additional_formats = %q, want empty", field)
				}
				return
			}
			var decoded []map[string]string
			if err := json.Unmarshal([]byte(field), &decoded); err != nil {
				t.Fatalf("parse %q: %v", field, err)
			}
			if len(decoded) != 1 || decoded[0]["format"] != tc.wantFormat {
				t.Fatalf("additional_formats = %+v, want one %s render", decoded, tc.wantFormat)
			}
			format, raw := elevenLabsAdditionalFormat(tc.responseFormat, elevenLabsSTTFormats)
			if format != tc.wantFormat || raw != tc.wantRaw {
				t.Errorf("resolve = (%q, %v), want (%q, %v)", format, raw, tc.wantFormat, tc.wantRaw)
			}
		})
	}
}

func TestExtractElevenLabsAdditionalFormat(t *testing.T) {
	segments := "[{\"id\":0,\"text\":\"hi\"}]"
	tests := []struct {
		name   string
		body   string
		format string
		ok     bool
		isText bool
		text   string
	}{
		{name: "missing render", body: `{}`, format: "srt"},
		{name: "no format requested", body: "{\"additional_formats\":[{\"format\":\"srt\",\"content\":\"x\"}]}", format: ""},
		{name: "srt returns the plain string", body: `{"additional_formats":[{"format":"srt","content":"SUB"}]}`, format: "srt", ok: true, isText: true, text: "SUB"},
		{name: "seg_json parses the JSON string", body: fmt.Sprintf("{\"additional_formats\":[{\"format\":\"seg_json\",\"content\":%q}]}", segments), format: "seg_json", ok: true},
		{name: "unparseable seg_json degrades", body: `{"additional_formats":[{"format":"seg_json","content":"{oops"}]}`, format: "seg_json"},
		{name: "non-string content degrades", body: `{"additional_formats":[{"format":"srt","content":5}]}`, format: "srt"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var upstream map[string]any
			if err := json.Unmarshal([]byte(tc.body), &upstream); err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			value, ok := extractElevenLabsAdditionalFormat(upstream, tc.format)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if tc.isText && value != tc.text {
				t.Errorf("value = %v, want %q", value, tc.text)
			}
		})
	}
}

func TestElevenLabsNumSpeakers(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: ""},
		{in: "0", want: ""},
		{in: "1", want: "1"},
		{in: "32", want: "32"},
		{in: "33", want: ""},
		{in: "-2", want: ""},
		{in: "2.5", want: ""},
		{in: "three", want: ""},
		{in: "1e3", want: ""},
	}
	for _, tc := range tests {
		if got := elevenLabsNumSpeakers(tc.in); got != tc.want {
			t.Errorf("numSpeakers(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
