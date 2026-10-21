package httpsrv

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// authorized checks the bearer token with a constant-time comparison over
// fixed-size digests, so neither the value nor its length leaks through
// timing.
func authorized(r *http.Request, expected string) bool {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	return constantTimeEqual(strings.TrimSpace(header[len(prefix):]), expected)
}

func constantTimeEqual(a, b string) bool {
	sumA := sha256.Sum256([]byte(a))
	sumB := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(sumA[:], sumB[:]) == 1
}
