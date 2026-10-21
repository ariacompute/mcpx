package upstream

import (
	"net/http"
	"regexp"
	"strings"
)

var placeholderRe = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Sensitive headers never reach a log line and are never echoed in an error.
var sensitiveHeaders = map[string]bool{
	"authorization":       true,
	"cookie":              true,
	"proxy-authorization": true,
	"set-cookie":          true,
	"x-api-key":           true,
}

// Hop-by-hop and request-shaping headers a caller may not set. Letting a model
// choose Host or Content-Length would break the request the gateway built.
var reservedHeaders = map[string]bool{
	"host":              true,
	"content-length":    true,
	"connection":        true,
	"transfer-encoding": true,
	"te":                true,
	"trailer":           true,
	"upgrade":           true,
	"proxy-connection":  true,
	"keep-alive":        true,
	"authorization":     true,
	"cookie":            true,
}

// RedactHeaders renders headers for a log line with the credentials masked.
func RedactHeaders(headers http.Header) map[string]string {
	out := make(map[string]string, len(headers))
	for name, values := range headers {
		if sensitiveHeaders[strings.ToLower(name)] {
			out[name] = "***"
			continue
		}
		out[name] = strings.Join(values, ", ")
	}
	return out
}

// Truncate shortens text for an error message so an oversized upstream body
// cannot flood the model context.
func Truncate(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

func isReservedHeader(name string) bool {
	return reservedHeaders[strings.ToLower(name)]
}
