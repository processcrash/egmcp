package api

import (
	"crypto/rand"
	"encoding/hex"
)

// randomKey returns a hex-encoded n-byte secret suitable for
// per-instance API keys.
func randomKey(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
