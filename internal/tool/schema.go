package tool

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ErrInvalidArguments is the sentinel a caller matches with errors.Is when it
// wants to turn bad arguments into an isError result rather than a protocol
// error.
var ErrInvalidArguments = errors.New("invalid_arguments")

// InvalidArgumentsError explains which argument was rejected and why.
type InvalidArgumentsError struct {
	Message string
}

func (e *InvalidArgumentsError) Error() string { return e.Message }

// Is makes errors.Is(err, ErrInvalidArguments) work through wrapping.
func (e *InvalidArgumentsError) Is(target error) bool { return target == ErrInvalidArguments }

func invalidf(format string, args ...any) error {
	return &InvalidArgumentsError{Message: fmt.Sprintf(format, args...)}
}

// BuildSchema derives the input schema a client sees from the parameter
// declarations. Every property is required to be declared: unknown arguments
// are rejected by Validate, so a model cannot smuggle fields upstream.
func BuildSchema(params []Param) map[string]any {
	properties := make(map[string]any, len(params))
	required := make([]string, 0, len(params))
	for _, p := range params {
		prop := make(map[string]any, len(p.Schema)+2)
		for key, value := range p.Schema {
			prop[key] = value
		}
		if _, ok := prop["type"]; !ok && !p.Any {
			prop["type"] = "string"
		}
		if p.Description != "" {
			prop["description"] = p.Description
		}
		properties[p.Name] = prop
		if p.Required {
			required = append(required, p.Name)
		}
	}
	sort.Strings(required)
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// Validate checks arguments against a schema produced by BuildSchema (or the
// hand-written passthrough schema). It enforces unknown keys, required keys,
// primitive types and enum membership.
func Validate(schema map[string]any, args map[string]any) error {
	if args == nil {
		args = map[string]any{}
	}
	properties, _ := schema["properties"].(map[string]any)
	anyExtra := true
	if flag, ok := schema["additionalProperties"].(bool); ok {
		anyExtra = flag
	}
	for name := range args {
		if !anyExtra {
			if _, declared := properties[name]; !declared {
				return invalidf("unknown argument %q", name)
			}
		}
	}
	for _, name := range asStringSlice(schema["required"]) {
		if _, ok := args[name]; !ok {
			return invalidf("missing required argument %q", name)
		}
	}
	for name, raw := range args {
		prop, ok := properties[name].(map[string]any)
		if !ok {
			continue
		}
		if want, _ := prop["type"].(string); want != "" {
			if err := checkType(name, want, raw); err != nil {
				return err
			}
		}
		if values := asSlice(prop["enum"]); len(values) > 0 {
			text, err := ScalarArg(raw)
			if err != nil {
				return invalidf("argument %q: %v", name, err)
			}
			matched := false
			for _, candidate := range values {
				if other, err := ScalarArg(candidate); err == nil && other == text {
					matched = true
					break
				}
			}
			if !matched {
				return invalidf("argument %q must be one of %v", name, values)
			}
		}
	}
	return nil
}

func checkType(name, want string, raw any) error {
	switch want {
	case "string":
		if _, ok := raw.(string); !ok {
			return invalidf("argument %q must be a string", name)
		}
	case "boolean":
		if _, ok := raw.(bool); !ok {
			return invalidf("argument %q must be a boolean", name)
		}
	case "integer":
		value, ok := numeric(raw)
		if !ok || value != float64(int64(value)) {
			return invalidf("argument %q must be an integer", name)
		}
	case "number":
		if _, ok := numeric(raw); !ok {
			return invalidf("argument %q must be a number", name)
		}
	case "object":
		if _, ok := raw.(map[string]any); !ok {
			return invalidf("argument %q must be an object", name)
		}
	case "array":
		if _, ok := raw.([]any); !ok {
			return invalidf("argument %q must be an array", name)
		}
	case "null":
		if raw != nil {
			return invalidf("argument %q must be null", name)
		}
	}
	return nil
}

func numeric(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case string:
		parsed, err := strconv.ParseFloat(v, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func asSlice(raw any) []any {
	switch v := raw.(type) {
	case []any:
		return v
	case []string:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func asStringSlice(raw any) []string {
	values := asSlice(raw)
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, item := range values {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			out = append(out, text)
		}
	}
	return out
}
