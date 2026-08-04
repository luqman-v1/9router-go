package media

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/config"
	"9router/proxy/internal/db"
	"9router/proxy/internal/headroom"
	"9router/proxy/internal/handlerutil"
)

// hopByHopHeaders are stripped from both forwarded requests and responses.
var hopByHopHeaders = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

// proxyLoopbackHosts is the narrower loopback set used by the reverse-proxy
// SSRF guard (0.0.0.0 is loopback for start/status, but still strips creds).
var proxyLoopbackHosts = map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}

// HeadroomHandler manages the external Headroom (token compression) proxy
// process and reverse-proxies its dashboard.
type HeadroomHandler struct {
	Repo    *db.Repo
	DataDir string
	Client  *http.Client
}

// NewHeadroomHandler creates a HeadroomHandler bound to the app data dir.
func NewHeadroomHandler(repo *db.Repo) *HeadroomHandler {
	return &HeadroomHandler{
		Repo:    repo,
		DataDir: config.ResolveDataDir(),
		Client: &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse // redirect: "manual"
			},
		},
	}
}

// writeHeadroomError writes the flat {error, code} shape the dashboard parses.
func writeHeadroomError(w http.ResponseWriter, status int, message, code string) {
	body := map[string]any{"error": message}
	if code != "" {
		body["code"] = code
	}
	handlerutil.WriteJSON(w, status, body)
}

func (h *HeadroomHandler) headroomURL() string {
	s, err := h.Repo.GetSettings()
	if err != nil || s == nil || s.HeadroomUrl == "" {
		return headroom.DefaultURL
	}
	return s.HeadroomUrl
}

func (h *HeadroomHandler) headroomFlags() (codeAware, kompress bool) {
	s, _ := h.Repo.GetSettings()
	if s == nil {
		return false, true
	}
	return s.HeadroomCodeAware, s.HeadroomKompress
}

// HandleHeadroomStart starts the Headroom proxy (POST /headroom/start).
func (h *HeadroomHandler) HandleHeadroomStart(w http.ResponseWriter, r *http.Request) {
	h.handleHeadroomStart(w, r, false)
}

// HandleHeadroomRestart stops and restarts the proxy (POST /headroom/restart).
func (h *HeadroomHandler) HandleHeadroomRestart(w http.ResponseWriter, r *http.Request) {
	h.handleHeadroomStart(w, r, true)
}

func (h *HeadroomHandler) handleHeadroomStart(w http.ResponseWriter, r *http.Request, restart bool) {
	hurl := h.headroomURL()
	if !headroom.IsLoopbackHeadroomUrl(hurl) {
		writeHeadroomError(w, http.StatusBadRequest, "External Headroom proxies must be started outside 9Router", "EXTERNAL_PROXY")
		return
	}
	port := headroom.ParsePortFromURL(hurl)
	if port == 0 {
		port = headroom.DefaultPort
	}
	codeAware, kompress := h.headroomFlags()
	var (
		res headroom.StartResult
		err error
	)
	if restart {
		res, err = headroom.RestartHeadroomProxy(h.DataDir, port, codeAware, kompress)
	} else {
		res, err = headroom.StartHeadroomProxy(h.DataDir, port, codeAware, kompress)
	}
	if err != nil {
		code := headroom.CodeOf(err)
		status := http.StatusInternalServerError
		if code == "NOT_INSTALLED" {
			status = http.StatusBadRequest
		}
		writeHeadroomError(w, status, err.Error(), code)
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":        true,
		"pid":            res.PID,
		"alreadyRunning": res.AlreadyRunning,
	})
}

// HandleHeadroomStop stops the proxy (POST /headroom/stop).
func (h *HeadroomHandler) HandleHeadroomStop(w http.ResponseWriter, r *http.Request) {
	res, err := headroom.StopHeadroomProxy(h.DataDir)
	if err != nil {
		writeHeadroomError(w, http.StatusInternalServerError, err.Error(), headroom.CodeOf(err))
		return
	}
	status := http.StatusOK
	if !res.Stopped {
		status = http.StatusConflict
	}
	handlerutil.WriteJSON(w, status, res)
}

