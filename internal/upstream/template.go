package upstream

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Input errors shared by every upstream call. Callers use errors.Is, the
// message stays human readable for the tool result.
var (
	ErrPathNotAllowed      = errors.New("upstream: path not allowed")
	ErrMethodNotAllowed    = errors.New("upstream: method not allowed")
	ErrCredentialsMissing  = errors.New("upstream: credentials missing")
	ErrUnreachable         = errors.New("upstream: unreachable")
	ErrTimeout             = errors.New("upstream: timeout")
	ErrResponseTooLarge    = errors.New("upstream: response too large")
	ErrRequestBodyTooLarge = errors.New("upstream: request body too large")
)

// Template is a compiled path such as /api/v1/pets/{id}. Compiling once at
// startup means a call only substitutes values; syntax problems surface as a
// config error instead of a 500 on the first request.
type Template struct {
	raw      string
	segments []segment
	params   []string
}

type segment struct {
	literal string
	param   string
}

// CompileTemplate validates and splits one path template.
func CompileTemplate(raw string) (*Template, error) {
	if err := ValidatePath(raw); err != nil {
		return nil, err
	}
	t := &Template{raw: raw}
	seen := make(map[string]bool)
	last := 0
	for _, match := range placeholderRe.FindAllStringSubmatchIndex(raw, -1) {
		if match[0] > last {
			t.segments = append(t.segments, segment{literal: raw[last:match[0]]})
		}
		name := raw[match[2]:match[3]]
		if seen[name] {
			return nil, fmt.Errorf("upstream: placeholder {%s} appears twice in %q", name, raw)
		}
		seen[name] = true
		t.segments = append(t.segments, segment{param: name})
		t.params = append(t.params, name)
		last = match[1]
	}
	if last < len(raw) {
		t.segments = append(t.segments, segment{literal: raw[last:]})
	}
	return t, nil
}

// Raw returns the template as written, for diagnostics.
func (t *Template) Raw() string { return t.raw }

// Params lists the placeholders in order.
func (t *Template) Params() []string { return t.params }

// Expand substitutes escaped values. Values are percent escaped, so a value
// can never introduce a new segment or a traversal.
func (t *Template) Expand(values map[string]string) (string, error) {
	var builder strings.Builder
	for _, seg := range t.segments {
		if seg.param == "" {
			builder.WriteString(seg.literal)
			continue
		}
		value, ok := values[seg.param]
		if !ok {
			return "", fmt.Errorf("upstream: missing value for {%s}", seg.param)
		}
		escaped, err := escapePathValue(value)
		if err != nil {
			return "", fmt.Errorf("upstream: {%s}: %w", seg.param, err)
		}
		builder.WriteString(escaped)
	}
	out := builder.String()
	// Validate the decoded form: the expanded string legitimately contains the
	// percent escapes produced above, while the decoded path must not contain
	// a dot segment or a second slash.
	decoded, err := url.PathUnescape(out)
	if err != nil {
		return "", fmt.Errorf("upstream: %q is not a valid path", out)
	}
	if err := ValidatePath(decoded); err != nil {
		return "", err
	}
	return out, nil
}

// escapePathValue rejects the two values that would survive PathEscape and
// still mean something to a filesystem-backed upstream.
func escapePathValue(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("must not be empty")
	}
	if trimmed == "." || trimmed == ".." {
		return "", ErrPathNotAllowed
	}
	if strings.ContainsAny(trimmed, "\x00\r\n") {
		return "", ErrPathNotAllowed
	}
	return url.PathEscape(trimmed), nil
}

// ValidatePath rejects everything that could escape the configured prefix:
// absolute URLs, double slashes, dot segments, queries, fragments, spaces and
// percent escapes (the gateway escapes values itself, so a literal % is a bug).
func ValidatePath(path string) error {
	switch {
	case !strings.HasPrefix(path, "/"):
		return fmt.Errorf("%w: %q must start with /", ErrPathNotAllowed, path)
	case strings.HasPrefix(path, "//"):
		return fmt.Errorf("%w: %q must not start with //", ErrPathNotAllowed, path)
	case strings.ContainsAny(path, "?# \t\r\n%\\"):
		return fmt.Errorf("%w: %q contains a forbidden character", ErrPathNotAllowed, path)
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("%w: %q contains a dot segment", ErrPathNotAllowed, path)
		}
	}
	return nil
}

// MatchPrefix reports whether path is inside one of the allowlisted prefixes.
// Matching is segment-aware: "/api/v1" covers "/api/v1" and "/api/v1/x" but
// never "/api/v1x", which a plain string prefix would let through.
func MatchPrefix(path string, prefixes []string) bool {
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return false
	}
	if err := ValidatePath(decoded); err != nil {
		return false
	}
	for _, prefix := range prefixes {
		base := strings.TrimSuffix(prefix, "/")
		if base == "" {
			base = "/"
		}
		if decoded == base || strings.HasPrefix(decoded, base+"/") {
			return true
		}
	}
	return false
}
