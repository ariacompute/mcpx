// Package tool is the gateway's own vocabulary for "something an MCP client
// can call". The MCP protocol layer speaks this vocabulary; it does not know
// whether a tool is an HTTP operation, a passthrough or anything added later.
package tool

import (
	"context"
	"strconv"
)

// Tool is one callable unit exposed through tools/list and tools/call.
type Tool interface {
	Name() string
	Description() string
	InputSchema() map[string]any
	Invoke(ctx context.Context, args map[string]any) (Result, error)
}

// Result is the outcome rendered back as MCP content. IsError marks a failure
// the upstream reported (bad status, timeout); the protocol call itself
// succeeded, which is why this is not a JSON-RPC error.
type Result struct {
	Text       string
	Structured any
	IsError    bool
}

// Param declares one argument and where it goes in the outgoing request.
type Param struct {
	Name        string
	In          string
	Required    bool
	Description string
	// Schema overrides the default {"type":"string"} for this argument.
	Schema map[string]any
	// Any leaves the argument untyped, so any JSON value is accepted. A body
	// argument uses it: the body may legitimately be an object, an array or a
	// primitive, and constraining it to a string would reject valid payloads.
	Any bool
}

// ScalarArg renders an argument as text. JSON numbers arrive as float64, and
// turning 2 into "2" (not "2.000000") is what upstream query strings expect.
func ScalarArg(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case bool:
		return strconv.FormatBool(v), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32), nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case nil:
		return "", nil
	default:
		return "", invalidf("value %v is not a scalar", value)
	}
}
