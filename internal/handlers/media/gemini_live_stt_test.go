package media

import (
	"bytes"
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// newLiveSTTServer stands in for the Gemini Live endpoint. It answers the
// handshake, records the frames it receives, and drives the server side of the
// protocol: setupComplete, then transcription deltas, then turnComplete.
type liveSTTServer struct {
	URL      string
	frames   []map[string]any
	upgrader websocket.Upgrader
}

func newLiveSTTServer(t *testing.T, script func(conn *websocket.Conn)) *liveSTTServer {
	t.Helper()
	s := &liveSTTServer{upgrader: websocket.Upgrader{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := s.upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		// Read the setup frame before the script drives anything.
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var frame map[string]any
		if err := json.Unmarshal(data, &frame); err == nil {
			s.frames = append(s.frames, frame)
		}
		script(conn)
	}))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

func sendFrame(t *testing.T, conn *websocket.Conn, frame map[string]any) {
	t.Helper()
	if err := conn.WriteJSON(frame); err != nil {
		t.Errorf("write frame: %v", err)
	}
}

func TestToLiveWSURL(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		model   string
		token   string
		want    string
		wantErr bool
	}{
		{
			name:  "rest base becomes the bidi websocket endpoint",
			base:  "https://generativelanguage.googleapis.com/v1beta/models",
			model: "gemini-2.5-flash-native-audio-preview-09-17",
			token: "key-1",
			want:  "wss://generativelanguage.googleapis.com/ws/api/v1beta/models/gemini-2.5-flash-native-audio-preview-09-17:bidiGenerateContent?key=key-1",
		},
		{
			name:  "a trailing slash does not double up",
			base:  "https://host.example/v1beta/models/",
			model: "m",
			token: "t",
			want:  "wss://host.example/ws/api/v1beta/models/m:bidiGenerateContent?key=t",
		},
		{
			name:  "an existing ws prefix is left alone",
			base:  "wss://host.example/ws/api/v1beta/models",
			model: "m",
			token: "t",
			want:  "wss://host.example/ws/api/v1beta/models/m:bidiGenerateContent?key=t",
		},
		{name: "a non-http scheme is rejected", base: "ftp://host/v1beta/models", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toLiveWSURL(tt.base, tt.model, tt.token)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("toLiveWSURL: %v", err)
			}
			if got != tt.want {
				t.Errorf("toLiveWSURL = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestTranscribeGeminiLive_StreamsAudioAndSettlesOnTurnComplete drives the whole
// session against a stand-in server: the setup frame must carry the model and
// the system instruction, every audio byte must go up as base64 mediaChunks, and
// the run ends on turnComplete with the accumulated transcript.
func TestTranscribeGeminiLive_StreamsAudioAndSettlesOnTurnComplete(t *testing.T) {
	audio := bytes.Repeat([]byte{0x01, 0x02, 0x03, 0x04}, 5000) // 20000 bytes
	var received int

	srv := newLiveSTTServer(t, func(conn *websocket.Conn) {
		sendFrame(t, conn, map[string]any{"serverContent": map[string]any{"setupComplete": true}})
		// Drain the media chunks and the flushing turn, then answer.
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var frame map[string]any
			if err := json.Unmarshal(data, &frame); err != nil {
				continue
			}
			if ri, ok := frame["realtimeInput"].(map[string]any); ok {
				for _, chunk := range ri["mediaChunks"].([]any) {
					m, _ := chunk.(map[string]any)
					data, _ := m["data"].(string)
					raw, _ := base64.StdEncoding.DecodeString(data)
					received += len(raw)
				}
				continue
			}
			if _, ok := frame["clientContent"]; ok {
				sendFrame(t, conn, map[string]any{"serverContent": map[string]any{
					"inputTranscription": map[string]any{"text": "hello "},
				}})
				sendFrame(t, conn, map[string]any{"serverContent": map[string]any{
					"inputTranscription": map[string]any{"text": "world"},
				}})
				sendFrame(t, conn, map[string]any{"serverContent": map[string]any{"turnComplete": true}})
				return
			}
		}
	})

	srv.upgrader.CheckOrigin = func(*http.Request) bool { return true }
	// Route the production https base at the test server over plain ws.
	result, err := transcribeGeminiLive(context.Background(),
		"ws://"+strings.TrimPrefix(srv.URL, "http://")+"/v1beta/models",
		"live-model", "key-1", audio, "audio/wav",
		map[string]string{"prompt": "transcribe", "language": "id"})
	if err != nil {
		t.Fatalf("transcribeGeminiLive: %v", err)
	}

	if result.text != "hello world" {
		t.Errorf("transcript = %q, want %q", result.text, "hello world")
	}
	if len(result.chunks) != 2 || result.chunks[0] != "hello " || result.chunks[1] != "world" {
		t.Errorf("chunks = %q, want the two raw deltas", result.chunks)
	}
	if received != len(audio) {
		t.Errorf("streamed %d audio bytes, want %d", received, len(audio))
	}

	setup, ok := srv.frames[0]["setup"].(map[string]any)
	if !ok {
		t.Fatalf("no setup frame: %v", srv.frames)
	}
	if got, _ := setup["model"].(string); got != "models/live-model" {
		t.Errorf("setup.model = %q, want models/live-model", got)
	}
	gen, _ := setup["generationConfig"].(map[string]any)
	if _, has := gen["inputAudioTranscription"]; !has {
		t.Errorf("setup must request inputAudioTranscription: %v", gen)
	}
	sys, _ := setup["systemInstruction"].(map[string]any)
	parts, _ := sys["parts"].([]any)
	if len(parts) == 0 {
		t.Fatalf("setup.systemInstruction has no parts: %v", setup)
	}
	first, _ := parts[0].(map[string]any)
	if text, _ := first["text"].(string); !strings.Contains(text, "Language: id.") {
		t.Errorf("system instruction = %q, want the language appended to the prompt", text)
	}
}

// TestTranscribeGeminiLive_PartialTranscriptOnClose: a graceful close with a
// transcript settles on the text; the same close with silence is a hard error.
func TestTranscribeGeminiLive_PartialTranscriptOnClose(t *testing.T) {
	tests := []struct {
		name    string
		delta   string
		wantErr bool
	}{
		{name: "a partial transcript beats a hard error", delta: "half a sen"},
		{name: "silence is a hard error", delta: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newLiveSTTServer(t, func(conn *websocket.Conn) {
				sendFrame(t, conn, map[string]any{"serverContent": map[string]any{"setupComplete": true}})
				// Drain the client's turn before sending anything. After
				// setupComplete the client streams its audio chunks and then
				// the flushing clientContent frame; closing on the first read
				// raced that write, so the close could land before the client
				// had read the delta sent below — turning a passing case into
				// "socket closed before completion" about one run in twelve.
				// Reading through turnComplete means the client is parked in
				// its read loop by the time this sends, so the close is always
				// the last thing it observes.
				for {
					_, data, err := conn.ReadMessage()
					if err != nil {
						return
					}
					var frame map[string]any
					if err := json.Unmarshal(data, &frame); err != nil {
						continue
					}
					if cc, ok := frame["clientContent"].(map[string]any); ok {
						if done, _ := cc["turnComplete"].(bool); done {
							break
						}
					}
				}
				if tt.delta != "" {
					sendFrame(t, conn, map[string]any{"serverContent": map[string]any{
						"inputTranscription": map[string]any{"text": tt.delta},
					}})
				}
				// Close without ever signalling turnComplete.
			})
			srv.upgrader.CheckOrigin = func(*http.Request) bool { return true }

			result, err := transcribeGeminiLive(context.Background(),
				"ws://"+strings.TrimPrefix(srv.URL, "http://")+"/v1beta/models",
				"live-model", "key-1", []byte{0x01, 0x02}, "audio/wav", nil)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", result)
				}
				return
			}
			if err != nil {
				t.Fatalf("transcribeGeminiLive: %v", err)
			}
			if result.text != tt.delta {
				t.Errorf("transcript = %q, want the partial %q", result.text, tt.delta)
			}
		})
	}
}

