package media

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
	json "encoding/json/v2"
)

// ucTestEnv points every persona endpoint at fakes and shortens the poll
// cadence so generation flows finish fast.
func ucTestEnv(t *testing.T) (clerk *httptest.Server, api *httptest.Server) {
	t.Helper()
	clerkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/client":
			// The rotating __client chain: every call hands back a new value.
			atomic.AddInt64(&ucClerkRotations, 1)
			w.Header().Add("Set-Cookie", "__client=rotated-client-jwt; Path=/; Secure")
			w.Write([]byte(`{"response":{"sessions":[{"id":"sess_active","status":"active"},{"id":"sess_idle","status":""}]}}`))
		case strings.HasSuffix(r.URL.Path, "/tokens"):
			w.Write([]byte(`{"jwt":"` + ucFakeJWT(map[string]any{"uid": "user_42"}) + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	t.Cleanup(func() {
		clerkServer.Close()
		apiServer.Close()
	})
	previousClerk, previousImage, previousVideo, previousSigned, previousInterval := ucClerkFAPI, ucImageURL, ucVideoURL, ucSignedURL, ucPollInterval
	ucClerkFAPI, ucImageURL, ucVideoURL, ucSignedURL, ucPollInterval = clerkServer.URL, apiServer.URL+"/v2/image-gen", apiServer.URL+"/image_to_video", apiServer.URL+"/generate-signed-url", 10*time.Millisecond
	t.Cleanup(func() {
		ucClerkFAPI, ucImageURL, ucVideoURL, ucSignedURL, ucPollInterval = previousClerk, previousImage, previousVideo, previousSigned, previousInterval
	})
	return clerkServer, apiServer
}

var ucClerkRotations int64

// ucFakeJWT builds an unverified JWT whose payload carries the claims.
func ucFakeJWT(claims map[string]any) string {
	payload, _ := json.Marshal(claims)
	return "head." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestUcDecodeJWT(t *testing.T) {
	token := ucFakeJWT(map[string]any{"uid": "user_1", "sid": "sess_9"})
	claims := ucDecodeJWT(token)
	if claims == nil || claims["uid"] != "user_1" || claims["sid"] != "sess_9" {
		t.Fatalf("claims = %v", claims)
	}
	if ucDecodeJWT("garbage") != nil || ucDecodeJWT("a.b") != nil {
		t.Error("malformed tokens must read as nil")
	}
}

func TestUcCookieJarAndRotation(t *testing.T) {
	jar := parseUcCookieJar("__client=abc; other=1; empty=; __client=dup")
	if jar["__client"] != "dup" {
		t.Errorf("last duplicate should win, got %q", jar["__client"])
	}
	if jar.header() == "" || !strings.Contains(jar.header(), "other=1") {
		t.Errorf("header rebuild lost entries: %q", jar.header())
	}

	if got := ucWithRotatedClient("__client=old; x=1", ""); got != "" {
		t.Errorf("no rotation must answer empty, got %q", got)
	}
	if got := ucWithRotatedClient("__client=old; x=1", "old"); got != "" {
		t.Errorf("same rotation must answer empty, got %q", got)
	}
	got := ucWithRotatedClient("__client=old; x=1", "new")
	if got != "__client=new; x=1" && got != "x=1; __client=new" {
		t.Errorf("rotated header = %q", got)
	}
	if !strings.Contains(got, "__client=new") || !strings.Contains(got, "x=1") {
		t.Errorf("rotated header lost a cookie: %q", got)
	}
}

func TestUcMintSession(t *testing.T) {
	clerk, _ := ucTestEnv(t)
	t.Cleanup(func() { atomic.StoreInt64(&ucClerkRotations, 0) })

	t.Run("missing __client cookie answers 401 with guidance", func(t *testing.T) {
		_, err := ucMintSession(context.Background(), "other=1")
		var ucErr *ucMediaError
		if err == nil || !strings.Contains(err.Error(), "__client") {
			t.Fatalf("expected __client guidance, got %v", err)
		}
		if !asUcError(err, &ucErr) || ucErr.status != 401 {
			t.Fatalf("expected typed 401, got %v", err)
		}
	})

	t.Run("legacy sid claim mints without the client listing", func(t *testing.T) {
		atomic.StoreInt64(&ucClerkRotations, 0)
		legacy := "__client=" + ucFakeJWT(map[string]any{"sid": "sess_legacy"})
		session, err := ucMintSession(context.Background(), legacy)
		if err != nil {
			t.Fatalf("mint: %v", err)
		}
		if session.uid != "user_42" {
			t.Errorf("uid = %q", session.uid)
		}
		if atomic.LoadInt64(&ucClerkRotations) != 0 {
			t.Error("legacy sid must skip the /v1/client listing")
		}
	})

	t.Run("client listing resolves the active session and rotation", func(t *testing.T) {
		atomic.StoreInt64(&ucClerkRotations, 0)
		session, err := ucMintSession(context.Background(), "__client=opaque")
		if err != nil {
			t.Fatalf("mint: %v", err)
		}
		if session.uid != "user_42" {
			t.Errorf("uid = %q", session.uid)
		}
		if session.rotatedClient == "" {
			t.Error("rotated __client was not surfaced")
		}
	})
	_ = clerk
}

func asUcError(err error, target **ucMediaError) bool {
	return errors.As(err, target)
}

// ucGenerationEnv fakes the persona API: a generation post answers the result
// URL, which serves HEAD 404 until ready and then the artifact bytes.
type ucGenerationEnv struct {
	api          *httptest.Server
	clerk        *httptest.Server
	posts        int
	signedPosts  int
	uploadBytes  []byte
	lastBody     map[string]any
	lastHeaders  http.Header
	resultBearer string
}

func ucWithGenerationEnv(t *testing.T) *ucGenerationEnv {
	t.Helper()
	env := &ucGenerationEnv{}
	env.clerk, env.api = ucTestEnv(t)

	ready := int32(0)
	resultURL := ""
	env.api.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/result"):
			if r.Method == http.MethodHead {
				if atomic.LoadInt32(&ready) == 1 {
					w.WriteHeader(http.StatusOK)
				} else {
					atomic.StoreInt32(&ready, 1)
					w.WriteHeader(http.StatusNotFound)
				}
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("PNGDATA"))
		case strings.HasSuffix(r.URL.Path, "/generate-signed-url"):
			env.signedPosts++
			env.resultBearer = r.Header.Get("Authorization")
			w.Write([]byte(`{"signed_url":"` + env.api.URL + `/upload", "blob_name":"blob-1"}`))
		case strings.HasSuffix(r.URL.Path, "/upload"):
			buf := make([]byte, r.ContentLength)
			r.Body.Read(buf)
			env.uploadBytes = buf
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/v2/image-gen") || strings.HasSuffix(r.URL.Path, "/image_to_video"):
			env.posts++
			env.lastHeaders = r.Header.Clone()
			buf := make([]byte, r.ContentLength)
			r.Body.Read(buf)
			_ = json.Unmarshal(buf, &env.lastBody)
			resultURL = env.api.URL + "/result"
			fmt.Fprintf(w, `{"url":%q,"timeout_seconds":1}`, resultURL)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	return env
}

// ucSeedHandler returns a MediaHandler with one uc-web cookie connection.
func ucSeedHandler(t *testing.T, cookie string) *MediaHandler {
	t.Helper()
	database, cleanup := setupMultimodalTestDB(t)
	t.Cleanup(cleanup)
	connData, _ := json.Marshal(map[string]any{"apiKey": cookie})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-uc-1', 'uc-web', 'cookie', 'UC Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	return newTestMediaHandler(db.NewRepo(database))
}

func ucCookie() string {
	return "__client=" + ucFakeJWT(map[string]any{"sid": "sess_legacy"}) + "; other=1"
}

func TestHandleUcWebImage(t *testing.T) {
	env := ucWithGenerationEnv(t)
	h := ucSeedHandler(t, ucCookie())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(
		`{"model":"uc/seedream-v4.5","prompt":"a cat","size":"1792x1024"}`))
	modelInfo := &chat.ModelInfo{Provider: "uc-web", Model: "seedream-v4.5"}
	if err := h.handleUcWebImage(rec, req, []byte(`{"model":"uc/seedream-v4.5","prompt":"a cat","size":"1792x1024"}`), modelInfo); err != nil {
		t.Fatalf("handleUcWebImage: %v", err)
	}

	if env.posts != 1 {
		t.Fatalf("generation posts = %d", env.posts)
	}
	if env.lastBody["prompt"] != "a cat" || env.lastBody["model_version"] != "seedream-v4.5" {
		t.Errorf("payload = %v", env.lastBody)
	}
	// 1792x1024 (1.75) is closest to 16:9 (1.78) within the model's aspects.
	if env.lastBody["aspect_ratio"] != "16:9" || env.lastBody["imageWidth"] != "1024" || env.lastBody["imageHeight"] != "576" {
		t.Errorf("aspect resolution = %v", env.lastBody)
	}
	if bearer := env.lastHeaders.Get("Authorization"); !strings.HasPrefix(bearer, "Bearer head.") {
		t.Errorf("Authorization = %q", bearer)
	}
	var out struct {
		Data []struct {
			B64  string `json:"b64_json"`
			Mime string `json:"mime_type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Data) != 1 {
		t.Fatalf("client body = %s (%v)", rec.Body.String(), err)
	}
	if decoded, _ := base64.StdEncoding.DecodeString(out.Data[0].B64); string(decoded) != "PNGDATA" {
		t.Errorf("artifact bytes = %q", decoded)
	}
	if out.Data[0].Mime != "image/png" {
		t.Errorf("mime = %q", out.Data[0].Mime)
	}
}

func TestHandleUcWebImageValidation(t *testing.T) {
	env := ucWithGenerationEnv(t)
	h := ucSeedHandler(t, ucCookie())

	t.Run("unknown model answers 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		err := h.handleUcWebImage(rec, httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil),
			[]byte(`{"prompt":"x"}`), &chat.ModelInfo{Provider: "uc-web", Model: "nope"})
		var ucErr *ucMediaError
		if !asUcError(err, &ucErr) || ucErr.status != 400 {
			t.Fatalf("expected typed 400, got %v", err)
		}
	})

	t.Run("resolution pins to the first allowed value", func(t *testing.T) {
		rec := httptest.NewRecorder()
		err := h.handleUcWebImage(rec, httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil),
			[]byte(`{"prompt":"x"}`), &chat.ModelInfo{Provider: "uc-web", Model: "seedream-v5-flash"})
		if err != nil {
			t.Fatalf("handleUcWebImage: %v", err)
		}
		if env.lastBody["resolution"] != "1k" {
			t.Errorf("resolution = %v", env.lastBody["resolution"])
		}
	})
}

func TestHandleUcWebVideo(t *testing.T) {
	t.Run("text-to-video posts a null blob", func(t *testing.T) {
		env := ucWithGenerationEnv(t)
		h := ucSeedHandler(t, ucCookie())

		err := h.handleUcWebVideo(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/videos/generations", nil),
			[]byte(`{"prompt":"a dance","seconds":8}`), &chat.ModelInfo{Provider: "uc-web", Model: "default"})
		if err != nil {
			t.Fatalf("handleUcWebVideo: %v", err)
		}
		if env.lastBody["media_blob_name"] != nil {
			t.Errorf("t2v must post a null blob, got %v", env.lastBody["media_blob_name"])
		}
		if env.lastBody["seconds"] != 8.0 && env.lastBody["seconds"] != 8 {
			t.Errorf("seconds = %v", env.lastBody["seconds"])
		}
		if env.lastBody["num_frames"] != 129.0 && env.lastBody["num_frames"] != 129 {
			t.Errorf("num_frames = %v", env.lastBody["num_frames"])
		}
		if env.signedPosts != 0 {
			t.Error("t2v must not touch the signed-url endpoint")
		}
	})

	t.Run("image-to-video uploads through the signed URL", func(t *testing.T) {
		env := ucWithGenerationEnv(t)
		h := ucSeedHandler(t, ucCookie())
		dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("INPUTIMAGE"))

		err := h.handleUcWebVideo(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/videos/generations", nil),
			[]byte(`{"prompt":"animate","image":"`+dataURL+`"}`), &chat.ModelInfo{Provider: "uc-web", Model: "wan-2.6"})
		if err != nil {
			t.Fatalf("handleUcWebVideo: %v", err)
		}
		if env.signedPosts != 1 {
			t.Fatalf("signed-url posts = %d", env.signedPosts)
		}
		if !strings.HasPrefix(env.resultBearer, "Bearer head.") {
			t.Errorf("signed-url Authorization = %q", env.resultBearer)
		}
		if string(env.uploadBytes) != "INPUTIMAGE" {
			t.Errorf("uploaded bytes = %q", env.uploadBytes)
		}
		if env.lastBody["media_blob_name"] != "blob-1" {
			t.Errorf("blob = %v", env.lastBody["media_blob_name"])
		}
	})

	t.Run("an unknown model refuses the request", func(t *testing.T) {
		ucWithGenerationEnv(t)
		h := ucSeedHandler(t, ucCookie())
		err := h.handleUcWebVideo(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/videos/generations", nil),
			[]byte(`{"prompt":"x"}`), &chat.ModelInfo{Provider: "uc-web", Model: "not-a-model"})
		var ucErr *ucMediaError
		if !asUcError(err, &ucErr) || ucErr.status != 400 {
			t.Fatalf("expected typed 400, got %v", err)
		}
	})
}

func TestUcAspectFor(t *testing.T) {
	tests := []struct {
		name    string
		size    string
		allowed []string
		want    string
	}{
		{"direct ratio", "16:9", nil, "16:9"},
		{"direct ratio outside the allowed list falls back", "16:9", []string{"1:1", "9:16"}, "1:1"},
		{"closest WxH", "1792x1024", []string{"1:1", "16:9", "9:16"}, "16:9"},
		{"portrait WxH", "768x1344", nil, "9:16"},
		{"garbage falls back to the first allowed", "banana", []string{"3:4"}, "3:4"},
		{"garbage with no list falls back to 1:1", "banana", nil, "1:1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ucAspectFor(tt.size, tt.allowed)
			if got["aspect_ratio"] != tt.want {
				t.Errorf("ucAspectFor(%q, %v) = %v, want aspect %q", tt.size, tt.allowed, got, tt.want)
			}
			if got["imageWidth"] == nil || got["imageHeight"] == nil {
				t.Errorf("pixel size missing: %v", got)
			}
		})
	}
}

func TestValidateUcWebCookie(t *testing.T) {
	ucTestEnv(t)
	valid, message := ValidateUcWebCookie(context.Background(), ucCookie())
	if !valid || message != "" {
		t.Fatalf("valid=%v message=%q", valid, message)
	}
	valid, message = ValidateUcWebCookie(context.Background(), "other=1")
	if valid || message == "" {
		t.Fatalf("a cookie without __client must fail with guidance, got %v %q", valid, message)
	}
}
