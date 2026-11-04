package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"mcpx/internal/config"
	"mcpx/internal/tool"
)

func TestNewClientValidation(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		want    string
	}{
		{name: "unparsable", baseURL: "://bad", want: "base_url"},
		{name: "wrong scheme", baseURL: "ftp://example.com", want: "http or https"},
		{name: "no host", baseURL: "https://", want: "include a host"},
		{name: "credentials", baseURL: "https://user:pass@example.com", want: "must not embed credentials"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NewClient(ClientOptions{Name: "demo", BaseURL: testCase.baseURL})
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("err = %v, want %q", err, testCase.want)
			}
		})
	}

	client, err := NewClient(ClientOptions{Name: "demo", BaseURL: "https://example.com/api/"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if !strings.Contains(client.BaseURL(), "example.com") || client.Name() != "demo" {
		t.Fatalf("client = %q %q", client.Name(), client.BaseURL())
	}
}

func TestDoRejectsUnsupportedMethod(t *testing.T) {
	client := newClient(t, "http://127.0.0.1:1", nil, 1<<20, 1<<20)
	_, err := client.Do(context.Background(), Request{Method: "TRACE", Path: "/x"})
	if !errors.Is(err, ErrMethodNotAllowed) {
		t.Fatalf("err = %v, want ErrMethodNotAllowed", err)
	}
}

func TestDoDefaultsMethodAndPath(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	client := newClient(t, server.URL, nil, 1<<20, 1<<20)
	if _, err := client.Do(context.Background(), Request{Path: ""}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if method != http.MethodGet || path != "/" {
		t.Fatalf("method = %q path = %q", method, path)
	}
}

func TestCallerCannotOverrideReservedHeaders(t *testing.T) {
	var authorization, host string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		host = r.Host
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	t.Setenv("DEMO_TOKEN", "configured")
	client := newClient(t, server.URL, &config.Auth{Scheme: config.SchemeBearer, TokenEnv: "DEMO_TOKEN"}, 1<<20, 1<<20)
	_, err := client.Do(context.Background(), Request{
		Method: "GET",
		Path:   "/x",
		Headers: http.Header{
			"Authorization": {"Bearer injected"},
			"Host":          {"evil.example"},
		},
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if authorization != "Bearer configured" {
		t.Fatalf("authorization = %q, a caller must not override the configured credential", authorization)
	}
	if strings.Contains(host, "evil.example") {
		t.Fatalf("host = %q", host)
	}
}

func TestHeaderAuthScheme(t *testing.T) {
	var apiKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey = r.Header.Get("X-Api-Key")
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	t.Setenv("DEMO_KEY", "k-123")
	client := newClient(t, server.URL, &config.Auth{Scheme: config.SchemeHeader, TokenEnv: "DEMO_KEY", HeaderName: "X-Api-Key"}, 1<<20, 1<<20)
	if _, err := client.Do(context.Background(), Request{Method: "GET", Path: "/x"}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if apiKey != "k-123" {
		t.Fatalf("api key header = %q", apiKey)
	}
}

func TestUnsupportedAuthScheme(t *testing.T) {
	client := newClient(t, "http://127.0.0.1:1", &config.Auth{Scheme: "digest"}, 1<<20, 1<<20)
	if _, err := client.Do(context.Background(), Request{Method: "GET", Path: "/x"}); err == nil {
		t.Fatal("expected an error for an unsupported auth scheme")
	}
}

func TestBearerWithoutTokenEnv(t *testing.T) {
	client := newClient(t, "http://127.0.0.1:1", &config.Auth{Scheme: config.SchemeBearer}, 1<<20, 1<<20)
	if _, err := client.Do(context.Background(), Request{Method: "GET", Path: "/x"}); !errors.Is(err, ErrCredentialsMissing) {
		t.Fatalf("err = %v, want ErrCredentialsMissing", err)
	}
}

func TestResultFromResponse(t *testing.T) {
	empty := ResultFromResponse("demo", &Response{StatusCode: 200, Status: "200 OK"})
	if empty.IsError || empty.Text != "(empty body)" {
		t.Fatalf("empty = %+v", empty)
	}

	failure := ResultFromResponse("demo", &Response{StatusCode: 503, Status: "503 Service Unavailable", Body: []byte("down")})
	if !failure.IsError || !strings.Contains(failure.Text, "503") || !strings.Contains(failure.Text, "down") {
		t.Fatalf("failure = %+v", failure)
	}

	pretty := ResultFromResponse("demo", &Response{StatusCode: 200, Status: "200 OK", Body: []byte(`{"a":1}`)})
	if pretty.Structured == nil || !strings.Contains(pretty.Text, "\n") {
		t.Fatalf("pretty = %+v", pretty)
	}

	truncated := ResultFromResponse("demo", &Response{StatusCode: 200, Status: "200 OK", Body: []byte(`{"a"`), Truncated: true})
	if !strings.Contains(truncated.Text, "truncated") {
		t.Fatalf("truncated = %+v", truncated)
	}
	if truncated.Structured != nil {
		t.Fatalf("a truncated body must not become structured content: %+v", truncated)
	}

	plain := ResultFromResponse("demo", &Response{StatusCode: 200, Status: "200 OK", Body: []byte("hello")})
	if plain.Structured != nil || plain.Text != "hello" {
		t.Fatalf("plain = %+v", plain)
	}
}

func TestRedactionAndTruncation(t *testing.T) {
	redacted := RedactHeaders(http.Header{
		"Authorization": {"Bearer secret"},
		"Cookie":        {"a=b"},
		"X-Api-Key":     {"secret"},
		"X-Trace":       {"visible", "second"},
	})
	if redacted["Authorization"] != "***" || redacted["Cookie"] != "***" || redacted["X-Api-Key"] != "***" {
		t.Fatalf("redacted = %v", redacted)
	}
	if redacted["X-Trace"] != "visible, second" {
		t.Fatalf("ordinary header = %v", redacted["X-Trace"])
	}

	if got := Truncate("short", 32); got != "short" {
		t.Errorf("Truncate = %q", got)
	}
	if got := Truncate("0123456789", 4); got != "0123…" {
		t.Errorf("Truncate = %q", got)
	}
	if got := Truncate("0123456789", 0); got != "0123456789" {
		t.Errorf("Truncate with no limit = %q", got)
	}
}

func TestTemplateAndOperationAccessors(t *testing.T) {
	template, err := CompileTemplate("/pets/{id}")
	if err != nil {
		t.Fatal(err)
	}
	if template.Raw() != "/pets/{id}" {
		t.Errorf("Raw = %q", template.Raw())
	}
	if params := template.Params(); len(params) != 1 || params[0] != "id" {
		t.Errorf("Params = %v", params)
	}

	client := newClient(t, "https://example.com", nil, 1<<20, 1<<20)
	operation, err := NewOperation(client, config.Operation{Name: "get", Method: "GET", Path: "/pets/{id}"})
	if err != nil {
		t.Fatal(err)
	}
	if operation.Name() != "demo_get" {
		t.Errorf("Name = %q", operation.Name())
	}
	if !strings.Contains(operation.Description(), "GET /pets/{id}") {
		t.Errorf("Description = %q", operation.Description())
	}
	if operation.InputSchema() == nil {
		t.Error("InputSchema is nil")
	}
}

func TestPassthroughAccessorsAndMapping(t *testing.T) {
	var (
		query   url.Values
		trace   string
		body    string
		method  string
		content string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		query = r.URL.Query()
		trace = r.Header.Get("X-Trace")
		content = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_, _ = io.WriteString(w, `{"ok":true}`)
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
		t.Fatalf("NewPassthrough: %v", err)
	}
	if passthrough.Name() != "demo_request" || !strings.Contains(passthrough.Description(), "/api") {
		t.Fatalf("accessors = %q %q", passthrough.Name(), passthrough.Description())
	}
	if passthrough.InputSchema()["type"] != "object" {
		t.Fatalf("schema = %v", passthrough.InputSchema())
	}

	result, err := passthrough.Invoke(context.Background(), map[string]any{
		"method":  "POST",
		"path":    "/api/things",
		"query":   map[string]any{"tag": []any{"a", "b"}, "limit": float64(2)},
		"headers": map[string]any{"X-Trace": "t-1"},
		"body":    "raw text",
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected isError: %s", result.Text)
	}
	if method != "POST" || query["tag"][0] != "a" || query["tag"][1] != "b" || query["limit"][0] != "2" {
		t.Fatalf("method = %q query = %v", method, query)
	}
	if trace != "t-1" {
		t.Fatalf("header = %q", trace)
	}
	if body != "raw text" || !strings.HasPrefix(content, "text/plain") {
		t.Fatalf("body = %q content type = %q", body, content)
	}
}

func TestPassthroughRejectsBadArguments(t *testing.T) {
	client := newClient(t, "http://127.0.0.1:1", nil, 1<<20, 1<<20)
	passthrough, err := NewPassthrough(client, config.Passthrough{
		Enabled:       true,
		Name:          "demo_request",
		Methods:       []string{"GET"},
		AllowPrefixes: []string{"/api"},
	})
	if err != nil {
		t.Fatalf("NewPassthrough: %v", err)
	}

	cases := []map[string]any{
		{"path": ""},
		{"path": "/api/x", "query": "not-an-object"},
		{"path": "/api/x", "headers": "not-an-object"},
		{"path": "/api/x", "headers": map[string]any{"X-Bad": "a\r\nb"}},
		{"path": "/api/x", "headers": map[string]any{"Bad Name": "v"}},
		{"path": "/api/x", "body": map[string]any{"bad": make(chan int)}},
	}
	for _, args := range cases {
		if _, err := passthrough.Invoke(context.Background(), args); err == nil {
			t.Errorf("args %v should be refused", args)
		} else if !errors.Is(err, tool.ErrInvalidArguments) {
			t.Errorf("args %v: err = %v", args, err)
		}
	}
}

func TestNewPassthroughValidation(t *testing.T) {
	client := newClient(t, "https://example.com", nil, 1<<20, 1<<20)
	if _, err := NewPassthrough(client, config.Passthrough{Enabled: true, Name: "x"}); err == nil {
		t.Fatal("an empty prefix list must be refused")
	}
	if _, err := NewPassthrough(client, config.Passthrough{Enabled: true, Name: "x", AllowPrefixes: []string{"/api"}, Methods: []string{"TRACE"}}); !errors.Is(err, ErrMethodNotAllowed) {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewPassthrough(client, config.Passthrough{Enabled: true, Name: "x", AllowPrefixes: []string{"/api"}}); err == nil {
		t.Fatal("an empty method list must be refused")
	}
}

func TestOperationConfiguredHeadersAreSent(t *testing.T) {
	var trace string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trace = r.Header.Get("X-Trace")
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	client := newClient(t, server.URL, nil, 1<<20, 1<<20)
	operation, err := NewOperation(client, config.Operation{
		Name:    "ping",
		Method:  "GET",
		Path:    "/ping",
		Headers: map[string]string{"X-Trace": "static"},
	})
	if err != nil {
		t.Fatalf("NewOperation: %v", err)
	}
	if _, err := operation.Invoke(context.Background(), nil); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if trace != "static" {
		t.Fatalf("X-Trace = %q", trace)
	}
}

// laxSchema disables argument-shape enforcement so the binding errors behind it
// can be exercised directly. It mirrors what a caller of Invoke could construct,
// not what tools/list publishes.
func laxSchema() map[string]any { return map[string]any{"type": "object"} }

func TestOperationBindingErrors(t *testing.T) {
	client := newClient(t, "http://127.0.0.1:1", nil, 1<<20, 1<<20)
	operation, err := NewOperation(client, config.Operation{
		Name:   "get",
		Method: "GET",
		Path:   "/things/{id}",
		Params: []config.Param{
			{Name: "id", In: config.ParamInPath},
			{Name: "q", In: config.ParamInQuery},
			{Name: "X-Trace", In: config.ParamInHeader},
		},
		InputSchema: laxSchema(),
	})
	if err != nil {
		t.Fatalf("NewOperation: %v", err)
	}

	cases := []struct {
		name string
		args map[string]any
	}{
		{name: "missing path value", args: map[string]any{}},
		{name: "non-scalar path value", args: map[string]any{"id": []any{"a"}}},
		{name: "unexpandable path value", args: map[string]any{"id": ".."}},
		{name: "non-scalar query value", args: map[string]any{"id": "1", "q": map[string]any{"a": 1}}},
		{name: "non-scalar header value", args: map[string]any{"id": "1", "X-Trace": []any{"a"}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := operation.Invoke(context.Background(), testCase.args); !errors.Is(err, tool.ErrInvalidArguments) {
				t.Fatalf("err = %v, want an invalid-arguments error", err)
			}
		})
	}
}

func TestOperationOptionalParamAndNilHeaders(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	client := newClient(t, server.URL, nil, 1<<20, 1<<20)
	operation, err := NewOperation(client, config.Operation{
		Name:   "get",
		Method: "GET",
		Path:   "/things/{id}",
		Params: []config.Param{
			{Name: "id", In: config.ParamInPath},
			{Name: "q", In: config.ParamInQuery},
		},
	})
	if err != nil {
		t.Fatalf("NewOperation: %v", err)
	}
	// An Operation built by hand may carry no headers at all.
	operation.headers = nil
	if _, err := operation.Invoke(context.Background(), map[string]any{"id": "7"}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if path != "/things/7" {
		t.Fatalf("path = %q", path)
	}
}

func TestOperationQueryArrays(t *testing.T) {
	var query url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	client := newClient(t, server.URL, nil, 1<<20, 1<<20)
	operation, err := NewOperation(client, config.Operation{
		Name:   "search",
		Method: "GET",
		Path:   "/search",
		Params: []config.Param{{Name: "tags", In: config.ParamInQuery, Schema: map[string]any{"type": "array"}}},
	})
	if err != nil {
		t.Fatalf("NewOperation: %v", err)
	}
	if _, err := operation.Invoke(context.Background(), map[string]any{"tags": []any{"a", "b"}}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if got := query["tags"]; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("tags = %v", got)
	}
	if _, err := operation.Invoke(context.Background(), map[string]any{"tags": []any{map[string]any{"a": 1}}}); !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("err = %v, want an invalid-arguments error", err)
	}
}

func TestOperationBodyEncodingFailure(t *testing.T) {
	client := newClient(t, "http://127.0.0.1:1", nil, 1<<20, 1<<20)
	operation, err := NewOperation(client, config.Operation{
		Name:      "create",
		Method:    "POST",
		Path:      "/things",
		BodyParam: "payload",
		Params:    []config.Param{{Name: "payload", In: config.ParamInBody}},
	})
	if err != nil {
		t.Fatalf("NewOperation: %v", err)
	}
	if _, err := operation.Invoke(context.Background(), map[string]any{"payload": make(chan int)}); !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("err = %v, want an invalid-arguments error", err)
	}
}

func TestOperationUpstreamFailureIsAnErrorResult(t *testing.T) {
	client := newClient(t, "http://127.0.0.1:1", nil, 1<<20, 1<<20)
	operation, err := NewOperation(client, config.Operation{Name: "ping", Method: "GET", Path: "/ping"})
	if err != nil {
		t.Fatalf("NewOperation: %v", err)
	}
	result, err := operation.Invoke(context.Background(), nil)
	if err != nil {
		t.Fatalf("Invoke returned a protocol error: %v", err)
	}
	if !result.IsError || result.Text == "" {
		t.Fatalf("result = %+v, want an isError result with the failure text", result)
	}
}

func TestPassthroughMapperErrors(t *testing.T) {
	client := newClient(t, "http://127.0.0.1:1", nil, 1<<20, 1<<20)
	passthrough, err := NewPassthrough(client, config.Passthrough{
		Enabled:       true,
		Name:          "demo_request",
		Methods:       []string{"GET"},
		AllowPrefixes: []string{"/api"},
	})
	if err != nil {
		t.Fatalf("NewPassthrough: %v", err)
	}
	if _, err := passthrough.Invoke(context.Background(), map[string]any{
		"path":    "/api/x",
		"headers": map[string]any{"X-List": []any{"a"}},
	}); !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("err = %v, want an invalid-arguments error for a non-scalar header", err)
	}

	// The published schema rejects these shapes first; replacing it exercises
	// the mapper's own guards.
	passthrough.schema = laxSchema()
	cases := []struct {
		name string
		args map[string]any
	}{
		{name: "method is not scalar", args: map[string]any{"path": "/api/x", "method": []any{"GET"}}},
		{name: "method is not allowed", args: map[string]any{"path": "/api/x", "method": "POST"}},
		{name: "query is not an object", args: map[string]any{"path": "/api/x", "query": "nope"}},
		{name: "headers is not an object", args: map[string]any{"path": "/api/x", "headers": "nope"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := passthrough.Invoke(context.Background(), testCase.args); !errors.Is(err, tool.ErrInvalidArguments) {
				t.Fatalf("err = %v, want an invalid-arguments error", err)
			}
		})
	}
}

func TestPassthroughUpstreamFailureIsAnErrorResult(t *testing.T) {
	client := newClient(t, "http://127.0.0.1:1", nil, 1<<20, 1<<20)
	passthrough, err := NewPassthrough(client, config.Passthrough{
		Enabled:       true,
		Name:          "demo_request",
		Methods:       []string{"GET"},
		AllowPrefixes: []string{"/api"},
	})
	if err != nil {
		t.Fatalf("NewPassthrough: %v", err)
	}
	result, err := passthrough.Invoke(context.Background(), map[string]any{"path": "/api/x"})
	if err != nil {
		t.Fatalf("Invoke returned a protocol error: %v", err)
	}
	if !result.IsError || result.Text == "" {
		t.Fatalf("result = %+v, want an isError result with the failure text", result)
	}
}

func TestEncodeBody(t *testing.T) {
	if encoded, contentType, err := encodeBody(nil, ""); err != nil || encoded != nil || contentType != "" {
		t.Fatalf("nil = %v %q %v", encoded, contentType, err)
	}
	encoded, contentType, err := encodeBody("text", "application/json")
	if err != nil || string(encoded) != "text" || contentType != "application/json" {
		t.Fatalf("string = %q %q %v", encoded, contentType, err)
	}
	encoded, contentType, err = encodeBody(map[string]any{"a": 1}, "")
	if err != nil || !strings.Contains(string(encoded), `"a":1`) || contentType != "application/json" {
		t.Fatalf("object = %q %q %v", encoded, contentType, err)
	}
	if _, _, err := encodeBody(map[string]any{"bad": make(chan int)}, ""); !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("err = %v", err)
	}
}
