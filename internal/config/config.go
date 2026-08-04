package config

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"9router/proxy/internal/log"
		"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"9router/proxy/internal/constants"
)

// loadDotenv reads key=value pairs from .env file and sets them as env vars.
// Supports single/double-quoted values, strips inline `#` comments (except
// inside quotes), and never overrides an existing environment variable.
func loadDotenv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		} else if idx := strings.IndexByte(v, '#'); idx >= 0 {
			v = strings.TrimSpace(v[:idx])
		}
		// Existing env vars take precedence
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

// Config holds the proxy gateway configuration.
type Config struct {
	Port            int
	DatabasePath    string
	JWTSecret       string
	InitialPassword string
	APIKeySecret    string
	MachineIDSalt   string
	RTKEnabled      bool
	CavemanEnabled  bool
	PonytailEnabled bool
}

// ResolveDataDir returns the base data directory: DATA_DIR env, else the
// platform default (~/.9router, or %APPDATA%/9router on Windows).
func ResolveDataDir() string {
	if dataDir := os.Getenv("DATA_DIR"); dataDir != "" {
		return dataDir
	}
	if homeDir, err := os.UserHomeDir(); err == nil {
		if runtime.GOOS == "windows" {
			appData := os.Getenv("APPDATA")
			if appData == "" {
				appData = filepath.Join(homeDir, "AppData", "Roaming")
			}
			return filepath.Join(appData, "9router")
		}
		return filepath.Join(homeDir, ".9router")
	}
	return ".9router"
}

// LoadConfig loads the configuration from environment variables and platform defaults.
func LoadConfig() *Config {
	loadDotenv(".env")
	portStr := os.Getenv("PORT")
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		port = 20128 // Default port
	}

	dataDir := ResolveDataDir()

	// Ensure the base data directory exists
	if err := os.MkdirAll(dataDir, constants.FilePermDir); err != nil {
		log.Warn("config", "create data dir failed", "dir", dataDir, "error", err)
	}

	// Database file: DB_PATH overrides default DATA_DIR/db/data.sqlite
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(dataDir, "db", "data.sqlite")
	}

	// INITIAL_PASSWORD has no hardcoded default — an empty value forces the
	// operator to set one explicitly rather than shipping a known password.
	initialPassword := os.Getenv("INITIAL_PASSWORD")

	apiKeySecret := os.Getenv("API_KEY_SECRET")
	if apiKeySecret == "" {
		apiKeySecret = "endpoint-proxy-api-key-secret"
	}

	machineIDSalt := os.Getenv("MACHINE_ID_SALT")
	if machineIDSalt == "" {
		machineIDSalt = "endpoint-proxy-salt"
	}

	rtkEnabled := os.Getenv("RTK_ENABLED") != "false" // default on
	cavemanEnabled := os.Getenv("CAVEMAN_ENABLED") == "true"
	ponytailEnabled := os.Getenv("PONYTAIL_ENABLED") == "true"

	return &Config{
		Port:            port,
		DatabasePath:    dbPath,
		JWTSecret:       loadJWTSecret(dataDir),
		InitialPassword: initialPassword,
		APIKeySecret:    apiKeySecret,
		MachineIDSalt:   machineIDSalt,
		RTKEnabled:      rtkEnabled,
		CavemanEnabled:  cavemanEnabled,
		PonytailEnabled: ponytailEnabled,
	}
}

func loadJWTSecret(dataDir string) string {
	secret := os.Getenv("JWT_SECRET")
	if secret != "" {
		return secret
	}

	secretFile := filepath.Join(dataDir, "jwt-secret")
	data, err := os.ReadFile(secretFile)
	if err == nil {
		return string(data)
	}

	// Generate 32 cryptographically secure random bytes
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		log.Error("config", "crypto/rand failed to generate JWT secret; refusing to fall back to a static secret", "error", err)
		return ""
	}

	generated := hex.EncodeToString(bytes)
	if err := os.WriteFile(secretFile, []byte(generated), constants.FilePermKey); err != nil {
		log.Warn("config", "write JWT secret failed", "file", secretFile, "error", err)
	}
	return generated
}
