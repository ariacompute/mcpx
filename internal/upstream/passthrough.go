package upstream

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"mcpx/internal/config"
	"mcpx/internal/tool"
)

// Passthrough is the nginx-like escape hatch: one generic tool per upstream
// that forwards a caller-chosen method and path. The allowlist is the whole
// point — without it the gateway would be an open proxy for every internal
// service the operator can reach.
type Passthrough struct {
	up       *Client
	name     string
	methods  map[string]bool
	prefixes []string
	schema   map[string]any
}

// NewPassthrough builds the restricted generic tool.
func NewPassthrough(up *Client, cfg config.Passthrough) (*Passthrough, error) {
	if len(cfg.AllowPrefixes) == 0 {
		return nil, fmt.Errorf("upstream %s passthrough: allow_prefixes must not be empty", up.Name())
	}
	methods := make(map[string]bool, len(cfg.Methods))
	names := make([]string, 0, len(cfg.Methods))
	for _, method := range cfg.Methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if !methodAllowed(method) {
			return nil, fmt.Errorf("%w: %s", ErrMethodNotAllowed, method)
		}
		if !methods[method] {
			methods[method] = true
			names = append(names, method)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("upstream %s passthrough: methods must not be empty", up.Name())
	}
	sort.Strings(names)

	enum := make([]any, 0, len(names))
	for _, name := range names {
		enum = append(enum, name)
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"method": map[string]any{
				"type":        "string",
				"enum":        enum,
				"default":     names[0],
				"description": fmt.Sprintf("HTTP method, one of %s", strings.Join(names, "/")),
			},
			"path": map[string]any{
				"type":        "string",
				"description": fmt.Sprintf("Absolute path on upstream %s, inside %s", up.Name(), strings.Join(cfg.AllowPrefixes, ", ")),
			},
			"query": map[string]any{
				"type":                 "object",
				"description":          "Query parameters; array values become repeated parameters",
				"additionalProperties": true,
			},
			"headers": map[string]any{
				"type":                 "object",
				"description":          "Extra headers; transport headers and credentials are refused",
				"additionalProperties": true,
			},
			"body": map[string]any{
				"description": "Request body: strings are sent verbatim, other values as JSON",
			},
		},
		"required":             []string{"path"},
		"additionalProperties": false,
	}
	return &Passthrough{
		up:       up,
		name:     cfg.Name,
		methods:  methods,
		prefixes: append([]string(nil), cfg.AllowPrefixes...),
		schema:   schema,
	}, nil
}

// Name is the published MCP tool name.
func (p *Passthrough) Name() string { return p.name }

// Description explains the restriction so a model does not discover it by
// failing.
func (p *Passthrough) Description() string {
	return fmt.Sprintf("Generic request tool for upstream %s. Allowed methods: %s. Paths must stay inside: %s.",
		p.up.Name(), strings.Join(sortedKeys(p.methods), "/"), strings.Join(p.prefixes, ", "))
}

// InputSchema is the hand-written generic schema.
func (p *Passthrough) InputSchema() map[string]any { return p.schema }

// Invoke validates the allowlist before anything leaves the process.
func (p *Passthrough) Invoke(ctx context.Context, args map[string]any) (tool.Result, error) {
	if err := tool.Validate(p.schema, args); err != nil {
		return tool.Result{}, err
	}
	rawPath, _ := args["path"].(string)
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return tool.Result{}, &tool.InvalidArgumentsError{Message: "argument \"path\" must not be empty"}
	}
	if err := ValidatePath(path); err != nil {
		return tool.Result{}, &tool.InvalidArgumentsError{Message: err.Error()}
	}
	if !MatchPrefix(path, p.prefixes) {
		return tool.Result{}, &tool.InvalidArgumentsError{
			Message: fmt.Sprintf("path %q is outside the allowlisted prefixes (%s)", path, strings.Join(p.prefixes, ", ")),
		}
	}

	method := ""
	if rawMethod, ok := args["method"]; ok && rawMethod != nil {
		text, err := tool.ScalarArg(rawMethod)
		if err != nil {
			return tool.Result{}, err
		}
		method = strings.ToUpper(strings.TrimSpace(text))
	}
	if method == "" {
		method = sortedKeys(p.methods)[0]
	}
	if !p.methods[method] {
		return tool.Result{}, &tool.InvalidArgumentsError{
			Message: fmt.Sprintf("method %q is not allowed; use %s", method, strings.Join(sortedKeys(p.methods), "/")),
		}
	}

	query, err := queryFromArgs(args["query"])
	if err != nil {
		return tool.Result{}, err
	}
	headers, err := headersFromArgs(args["headers"])
	if err != nil {
		return tool.Result{}, err
	}
	body, contentType, err := encodeBody(args["body"], headers.Get("Content-Type"))
	if err != nil {
		return tool.Result{}, err
	}

	resp, err := p.up.Do(ctx, Request{
		Method:      method,
		Path:        path,
		Query:       query,
		Headers:     headers,
		Body:        body,
		ContentType: contentType,
	})
	if err != nil {
		return tool.Result{Text: err.Error(), IsError: true}, nil
	}
	return ResultFromResponse(p.up.Name(), resp), nil
}

func queryFromArgs(raw any) (url.Values, error) {
	query := url.Values{}
	if raw == nil {
		return query, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, &tool.InvalidArgumentsError{Message: "argument \"query\" must be an object"}
	}
	for name, value := range values {
		if list, ok := value.([]any); ok {
			for _, item := range list {
				text, err := tool.ScalarArg(item)
				if err != nil {
					return nil, err
				}
				query.Add(name, text)
			}
			continue
		}
		text, err := tool.ScalarArg(value)
		if err != nil {
			return nil, err
		}
		query.Set(name, text)
	}
	return query, nil
}

func headersFromArgs(raw any) (http.Header, error) {
	headers := http.Header{}
	if raw == nil {
		return headers, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, &tool.InvalidArgumentsError{Message: "argument \"headers\" must be an object"}
	}
	for name, value := range values {
		if isReservedHeader(name) {
			return nil, &tool.InvalidArgumentsError{Message: fmt.Sprintf("header %q cannot be set by a caller", name)}
		}
		if strings.ContainsAny(name, "\r\n: ") {
			return nil, &tool.InvalidArgumentsError{Message: fmt.Sprintf("header %q is not a valid name", name)}
		}
		text, err := tool.ScalarArg(value)
		if err != nil {
			return nil, err
		}
		if strings.ContainsAny(text, "\r\n") {
			return nil, &tool.InvalidArgumentsError{Message: fmt.Sprintf("header %q must not contain CR or LF", name)}
		}
		headers.Set(name, text)
	}
	return headers, nil
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
