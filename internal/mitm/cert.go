package mitm

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"time"
)

const (
	mitmDirName  = "mitm"
	caCertName   = "rootCA.pem"
	caKeyName    = "rootCA-key.pem"
	certDirName  = "certs"
	leafValidity = 365 * 24 * time.Hour
	caValidity   = 10 * 365 * 24 * time.Hour
)

// MITMDir returns the MITM data directory.
func MITMDir(baseDir string) string {
	return filepath.Join(baseDir, mitmDirName)
}

// CertDir returns the per-domain cert cache directory.
func CertDir(baseDir string) string {
	return filepath.Join(MITMDir(baseDir), certDirName)
}

// rootCAPaths returns paths to root CA files.
func rootCAPaths(baseDir string) (certPath, keyPath string) {
	d := MITMDir(baseDir)
	return filepath.Join(d, caCertName), filepath.Join(d, caKeyName)
}

// ensureMITMDir creates MITM directory structure.
func ensureMITMDir(baseDir string) error {
	return os.MkdirAll(CertDir(baseDir), 0700)
}

// EnsureRootCA generates and installs a root CA if one doesn't exist.
// Returns the parsed certificate.
func EnsureRootCA(baseDir string) (*x509.Certificate, crypto.PrivateKey, error) {
	if err := ensureMITMDir(baseDir); err != nil {
		return nil, nil, fmt.Errorf("create mitm dir: %w", err)
	}

	certPath, keyPath := rootCAPaths(baseDir)
	if caCert, caKey, err := loadRootCA(certPath, keyPath); err == nil {
		return caCert, caKey, nil
	}

	caCert, caKey, err := generateRootCA()
	if err != nil {
		return nil, nil, fmt.Errorf("generate root CA: %w", err)
	}

	if err := saveRootCA(certPath, keyPath, caCert, caKey); err != nil {
		return nil, nil, fmt.Errorf("save root CA: %w", err)
	}

	// Non-fatal: cert file is saved; install manually if sudo unavailable.
	if err := installRootCA(certPath); err != nil {
		fmt.Fprintf(os.Stderr, "[mitm] warning: root CA install failed: %v (cert saved at %s)\n", err, certPath)
	}

	return caCert, caKey, nil
}

func loadRootCA(certPath, keyPath string) (*x509.Certificate, crypto.PrivateKey, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, fmt.Errorf("no PEM block in root CA cert")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("no PEM block in root CA key")
	}
	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}

	return cert, key.(crypto.PrivateKey), nil
}

func generateRootCA() (*x509.Certificate, crypto.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "9router MITM Root CA",
			Organization: []string{"9router"},
		},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, err
	}

	return cert, key, nil
}

func saveRootCA(certPath, keyPath string, cert *x509.Certificate, key crypto.PrivateKey) error {
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		return err
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return err
	}

	return nil
}

func installRootCA(certPath string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("sudo", "security", "add-trusted-cert", "-d", "-r", "trustRoot", "-k", "/Library/Keychains/System.keychain", certPath).Run()
	case "linux":
		dest := "/usr/local/share/ca-certificates/9router-mitm-root.crt"
		if err := exec.Command("sudo", "cp", certPath, dest).Run(); err != nil {
			return err
		}
		return exec.Command("sudo", "update-ca-certificates").Run()
	default:
		currentUser, err := user.Current()
		if err != nil {
			return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
		}
		fmt.Fprintf(os.Stderr, "[mitm] Install root CA manually:\n  cp %s %s/.9router-mitm-root.crt\n", certPath, currentUser.HomeDir)
		return nil
	}
}

// GenerateLeafCert creates a TLS certificate for the given domain, signed by the root CA.
func GenerateLeafCert(domain string, caCert *x509.Certificate, caKey crypto.PrivateKey) (*x509.Certificate, crypto.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}

	// Subject Key Identifier from public key
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	ski := sha1.Sum(pubDER)

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: domain,
		},
		DNSNames:              []string{domain},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(leafValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		SubjectKeyId:          ski[:],
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, err
	}

	return cert, key, nil
}

// GetOrCreateLeafCert returns a cached leaf cert for domain, generating one if needed.
func GetOrCreateLeafCert(baseDir, domain string, caCert *x509.Certificate, caKey crypto.PrivateKey) (*x509.Certificate, crypto.PrivateKey, error) {
	certDir := CertDir(baseDir)
	certPath := filepath.Join(certDir, domain+".pem")
	keyPath := filepath.Join(certDir, domain+"-key.pem")

	if certPEM, err := os.ReadFile(certPath); err == nil {
		if keyPEM, err := os.ReadFile(keyPath); err == nil {
			certBlock, _ := pem.Decode(certPEM)
			keyBlock, _ := pem.Decode(keyPEM)
			if certBlock != nil && keyBlock != nil {
				if cert, err := x509.ParseCertificate(certBlock.Bytes); err == nil {
					if cert.NotAfter.After(time.Now()) {
						if key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes); err == nil {
							return cert, key.(crypto.PrivateKey), nil
						}
					}
				}
			}
		}
	}

	cert, key, err := GenerateLeafCert(domain, caCert, caKey)
	if err != nil {
		return nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	os.WriteFile(certPath, certPEM, 0644)

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err == nil {
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
		os.WriteFile(keyPath, keyPEM, 0600)
	}

	return cert, key, nil
}
