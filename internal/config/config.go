// Package config loads, validates and bootstraps the platform's
// admin-level configuration.
//
// The file format is YAML; secrets may reference environment variables
// using the ${VAR} and ${VAR:-default} syntax. On first boot Load()
// creates the file with safe defaults and a random admin password, so a
// fresh checkout is runnable with a single `egmcp` invocation.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
	"golang.org/x/crypto/bcrypt"
)

// ServerConfig groups server-bind settings.
type ServerConfig struct {
	Listen      string        `mapstructure:"listen"`
	ReadTimeout time.Duration `mapstructure:"read_timeout"`
}

// AuthConfig holds admin credentials and JWT settings.
type AuthConfig struct {
	AdminUsername    string        `mapstructure:"admin_username"`
	AdminPasswordHash string       `mapstructure:"admin_password_hash"`
	JWTSecret        string        `mapstructure:"jwt_secret"`
	JWTTTL           time.Duration `mapstructure:"jwt_ttl"`
}

// Config is the root configuration loaded from disk.
type Config struct {
	Server ServerConfig `mapstructure:"server"`
	Auth   AuthConfig   `mapstructure:"auth"`

	DataDir      string `mapstructure:"data_dir"`
	InstancesDir string `mapstructure:"instances_dir"`
	PluginsDir   string `mapstructure:"plugins_dir"`
	LogLevel     string `mapstructure:"log_level"`

	// FirstBootPassword is populated only on first run when a random
	// admin password is generated. The caller is expected to surface
	// it once via a log line and zero it.
	FirstBootPassword string `mapstructure:"-"`
}

// Load reads, parses and validates the config file at path. If the file
// does not exist, it is created with safe defaults and a random admin
// password; the second return value (firstBoot) is true in that case.
func Load(path string) (*Config, bool, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")

	// Defaults — kept in sync with the documented schema in the design doc.
	v.SetDefault("server.listen", ":8080")
	v.SetDefault("server.read_timeout", "30s")
	v.SetDefault("auth.admin_username", "admin")
	v.SetDefault("auth.jwt_ttl", "12h")
	v.SetDefault("data_dir", "./data")
	v.SetDefault("instances_dir", "./data/instances")
	v.SetDefault("plugins_dir", "./data/plugins")
	v.SetDefault("log_level", "info")

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, fmt.Errorf("create config dir: %w", err)
	}

	firstBoot := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := writeDefaults(path); err != nil {
			return nil, false, fmt.Errorf("write defaults: %w", err)
		}
		firstBoot = true
	}

	if err := v.ReadInConfig(); err != nil {
		return nil, false, fmt.Errorf("read config: %w", err)
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, false, fmt.Errorf("unmarshal: %w", err)
	}

	if err := os.MkdirAll(cfg.InstancesDir, 0o755); err != nil {
		return nil, false, fmt.Errorf("instances dir: %w", err)
	}
	if err := os.MkdirAll(cfg.PluginsDir, 0o755); err != nil {
		return nil, false, fmt.Errorf("plugins dir: %w", err)
	}

	// ENV substitution for sensitive strings happens here so that
	// downstream code never sees ${...} literals.
	if err := resolveEnv(cfg); err != nil {
		return nil, false, fmt.Errorf("env substitution: %w", err)
	}

	// Fill in first-boot secrets.
	if firstBoot {
		pwd, err := randomPassword(16)
		if err != nil {
			return nil, false, fmt.Errorf("random password: %w", err)
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(pwd), bcrypt.DefaultCost)
		if err != nil {
			return nil, false, fmt.Errorf("bcrypt: %w", err)
		}
		cfg.Auth.AdminPasswordHash = string(hash)
		cfg.FirstBootPassword = pwd

		secret, err := randomPassword(32)
		if err != nil {
			return nil, false, fmt.Errorf("random jwt secret: %w", err)
		}
		cfg.Auth.JWTSecret = secret

		// Persist the JWT secret so subsequent restarts stay valid.
		if err := updateYAMLField(path, "auth.jwt_secret", secret); err != nil {
			return nil, false, fmt.Errorf("persist jwt secret: %w", err)
		}
		if err := updateYAMLField(path, "auth.admin_password_hash", string(hash)); err != nil {
			return nil, false, fmt.Errorf("persist password hash: %w", err)
		}
	}

	if cfg.Auth.JWTSecret == "" {
		return nil, false, fmt.Errorf("auth.jwt_secret must be set (regenerate config or set in env)")
	}
	if cfg.Auth.AdminPasswordHash == "" {
		return nil, false, fmt.Errorf("auth.admin_password_hash must be set (regenerate config or set in env)")
	}

	return cfg, firstBoot, nil
}

// resolveEnv replaces ${VAR} and ${VAR:-default} references in any
// string-typed leaf with the corresponding environment value.
func resolveEnv(c *Config) error {
	if err := substitute(&c.Auth.AdminUsername); err != nil {
		return err
	}
	if err := substitute(&c.Auth.JWTSecret); err != nil {
		return err
	}
	if err := substitute(&c.Auth.AdminPasswordHash); err != nil {
		return err
	}
	return nil
}

func substitute(field *string) error {
	if field == nil || *field == "" {
		return nil
	}
	v := *field
	for {
		start := strings.Index(v, "${")
		if start < 0 {
			break
		}
		end := strings.Index(v[start:], "}")
		if end < 0 {
			return fmt.Errorf("unterminated env reference in %q", v)
		}
		ref := v[start+2 : start+end]
		var name, def string
		if i := strings.Index(ref, ":-"); i >= 0 {
			name, def = ref[:i], ref[i+2:]
		} else {
			name, def = ref, ""
		}
		envVal, ok := os.LookupEnv(name)
		if !ok {
			if def == "" {
				return fmt.Errorf("missing required env var %q", name)
			}
			envVal = def
		} else if envVal == "" && def != "" {
			// Variable is set to empty and a default was supplied — use it.
			envVal = def
		}
		v = v[:start] + envVal + v[start+end+1:]
	}
	*field = v
	return nil
}

// writeDefaults writes the seed config file. It deliberately omits the
// password hash and JWT secret — those are filled by Load() right after.
func writeDefaults(path string) error {
	body := `# egmcp admin config — generated on first boot.
# Edit and restart the server to apply changes.

server:
  listen: ":8080"

auth:
  admin_username: admin
  admin_password_hash: ""
  jwt_secret: ""
  jwt_ttl: 12h

data_dir: ./data
instances_dir: ./data/instances
plugins_dir: ./data/plugins
log_level: info
`
	return os.WriteFile(path, []byte(body), 0o600)
}

// updateYAMLField uses viper to rewrite a single key in the YAML file
// while preserving comments and ordering as best as possible.
func updateYAMLField(path, key, value string) error {
	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return err
	}
	v.Set(key, value)
	return v.WriteConfigAs(path)
}

// randomPassword returns a hex-encoded n-byte secret.
func randomPassword(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
