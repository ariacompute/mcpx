package httpsrv

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcpx/internal/mcp"
	"mcpx/internal/tool"
)

type echoTool struct{}

func (echoTool) Name() string                { return "echo" }
func (echoTool) Description() string         { return "echo back" }
func (echoTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (echoTool) Invoke(context.Context, map[string]any) (tool.Result, error) {
	return tool.Result{Text: "OK"}, nil
}

type echoCaller struct{}

func (echoCaller) Call(context.Context, string, map[string]any) (tool.Result, error) {
	return tool.Result{Text: "OK"}, nil
}

func newHandler(t *testing.T, token string, origins []string, maxBytes int64) *Handler {
	t.Helper()
	registry, err := tool.NewRegistry(echoTool{})
	if err != nil {
		t.Fatal(err)
	}
	server := &mcp.Server{
		Name:      "mcpx",
		Version:   "test",
		Protocols: []string{"2025-06-18"},
		Tools:     registry,
		Caller:    echoCaller{},
		Logger:    slog.New(slog.NewTextHandler(discard{}, nil)),
	}
	return New(Options{
		Path:            "/mcp",
		Server:          server,
		BearerToken:     token,
		AllowedOrigins:  origins,
		MaxRequestBytes: maxBytes,
		Version:         "test",
		Logger:          slog.New(slog.NewTextHandler(discard{}, nil)),
	})
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func do(t *testing.T, handler http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestFullSessionFlow(t *testing.T) {
	handler := newHandler(t, "", nil, 1<<20)

	initialized := do(t, handler, http.MethodPost, "/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, nil)
	if initialized.Code != http.StatusOK {
		t.Fatalf("initialize status = %d: %s", initialized.Code, initialized.Body.String())
	}
	session := initialized.Header().Get("Mcp-Session-Id")
	if session == "" {
		t.Fatal("initialize must return a session id")
	}

	missing := do(t, handler, http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing session status = %d, want 400", missing.Code)
	}

	listed := do(t, handler, http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		map[string]string{"Mcp-Session-Id": session})
	if listed.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d: %s", listed.Code, listed.Body.String())
	}
	if !strings.Contains(listed.Body.String(), `"echo"`) {
		t.Fatalf("tools/list body = %s", listed.Body.String())
	}

	notified := do(t, handler, http.MethodPost, "/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		map[string]string{"Mcp-Session-Id": session})
	if notified.Code != http.StatusAccepted {
		t.Fatalf("notification status = %d, want 202", notified.Code)
	}
	if notified.Body.Len() != 0 {
		t.Fatalf("notification body = %q, want empty", notified.Body.String())
	}

	called := do(t, handler, http.MethodPost, "/mcp",
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo"}}`,
		map[string]string{"Mcp-Session-Id": session})
	if called.Code != http.StatusOK || !strings.Contains(called.Body.String(), "OK") {
		t.Fatalf("tools/call status = %d body = %s", called.Code, called.Body.String())
	}

	deleted := do(t, handler, http.MethodDelete, "/mcp", "", map[string]string{"Mcp-Session-Id": session})
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", deleted.Code)
	}

	after := do(t, handler, http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`,
		map[string]string{"Mcp-Session-Id": session})
	if after.Code != http.StatusNotFound {
		t.Fatalf("terminated session status = %d, want 404", after.Code)
	}
}

func TestBearerAuthentication(t *testing.T) {
	handler := newHandler(t, "s3cret", nil, 1<<20)
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`

	anonymous := do(t, handler, http.MethodPost, "/mcp", body, nil)
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", anonymous.Code)
	}
	if anonymous.Header().Get("WWW-Authenticate") == "" {
		t.Error("401 must advertise the scheme")
	}

	wrong := do(t, handler, http.MethodPost, "/mcp", body, map[string]string{"Authorization": "Bearer nope"})
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want 401", wrong.Code)
	}

	right := do(t, handler, http.MethodPost, "/mcp", body, map[string]string{"Authorization": "Bearer s3cret"})
	if right.Code != http.StatusOK {
		t.Fatalf("right token status = %d: %s", right.Code, right.Body.String())
	}
}

func TestOriginPolicy(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`

	loopback := newHandler(t, "", nil, 1<<20)
	if recorder := do(t, loopback, http.MethodPost, "/mcp", body, map[string]string{"Origin": "http://localhost:5173"}); recorder.Code != http.StatusOK {
		t.Fatalf("loopback origin status = %d", recorder.Code)
	}
	if recorder := do(t, loopback, http.MethodPost, "/mcp", body, map[string]string{"Origin": "https://evil.example"}); recorder.Code != http.StatusForbidden {
		t.Fatalf("unknown origin status = %d, want 403", recorder.Code)
	}

	explicit := newHandler(t, "", []string{"https://app.example.com"}, 1<<20)
	if recorder := do(t, explicit, http.MethodPost, "/mcp", body, map[string]string{"Origin": "https://app.example.com"}); recorder.Code != http.StatusOK {
		t.Fatalf("allowlisted origin status = %d", recorder.Code)
	}
	if recorder := do(t, explicit, http.MethodPost, "/mcp", body, map[string]string{"Origin": "http://localhost:5173"}); recorder.Code != http.StatusForbidden {
		t.Fatalf("origin outside the allowlist = %d, want 403", recorder.Code)
	}
}

func TestRequestLimitsAndMethods(t *testing.T) {
	handler := newHandler(t, "", nil, 32)
	big := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"pad":"` + strings.Repeat("x", 128) + `"}}`
	if recorder := do(t, handler, http.MethodPost, "/mcp", big, nil); recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", recorder.Code)
	}

	stream := newHandler(t, "", nil, 1<<20)
	if recorder := do(t, stream, http.MethodGet, "/mcp", "", nil); recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", recorder.Code)
	}

	noContentType := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
	recorder := httptest.NewRecorder()
	stream.ServeHTTP(recorder, noContentType)
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("missing content type status = %d, want 415", recorder.Code)
	}
}

func TestParseErrorUsesBadRequest(t *testing.T) {
	handler := newHandler(t, "", nil, 1<<20)
	recorder := do(t, handler, http.MethodPost, "/mcp", `{"jsonrpc":`, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	var decoded struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if decoded.Error.Code != mcp.CodeParseError {
		t.Fatalf("code = %d, want %d", decoded.Error.Code, mcp.CodeParseError)
	}
}

func TestHealthz(t *testing.T) {
	handler := newHandler(t, "", nil, 1<<20)
	recorder := do(t, handler, http.MethodGet, "/healthz", "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"status":"ok"`) {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

func TestUnknownPath(t *testing.T) {
	handler := newHandler(t, "", nil, 1<<20)
	if recorder := do(t, handler, http.MethodPost, "/nope", `{}`, nil); recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}
