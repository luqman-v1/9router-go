package media

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// veoFreeEnv fakes the WordPress site over TLS (the artifact download only
// accepts https): a homepage carrying the nonce cookie flow, the AJAX
// endpoint, and an optional fallback handler for artifact paths.
func veoFreeEnv(t *testing.T, ajax http.HandlerFunc, extra ...http.HandlerFunc) *httptest.Server {
	t.Helper()
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	if len(extra) > 0 {
		fallback = extra[0]
	}
	site := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Add("Set-Cookie", "wp_session=abc; Path=/")
			w.Write([]byte(`<html> config = {"nonce":"deadbeef01"}; </html>`))
		case "/wp-admin/admin-ajax.php":
			ajax(w, r)
		default:
			fallback(w, r)
		}
	}))
	t.Cleanup(site.Close)

	pool := x509.NewCertPool()
	pool.AddCert(site.Certificate())
	prevBase, prevAjax, prevInterval, prevWait, prevClient, prevAllow :=
		veoFreeBaseURL, veoFreeAJAXURL, veoFreePollInterval, veoFreeReadyWait, veoFreeClient, veoFreeAllowPrivateHosts
	veoFreeBaseURL = site.URL
	veoFreeAJAXURL = site.URL + "/wp-admin/admin-ajax.php"
	veoFreePollInterval = 10 * time.Millisecond
	veoFreeReadyWait = time.Millisecond
	veoFreeAllowPrivateHosts = true
	veoFreeClient = &http.Client{
		Timeout: veoFreeFetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}
	t.Cleanup(func() {
		veoFreeBaseURL = prevBase
		veoFreeAJAXURL = prevAjax
		veoFreePollInterval = prevInterval
		veoFreeReadyWait = prevWait
		veoFreeClient = prevClient
		veoFreeAllowPrivateHosts = prevAllow
	})
	return site
}

func TestVeoFreeAspect(t *testing.T) {
	tests := []struct {
		size   string
		prefix string
		want   string
	}{
		{"1792x1024", "VIDEO_ASPECT_RATIO", "VIDEO_ASPECT_RATIO_LANDSCAPE"},
		{"9:16", "VIDEO_ASPECT_RATIO", "VIDEO_ASPECT_RATIO_PORTRAIT"},
		{"portrait", "IMAGE_ASPECT_RATIO", "IMAGE_ASPECT_RATIO_PORTRAIT"},
		{"1:1", "IMAGE_ASPECT_RATIO", "IMAGE_ASPECT_RATIO_SQUARE"},
		{"square", "IMAGE_ASPECT_RATIO", "IMAGE_ASPECT_RATIO_SQUARE"},
		{"", "VIDEO_ASPECT_RATIO", "VIDEO_ASPECT_RATIO_LANDSCAPE"},
	}
	for _, tt := range tests {
		if got := veoFreeAspect(tt.size, tt.prefix); got != tt.want {
			t.Errorf("veoFreeAspect(%q, %s) = %q, want %q", tt.size, tt.prefix, got, tt.want)
		}
	}
}

func TestVeoFreeSessionAndNonce(t *testing.T) {
	veoFreeEnv(t, func(w http.ResponseWriter, r *http.Request) {})
	session := &veoFreeSession{}
	nonce, err := veoFreeFetchNonce(context.Background(), session)
	if err != nil {
		t.Fatalf("nonce: %v", err)
	}
	if nonce != "deadbeef01" {
		t.Errorf("nonce = %q", nonce)
	}
	if !strings.Contains(session.cookieHeader(), "wp_session=abc") {
		t.Errorf("session cookie not merged: %q", session.cookieHeader())
	}
}

func TestHandleVeoFreeWebImage(t *testing.T) {
	var lastForm string
	veoFreeEnv(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		lastForm = r.PostForm.Encode()
		w.Write([]byte("https://cdn.example/a.png, aGVsbG8="))
	})
	h := newTestMediaHandler(nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	if err := h.handleVeoFreeWebImage(rec, req, []byte(`{"prompt":"a cat","size":"9:16"}`), nil); err != nil {
		t.Fatalf("handleVeoFreeWebImage: %v", err)
	}
	if !strings.Contains(lastForm, "actionType=banan-image-generator") ||
		!strings.Contains(lastForm, "aspectRatioIMG=IMAGE_ASPECT_RATIO_PORTRAIT") ||
		!strings.Contains(lastForm, "nonce=deadbeef01") {
		t.Errorf("ajax form = %q", lastForm)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"url":"https://cdn.example/a.png"`) || !strings.Contains(body, `"b64_json":"aGVsbG8="`) {
		t.Errorf("client body = %s", body)
	}
}

func TestHandleVeoFreeWebVideo(t *testing.T) {
	mp4 := append([]byte("aaaaftyp"), make([]byte, 16)...)
	polls := 0
	// Artifact host: 404 once (retryable), then the bytes.
	artifactReady := false
	var site *httptest.Server
	site = veoFreeEnv(t,
		func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			switch r.PostForm.Get("actionType") {
			case "full-video-generate":
				w.Write([]byte("SCENEDATA-1"))
			case "final-video-results":
				polls++
				if polls < 2 {
					w.Write([]byte("0"))
					return
				}
				w.Write([]byte("processing, " + site.URL + "/artifacts/v.mp4"))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/artifacts/v.mp4" {
				if !artifactReady {
					artifactReady = true
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "video/mp4")
				w.Write(mp4)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		},
	)

	h := newTestMediaHandler(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/videos/generations", nil)
	if err := h.handleVeoFreeWebVideo(rec, req, []byte(`{"prompt":"a dance"}`), nil); err != nil {
		t.Fatalf("handleVeoFreeWebVideo: %v", err)
	}
	if polls != 2 {
		t.Errorf("polls = %d, want 2 (the rejected answer must be retried)", polls)
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
	if decoded, _ := base64.StdEncoding.DecodeString(out.Data[0].B64); string(decoded) != string(mp4) {
		t.Error("artifact bytes mismatch")
	}
	if out.Data[0].Mime != "video/mp4" {
		t.Errorf("mime = %q", out.Data[0].Mime)
	}
}

func TestVeoFreeArtifactGuards(t *testing.T) {
	t.Run("private artifact host is refused", func(t *testing.T) {
		if err := veoFreeAssertPublicHost(context.Background(), "127.0.0.1"); err == nil {
			t.Fatal("loopback must be refused")
		}
		if err := veoFreeAssertPublicHost(context.Background(), "10.0.0.5"); err == nil {
			t.Fatal("private v4 must be refused")
		}
	})
	t.Run("non-mp4 artifact is refused", func(t *testing.T) {
		site := veoFreeEnv(t, func(w http.ResponseWriter, r *http.Request) {}, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html>not a video</html>"))
		})
		if _, err := veoFreeDownload(context.Background(), &veoFreeSession{}, site.URL+"/x.mp4"); err == nil {
			t.Fatal("html artifact must be refused")
		}
	})
	t.Run("http artifact URL is refused", func(t *testing.T) {
		if _, err := veoFreeDownload(context.Background(), &veoFreeSession{}, "http://example.com/x.mp4"); err == nil {
			t.Fatal("plain http must be refused")
		}
	})
}
