package config

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"9router/proxy/internal/constants"
)

// loadDotenv reads key=value pairs from .env file and sets them as env vars.
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
		// envOr checks os.Getenv first, so existing env vars take precedence
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

// LoadConfig loads the configuration from environment variables and platform defaults.
func LoadConfig() *Config {
	loadDotenv(".env")
	portStr := os.Getenv("PORT")
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		port = 20128 // Default port
	}

	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			if runtime.GOOS == "windows" {
				appData := os.Getenv("APPDATA")
				if appData == "" {
					appData = filepath.Join(homeDir, "AppData", "Roaming")
				}
				dataDir = filepath.Join(appData, "9router")
			} else {
				dataDir = filepath.Join(homeDir, ".9router")
			}
		} else {
			dataDir = ".9router" // fallback
		}
	}

	// Ensure the base data directory exists
	if err := os.MkdirAll(dataDir, constants.FilePermDir); err != nil {
		log.Printf("[config] warning: could not create data directory %s: %v", dataDir, err)
	}

	// Database file: DB_PATH overrides default DATA_DIR/db/data.sqlite
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(dataDir, "db", "data.sqlite")
	}

	initialPassword := os.Getenv("INITIAL_PASSWORD")
	if initialPassword == "" {
		initialPassword = "123456" // Default fallback password
	}

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
		return "fallback-insecure-jwt-secret"
	}

	generated := hex.EncodeToString(bytes)
	if err := os.WriteFile(secretFile, []byte(generated), constants.FilePermKey); err != nil {
		log.Printf("[config] warning: could not write JWT secret file %s: %v", secretFile, err)
	}
	return generated
}
