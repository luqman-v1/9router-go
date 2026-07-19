package mitm

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// Manager handles MITM proxy lifecycle.
type Manager struct {
	server  *Server
	baseDir string
}

// NewManager creates a new MITM manager.
func NewManager(baseDir string) *Manager {
	return &Manager{baseDir: baseDir}
}

// Enable starts the MITM proxy: installs DNS entries, ensures CA, starts server.
func (m *Manager) Enable() error {
	mitmDir := MITMDir(m.baseDir)
	if err := os.MkdirAll(mitmDir, 0700); err != nil {
		return fmt.Errorf("create mitm dir: %w", err)
	}

	// 1. Install DNS entries (redirect domains to localhost)
	log.Printf("[mitm] adding DNS entries...")
	if err := AddHostsEntries(); err != nil {
		return fmt.Errorf("DNS entries: %w (try: sudo %s)", err, os.Args[0])
	}

	// 2. Ensure CA cert exists
	log.Printf("[mitm] ensuring root CA...")
	server, err := NewServer(m.baseDir)
	if err != nil {
		RemoveHostsEntries()
		return fmt.Errorf("CA setup: %w", err)
	}

	// 3. Start TLS server
	log.Printf("[mitm] starting TLS proxy on :443...")
	if err := server.Start(); err != nil {
		RemoveHostsEntries()
		return fmt.Errorf("server start: %w", err)
	}

	m.server = server
	log.Printf("[mitm] MITM proxy enabled — intercepting %d domains", len(AllDomains()))
	return nil
}

// Disable stops the MITM proxy: stops server, removes DNS entries.
func (m *Manager) Disable() error {
	if m.server != nil {
		m.server.Stop()
		m.server = nil
	}

	if err := RemoveHostsEntries(); err != nil {
		return fmt.Errorf("remove DNS entries: %w", err)
	}

	// Note: root CA cert stays installed (removing would break existing TLS sessions)
	log.Printf("[mitm] MITM proxy disabled")
	return nil
}

// Status returns the current MITM status.
func (m *Manager) Status() map[string]any {
	status := map[string]any{
		"running": false,
	}

	if m.server != nil && m.server.IsRunning() {
		status["running"] = true
	}

	dnsStatus, err := CheckDNSStatus()
	if err == nil {
		status["dns"] = dnsStatus
	}

	certPath, _ := rootCAPaths(m.baseDir)
	if _, err := os.Stat(certPath); err == nil {
		status["ca_installed"] = true
		status["ca_path"] = certPath
	} else {
		status["ca_installed"] = false
	}

	status["mitm_dir"] = MITMDir(m.baseDir)
	status["cert_dir"] = CertDir(m.baseDir)

	return status
}

// ConfigFilePath returns the path to the MITM config file.
func ConfigFilePath(baseDir string) string {
	return filepath.Join(MITMDir(baseDir), "config.json")
}

// LogDir returns the MITM log directory.
func LogDir(baseDir string) string {
	d := filepath.Join(MITMDir(baseDir), "logs")
	os.MkdirAll(d, 0700)
	return d
}
