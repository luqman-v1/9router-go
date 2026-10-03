package config

import (
	"os"
	"path/filepath"
	"strings"
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

	if cfg.Port != 20130 { // Default port
		t.Errorf("expected default port 20130, got %d", cfg.Port)
	}
	if cfg.InitialPassword != "" {
		t.Errorf("expected no default password (operator must set INITIAL_PASSWORD), got %s", cfg.InitialPassword)
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
	if cfg.Port != 20130 {
		t.Errorf("expected fallback port 20130 for invalid port, got %d", cfg.Port)
	}

	os.Setenv("PORT", "-1") // negative port
	cfg2 := LoadConfig()
	if cfg2.Port != 20130 {
		t.Errorf("expected fallback port 20130 for negative port, got %d", cfg2.Port)
	}
}

func TestLoadConfigFromDotEnv(t *testing.T) {
	tempDir := t.TempDir()
	tempDataDir := filepath.Join(tempDir, "data")
	envContent := `PORT=20140
DATA_DIR=` + tempDataDir + `
JWT_SECRET=dotenv-jwt-secret
INITIAL_PASSWORD=dotenv-initial-password
API_KEY_SECRET=dotenv-api-key-secret
MACHINE_ID_SALT=dotenv-salt
RTK_ENABLED=false
CAVEMAN_ENABLED=true
PONYTAIL_ENABLED=true
ADHD_ENABLED=true
`
	envFile := filepath.Join(tempDir, ".env")
	if err := os.WriteFile(envFile, []byte(envContent), 0600); err != nil {
		t.Fatalf("failed to write test .env file: %v", err)
	}

	v := NewViperWithFile(envFile)
	cfg := LoadConfigFromViper(v)

	if cfg.Port != 20140 {
		t.Errorf("expected port 20140 from .env, got %d", cfg.Port)
	}
	expectedDb := filepath.Join(tempDataDir, "db", "data.sqlite")
	if cfg.DatabasePath != expectedDb {
		t.Errorf("expected db path %s, got %s", expectedDb, cfg.DatabasePath)
	}
	if cfg.JWTSecret != "dotenv-jwt-secret" {
		t.Errorf("expected jwt secret from .env, got %s", cfg.JWTSecret)
	}
	if cfg.InitialPassword != "dotenv-initial-password" {
		t.Errorf("expected initial password from .env, got %s", cfg.InitialPassword)
	}
	if cfg.APIKeySecret != "dotenv-api-key-secret" {
		t.Errorf("expected api key secret from .env, got %s", cfg.APIKeySecret)
	}
	if cfg.MachineIDSalt != "dotenv-salt" {
		t.Errorf("expected machine id salt from .env, got %s", cfg.MachineIDSalt)
	}
	if cfg.RTKEnabled != false {
		t.Errorf("expected rtk false from .env, got %v", cfg.RTKEnabled)
	}
	if cfg.CavemanEnabled != true {
		t.Errorf("expected caveman true from .env, got %v", cfg.CavemanEnabled)
	}
	if cfg.PonytailEnabled != true {
		t.Errorf("expected ponytail true from .env, got %v", cfg.PonytailEnabled)
	}
	if cfg.ADHDEnabled != true {
		t.Errorf("expected adhd true from .env, got %v", cfg.ADHDEnabled)
	}
}

func TestEnvPrecedenceOverDotEnv(t *testing.T) {
	tempDir := t.TempDir()
	envContent := `PORT=20140
API_KEY_SECRET=dotenv-secret
MACHINE_ID_SALT=dotenv-salt
RTK_ENABLED=false
CAVEMAN_ENABLED=false
PONYTAIL_ENABLED=false
ADHD_ENABLED=false
`
	envFile := filepath.Join(tempDir, ".env")
	if err := os.WriteFile(envFile, []byte(envContent), 0600); err != nil {
		t.Fatalf("failed to write test .env file: %v", err)
	}

	// OS env must take precedence over .env file
	t.Setenv("PORT", "20188")
	t.Setenv("API_KEY_SECRET", "os-api-key-secret")
	t.Setenv("MACHINE_ID_SALT", "os-salt")
	t.Setenv("RTK_ENABLED", "true")
	t.Setenv("CAVEMAN_ENABLED", "true")
	t.Setenv("PONYTAIL_ENABLED", "true")
	t.Setenv("ADHD_ENABLED", "true")

	v := NewViperWithFile(envFile)
	cfg := LoadConfigFromViper(v)

	if cfg.Port != 20188 {
		t.Errorf("expected OS env PORT 20188 to override .env, got %d", cfg.Port)
	}
	if cfg.APIKeySecret != "os-api-key-secret" {
		t.Errorf("expected OS env API_KEY_SECRET to override .env, got %s", cfg.APIKeySecret)
	}
	if cfg.MachineIDSalt != "os-salt" {
		t.Errorf("expected OS env MACHINE_ID_SALT to override .env, got %s", cfg.MachineIDSalt)
	}
	if cfg.RTKEnabled != true {
		t.Errorf("expected OS env RTK_ENABLED true to override .env, got %v", cfg.RTKEnabled)
	}
	if cfg.CavemanEnabled != true {
		t.Errorf("expected OS env CAVEMAN_ENABLED true to override .env, got %v", cfg.CavemanEnabled)
	}
	if cfg.PonytailEnabled != true {
		t.Errorf("expected OS env PONYTAIL_ENABLED true to override .env, got %v", cfg.PonytailEnabled)
	}
	if cfg.ADHDEnabled != true {
		t.Errorf("expected OS env ADHD_ENABLED true to override .env, got %v", cfg.ADHDEnabled)
	}
}

func TestProvideViper(t *testing.T) {
	v := ProvideViper()
	if v == nil {
		t.Fatal("expected ProvideViper to return non-nil instance")
	}
	if v.GetInt("PORT") <= 0 {
		t.Errorf("expected positive default PORT, got %d", v.GetInt("PORT"))
	}
	if v.GetString("API_KEY_SECRET") == "" {
		t.Error("expected non-empty API_KEY_SECRET")
	}
	if v.GetString("MACHINE_ID_SALT") == "" {
		t.Error("expected non-empty MACHINE_ID_SALT")
	}
}

func TestLoadConfig_HostAndBindAddr(t *testing.T) {
	t.Setenv("HOST", "127.0.0.1")
	cfg := LoadConfig()
	if cfg.Host != "127.0.0.1" {
		t.Errorf("expected host 127.0.0.1 from HOST, got %s", cfg.Host)
	}

	t.Setenv("HOST", "")
	t.Setenv("BIND_ADDR", "0.0.0.0")
	cfg2 := LoadConfig()
	if cfg2.Host != "0.0.0.0" {
		t.Errorf("expected host 0.0.0.0 from BIND_ADDR, got %s", cfg2.Host)
	}
}

// The daemon CLI (status/stop/logs) runs in its own short-lived process and
// resolves the data dir through this function, while the server resolves it
// through viper — which reads .env. When the two disagreed, a deployment
// configured only through .env reported "not running" against a live listener
// and refused to stop it. DATA_DIR from the OS environment must still win.
func TestResolveDataDir_PrefersOSEnvOverDotEnv(t *testing.T) {
	dir := t.TempDir()
	writeDotEnv(t, dir, "DATA_DIR="+filepath.Join(dir, "from-file")+"\n")
	t.Chdir(dir)
	t.Setenv("DATA_DIR", filepath.Join(dir, "from-env"))

	if got := ResolveDataDir(); got != filepath.Join(dir, "from-env") {
		t.Fatalf("DATA_DIR from the environment must win, got %q", got)
	}
}

// The regression itself: no DATA_DIR in the environment at all, which is the
// shape of every compose deployment that configures the gateway through .env.
func TestResolveDataDir_FallsBackToDotEnv(t *testing.T) {
	dir := t.TempDir()
fromFile := filepath.Join(dir, "from-file")
	writeDotEnv(t, dir, "DATA_DIR="+fromFile+"\n")
	t.Chdir(dir)
	t.Setenv("DATA_DIR", "")

	if got := ResolveDataDir(); got != fromFile {
		t.Fatalf("ResolveDataDir() = %q, want the .env value %q", got, fromFile)
	}
}

// A .env without DATA_DIR must not be answered with an empty string: the
// platform default is still the correct answer.
func TestResolveDataDir_DotEnvWithoutDataDirUsesDefault(t *testing.T) {
	dir := t.TempDir()
	writeDotEnv(t, dir, "PORT=20140\n")
	t.Chdir(dir)
	t.Setenv("DATA_DIR", "")

	got := ResolveDataDir()
	if got == "" {
	t.Fatal("ResolveDataDir() returned empty; want the platform default")
	}
	if strings.Contains(got, ".env") {
	t.Errorf("ResolveDataDir() = %q, want the platform default rather than a config path", got)
	}
}

func writeDotEnv(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
}
