package mitm

import (
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAllDomains(t *testing.T) {
	domains := AllDomains()
	if len(domains) == 0 {
		t.Fatal("expected at least one intercepted domain")
	}
	found := false
	for _, d := range domains {
		if d == "cloudcode-pa.googleapis.com" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected cloudcode-pa.googleapis.com in domains")
	}
}

func TestGetHandler_stripsPort(t *testing.T) {
	h := getHandler("cloudcode-pa.googleapis.com:443")
	if h == nil {
		t.Error("expected handler even with :443 port suffix")
	}
}

func TestCertToPEM(t *testing.T) {
	caCert, _, err := generateRootCA()
	if err != nil {
		t.Fatalf("generateRootCA: %v", err)
	}
	pemBytes := certToPEM(caCert)
	if len(pemBytes) == 0 {
		t.Fatal("expected non-empty PEM")
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("expected valid PEM block")
	}
	if block.Type != "CERTIFICATE" {
		t.Errorf("expected CERTIFICATE block, got %s", block.Type)
	}
}

func TestGenerateLeafCert(t *testing.T) {
	caCert, caKey, err := generateRootCA()
	if err != nil {
		t.Fatalf("generateRootCA: %v", err)
	}

	leafCert, leafKey, err := GenerateLeafCert("test.example.com", caCert, caKey)
	if err != nil {
		t.Fatalf("GenerateLeafCert: %v", err)
	}
	if leafCert == nil || leafKey == nil {
		t.Fatal("expected non-nil cert and key")
	}

	if len(leafCert.DNSNames) != 1 || leafCert.DNSNames[0] != "test.example.com" {
		t.Errorf("expected DNSNames [test.example.com], got %v", leafCert.DNSNames)
	}

	if err := leafCert.CheckSignatureFrom(caCert); err != nil {
		t.Errorf("leaf cert signature not valid: %v", err)
	}
}

func TestPrivateKeyToPEM(t *testing.T) {
	_, caKey, err := generateRootCA()
	if err != nil {
		t.Fatalf("generateRootCA: %v", err)
	}
	pemBytes := privateKeyToPEM(caKey)
	if len(pemBytes) == 0 {
		t.Fatal("expected non-empty PEM")
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("expected valid PEM block")
	}
	if block.Type != "PRIVATE KEY" {
		t.Errorf("expected PRIVATE KEY block, got %s", block.Type)
	}
}

func TestRootCASaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "testCA.pem")
	keyPath := filepath.Join(dir, "testCA-key.pem")

	caCert, caKey, err := generateRootCA()
	if err != nil {
		t.Fatalf("generateRootCA: %v", err)
	}

	if err := saveRootCA(certPath, keyPath, caCert, caKey); err != nil {
		t.Fatalf("saveRootCA: %v", err)
	}

	loadedCert, loadedKey, err := loadRootCA(certPath, keyPath)
	if err != nil {
		t.Fatalf("loadRootCA: %v", err)
	}

	if !loadedCert.IsCA {
		t.Error("loaded cert should be CA")
	}
	if loadedKey == nil {
		t.Error("loaded key should not be nil")
	}
}

func TestHostsEntries(t *testing.T) {
	entries := hostsEntries()
	if !strings.Contains(entries, "cloudcode-pa.googleapis.com") {
		t.Error("expected antigravity domain in entries")
	}
	if !strings.Contains(entries, "chatgpt.com") {
		t.Error("expected codex domain in entries")
	}
	if !strings.Contains(entries, "# 9router-mitm") {
		t.Error("expected marker comment")
	}
}

func TestEnsureRootCA(t *testing.T) {
	dir := t.TempDir()

	cert, key, err := EnsureRootCA(dir)
	if err != nil {
		t.Fatalf("EnsureRootCA: %v", err)
	}
	if cert == nil || key == nil {
		t.Fatal("expected non-nil cert and key")
	}

	cert2, key2, err := EnsureRootCA(dir)
	if err != nil {
		t.Fatalf("EnsureRootCA second call: %v", err)
	}
	if !cert.Equal(cert2) {
		t.Error("second call should return cached cert")
	}
	_ = key2
}

func TestMITMDir(t *testing.T) {
	dir := MITMDir("/tmp/test")
	if !strings.HasSuffix(dir, "/tmp/test/mitm") {
		t.Errorf("expected /tmp/test/mitm, got %s", dir)
	}
}

func TestCertDir(t *testing.T) {
	dir := CertDir("/tmp/test")
	if !strings.HasSuffix(dir, "/tmp/test/mitm/certs") {
		t.Errorf("expected /tmp/test/mitm/certs, got %s", dir)
	}
}

func TestGetOrCreateLeafCert(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey, err := EnsureRootCA(dir)
	if err != nil {
		t.Fatalf("EnsureRootCA: %v", err)
	}

	cert1, key1, err := GetOrCreateLeafCert(dir, "test.example.com", caCert, caKey)
	if err != nil {
		t.Fatalf("GetOrCreateLeafCert: %v", err)
	}

	cert2, key2, err := GetOrCreateLeafCert(dir, "test.example.com", caCert, caKey)
	if err != nil {
		t.Fatalf("GetOrCreateLeafCert second call: %v", err)
	}

	if !cert1.Equal(cert2) {
		t.Error("second call should return cached cert")
	}
	_ = key1
	_ = key2
}

func TestManagerStatus_noInit(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	status := mgr.Status()
	if running, _ := status["running"].(bool); running {
		t.Error("expected not running before Enable")
	}
}

func TestNewServer_noCA(t *testing.T) {
	dir := t.TempDir()
	os.RemoveAll(dir)

	srv, err := NewServer(dir)
	if err != nil {
		t.Fatalf("NewServer should create CA: %v", err)
	}
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
}
