// Package config loads the declarative gateway description: which HTTP
// services exist (upstreams), which of their endpoints become MCP tools
// (operations) and what a caller may reach through the generic passthrough.
//
// The file is YAML; JSON works too because YAML is a superset and the same
// decoder reads both. Credentials never live in the file: an auth block only
// names the environment variable that holds the token.
package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Supported values for auth.scheme.
const (
	SchemeNone   = "none"
	SchemeBearer = "bearer"
	SchemeHeader = "header"
)

// Locations a parameter can bind to.
const (
	ParamInPath   = "path"
	ParamInQuery  = "query"
	ParamInHeader = "header"
	ParamInBody   = "body"
)

// Defaults applied when the file leaves a field out.
const (
	DefaultListen           = "127.0.0.1:8848"
	DefaultPath             = "/mcp"
	DefaultTimeout          = 15 * time.Second
	DefaultMaxResponseBytes = 4 << 20
	DefaultMaxBodyBytes     = 1 << 20
	DefaultMaxRequestBytes  = 1 << 20
	DefaultSessionTTL       = 30 * time.Minute
	DefaultShutdownGrace    = 15 * time.Second
)

// DefaultProtocolVersions lists the MCP revisions this gateway speaks, most
// preferred first. The first entry is what an unknown client is told we use.
var DefaultProtocolVersions = []string{"2025-06-18", "2025-03-26"}

// Duration is a time.Duration read from "15s" in YAML or JSON.
type Duration time.Duration

// Std returns the standard library value.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// String renders the duration the way it is written in the file.
func (d Duration) String() string { return time.Duration(d).String() }

// UnmarshalYAML accepts the "15s" form and rejects bare numbers, which are
// almost always a unit mistake.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be a scalar like \"15s\"")
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value.Value))
	if err != nil {
		return fmt.Errorf("invalid duration %q", value.Value)
	}
	*d = Duration(parsed)
	return nil
}

// UnmarshalJSON mirrors UnmarshalYAML and additionally accepts nanoseconds,
// the shape encoding/json produces for a time.Duration.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		parsed, err := time.ParseDuration(strings.TrimSpace(text))
		if err != nil {
			return fmt.Errorf("invalid duration %q", text)
		}
		*d = Duration(parsed)
		return nil
	}
	var nanos int64
	if err := json.Unmarshal(data, &nanos); err != nil {
		return fmt.Errorf("invalid duration %s", string(data))
	}
	*d = Duration(time.Duration(nanos))
	return nil
}

// Config is one gateway description.
type Config struct {
	Server    Server     `yaml:"server" json:"server"`
	Defaults  Defaults   `yaml:"defaults" json:"defaults"`
	Upstreams []Upstream `yaml:"upstreams" json:"upstreams"`
}

// Server describes the MCP endpoint this process exposes.
type Server struct {
	Listen           string   `yaml:"listen" json:"listen"`
	Path             string   `yaml:"path" json:"path"`
	Auth             *Auth    `yaml:"auth" json:"auth"`
	ProtocolVersions []string `yaml:"protocol_versions" json:"protocol_versions"`
	AllowedOrigins   []string `yaml:"allowed_origins" json:"allowed_origins"`
	Instructions     string   `yaml:"instructions" json:"instructions"`
	MaxRequestBytes  int64    `yaml:"max_request_bytes" json:"max_request_bytes"`
	SessionTTL       Duration `yaml:"session_ttl" json:"session_ttl"`
	ShutdownGrace    Duration `yaml:"shutdown_grace" json:"shutdown_grace"`
}

// Defaults are inherited by every upstream that does not override them.
type Defaults struct {
	Timeout          Duration `yaml:"timeout" json:"timeout"`
	MaxResponseBytes int64    `yaml:"max_response_bytes" json:"max_response_bytes"`
	MaxBodyBytes     int64    `yaml:"max_body_bytes" json:"max_body_bytes"`
}

// Upstream is one HTTP service, the nginx-style "server" of this gateway.
type Upstream struct {
	Name             string            `yaml:"name" json:"name"`
	BaseURL          string            `yaml:"base_url" json:"base_url"`
	Headers          map[string]string `yaml:"headers" json:"headers"`
	Auth             *Auth             `yaml:"auth" json:"auth"`
	Timeout          Duration          `yaml:"timeout" json:"timeout"`
	MaxResponseBytes int64             `yaml:"max_response_bytes" json:"max_response_bytes"`
	// AllowPrefixes bounds every path this upstream may be asked for. It is
	// the upstream-level half of the passthrough guard.
	AllowPrefixes []string     `yaml:"allow_prefixes" json:"allow_prefixes"`
	Operations    []Operation  `yaml:"operations" json:"operations"`
	Passthrough   *Passthrough `yaml:"passthrough" json:"passthrough"`
}

// Operation is one HTTP endpoint promoted to a named MCP tool.
type Operation struct {
	Name        string            `yaml:"name" json:"name"`
	Description string            `yaml:"description" json:"description"`
	Method      string            `yaml:"method" json:"method"`
	Path        string            `yaml:"path" json:"path"`
	Params      []Param           `yaml:"params" json:"params"`
	BodyParam   string            `yaml:"body_param" json:"body_param"`
	Headers     map[string]string `yaml:"headers" json:"headers"`
	// InputSchema replaces the schema derived from Params. It only affects
	// what a client is shown; the derived bindings still decide what is sent.
	InputSchema map[string]any `yaml:"input_schema" json:"input_schema"`
}

// Param binds one tool argument to a place in the outgoing request.
type Param struct {
	Name        string         `yaml:"name" json:"name"`
	In          string         `yaml:"in" json:"in"`
	Required    bool           `yaml:"required" json:"required"`
	Description string         `yaml:"description" json:"description"`
	Schema      map[string]any `yaml:"schema" json:"schema"`
}

// Passthrough exposes one restricted generic tool per upstream: the caller
// picks method and path, the gateway only permits the configured prefixes.
type Passthrough struct {
	Enabled       bool     `yaml:"enabled" json:"enabled"`
	Name          string   `yaml:"name" json:"name"`
	Methods       []string `yaml:"methods" json:"methods"`
	AllowPrefixes []string `yaml:"allow_prefixes" json:"allow_prefixes"`
}

// Auth references a credential by environment variable name. The value itself
// is read at call time and never stored in the config, the logs or a task.
type Auth struct {
	Scheme     string `yaml:"scheme" json:"scheme"`
	TokenEnv   string `yaml:"token_env" json:"token_env"`
	HeaderName string `yaml:"header_name" json:"header_name"`
}

// ToolName is the MCP tool name an operation is published under. Namespacing
// by upstream keeps two services from fighting over "list".
func ToolName(upstream, operation string) string {
	return upstream + "_" + operation
}

// FindUpstream returns the upstream carrying a name, or nil.
func (c *Config) FindUpstream(name string) *Upstream {
	for i := range c.Upstreams {
		if c.Upstreams[i].Name == name {
			return &c.Upstreams[i]
		}
	}
	return nil
}
