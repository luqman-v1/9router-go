package mitm

import (
	"bufio"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"

	"9router/proxy/internal/mitm/handlers"
)

// toolDomains maps SNI hostname → handler function.
var toolHandlers = map[string]func(http.ResponseWriter, *http.Request, []byte){
	"cloudcode-pa.googleapis.com":       handlers.HandleAntigravity,
	"daily-cloudcode-pa.googleapis.com": handlers.HandleAntigravity,
	"chatgpt.com":                       handlers.HandleCodex,
	"api.chatgpt.com":                   handlers.HandleCodex,
	"api.githubcopilot.com":             handlers.HandleCopilot,
	"api.cursor.com":                    handlers.HandleCursor,
	"api2.cursor.com":                   handlers.HandleCursor,
	"runtime.us-east-1.kiro.dev":        handlers.HandleKiro,
}

// getHandler returns the handler for a given hostname, or nil if not intercepted.
func getHandler(host string) func(http.ResponseWriter, *http.Request, []byte) {
	host = strings.TrimSuffix(host, ":443")
	host = strings.ToLower(host)
	if h, ok := toolHandlers[host]; ok {
		return h
	}
	return nil
}

// Server is the MITM TLS proxy server.
type Server struct {
	baseDir  string
	listener net.Listener
	mu       sync.Mutex
	running  bool
}

// NewServer creates a MITM server with root CA in the given base directory.
func NewServer(baseDir string) (*Server, error) {
	_, _, err := EnsureRootCA(baseDir)
	if err != nil {
		return nil, fmt.Errorf("mitm root CA: %w", err)
	}

	return &Server{
		baseDir: baseDir,
	}, nil
}

// Start begins listening on port 443 with TLS SNI.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("MITM server already running")
	}

	caCert, caKey, err := EnsureRootCA(s.baseDir)
	if err != nil {
		return err
	}

	tlsConfig := &tls.Config{
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if getHandler(hello.ServerName) == nil {
				return nil, fmt.Errorf("domain not intercepted: %s", hello.ServerName)
			}
			leafCert, leafKey, err := GetOrCreateLeafCert(s.baseDir, hello.ServerName, caCert, caKey)
			if err != nil {
				return nil, err
			}
			leafDER, err := tls.X509KeyPair(
				certToPEM(leafCert),
				privateKeyToPEM(leafKey),
			)
			if err != nil {
				return nil, err
			}
			return &leafDER, nil
		},
	}

	listener, err := tls.Listen("tcp", ":443", tlsConfig)
	if err != nil {
		return fmt.Errorf("listen :443: %w (try: sudo or CAP_NET_BIND_SERVICE)", err)
	}

	s.listener = listener
	s.running = true

	go s.acceptLoop()

	log.Printf("[mitm] TLS proxy listening on :443")
	return nil
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if !s.running {
				return
			}
			log.Printf("[mitm] accept error: %v", err)
			continue
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return
	}
	if err := tlsConn.Handshake(); err != nil {
		return
	}

	req, err := http.ReadRequest(bufio.NewReader(tlsConn))
	if err != nil {
		return
	}

	host := req.Host
	if host == "" {
		host = tlsConn.ConnectionState().ServerName
	}

	handler := getHandler(host)
	if handler == nil {
		return
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return
	}

	rw := newResponseWriter(tlsConn)
	handler(rw, req, body)
}

// Stop gracefully shuts down the MITM server.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	if s.listener != nil {
		s.listener.Close()
	}
}

// IsRunning returns whether the server is active.
func (s *Server) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// responseWriter wraps a net.Conn as an http.ResponseWriter.
type responseWriter struct {
	conn        net.Conn
	header      http.Header
	wroteHeader bool
}

func newResponseWriter(conn net.Conn) *responseWriter {
	return &responseWriter{
		conn:   conn,
		header: make(http.Header),
	}
}

func (w *responseWriter) Header() http.Header { return w.header }

func (w *responseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.conn.Write(b)
}

func (w *responseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	status := http.StatusText(code)
	fmt.Fprintf(w.conn, "HTTP/1.1 %d %s\r\n", code, status)
	for k, vs := range w.header {
		for _, v := range vs {
			fmt.Fprintf(w.conn, "%s: %s\r\n", k, v)
		}
	}
	fmt.Fprint(w.conn, "\r\n")
}

// certToPEM returns the PEM-encoded bytes of an x509 certificate.
func certToPEM(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}

// privateKeyToPEM returns the PEM-encoded bytes of a private key.
func privateKeyToPEM(key crypto.PrivateKey) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}
