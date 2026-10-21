package tool

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// Page sizes for tools/list. The cursor is opaque to the client, so it is a
// base64 offset rather than an index that could be mistaken for a tool name.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// Registry is an immutable, ordered tool set built once at startup. Order is
// the configuration order, which keeps tools/list stable between runs.
type Registry struct {
	ordered []Tool
	index   map[string]Tool
}

// NewRegistry rejects duplicate names instead of silently keeping the first:
// an operator who declared the same tool twice wants to hear about it.
func NewRegistry(tools ...Tool) (*Registry, error) {
	r := &Registry{
		ordered: make([]Tool, 0, len(tools)),
		index:   make(map[string]Tool, len(tools)),
	}
	for _, t := range tools {
		if t == nil {
			return nil, fmt.Errorf("tool: nil tool")
		}
		key := strings.ToLower(t.Name())
		if previous, ok := r.index[key]; ok {
			return nil, fmt.Errorf("tool %q declared twice (%s)", t.Name(), previous.Name())
		}
		r.index[key] = t
		r.ordered = append(r.ordered, t)
	}
	return r, nil
}

// All returns the tools in configuration order. The slice is shared and must
// not be mutated by callers.
func (r *Registry) All() []Tool {
	if r == nil {
		return nil
	}
	return r.ordered
}

// Len counts the registered tools.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.ordered)
}

// Get looks a tool up by exact name, falling back to a case-insensitive match
// because agents occasionally re-case a tool name they read from a list.
func (r *Registry) Get(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	if t, ok := r.index[name]; ok {
		return t, true
	}
	t, ok := r.index[strings.ToLower(name)]
	return t, ok
}

// Page returns one slice of tools plus the cursor of the next page ("" when
// this was the last page).
func (r *Registry) Page(cursor string, size int) ([]Tool, string, error) {
	if size <= 0 {
		size = DefaultPageSize
	}
	if size > MaxPageSize {
		size = MaxPageSize
	}
	offset := 0
	if strings.TrimSpace(cursor) != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("tool: invalid cursor")
		}
		parsed, err := strconv.Atoi(string(raw))
		if err != nil || parsed < 0 {
			return nil, "", fmt.Errorf("tool: invalid cursor")
		}
		offset = parsed
	}
	if offset > len(r.ordered) {
		return nil, "", fmt.Errorf("tool: cursor past the end")
	}
	end := offset + size
	if end > len(r.ordered) {
		end = len(r.ordered)
	}
	next := ""
	if end < len(r.ordered) {
		next = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end)))
	}
	return r.ordered[offset:end], next, nil
}