func TestTranscribeGeminiLive_EmptyAudio(t *testing.T) {
	_, err := transcribeGeminiLive(context.Background(), "https://host/v1beta/models", "m", "k", nil, "audio/wav", nil)
	var liveErr *liveSTTError
	if err == nil {
		t.Fatal("expected an error for empty audio")
	}
	if !errors.As(err, &liveErr) || liveErr.status != http.StatusBadRequest {
		t.Errorf("error = %v, want a 400 liveSTTError", err)
	}
}

func TestLiveSystemInstruction(t *testing.T) {
	tests := []struct {
		name string
		form map[string]string
		want string
	}{
		{name: "the default directive", form: nil, want: "Transcribe the spoken audio verbatim."},
		{name: "the prompt shapes the default", form: map[string]string{"prompt": "spell it out"},
			want: "spell it out"},
		{name: "the language is appended", form: map[string]string{"prompt": "spell it out", "language": "de"},
			want: "spell it out Language: de."},
		{name: "system_instruction replaces the default wholesale",
			form: map[string]string{"system_instruction": "only numbers", "prompt": "ignored", "language": "ignored"},
			want: "only numbers"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := liveSystemInstruction(tt.form); got != tt.want {
				t.Errorf("liveSystemInstruction = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLiveTimeoutFieldClampsAtMax(t *testing.T) {
	form := map[string]string{
		"setup_timeout_ms": "999999999",
		"turn_timeout_ms":  "not-a-number",
	}
	if got := liveTimeoutField(form, "setup_timeout_ms", time.Second); got != geminiLiveMaxTimeout {
		t.Errorf("an oversized timeout = %v, want the %v ceiling", got, geminiLiveMaxTimeout)
	}
	if got := liveTimeoutField(form, "turn_timeout_ms", 7*time.Second); got != 7*time.Second {
		t.Errorf("an unparseable timeout = %v, want the fallback", got)
	}
	if got := liveTimeoutField(nil, "turn_timeout_ms", 7*time.Second); got != 7*time.Second {
		t.Errorf("a missing timeout = %v, want the fallback", got)
	}
}
