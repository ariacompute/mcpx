package mcp

import "fmt"

// JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Error is the JSON-RPC error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error implements the error interface so a handler can return it directly.
func (e *Error) Error() string {
	return fmt.Sprintf("mcp: %d %s", e.Code, e.Message)
}

func newError(code int, message string) *Error {
	return &Error{Code: code, Message: message}
}

func invalidParams(message string) *Error {
	return newError(CodeInvalidParams, message)
}
