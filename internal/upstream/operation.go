package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"mcpx/internal/config"
	"mcpx/internal/tool"
)

// Operation is one configured endpoint published as a named MCP tool. The
// mapping is fully static: which arguments exist, where each one goes and how
// the body is encoded were decided at startup.
type Operation struct {
	up          *Client
	name        string
	description string
	method      string
	template    *Template
	params      []config.Param
	schema      map[string]any
	headers     http.Header
	bodyParam   string
}

// NewOperation compiles one operation. It trusts the config validator for
// semantics and re-checks only what needs the upstream address.
func NewOperation(up *Client, cfg config.Operation) (*Operation, error) {
	tpl, err := CompileTemplate(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("upstream %s operation %s: %w", up.Name(), cfg.Name, err)
	}
	params := make([]tool.Param, 0, len(cfg.Params))
	for _, p := range cfg.Params {
		params = append(params, tool.Param{
			Name: p.Name,
			In:   p.In,
			// A placeholder with no value cannot be rendered, so path params
			// are required whether or not the file says so.
			Required:    p.Required || p.In == config.ParamInPath,
			Description: p.Description,
			Schema:      p.Schema,
			Any:         p.In == config.ParamInBody && len(p.Schema) == 0,
		})
	}
	schema := cfg.InputSchema
	if len(schema) == 0 {
		schema = tool.BuildSchema(params)
	}
	headers := make(http.Header, len(cfg.Headers))
	for name, value := range cfg.Headers {
		headers.Set(name, value)
	}
	return &Operation{
		up:          up,
		name:        config.ToolName(up.Name(), cfg.Name),
		description: cfg.Description,
		method:      cfg.Method,
		template:    tpl,
		params:      cfg.Params,
		schema:      schema,
		headers:     headers,
		bodyParam:   cfg.BodyParam,
	}, nil
}

// Name is the published MCP tool name.
func (o *Operation) Name() string { return o.name }

// Description is what the client shows the model.
func (o *Operation) Description() string {
	if strings.TrimSpace(o.description) == "" {
		return fmt.Sprintf("%s %s on upstream %s", o.method, o.template.Raw(), o.up.Name())
	}
	return o.description
}

// InputSchema is derived from the parameter declarations.
func (o *Operation) InputSchema() map[string]any { return o.schema }

// Invoke binds the arguments and performs one HTTP request.
func (o *Operation) Invoke(ctx context.Context, args map[string]any) (tool.Result, error) {
	if err := tool.Validate(o.schema, args); err != nil {
		return tool.Result{}, err
	}

	pathValues := make(map[string]string, len(o.template.Params()))
	for _, name := range o.template.Params() {
		raw, ok := args[name]
		if !ok {
			return tool.Result{}, &tool.InvalidArgumentsError{Message: fmt.Sprintf("missing required argument %q", name)}
		}
		text, err := tool.ScalarArg(raw)
		if err != nil {
			return tool.Result{}, err
		}
		pathValues[name] = text
	}
	path, err := o.template.Expand(pathValues)
	if err != nil {
		return tool.Result{}, &tool.InvalidArgumentsError{Message: err.Error()}
	}

	query := url.Values{}
	headers := o.headers.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	var body []byte
	contentType := ""

	for _, param := range o.params {
		raw, ok := args[param.Name]
		if !ok {
			continue
		}
		switch param.In {
		case config.ParamInPath:
			continue
		case config.ParamInQuery:
			if list, ok := raw.([]any); ok {
				for _, item := range list {
					text, err := tool.ScalarArg(item)
					if err != nil {
						return tool.Result{}, err
					}
					query.Add(param.Name, text)
				}
				continue
			}
			text, err := tool.ScalarArg(raw)
			if err != nil {
				return tool.Result{}, err
			}
			query.Set(param.Name, text)
		case config.ParamInHeader:
			text, err := tool.ScalarArg(raw)
			if err != nil {
				return tool.Result{}, err
			}
			headers.Set(param.Name, text)
		case config.ParamInBody:
			encoded, ct, err := encodeBody(raw, headers.Get("Content-Type"))
			if err != nil {
				return tool.Result{}, err
			}
			body = encoded
			contentType = ct
		}
	}

	resp, err := o.up.Do(ctx, Request{
		Method:      o.method,
		Path:        path,
		Query:       query,
		Headers:     headers,
		Body:        body,
		ContentType: contentType,
	})
	if err != nil {
		return tool.Result{Text: err.Error(), IsError: true}, nil
	}
	return ResultFromResponse(o.up.Name(), resp), nil
}

// encodeBody picks the wire format from the value type: strings are sent
// verbatim (a plain-text endpoint is common), everything else is JSON.
func encodeBody(raw any, contentTypeHint string) ([]byte, string, error) {
	switch value := raw.(type) {
	case nil:
		return nil, "", nil
	case string:
		hint := contentTypeHint
		if hint == "" {
			hint = "text/plain; charset=utf-8"
		}
		return []byte(value), hint, nil
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, "", &tool.InvalidArgumentsError{Message: "body cannot be encoded as JSON"}
		}
		hint := contentTypeHint
		if hint == "" {
			hint = "application/json"
		}
		return encoded, hint, nil
	}
}
