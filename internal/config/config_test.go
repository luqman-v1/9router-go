package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	// Clean env values we'll test to ensure test predictability
	origPort := os.Getenv("PORT")
	origDataDir := os.Getenv("DATA_DIR")
	origJwtSecret := os.Getenv("JWT_SECRET")
	origInitialPassword := os.Getenv("INITIAL_PASSWORD")
	origApiKeySecret := os.Getenv("API_KEY_SECRET")
	origMachineIDSalt := os.Getenv("MACHINE_ID_SALT")

	defer func() {
		os.Setenv("PORT", origPort)
		os.Setenv("DATA_DIR", origDataDir)
		os.Setenv("JWT_SECRET", origJwtSecret)
		os.Setenv("INITIAL_PASSWORD", origInitialPassword)
		os.Setenv("API_KEY_SECRET", origApiKeySecret)
		os.Setenv("MACHINE_ID_SALT", origMachineIDSalt)
	}()

	// Create temp directory for DATA_DIR testing
	tempDir, err := os.MkdirTemp("", "test_config_dir_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Set test environment variables
	os.Setenv("PORT", "20129")
	os.Setenv("DATA_DIR", tempDir)
	os.Setenv("JWT_SECRET", "test-secret-value-123456")
	os.Setenv("INITIAL_PASSWORD", "custom-password")
	os.Setenv("API_KEY_SECRET", "custom-api-key-secret")
	os.Setenv("MACHINE_ID_SALT", "custom-salt")

	cfg := LoadConfig()

	if cfg.Port != 20129 {
		t.Errorf("expected port 20129, got %d", cfg.Port)
	}
	expectedDbPath := filepath.Join(tempDir, "db", "data.sqlite")
	if cfg.DatabasePath != expectedDbPath {
		t.Errorf("expected db path %s, got %s", expectedDbPath, cfg.DatabasePath)
	}
	if cfg.JWTSecret != "test-secret-value-123456" {
		t.Errorf("expected jwt secret, got %s", cfg.JWTSecret)
	}
	if cfg.InitialPassword != "custom-password" {
		t.Errorf("expected custom-password, got %s", cfg.InitialPassword)
	}
	if cfg.APIKeySecret != "custom-api-key-secret" {
		t.Errorf("expected custom-api-key-secret, got %s", cfg.APIKeySecret)
	}
	if cfg.MachineIDSalt != "custom-salt" {
		t.Errorf("expected custom-salt, got %s", cfg.MachineIDSalt)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	// Clean env values we'll test to ensure test predictability
	origPort := os.Getenv("PORT")
	origDataDir := os.Getenv("DATA_DIR")
	origJwtSecret := os.Getenv("JWT_SECRET")
	origInitialPassword := os.Getenv("INITIAL_PASSWORD")
	origApiKeySecret := os.Getenv("API_KEY_SECRET")
	origMachineIDSalt := os.Getenv("MACHINE_ID_SALT")

	defer func() {
		os.Setenv("PORT", origPort)
		os.Setenv("DATA_DIR", origDataDir)
		os.Setenv("JWT_SECRET", origJwtSecret)
		os.Setenv("INITIAL_PASSWORD", origInitialPassword)
		os.Setenv("API_KEY_SECRET", origApiKeySecret)
		os.Setenv("MACHINE_ID_SALT", origMachineIDSalt)
	}()

	// Clear out environment to test defaults
	os.Setenv("PORT", "")
	os.Setenv("JWT_SECRET", "")
	os.Setenv("INITIAL_PASSWORD", "")
	os.Setenv("API_KEY_SECRET", "")
	os.Setenv("MACHINE_ID_SALT", "")

	// Create temp directory for DATA_DIR testing
	tempDir, err := os.MkdirTemp("", "test_config_defaults_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	os.Setenv("DATA_DIR", tempDir)

	cfg := LoadConfig()

	if cfg.Port != 20128 { // Default port
		t.Errorf("expected default port 20128, got %d", cfg.Port)
	}
	if cfg.InitialPassword != "123456" {
		t.Errorf("expected default password 123456, got %s", cfg.InitialPassword)
	}
	if cfg.APIKeySecret != "endpoint-proxy-api-key-secret" {
		t.Errorf("expected default api-key-secret, got %s", cfg.APIKeySecret)
	}
	if cfg.MachineIDSalt != "endpoint-proxy-salt" {
		t.Errorf("expected default salt, got %s", cfg.MachineIDSalt)
	}

	// Verify JWT secret is auto-generated and saved to file
	jwtSecretFile := filepath.Join(tempDir, "jwt-secret")
	if _, err := os.Stat(jwtSecretFile); os.IsNotExist(err) {
		t.Error("expected jwt-secret file to be created")
	}

	// Loading again should read the saved secret
	cfg2 := LoadConfig()
	if cfg2.JWTSecret != cfg.JWTSecret {
		t.Errorf("expected second load to return same jwt secret %s, got %s", cfg.JWTSecret, cfg2.JWTSecret)
	}
}

func TestLoadConfigInvalidPort(t *testing.T) {
	origPort := os.Getenv("PORT")
	defer os.Setenv("PORT", origPort)

	os.Setenv("PORT", "abc") // invalid number
	cfg := LoadConfig()
	if cfg.Port != 20128 {
		t.Errorf("expected fallback port 20128 for invalid port, got %d", cfg.Port)
	}

	os.Setenv("PORT", "-1") // negative port
	cfg2 := LoadConfig()
	if cfg2.Port != 20128 {
		t.Errorf("expected fallback port 20128 for negative port, got %d", cfg2.Port)
	}
}
