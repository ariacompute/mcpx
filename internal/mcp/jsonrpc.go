package mcp

import "encoding/json"

// JSON-RPC method names this gateway implements. The notification names are
// listed because they must be accepted silently, not because they do work.
const (
	MethodInitialize  = "initialize"
	MethodInitialized = "notifications/initialized"
	MethodCancelled   = "notifications/cancelled"
	MethodPing        = "ping"
	MethodToolsList   = "tools/list"
	MethodToolsCall   = "tools/call"
	JSONRPCVersion    = "2.0"
	nullID            = "null"
)

// Request is one JSON-RPC message. ID is kept raw so a number id stays a
// number and a string id stays a string, exactly as the client sent it.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification reports whether the message carries no id. Per JSON-RPC such
// a message must never be answered.
func (r *Request) IsNotification() bool {
	return len(r.ID) == 0
}

// Response is one JSON-RPC reply.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

func successResponse(id json.RawMessage, result any) *Response {
	return &Response{JSONRPC: JSONRPCVersion, ID: orNull(id), Result: result}
}

func errorResponse(id json.RawMessage, err *Error) *Response {
	return &Response{JSONRPC: JSONRPCVersion, ID: orNull(id), Error: err}
}

// orNull keeps "the client sent no id" renderable as `"id": null` instead of
// dropping the field, which some clients treat as a malformed response.
func orNull(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage(nullID)
	}
	return id
}
