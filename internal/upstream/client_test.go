package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mcpx/internal/config"
	"mcpx/internal/tool"
)

func newClient(t *testing.T, baseURL string, auth *config.Auth, maxResponse, maxBody int64) *Client {
	t.Helper()
	client, err := NewClient(ClientOptions{
		Name:             "demo",
		BaseURL:          baseURL,
		Auth:             auth,
		Timeout:          2 * time.Second,
		MaxResponseBytes: maxResponse,
		MaxBodyBytes:     maxBody,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestOperationBuildsRequest(t *testing.T) {
	var (
		method, path, rawQuery string
		trace, authorization   string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		rawQuery = r.URL.RawQuery
		trace = r.Header.Get("X-Trace")
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"7","name":"rex"}`))
	}))
	defer server.Close()

	t.Setenv("DEMO_TOKEN", "secret")
	client := newClient(t, server.URL+"/root", &config.Auth{Scheme: config.SchemeBearer, TokenEnv: "DEMO_TOKEN"}, 1<<20, 1<<20)
	operation, err := NewOperation(client, config.Operation{
		Name:   "get_pet",
		Method: "GET",
		Path:   "/api/v1/pets/{id}",
		Params: []config.Param{
			{Name: "id", In: config.ParamInPath, Required: true},
			{Name: "verbose", In: config.ParamInQuery},
			{Name: "X-Trace", In: config.ParamInHeader},
		},
	})
	if err != nil {
		t.Fatalf("NewOperation: %v", err)
	}

	result, err := operation.Invoke(context.Background(), map[string]any{
		"id":      "7",
		"verbose": "true",
		"X-Trace": "trace-1",
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected isError: %s", result.Text)
	}
	if method != "GET" {
		t.Errorf("method = %q", method)
	}
	if path != "/root/api/v1/pets/7" {
		t.Errorf("path = %q, want the base path preserved", path)
	}
	if rawQuery != "verbose=true" {
		t.Errorf("query = %q", rawQuery)
	}
	if trace != "trace-1" {
		t.Errorf("header param = %q", trace)
	}
	if authorization != "Bearer secret" {
		t.Errorf("authorization = %q", authorization)
	}
	if result.Structured == nil {
		t.Error("a JSON body should also be structured content")
	}
	if !strings.Contains(result.Text, `"rex"`) {
		t.Errorf("text = %q", result.Text)
	}
}

func TestOperationSendsJSONBody(t *testing.T) {
	var (
		contentType string
		body        map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := newClient(t, server.URL, nil, 1<<20, 1<<20)
	operation, err := NewOperation(client, config.Operation{
		Name:      "create_pet",
		Method:    "POST",
		Path:      "/pets",
		BodyParam: "pet",
		Params:    []config.Param{{Name: "pet", In: config.ParamInBody, Required: true}},
	})
	if err != nil {
		t.Fatalf("NewOperation: %v", err)
	}
	result, err := operation.Invoke(context.Background(), map[string]any{
		"pet": map[string]any{"name": "fluffy"},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected isError: %s", result.Text)
	}
	if contentType != "application/json" {
		t.Errorf("content type = %q", contentType)
	}
	if body["name"] != "fluffy" {
		t.Errorf("body = %v", body)
	}
}

func TestOperationRejectsUnknownArgument(t *testing.T) {
	client := newClient(t, "http://127.0.0.1:1", nil, 1<<20, 1<<20)
	operation, err := NewOperation(client, config.Operation{
		Name:   "ping",
		Method: "GET",
		Path:   "/ping",
		Params: []config.Param{{Name: "id", In: config.ParamInPath, Required: true}},
	})
	if err != nil {
		t.Fatalf("NewOperation: %v", err)
	}
	if _, err := operation.Invoke(context.Background(), map[string]any{"id": "1", "extra": true}); !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("err = %v, want an invalid-arguments error", err)
	}
	if _, err := NewOperation(client, config.Operation{Name: "bad", Path: "relative"}); err == nil {
		t.Fatal("a relative path must not compile")
	}
}

func TestOperationNon2xxIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"missing"}`))
	}))
	defer server.Close()

	client := newClient(t, server.URL, nil, 1<<20, 1<<20)
	operation, err := NewOperation(client, config.Operation{Name: "ping", Method: "GET", Path: "/ping"})
	if err != nil {
		t.Fatalf("NewOperation: %v", err)
	}
	result, err := operation.Invoke(context.Background(), nil)
	if err != nil {
		t.Fatalf("Invoke returned an error instead of an isError result: %v", err)
	}
	if !result.IsError {
		t.Fatal("a 404 must be an isError result")
	}
	if !strings.Contains(result.Text, "404") || !strings.Contains(result.Text, "missing") {
		t.Errorf("text = %q", result.Text)
	}
}

func TestResponseIsTruncated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer server.Close()

	client := newClient(t, server.URL, nil, 64, 1<<20)
	operation, err := NewOperation(client, config.Operation{Name: "big", Method: "GET", Path: "/big"})
	if err != nil {
		t.Fatalf("NewOperation: %v", err)
	}
	result, err := operation.Invoke(context.Background(), nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(result.Text, "truncated") {
		t.Errorf("text = %q, want a truncation marker", result.Text)
	}
}

