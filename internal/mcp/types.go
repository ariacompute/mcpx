package mcp

// Implementation identifies this server to a client.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolsCapability announces the only capability this gateway has.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

// ServerCapabilities is the capabilities object of initialize.
type ServerCapabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

// InitializeParams is what the client sends.
type InitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities,omitempty"`
	ClientInfo      Implementation `json:"clientInfo"`
}

// InitializeResult is the negotiated handshake answer.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      Implementation     `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

// ToolDescription is one entry of tools/list.
type ToolDescription struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ToolListResult is the tools/list answer, with a cursor when more pages exist.
type ToolListResult struct {
	Tools      []ToolDescription `json:"tools"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

// ToolCallParams is what the client sends to tools/call.
type ToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// Content is one block of a tool result. This gateway only produces text.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// TextContent builds a text content block.
func TextContent(text string) Content {
	return Content{Type: "text", Text: text}
}

// CallToolResult is the tools/call answer. IsError marks a failed call whose
// explanation is in Content; the JSON-RPC call itself succeeded.
type CallToolResult struct {
	Content           []Content `json:"content"`
	StructuredContent any       `json:"structuredContent,omitempty"`
	IsError           bool      `json:"isError,omitempty"`
}
