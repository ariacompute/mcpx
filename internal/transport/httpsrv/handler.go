// Package httpsrv serves the MCP Streamable HTTP transport: one endpoint,
// JSON replies, a session header and 202 for notifications. It deliberately
// has no opinion about tools; the mcp package decides protocol behaviour.
package httpsrv

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mcpx/internal/mcp"
)

// Options configures the endpoint.
type Options struct {
	Path            string
	Server          *mcp.Server
	BearerToken     string
	AllowedOrigins  []string
	MaxRequestBytes int64
	SessionTTL      time.Duration
	Version         string
	Logger          *slog.Logger
}

// Handler is the HTTP face of the gateway. It is safe for concurrent use.
type Handler struct {
	opts     Options
	sessions *sessionStore
	logger   *slog.Logger
	started  time.Time
}

// New builds the handler and its session store.
func New(opts Options) *Handler {
	if strings.TrimSpace(opts.Path) == "" {
		opts.Path = "/mcp"
	}
	if opts.MaxRequestBytes <= 0 {
		opts.MaxRequestBytes = 1 << 20
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Handler{
		opts:     opts,
		sessions: newSessionStore(opts.SessionTTL),
		logger:   opts.Logger,
		started:  time.Now(),
	}
}

// ServeHTTP routes health and the single MCP endpoint.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		h.handleHealth(w, r)
		return
	}
	if r.URL.Path != h.opts.Path {
		http.NotFound(w, r)
		return
	}
	if !h.originAllowed(r) {
		writeProblem(w, http.StatusForbidden, "origin_not_allowed", "Origin is not allowed")
		return
	}
	if h.opts.BearerToken != "" && !authorized(r, h.opts.BearerToken) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="mcpx"`)
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "a bearer token is required")
		return
	}
	switch r.Method {
	case http.MethodPost:
		h.handlePost(w, r)
	case http.MethodDelete:
		h.handleDelete(w, r)
	case http.MethodGet, http.MethodHead:
		w.Header().Set("Allow", "POST, DELETE")
		writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed",
			"this gateway does not open server-initiated streams; use POST")
	default:
		w.Header().Set("Allow", "POST, DELETE")
		writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method")
	}
}

func (h *Handler) handlePost(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeProblem(w, http.StatusUnsupportedMediaType, "unsupported_media_type",
			"Content-Type must be application/json")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.opts.MaxRequestBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblem(w, http.StatusRequestEntityTooLarge, "request_too_large",
				fmt.Sprintf("request body exceeds %d bytes", h.opts.MaxRequestBytes))
			return
		}
		writeProblem(w, http.StatusBadRequest, "bad_request", "request body could not be read")
		return
	}

	// The session decision needs the method before the protocol layer parses
	// the message; a body that does not parse is answered by the protocol
	// layer with a proper JSON-RPC error.
	probe := struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
	}{}
	if json.Unmarshal(body, &probe) == nil && probe.Method != "" {
		sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
		if probe.Method == mcp.MethodInitialize {
			if sessionID != "" && !h.sessions.Touch(sessionID) {
				writeProblem(w, http.StatusNotFound, "session_not_found",
					"unknown Mcp-Session-Id; send initialize again")
				return
			}
			id, err := h.sessions.Create()
			if err != nil {
				h.logger.Error("create session", "error", err)
				writeProblem(w, http.StatusInternalServerError, "internal_error", "session could not be created")
				return
			}
			w.Header().Set("Mcp-Session-Id", id)
		} else {
			if sessionID == "" {
				writeProblem(w, http.StatusBadRequest, "session_required",
					"Mcp-Session-Id is required after initialize")
				return
			}
			if !h.sessions.Touch(sessionID) {
				writeProblem(w, http.StatusNotFound, "session_not_found",
					"unknown Mcp-Session-Id; send initialize again")
				return
			}
		}
	}

	response := h.opts.Server.Handle(r.Context(), body)
	if response == nil {
		// A notification is accepted and answered with no body, as the
		// Streamable HTTP transport requires.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	status := http.StatusOK
	var decoded struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(response, &decoded) == nil && decoded.Error != nil && decoded.Error.Code == mcp.CodeParseError {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, response)
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
	if sessionID == "" {
		writeProblem(w, http.StatusBadRequest, "session_required", "Mcp-Session-Id is required")
		return
	}
	if !h.sessions.Delete(sessionID) {
		writeProblem(w, http.StatusNotFound, "session_not_found", "unknown Mcp-Session-Id")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	tools := 0
	if h.opts.Server != nil && h.opts.Server.Tools != nil {
		tools = h.opts.Server.Tools.Len()
	}
	payload, err := json.Marshal(map[string]any{
		"status":         "ok",
		"version":        h.opts.Version,
		"tools":          tools,
		"sessions":       h.sessions.Count(),
		"uptime_seconds": int64(time.Since(h.started).Seconds()),
	})
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "internal_error", "health could not be rendered")
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

// originAllowed blocks browser origins that were not declared. Requests with
// no Origin (every non-browser MCP client) pass; with no allowlist configured
// only loopback origins are trusted, which covers local IDE plugins.
func (h *Handler) originAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	normalized := strings.TrimSuffix(origin, "/")
	if len(h.opts.AllowedOrigins) == 0 {
		parsed, err := url.Parse(normalized)
		if err != nil {
			return false
		}
		switch strings.ToLower(parsed.Hostname()) {
		case "localhost", "127.0.0.1", "::1":
			return true
		default:
			return false
		}
	}
	for _, allowed := range h.opts.AllowedOrigins {
		if allowed == "*" || strings.EqualFold(strings.TrimSuffix(allowed, "/"), normalized) {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, payload []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

type problem struct {
	Error problemDetail `json:"error"`
}

type problemDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeProblem(w http.ResponseWriter, status int, code, message string) {
	payload, err := json.Marshal(problem{Error: problemDetail{Code: code, Message: message}})
	if err != nil {
		http.Error(w, message, status)
		return
	}
	writeJSON(w, status, payload)
}
