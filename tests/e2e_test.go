package tests

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"mcpx/internal/config"
	"mcpx/internal/gateway"
	"mcpx/internal/mcp"
	"mcpx/internal/transport/httpsrv"
)

const configTemplate = `
server:
  listen: 127.0.0.1:0
  auth:
    scheme: bearer
    token_env: E2E_MCP_TOKEN
upstreams:
  - name: demo
    base_url: %s
    auth:
      scheme: bearer
      token_env: E2E_UPSTREAM_TOKEN
    operations:
      - name: get_pet
        description: Fetch one pet.
        method: GET
        path: /pets/{id}
        params:
          - name: id
            in: path
            required: true
    passthrough:
      enabled: true
      methods: [GET]
      allow_prefixes: [/pets]
`

type upstreamRecorder struct {
	mu    sync.Mutex
	paths []string
	auth  []string
}

func (u *upstreamRecorder) record(r *http.Request) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.paths = append(u.paths, r.URL.Path)
	u.auth = append(u.auth, r.Header.Get("Authorization"))
}

func (u *upstreamRecorder) snapshot() ([]string, []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.paths...), append([]string(nil), u.auth...)
}

func newStack(t *testing.T) (*httptest.Server, *upstreamRecorder) {
	t.Helper()
	t.Setenv("E2E_MCP_TOKEN", "mcpx-token")
	t.Setenv("E2E_UPSTREAM_TOKEN", "upstream-secret")

	recorder := &upstreamRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"9","name":"rex","status":"available"}`))
	}))
	t.Cleanup(upstream.Close)

	cfg, err := config.Decode(strings.NewReader(strings.Replace(configTemplate, "%s", upstream.URL, 1)))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	built, err := gateway.Build(cfg, logger)
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	server := &mcp.Server{
		Name:      "mcpx",
		Version:   "e2e",
		Protocols: cfg.Server.ProtocolVersions,
		Tools:     built.Registry(),
		Caller:    built,
		Logger:    logger,
	}
	handler := httpsrv.New(httpsrv.Options{
		Path:            cfg.Server.Path,
		Server:          server,
		BearerToken:     "mcpx-token",
		MaxRequestBytes: cfg.Server.MaxRequestBytes,
		SessionTTL:      cfg.Server.SessionTTL.Std(),
		Version:         "e2e",
		Logger:          logger,
	})
	front := httptest.NewServer(handler)
	t.Cleanup(front.Close)
	return front, recorder
}

func post(t *testing.T, front, session, body string) (int, string, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, front+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer mcpx-token")
	if session != "" {
		request.Header.Set("Mcp-Session-Id", session)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded := map[string]any{}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &decoded)
	}
	return response.StatusCode, response.Header.Get("Mcp-Session-Id"), decoded
}

func TestGatewayEndToEnd(t *testing.T) {
	front, recorder := newStack(t)

	status, session, initialized := post(t, front.URL, "",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"e2e","version":"1"}}}`)
	if status != http.StatusOK || session == "" {
		t.Fatalf("initialize: status=%d session=%q", status, session)
	}
	result := initialized["result"].(map[string]any)
	if result["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}

	status, _, listed := post(t, front.URL, session, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if status != http.StatusOK {
		t.Fatalf("tools/list status = %d", status)
	}
	tools := listed["result"].(map[string]any)["tools"].([]any)
	names := make([]string, 0, len(tools))
	for _, raw := range tools {
		names = append(names, raw.(map[string]any)["name"].(string))
	}
	if len(names) != 2 {
		t.Fatalf("tools = %v, want the operation and the passthrough", names)
	}
	if !contains(names, "demo_get_pet") || !contains(names, "demo_request") {
		t.Fatalf("tools = %v", names)
	}

	status, _, called := post(t, front.URL, session,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"demo_get_pet","arguments":{"id":"9"}}}`)
	if status != http.StatusOK {
		t.Fatalf("tools/call status = %d", status)
	}
	call := called["result"].(map[string]any)
	if call["isError"] == true {
		t.Fatalf("call failed: %v", call)
	}
	content := call["content"].([]any)
	if !strings.Contains(content[0].(map[string]any)["text"].(string), "rex") {
		t.Fatalf("content = %v", content)
	}

	paths, auths := recorder.snapshot()
	if len(paths) != 1 || paths[0] != "/pets/9" {
		t.Fatalf("upstream paths = %v", paths)
	}
	if auths[0] != "Bearer upstream-secret" {
		t.Fatalf("upstream authorization = %q", auths[0])
	}

	_, _, denied := post(t, front.URL, session,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"demo_request","arguments":{"path":"/admin/keys"}}}`)
	deniedCall := denied["result"].(map[string]any)
	if deniedCall["isError"] != true {
		t.Fatalf("an out-of-prefix passthrough call must be refused: %v", deniedCall)
	}
	if paths, _ := recorder.snapshot(); len(paths) != 1 {
		t.Fatalf("the refused call reached the upstream: %v", paths)
	}

	_, _, badArgs := post(t, front.URL, session,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"demo_get_pet","arguments":{"id":"9","extra":true}}}`)
	if badArgs["result"].(map[string]any)["isError"] != true {
		t.Fatalf("an unknown argument must be refused: %v", badArgs)
	}
}

func TestGatewayRejectsAnonymousClients(t *testing.T) {
	front, _ := newStack(t)
	request, err := http.NewRequest(http.MethodPost, front.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