func TestRequestBodyLimit(t *testing.T) {
	client := newClient(t, "http://127.0.0.1:1", nil, 1<<20, 8)
	_, err := client.Do(context.Background(), Request{
		Method: "POST",
		Path:   "/pets",
		Body:   []byte(strings.Repeat("x", 64)),
	})
	if !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("err = %v, want ErrRequestBodyTooLarge", err)
	}
}

func TestMissingCredentials(t *testing.T) {
	t.Setenv("MCPX_TEST_EMPTY_TOKEN", "")
	client := newClient(t, "http://127.0.0.1:1", &config.Auth{Scheme: config.SchemeBearer, TokenEnv: "MCPX_TEST_EMPTY_TOKEN"}, 1<<20, 1<<20)

	_, err := client.Do(context.Background(), Request{Method: "GET", Path: "/ping"})
	if !errors.Is(err, ErrCredentialsMissing) {
		t.Fatalf("err = %v, want ErrCredentialsMissing", err)
	}
}

func TestRedirectToAnotherOriginIsRefused(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("should not be reached"))
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	client := newClient(t, source.URL, nil, 1<<20, 1<<20)
	_, err := client.Do(context.Background(), Request{Method: "GET", Path: "/ping"})
	if err == nil {
		t.Fatal("a cross-origin redirect must fail")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("err = %v", err)
	}
}

func TestTimeoutIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		Name:    "demo",
		BaseURL: server.URL,
		Timeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.Do(context.Background(), Request{Method: "GET", Path: "/slow"})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

func TestPassthroughPrefixGuard(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	client := newClient(t, server.URL, nil, 1<<20, 1<<20)
	passthrough, err := NewPassthrough(client, config.Passthrough{
		Enabled:       true,
		Name:          "demo_request",
		Methods:       []string{"GET"},
		AllowPrefixes: []string{"/api/v1"},
	})
	if err != nil {
		t.Fatalf("NewPassthrough: %v", err)
	}

	result, err := passthrough.Invoke(context.Background(), map[string]any{"path": "/api/v1/pets"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.IsError || len(seen) != 1 || seen[0] != "/api/v1/pets" {
		t.Fatalf("result = %+v seen = %v", result, seen)
	}

	denied := []map[string]any{
		{"path": "/api/v1x"},
		{"path": "/api/v2/pets"},
		{"path": "/api/v1/../secret"},
		{"path": "https://evil.example/api/v1/pets"},
		{"path": "/api/v1/pets", "method": "POST"},
		{"path": "/api/v1/pets", "headers": map[string]any{"Authorization": "Bearer nope"}},
	}
	for _, args := range denied {
		if _, err := passthrough.Invoke(context.Background(), args); err == nil {
			t.Fatalf("args %v should be refused", args)
		} else if !errors.Is(err, tool.ErrInvalidArguments) {
			t.Fatalf("args %v: err = %v, want an invalid-arguments error", args, err)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("a refused call must not reach the upstream, saw %v", seen)
	}
}

func TestTemplateEscapesValues(t *testing.T) {
	template, err := CompileTemplate("/api/{owner}/{repo}")
	if err != nil {
		t.Fatalf("CompileTemplate: %v", err)
	}
	path, err := template.Expand(map[string]string{"owner": "acme", "repo": "a/b"})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if path != "/api/acme/a%2Fb" {
		t.Fatalf("path = %q", path)
	}
	if _, err := template.Expand(map[string]string{"owner": "..", "repo": "x"}); err == nil {
		t.Fatal("a dot-dot value must not expand")
	}
	if _, err := template.Expand(map[string]string{"owner": "a"}); err == nil {
		t.Fatal("a missing value must not expand")
	}
}

func TestValidatePath(t *testing.T) {
	valid := []string{"/", "/api/v1/pets", "/a-b_c/{id}"}
	for _, path := range valid {
		if err := ValidatePath(path); err != nil {
			t.Errorf("ValidatePath(%q) = %v", path, err)
		}
	}
	invalid := []string{"", "api", "//evil", "/a/../b", "/a?x=1", "/a#f", "/a b", "/a%2e%2e", "/a\\b"}
	for _, path := range invalid {
		if err := ValidatePath(path); err == nil {
			t.Errorf("ValidatePath(%q) should fail", path)
		}
	}
}

func TestMatchPrefix(t *testing.T) {
	cases := map[string]bool{
		"/api/v1":        true,
		"/api/v1/pets":   true,
		"/api/v1/":       true,
		"/api/v1x":       false,
		"/api/v10/pets":  false,
		"/api/v1/../etc": false,
		"/other":         false,
	}
	for path, want := range cases {
		if got := MatchPrefix(path, []string{"/api/v1"}); got != want {
			t.Errorf("MatchPrefix(%q) = %v, want %v", path, got, want)
		}
	}
}
