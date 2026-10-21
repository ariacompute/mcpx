package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"mcpx/internal/tool"
)

type stubTool struct{ name string }

func (s stubTool) Name() string                { return s.name }
func (s stubTool) Description() string         { return "stub " + s.name }
func (s stubTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (s stubTool) Invoke(context.Context, map[string]any) (tool.Result, error) {
	return tool.Result{Text: "invoked " + s.name}, nil
}

type stubCaller struct {
	called string
	args   map[string]any
	result tool.Result
	err    error
}

func (s *stubCaller) Call(_ context.Context, name string, args map[string]any) (tool.Result, error) {
	s.called = name
	s.args = args
	if s.err != nil {
		return tool.Result{}, s.err
	}
	return s.result, nil
}

func newTestServer(t *testing.T, pageSize int, names ...string) (*Server, *stubCaller) {
	t.Helper()
	tools := make([]tool.Tool, 0, len(names))
	for _, name := range names {
		tools = append(tools, stubTool{name: name})
	}
	registry, err := tool.NewRegistry(tools...)
	if err != nil {
		t.Fatal(err)
	}
	caller := &stubCaller{result: tool.Result{Text: "done"}}
	return &Server{
		Name:      "mcpx",
		Version:   "test",
		Protocols: []string{"2025-06-18", "2025-03-26"},
		Tools:     registry,
		Caller:    caller,
		PageSize:  pageSize,
	}, caller
}

func exchange(t *testing.T, server *Server, message string) map[string]any {
	t.Helper()
	payload := server.Handle(context.Background(), []byte(message))
	if payload == nil {
		return nil
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, payload)
	}
	return decoded
}

func errorCode(t *testing.T, response map[string]any) int {
	t.Helper()
	raw, ok := response["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no error: %v", response)
	}
	return int(raw["code"].(float64))
}

func TestInitializeNegotiatesProtocol(t *testing.T) {
	server, _ := newTestServer(t, 0, "a")

	response := exchange(t, server, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","clientInfo":{"name":"test","version":"1"}}}`)
	result := response["result"].(map[string]any)
	if result["protocolVersion"] != "2025-03-26" {
		t.Errorf("negotiated = %v, want the client's version", result["protocolVersion"])
	}
	if result["serverInfo"].(map[string]any)["name"] != "mcpx" {
		t.Errorf("serverInfo = %v", result["serverInfo"])
	}
	capabilities := result["capabilities"].(map[string]any)
	if _, ok := capabilities["tools"]; !ok {
		t.Error("tools capability missing")
	}

	response = exchange(t, server, `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	result = response["result"].(map[string]any)
	if result["protocolVersion"] != "2025-06-18" {
		t.Errorf("fallback = %v, want the preferred version", result["protocolVersion"])
	}
}

func TestPing(t *testing.T) {
	server, _ := newTestServer(t, 0, "a")
	response := exchange(t, server, `{"jsonrpc":"2.0","id":"p","method":"ping"}`)
	if response["id"] != "p" {
		t.Errorf("id = %v, want the string id echoed", response["id"])
	}
	if response["result"] == nil {
		t.Error("ping should return an empty result object")
	}
}

func TestToolsListPagination(t *testing.T) {
	server, _ := newTestServer(t, 2, "a", "b", "c")

	first := exchange(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	result := first["result"].(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("first page size = %d", len(tools))
	}
	cursor, _ := result["nextCursor"].(string)
	if cursor == "" {
		t.Fatal("first page should carry a cursor")
	}

	second := exchange(t, server, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"cursor":"`+cursor+`"}}`)
	result = second["result"].(map[string]any)
	tools = result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("second page size = %d", len(tools))
	}
	if _, ok := result["nextCursor"]; ok {
		t.Error("last page must not carry a cursor")
	}
}

func TestToolsListRejectsBadCursor(t *testing.T) {
	server, _ := newTestServer(t, 2, "a")
	response := exchange(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"cursor":"!!"}}`)
	if code := errorCode(t, response); code != CodeInvalidParams {
		t.Fatalf("code = %d, want %d", code, CodeInvalidParams)
	}
}

func TestToolsCall(t *testing.T) {
	server, caller := newTestServer(t, 0, "demo_run")
	caller.result = tool.Result{Text: "hello", Structured: map[string]any{"ok": true}}

	response := exchange(t, server, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"demo_run","arguments":{"q":"x"}}}`)
	result := response["result"].(map[string]any)
	content := result["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["text"] != "hello" {
		t.Fatalf("content = %v", content)
	}
	if caller.called != "demo_run" || caller.args["q"] != "x" {
		t.Fatalf("caller saw %q %v", caller.called, caller.args)
	}
	if _, ok := result["isError"]; ok {
		t.Error("a successful call must not set isError")
	}
}

func TestToolsCallUnknownTool(t *testing.T) {
	server, _ := newTestServer(t, 0, "a")
	response := exchange(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope"}}`)
	if code := errorCode(t, response); code != CodeInvalidParams {
		t.Fatalf("code = %d, want %d", code, CodeInvalidParams)
	}
}

func TestToolsCallInvalidArgumentsBecomesIsError(t *testing.T) {
	server, caller := newTestServer(t, 0, "a")
	caller.err = &tool.InvalidArgumentsError{Message: "argument \"id\" is required"}

	response := exchange(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"a","arguments":{}}}`)
	result := response["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("result = %v, want isError true", result)
	}
	content := result["content"].([]any)
	if content[0].(map[string]any)["text"] != "argument \"id\" is required" {
		t.Fatalf("content = %v", content)
	}
}

func TestProtocolErrors(t *testing.T) {
	server, _ := newTestServer(t, 0, "a")

	parse := exchange(t, server, `{"jsonrpc":`)
	if code := errorCode(t, parse); code != CodeParseError {
		t.Errorf("parse error code = %d", code)
	}
	if parse["id"] != nil {
		t.Errorf("parse error id = %v, want null", parse["id"])
	}

	version := exchange(t, server, `{"jsonrpc":"1.0","id":1,"method":"ping"}`)
	if code := errorCode(t, version); code != CodeInvalidRequest {
		t.Errorf("version code = %d", code)
	}

	method := exchange(t, server, `{"jsonrpc":"2.0","id":1,"method":"nope"}`)
	if code := errorCode(t, method); code != CodeMethodNotFound {
		t.Errorf("method code = %d", code)
	}

	empty := exchange(t, server, `{"jsonrpc":"2.0","id":1}`)
	if code := errorCode(t, empty); code != CodeInvalidRequest {
		t.Errorf("empty method code = %d", code)
	}
}

func TestMissingRegistryIsAnInternalError(t *testing.T) {
	server := &Server{Name: "mcpx", Version: "test"}
	response := exchange(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if code := errorCode(t, response); code != CodeInternalError {
		t.Fatalf("code = %d, want %d", code, CodeInternalError)
	}
	response = exchange(t, server, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"x"}}`)
	if code := errorCode(t, response); code != CodeInternalError {
		t.Fatalf("code = %d, want %d", code, CodeInternalError)
	}
}

func TestNotificationsGetNoResponse(t *testing.T) {
	server, caller := newTestServer(t, 0, "a")
	for _, message := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`,
		`{"jsonrpc":"2.0","method":"nope"}`,
	} {
		if response := exchange(t, server, message); response != nil {
			t.Fatalf("%s got a response: %v", message, response)
		}
	}
	if caller.called != "" {
		t.Fatal("a notification must not call a tool")
	}
}
