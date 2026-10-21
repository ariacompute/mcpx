package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"mcpx/internal/tool"
)

// DefaultProtocolVersions is the fallback list when the caller does not pass
// one; the config package normally supplies the same list.
var DefaultProtocolVersions = []string{"2025-06-18", "2025-03-26"}

// Caller executes a tool by name. The gateway implements it; keeping it an
// interface means the protocol layer has no dependency on HTTP.
type Caller interface {
	Call(ctx context.Context, name string, args map[string]any) (tool.Result, error)
}

// Server is the transport-independent MCP protocol core. One Handle call
// takes a raw JSON-RPC message and returns the raw reply (nil for a
// notification), so HTTP and stdio share every protocol decision.
type Server struct {
	Name         string
	Version      string
	Protocols    []string
	Instructions string
	Tools        *tool.Registry
	Caller       Caller
	PageSize     int
	Logger       *slog.Logger
}

// Handle processes one JSON-RPC message.
func (s *Server) Handle(ctx context.Context, payload []byte) []byte {
	response := s.handle(ctx, payload)
	if response == nil {
		return nil
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		s.logger().Error("encode response", "error", err)
		return nil
	}
	return encoded
}

func (s *Server) handle(ctx context.Context, payload []byte) *Response {
	req := &Request{}
	if err := json.Unmarshal(payload, req); err != nil {
		return errorResponse(nil, newError(CodeParseError, "parse error"))
	}
	if req.JSONRPC != JSONRPCVersion {
		return errorResponse(req.ID, newError(CodeInvalidRequest, `invalid request: jsonrpc must be "2.0"`))
	}
	if strings.TrimSpace(req.Method) == "" {
		return errorResponse(req.ID, newError(CodeInvalidRequest, "invalid request: method is empty"))
	}
	if req.IsNotification() {
		// Notifications are work the caller does not want an answer to. They
		// are accepted even when unknown, as the spec requires.
		return nil
	}
	result, rpcErr := s.dispatch(ctx, req)
	if rpcErr != nil {
		return errorResponse(req.ID, rpcErr)
	}
	return successResponse(req.ID, result)
}

func (s *Server) dispatch(ctx context.Context, req *Request) (any, *Error) {
	switch req.Method {
	case MethodInitialize:
		return s.initialize(req.Params)
	case MethodPing:
		return map[string]any{}, nil
	case MethodToolsList:
		return s.listTools(req.Params)
	case MethodToolsCall:
		return s.callTool(ctx, req.Params)
	default:
		return nil, newError(CodeMethodNotFound, "method not found: "+req.Method)
	}
}

func (s *Server) initialize(raw json.RawMessage) (any, *Error) {
	params := InitializeParams{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, invalidParams("initialize: params must be an object")
		}
	}
	return InitializeResult{
		ProtocolVersion: s.negotiate(params.ProtocolVersion),
		Capabilities:    ServerCapabilities{Tools: &ToolsCapability{ListChanged: false}},
		ServerInfo:      Implementation{Name: s.serverName(), Version: s.serverVersion()},
		Instructions:    s.Instructions,
	}, nil
}

// negotiate echoes the client's revision when this gateway speaks it and
// otherwise answers with its preferred one, which is what the spec asks a
// server to do instead of failing the handshake.
func (s *Server) negotiate(requested string) string {
	supported := s.protocols()
	for _, version := range supported {
		if version == requested {
			return version
		}
	}
	if len(supported) == 0 {
		return DefaultProtocolVersions[0]
	}
	return supported[0]
}

func (s *Server) listTools(raw json.RawMessage) (any, *Error) {
	if s.Tools == nil {
		return nil, newError(CodeInternalError, "tools/list: no tool registry configured")
	}
	params := struct {
		Cursor string `json:"cursor"`
	}{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, invalidParams("tools/list: params must be an object")
		}
	}
	tools, next, err := s.Tools.Page(params.Cursor, s.PageSize)
	if err != nil {
		return nil, invalidParams(err.Error())
	}
	described := make([]ToolDescription, 0, len(tools))
	for _, t := range tools {
		described = append(described, ToolDescription{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return ToolListResult{Tools: described, NextCursor: next}, nil
}

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (any, *Error) {
	if len(raw) == 0 {
		return nil, invalidParams("tools/call: params are required")
	}
	params := ToolCallParams{}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, invalidParams("tools/call: params must be an object")
	}
	if strings.TrimSpace(params.Name) == "" {
		return nil, invalidParams("tools/call: name is required")
	}
	if s.Tools == nil {
		return nil, newError(CodeInternalError, "tools/call: no tool registry configured")
	}
	registered, ok := s.Tools.Get(params.Name)
	if !ok {
		return nil, invalidParams("tools/call: unknown tool " + params.Name)
	}
	if s.Caller == nil {
		return nil, newError(CodeInternalError, "tools/call: no caller configured")
	}
	result, err := s.Caller.Call(ctx, registered.Name(), params.Arguments)
	if err != nil {
		s.logger().Error("tool call failed", "tool", registered.Name(), "error", err)
		var invalid *tool.InvalidArgumentsError
		if errors.As(err, &invalid) {
			return CallToolResult{
				Content: []Content{TextContent(invalid.Error())},
				IsError: true,
			}, nil
		}
		return nil, newError(CodeInternalError, "tools/call: execution failed")
	}
	return CallToolResult{
		Content:           []Content{TextContent(result.Text)},
		StructuredContent: result.Structured,
		IsError:           result.IsError,
	}, nil
}

func (s *Server) protocols() []string {
	if len(s.Protocols) > 0 {
		return s.Protocols
	}
	return DefaultProtocolVersions
}

func (s *Server) serverName() string {
	if strings.TrimSpace(s.Name) == "" {
		return "mcpx"
	}
	return s.Name
}

func (s *Server) serverVersion() string {
	if strings.TrimSpace(s.Version) == "" {
		return "dev"
	}
	return s.Version
}

func (s *Server) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}
