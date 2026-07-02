package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCreatesDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "configs", "admin.yaml")

	cfg, firstBoot, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !firstBoot {
		t.Fatalf("expected firstBoot=true")
	}
	if cfg.Auth.AdminUsername != "admin" {
		t.Fatalf("admin username: %q", cfg.Auth.AdminUsername)
	}
	if cfg.Auth.AdminPasswordHash == "" {
		t.Fatalf("admin password hash should be set on first boot")
	}
	if cfg.Auth.JWTSecret == "" {
		t.Fatalf("jwt secret should be set on first boot")
	}
	if cfg.FirstBootPassword == "" {
		t.Fatalf("first boot password should be present")
	}
	if cfg.FirstBootPassword == "" || len(cfg.FirstBootPassword) < 16 {
		t.Fatalf("first boot password too short: %q", cfg.FirstBootPassword)
	}
}

func TestLoadSubsequentBoot(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "configs", "admin.yaml")

	if _, _, err := Load(cfgPath); err != nil {
		t.Fatalf("first Load: %v", err)
	}

	// On a re-Load the random password should NOT be regenerated or
	// persisted, but the previously persisted secret must be present.
	cfg, firstBoot, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if firstBoot {
		t.Fatalf("expected firstBoot=false on second call")
	}
	if cfg.FirstBootPassword != "" {
		t.Fatalf("FirstBootPassword should be empty on later boots, got %q", cfg.FirstBootPassword)
	}
	if cfg.Auth.JWTSecret == "" {
		t.Fatalf("jwt secret must persist across boots")
	}
}

func TestSubstituteEnv(t *testing.T) {
	t.Setenv("EGMCP_TEST_USERNAME", "alice")

	raw := "hello ${EGMCP_TEST_USERNAME}"
	if err := substitute(&raw); err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if raw != "hello alice" {
		t.Fatalf("got %q", raw)
	}
}

func TestSubstituteEnvWithDefault(t *testing.T) {
	t.Setenv("EGMCP_TEST_MISSING", "")

	raw := "user ${EGMCP_TEST_MISSING:-fallback}"
	if err := substitute(&raw); err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if raw != "user fallback" {
		t.Fatalf("got %q", raw)
	}
}

func TestSubstituteMissingFails(t *testing.T) {
	// Ensure the env var is truly unset by t.Setenv + os.Unsetenv. t.Setenv
	// sets an empty value, which the substitute logic treats as "set but
	// empty" rather than missing. To exercise the missing branch we have
	// to actively remove the variable.
	if err := os.Unsetenv("EGMCP_TEST_DEFINITELY_NOT_SET"); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	raw := "${EGMCP_TEST_DEFINITELY_NOT_SET}"
	err := substitute(&raw)
	if err == nil {
		t.Fatalf("expected error for missing env var, got nil")
	}
	if !strings.Contains(err.Error(), "missing required env var") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSubstituteEmptyAllowsNoOp(t *testing.T) {
	// ${VAR} with VAR set to empty (no default) substitutes to empty — it
	// does not error. This matches POSIX-style behaviour where only an
	// unset variable is fatal.
	t.Setenv("EGMCP_TEST_EMPTY", "")
	raw := "before-${EGMCP_TEST_EMPTY}-after"
	if err := substitute(&raw); err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if raw != "before--after" {
		t.Fatalf("got %q", raw)
	}
}

func TestSubstituteIgnoresEmpty(t *testing.T) {
	// A nil/empty pointer (or empty string) should be a no-op.
	var empty string
	if err := substitute(&empty); err != nil {
		t.Fatalf("substitute empty: %v", err)
	}
}

func TestLoadEnvReferenceIsResolved(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "configs", "admin.yaml")
	// Write a config that references an env var with a default. We use
	// forward slashes for the path inside the YAML because Windows
	// backslashes clash with YAML escape sequences.
	posixDir := filepath.ToSlash(dir)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`
server:
  listen: ":9000"
auth:
  admin_username: "${EGMCP_TEST_USERNAME:-tester}"
  admin_password_hash: 'stub-hash'
  jwt_secret: 'stub-secret'
  jwt_ttl: '1h'
data_dir: "` + posixDir + `"
instances_dir: "` + posixDir + `/instances"
plugins_dir: "` + posixDir + `/plugins"
log_level: info
`)
	if err := os.WriteFile(cfgPath, body, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("EGMCP_TEST_USERNAME", "")
	cfg, _, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.AdminUsername != "tester" {
		t.Fatalf("env default not applied: %q", cfg.Auth.AdminUsername)
	}
}
