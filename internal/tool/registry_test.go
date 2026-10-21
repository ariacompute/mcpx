package tool

import (
	"context"
	"encoding/base64"
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

	untyped := BuildSchema([]Param{{Name: "body", In: "body", Any: true}})
	bodyProperty := untyped["properties"].(map[string]any)["body"].(map[string]any)
	if _, ok := bodyProperty["type"]; ok {
		t.Errorf("an Any param must stay untyped, got %v", bodyProperty)
	}
	if _, ok := untyped["required"]; ok {
		t.Error("an optional param must not appear in required")
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
	cases := []struct {
		value any
		want  string
	}{
		{value: "text", want: "text"},
		{value: float64(2), want: "2"},
		{value: float64(2.5), want: "2.5"},
		{value: float32(1.5), want: "1.5"},
		{value: true, want: "true"},
		{value: 7, want: "7"},
		{value: int64(8), want: "8"},
		{value: nil, want: ""},
	}
	for _, testCase := range cases {
		got, err := ScalarArg(testCase.value)
		if err != nil || got != testCase.want {
			t.Errorf("ScalarArg(%v) = %q, %v; want %q", testCase.value, got, err, testCase.want)
		}
	}
	if _, err := ScalarArg([]any{1}); err == nil {
		t.Error("a slice is not a scalar")
	}
	if _, err := ScalarArg(map[string]any{}); err == nil {
		t.Error("a map is not a scalar")
	}
}

func TestRegistryAccessors(t *testing.T) {
	registry, err := NewRegistry(stubTool{"a"}, stubTool{"b"})
	if err != nil {
		t.Fatal(err)
	}
	if registry.Len() != 2 {
		t.Errorf("Len = %d", registry.Len())
	}
	if len(registry.All()) != 2 || registry.All()[0].Name() != "a" {
		t.Errorf("All = %v", registry.All())
	}

	var empty *Registry
	if empty.All() != nil || empty.Len() != 0 {
		t.Error("a nil registry should look empty")
	}
	if _, ok := empty.Get("a"); ok {
		t.Error("a nil registry must not resolve tools")
	}
}

func TestNewRegistryRejectsNilTool(t *testing.T) {
	if _, err := NewRegistry(nil); err == nil {
		t.Fatal("expected an error for a nil tool")
	}
}

func TestRegistryPageBoundaries(t *testing.T) {
	many := make([]Tool, 0, 5)
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		many = append(many, stubTool{name})
	}
	registry, err := NewRegistry(many...)
	if err != nil {
		t.Fatal(err)
	}
	// size 0 falls back to the default page size, which fits everything here.
	tools, next, err := registry.Page("", 0)
	if err != nil || len(tools) != 5 || next != "" {
		t.Fatalf("default page = %d, %q, %v", len(tools), next, err)
	}
	// A size above the cap is clamped, and still returns a cursor.
	tools, next, err = registry.Page("", 10000)
	if err != nil || len(tools) != 5 || next != "" {
		t.Fatalf("clamped page = %d, %q, %v", len(tools), next, err)
	}
	past := base64.RawURLEncoding.EncodeToString([]byte("99"))
	if _, _, err := registry.Page(past, 2); err == nil {
		t.Fatal("expected a cursor-past-the-end error")
	}
	negative := base64.RawURLEncoding.EncodeToString([]byte("-1"))
	if _, _, err := registry.Page(negative, 2); err == nil {
		t.Fatal("expected a negative cursor error")
	}
	notANumber := base64.RawURLEncoding.EncodeToString([]byte("abc"))
	if _, _, err := registry.Page(notANumber, 2); err == nil {
		t.Fatal("expected a non-numeric cursor error")
	}
}

func TestValidateTypeCoverage(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text":    map[string]any{"type": "string"},
			"flag":    map[string]any{"type": "boolean"},
			"amount":  map[string]any{"type": "number"},
			"count":   map[string]any{"type": "integer"},
			"payload": map[string]any{"type": "object"},
			"items":   map[string]any{"type": "array"},
			"nothing": map[string]any{"type": "null"},
		},
		"required":             []any{"text"},
		"additionalProperties": true,
	}
	good := map[string]any{
		"text":    "x",
		"flag":    true,
		"amount":  int64(3),
		"count":   "4",
		"payload": map[string]any{"a": 1},
		"items":   []any{1, 2},
		"nothing": nil,
		"extra":   "allowed",
	}
	if err := Validate(schema, good); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	bad := []map[string]any{
		{"text": 1},
		{"text": "x", "flag": "yes"},
		{"text": "x", "amount": "abc"},
		{"text": "x", "count": "1.5"},
		{"text": "x", "payload": "no"},
		{"text": "x", "items": "no"},
		{"text": "x", "nothing": "no"},
	}
	for _, args := range bad {
		if err := Validate(schema, args); err == nil {
			t.Errorf("args %v should not validate", args)
		}
	}

	// A schema decoded from JSON carries required as []any.
	jsonSchema := map[string]any{
		"type":     "object",
		"required": []any{"id"},
		"properties": map[string]any{
			"id": map[string]any{"type": "string"},
		},
	}
	if err := Validate(jsonSchema, map[string]any{}); err == nil {
		t.Error("a JSON-decoded required list must be enforced")
	}
	if err := Validate(map[string]any{"type": "object"}, map[string]any{"anything": 1}); err != nil {
		t.Errorf("a schema without properties allows anything: %v", err)
	}
}
