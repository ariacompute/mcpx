package tool

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubTool struct{ name string }

func (s stubTool) Name() string                { return s.name }
func (s stubTool) Description() string         { return "stub " + s.name }
func (s stubTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (s stubTool) Invoke(context.Context, map[string]any) (Result, error) {
	return Result{Text: "ok"}, nil
}

func TestNewRegistryRejectsDuplicates(t *testing.T) {
	if _, err := NewRegistry(stubTool{"a"}, stubTool{"A"}); err == nil {
		t.Fatal("expected a duplicate tool error")
	}
}

func TestRegistryGetIsCaseInsensitive(t *testing.T) {
	registry, err := NewRegistry(stubTool{"Petstore_Get"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("petstore_get"); !ok {
		t.Fatal("case-insensitive lookup failed")
	}
	if _, ok := registry.Get("missing"); ok {
		t.Fatal("unknown tool resolved")
	}
}

func TestRegistryPage(t *testing.T) {
	registry, err := NewRegistry(stubTool{"a"}, stubTool{"b"}, stubTool{"c"})
	if err != nil {
		t.Fatal(err)
	}
	first, cursor, err := registry.Page("", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || cursor == "" {
		t.Fatalf("first page = %v cursor = %q", len(first), cursor)
	}
	second, next, err := registry.Page(cursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || next != "" {
		t.Fatalf("second page = %v next = %q", len(second), next)
	}
	if _, _, err := registry.Page("not-base64!", 2); err == nil {
		t.Fatal("expected an invalid cursor error")
	}
}

func TestBuildSchema(t *testing.T) {
	schema := BuildSchema([]Param{
		{Name: "id", In: "path", Required: true},
		{Name: "limit", In: "query", Schema: map[string]any{"type": "integer"}},
	})
	properties := schema["properties"].(map[string]any)
	if properties["id"].(map[string]any)["type"] != "string" {
		t.Fatal("id should default to string")
	}
	if properties["limit"].(map[string]any)["type"] != "integer" {
		t.Fatal("limit should keep the declared type")
	}
	required := schema["required"].([]string)
	if len(required) != 1 || required[0] != "id" {
		t.Fatalf("required = %v", required)
	}
}

func TestValidateArguments(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":     map[string]any{"type": "string"},
			"limit":  map[string]any{"type": "integer"},
			"status": map[string]any{"type": "string", "enum": []any{"open", "closed"}},
		},
		"required":             []string{"id"},
		"additionalProperties": false,
	}
	cases := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{name: "valid", args: map[string]any{"id": "1", "limit": float64(2)}},
		{name: "missing required", args: map[string]any{}, wantErr: "missing required"},
		{name: "unknown key", args: map[string]any{"id": "1", "extra": true}, wantErr: "unknown argument"},
		{name: "wrong type", args: map[string]any{"id": 1}, wantErr: "must be a string"},
		{name: "not an integer", args: map[string]any{"id": "1", "limit": 1.5}, wantErr: "must be an integer"},
		{name: "enum", args: map[string]any{"id": "1", "status": "gone"}, wantErr: "must be one of"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := Validate(schema, testCase.args)
			if testCase.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error = %v, want %q", err, testCase.wantErr)
			}
			if !errors.Is(err, ErrInvalidArguments) {
				t.Fatal("error should match ErrInvalidArguments")
			}
		})
	}
}

func TestScalarArg(t *testing.T) {
	if got, _ := ScalarArg(float64(2)); got != "2" {
		t.Errorf("float = %q", got)
	}
	if got, _ := ScalarArg(true); got != "true" {
		t.Errorf("bool = %q", got)
	}
	if _, err := ScalarArg([]any{1}); err == nil {
		t.Error("a slice is not a scalar")
	}
}