// HandleHeadroomStatus reports proxy install/running state (GET /headroom/status).
func (h *HeadroomHandler) HandleHeadroomStatus(w http.ResponseWriter, r *http.Request) {
	hurl := h.headroomURL()
	status := headroom.GetHeadroomStatus(hurl)
	status["url"] = hurl
	pid := headroom.GetManagedPid(h.DataDir)
	if pid == 0 {
		status["managedPid"] = nil
	} else {
		status["managedPid"] = pid
	}
	handlerutil.WriteJSON(w, http.StatusOK, status)
}

// HandleHeadroomExtras handles GET/POST/DELETE /headroom/extras.
func (h *HeadroomHandler) HandleHeadroomExtras(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("log") == "1" {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"log": headroom.GetInstallLogTail(h.DataDir, 15)})
			return
		}
		st := headroom.GetInstalledHeadroomExtras("")
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"available": headroom.CompressionExtras,
			"installed": st.Installed,
			"version":   st.Version,
			"extras":    st.Extras,
		})
	case http.MethodPost:
		var body struct {
			Extras []string `json:"extras"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body) // Next: req.json().catch(() => ({}))
		result, err := headroom.InstallHeadroomExtras(h.DataDir, body.Extras)
		if err != nil {
			code := headroom.CodeOf(err)
			status := http.StatusInternalServerError
			if code == "NOT_INSTALLED" || code == "NO_PYTHON" {
				status = http.StatusBadRequest
			}
			writeHeadroomError(w, status, err.Error(), code)
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, result)
	case http.MethodDelete:
		var body struct {
			Extras []string `json:"extras"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		result, err := headroom.UninstallHeadroomExtras(h.DataDir, body.Extras)
		if err != nil {
			code := headroom.CodeOf(err)
			status := http.StatusInternalServerError
			if code == "NO_PYTHON" || code == "INVALID_EXTRAS" {
				status = http.StatusBadRequest
			}
			writeHeadroomError(w, status, err.Error(), code)
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, result)
	default:
		handlerutil.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// HandleHeadroomProxy reverse-proxies the Headroom dashboard (all methods).
func (h *HeadroomHandler) HandleHeadroomProxy(w http.ResponseWriter, r *http.Request) {
	target, err := url.Parse(h.headroomURL())
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") {
		writeHeadroomError(w, http.StatusInternalServerError, "Headroom URL must use http or https", "")
		return
	}
	sub := chi.URLParam(r, "*")
	target.Path = "/" + strings.TrimLeft(sub, "/")
	target.RawQuery = r.URL.RawQuery

	var body io.Reader
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		body = r.Body
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), body)
	if err != nil {
		writeHeadroomError(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	if body != nil {
		req.ContentLength = r.ContentLength
	}
	// Forward headers minus hop-by-hop + host.
	for k, vv := range r.Header {
		lk := strings.ToLower(k)
		if hopByHopHeaders[lk] || lk == "host" {
			continue
		}
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	// Never leak viewer credentials to a non-loopback Headroom host.
	if !proxyLoopbackHosts[strings.ToLower(target.Hostname())] {
		req.Header.Del("Cookie")
		req.Header.Del("Authorization")
	}

	resp, err := h.Client.Do(req)
	if err != nil {
		writeHeadroomError(w, http.StatusBadGateway, err.Error(), "")
		return
	}
	defer resp.Body.Close()

	if sub == "dashboard" && strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		html, err := io.ReadAll(resp.Body)
		if err != nil {
			writeHeadroomError(w, http.StatusInternalServerError, err.Error(), "")
			return
		}
		resp.Header.Del("Content-Length")
		copyProxyResponseHeaders(w, resp.Header)
		w.WriteHeader(resp.StatusCode)
		w.Write([]byte(headroom.RewriteDashboardHTML(string(html))))
		return
	}

	copyProxyResponseHeaders(w, resp.Header)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func copyProxyResponseHeaders(w http.ResponseWriter, hdr http.Header) {
	for k, vv := range hdr {
		if hopByHopHeaders[strings.ToLower(k)] {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
}
