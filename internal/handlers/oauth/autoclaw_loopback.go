package oauth

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"9router/proxy/internal/log"
)

// AutoClaw's OAuth client is registered with exactly one local redirect URI:
// http://localhost:18432/auth/callback-zai. The desktop client binds that port
// for the duration of a login. chat.z.ai rejects any other redirect URI at the
// authorize step ("Redirect URI not registered for this client"), so the
// gateway must bind the same port and path.
const (
	autoclawLoopbackHost    = "localhost"
	autoclawLoopbackPort    = 18432
	autoclawLoopbackPath    = "/auth/callback-zai"
	autoclawLoopbackTimeout = 5 * time.Minute
)

type autoclawLoopbackServer struct {
	mu       sync.Mutex
	listener net.Listener
	srv      *http.Server
	store    *callbackRelay
	idle     *time.Timer
}

var autoclawLoopback = &autoclawLoopbackServer{store: defaultCallbackRelay}

// start binds the registered loopback port. Like the desktop client, one
// listener serves the process; concurrent logins from the same gateway are
// serialised by the port itself.
func (s *autoclawLoopbackServer) start() error {
	s.mu.Lock()
	if s.listener != nil {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", net.JoinHostPort(autoclawLoopbackHost, strconv.Itoa(autoclawLoopbackPort)))
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	s.mu.Lock()
	s.listener = ln
	s.srv = srv
	if s.idle != nil {
		s.idle.Stop()
	}
	s.idle = time.AfterFunc(autoclawLoopbackTimeout, s.stop)
	s.mu.Unlock()

	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			log.Warn("oauth", "autoclaw loopback listener stopped", "err", serveErr.Error())
		}
	}()
	return nil
}

func (s *autoclawLoopbackServer) stop() {
	s.mu.Lock()
	srv, idle := s.srv, s.idle
	s.srv, s.listener, s.idle = nil, nil, nil
	s.mu.Unlock()
	if idle != nil {
		idle.Stop()
	}
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			_ = srv.Close()
		}
	}
}

// ServeHTTP records the provider's redirect into the same relay the dashboard
// polls via /api/oauth/callback-result, then shows a completion page.
func (s *autoclawLoopbackServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != autoclawLoopbackPath {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	errCode := firstNonEmpty(q.Get("error"), q.Get("error_code"), q.Get("errorCode"))
	errDesc := firstNonEmpty(q.Get("error_description"), q.Get("error_desc"), q.Get("message"))
	raw := ""
	if errCode == "" {
		raw = extractCallbackRaw(q, r.URL.RawQuery)
	}
	if state != "" && (errCode != "" || raw != "") {
		s.store.record(state, raw, errCode, errDesc)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(`<!doctype html><html><body><p>AutoClaw authorization received. You can close this window and return to the dashboard.</p><script>window.close()</script></body></html>`))
	go s.stop()
}
