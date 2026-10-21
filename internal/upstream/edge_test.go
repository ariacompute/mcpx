package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcpx/internal/config"
	"mcpx/internal/tool"
)

func TestOperationRejectsNonScalarArguments(t *testing.T) {
	client := newClient(t, "http://127.0.0.1:1", nil, 1<<20, 1<<20)
	operation, err := NewOperation(client, config.Operation{
		Name:        "get",
		Description: "Fetch a pet.",
		Method:      "GET",
		Path:        "/pets/{id}",
		Params: []config.Param{
			{Name: "id", In: config.ParamInPath, Required: true},
			{Name: "tags", In: config.ParamInQuery},
			{Name: "X-Trace", In: config.ParamInHeader},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if operation.Description() != "Fetch a pet." {
		t.Errorf("Description = %q", operation.Description())
	}

	cases := []map[string]any{
		{"id": []any{"a"}},
		{"id": "1", "tags": []any{map[string]any{"a": 1}}},
		{"id": "1", "X-Trace": map[string]any{"a": 1}},
	}
	for _, args := range cases {
		if _, err := operation.Invoke(context.Background(), args); !errors.Is(err, tool.ErrInvalidArguments) {
			t.Errorf("args %v: err = %v, want an invalid-arguments error", args, err)
		}
	}
}

func TestDoRejectsPathsOutsideTheBase(t *testing.T) {
	client := newClient(t, "https://example.com/root", nil, 1<<20, 1<<20)
	for _, path := range []string{"/a/../b", "//evil.example", "no-slash"} {
		if _, err := client.Do(context.Background(), Request{Method: "GET", Path: path}); !errors.Is(err, ErrPathNotAllowed) {
			t.Errorf("path %q: err = %v", path, err)
		}
	}
}

func TestDoReportsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("late"))
	}))
	defer server.Close()

	client := newClient(t, server.URL, nil, 1<<20, 1<<20)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Do(ctx, Request{Method: "GET", Path: "/x"}); err == nil {
		t.Fatal("a canceled context must fail the call")
	}
}

func TestCompileTemplateEdgeCases(t *testing.T) {
	if _, err := CompileTemplate("/a/{id}/{id}"); err == nil {
		t.Fatal("a repeated placeholder must be refused")
	}
	if _, err := CompileTemplate(""); err == nil {
		t.Fatal("an empty template must be refused")
	}

	template, err := CompileTemplate("/a/{v}")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "   ", "a\r\nb"} {
		if _, err := template.Expand(map[string]string{"v": value}); err == nil {
			t.Errorf("value %q should not expand", value)
		}
	}
}

func TestMatchPrefixRootAndBadEncoding(t *testing.T) {
	if !MatchPrefix("/anything/at/all", []string{"/"}) {
		t.Error("the root prefix should match every path")
	}
	if MatchPrefix("/%zz", []string{"/"}) {
		t.Error("an invalid escape must not match")
	}
	// An encoded slash is decoded before matching, so it can only ever land
	// inside the prefix; an encoded traversal still fails the path check.
	if !MatchPrefix("/api%2Fv1", []string{"/api/v1"}) {
		t.Error("an encoded slash that decodes inside the prefix should match")
	}
	if MatchPrefix("/api%2F..%2Fsecret", []string{"/api/v1"}) {
		t.Error("an encoded traversal must not match")
	}
}

func TestQueryFromArgsRejectsNonScalars(t *testing.T) {
	if _, err := queryFromArgs(map[string]any{"q": []any{map[string]any{"a": 1}}}); err == nil {
		t.Fatal("a nested object inside a query array must be refused")
	}
	if _, err := queryFromArgs(map[string]any{"q": map[string]any{"a": 1}}); err == nil {
		t.Fatal("a nested object query value must be refused")
	}
}

func TestPassthroughBodyAsJSON(t *testing.T) {
	var body map[string]any
	var contentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := newClient(t, server.URL, nil, 1<<20, 1<<20)
	passthrough, err := NewPassthrough(client, config.Passthrough{
		Enabled:       true,
		Name:          "demo_request",
		Methods:       []string{"POST"},
		AllowPrefixes: []string{"/api"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := passthrough.Invoke(context.Background(), map[string]any{
		"method": "POST",
		"path":   "/api/things",
		"body":   map[string]any{"name": "x"},
	}); err != nil {
		t.Fatal(err)
	}
	if contentType != "application/json" || body["name"] != "x" {
		t.Fatalf("content type = %q body = %v", contentType, body)
	}
}

func TestPassthroughMethodDefaultsToTheFirstAllowed(t *testing.T) {
	var method string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := newClient(t, server.URL, nil, 1<<20, 1<<20)
	passthrough, err := NewPassthrough(client, config.Passthrough{
		Enabled:       true,
		Name:          "demo_request",
		Methods:       []string{"GET", "POST"},
		AllowPrefixes: []string{"/api"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := passthrough.Invoke(context.Background(), map[string]any{"path": "/api/x"}); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet {
		t.Fatalf("method = %q, want the sorted first allowed method", method)
	}
}
