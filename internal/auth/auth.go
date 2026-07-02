// Package auth provides admin authentication: bcrypt password
// verification, JWT issuance/parsing, and a Gin middleware that
// enforces authentication on protected routes.
//
// The package is independent of the storage layer; it operates purely
// against values. The platform wires it with the bcrypt hash and
// HMAC secret from admin.yaml.
package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// Claims is the JWT payload.
type Claims struct {
	jwt.RegisteredClaims
	// Username is preserved for human-readable log lines.
	Username string `json:"usr,omitempty"`
}

// Manager bundles the secret material and tokens configuration. It is
// safe to share across goroutines.
type Manager struct {
	username       string
	passwordHash   []byte
	hmacSecret     []byte
	tokenLifetime  time.Duration
}

// NewManager returns a Manager configured with the supplied credentials.
// passwordHash must already be a bcrypt-encoded byte slice.
func NewManager(username, passwordHash string, hmacSecret string, lifetime time.Duration) (*Manager, error) {
	if username == "" {
		return nil, errors.New("auth: username is empty")
	}
	if len(passwordHash) == 0 {
		return nil, errors.New("auth: password hash is empty")
	}
	if len(hmacSecret) < 16 {
		return nil, errors.New("auth: hmac secret must be at least 16 bytes")
	}
	if lifetime <= 0 {
		lifetime = 12 * time.Hour
	}
	return &Manager{
		username:      username,
		passwordHash:  []byte(passwordHash),
		hmacSecret:    []byte(hmacSecret),
		tokenLifetime: lifetime,
	}, nil
}

// VerifyPassword returns nil if plain matches the stored bcrypt hash.
func (m *Manager) VerifyPassword(plain string) error {
	if plain == "" {
		return errors.New("auth: empty password")
	}
	return bcrypt.CompareHashAndPassword(m.passwordHash, []byte(plain))
}

// Issue creates a signed JWT for the supplied username.
func (m *Manager) Issue(username string) (string, time.Time, error) {
	exp := time.Now().Add(m.tokenLifetime)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   username,
			Issuer:    "egmcp",
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		Username: username,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(m.hmacSecret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign: %w", err)
	}
	return signed, exp, nil
}

// Parse validates a token and returns its claims.
func (m *Manager) Parse(token string) (*Claims, error) {
	if token == "" {
		return nil, errors.New("auth: empty token")
	}
	parsed, err := jwt.ParseWithClaims(token, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.hmacSecret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid {
		return nil, errors.New("auth: invalid claims")
	}
	return claims, nil
}

// Middleware returns a Gin handler that rejects any request without a
// valid bearer token, except for the configured allowlist.
func (m *Manager) Middleware(allowPathPrefixes ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Convenience: skip auth for explicit allowlisted prefixes.
		for _, p := range allowPathPrefixes {
			if strings.HasPrefix(c.Request.URL.Path, p) {
				c.Next()
				return
			}
		}

		raw := bearerToken(c.Request)
		if raw == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "UNAUTHORIZED", "message": "missing bearer token"},
			})
			return
		}
		claims, err := m.Parse(raw)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "UNAUTHORIZED", "message": err.Error()},
			})
			return
		}
		// Expose claims to handlers via context.
		c.Set("auth.claims", claims)
		c.Set("auth.username", claims.Username)
		c.Next()
	}
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	// Some MCP clients struggle with custom headers; allow ?token=
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	if t := r.URL.Query().Get("access_token"); t != "" {
		return t
	}
	return ""
}

// LifetimeSeconds reports the configured TTL in seconds, suitable for
// the /login response body.
func (m *Manager) LifetimeSeconds() int {
	return int(m.tokenLifetime.Seconds())
}

// ConfiguredUsername returns the admin username configured at
// construction time. Used by login handlers to compare incoming
// usernames.
func (m *Manager) ConfiguredUsername() string {
	return m.username
}

// MustParseLifetime parses a duration string and falls back to 12h.
func MustParseLifetime(s string) time.Duration {
	if s == "" {
		return 12 * time.Hour
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	if secs, err := strconv.Atoi(s); err == nil {
		return time.Duration(secs) * time.Second
	}
	return 12 * time.Hour
}
