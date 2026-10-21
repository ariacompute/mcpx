package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const sampleYAML = `
server:
  listen: 127.0.0.1:9000
  auth:
    scheme: bearer
    token_env: MCPX_TOKEN
  allowed_origins: ["https://app.example.com", "*"]
defaults:
  timeout: 5s
  max_response_bytes: 1024
upstreams:
  - name: demo
    base_url: https://example.com/
    auth:
      scheme: header
      token_env: DEMO_TOKEN
      header_name: X-Api-Key
    allow_prefixes: ["/api"]
    operations:
      - name: get_thing
        method: get
        path: /api/things/{id}
        params:
          - name: id
            in: path
          - name: verbose
            in: query
          - name: X-Trace
            in: header
      - name: create_thing
        method: POST
        path: /api/things
        body_param: payload
        params:
          - name: payload
            in: body
    passthrough:
      enabled: true
      methods: [get]
`

func mustDecode(t *testing.T, document string) *Config {
	t.Helper()
	cfg, err := Decode(strings.NewReader(document))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return cfg
}

func TestDecodeAppliesDefaults(t *testing.T) {
	cfg := mustDecode(t, sampleYAML)

	if cfg.Server.Path != "/mcp" {
		t.Errorf("path = %q, want /mcp", cfg.Server.Path)
	}
	if got := cfg.Server.SessionTTL.Std(); got != DefaultSessionTTL {
		t.Errorf("session ttl = %v, want %v", got, DefaultSessionTTL)
	}
	if got := cfg.Server.ShutdownGrace.Std(); got != DefaultShutdownGrace {
		t.Errorf("shutdown grace = %v, want %v", got, DefaultShutdownGrace)
	}
	if got := cfg.Defaults.Timeout.Std(); got != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", got)
	}
	if cfg.Defaults.MaxBodyBytes != DefaultMaxBodyBytes || cfg.Server.MaxRequestBytes != DefaultMaxRequestBytes {
		t.Error("size defaults were not applied")
	}
	demo := cfg.FindUpstream("demo")
	if demo == nil {
		t.Fatal("upstream demo missing")
	}
	if demo.BaseURL != "https://example.com" {
		t.Errorf("base_url = %q, want the trailing slash trimmed", demo.BaseURL)
	}
	if demo.Operations[0].Method != "GET" {
		t.Errorf("method = %q, want GET", demo.Operations[0].Method)
	}
	if demo.Operations[0].Params[1].In != ParamInQuery {
		t.Errorf("param default in = %q, want query", demo.Operations[0].Params[1].In)
	}
	if demo.Passthrough.Name != "demo_request" {
		t.Errorf("passthrough name = %q", demo.Passthrough.Name)
	}
	if got := demo.Passthrough.AllowPrefixes; len(got) != 1 || got[0] != "/api" {
		t.Errorf("passthrough prefixes = %v, want the upstream allowlist", got)
	}
	if got := demo.Passthrough.Methods; len(got) != 1 || got[0] != "GET" {
		t.Errorf("passthrough methods = %v, want GET", got)
	}
	if cfg.FindUpstream("missing") != nil {
		t.Error("FindUpstream matched a name it should not")
	}
}

func TestDecodeJSON(t *testing.T) {
	document := `{
	  "server": {"listen": "127.0.0.1:1234", "session_ttl": "1m"},
	  "upstreams": [{
	    "name": "demo",
	    "base_url": "https://example.com",
	    "operations": [{"name": "ping", "path": "/ping"}]
	  }]
	}`
	cfg := mustDecode(t, document)
	if cfg.Server.Listen != "127.0.0.1:1234" {
		t.Errorf("listen = %q", cfg.Server.Listen)
	}
	if cfg.Server.SessionTTL.Std() != time.Minute {
		t.Errorf("session ttl = %v", cfg.Server.SessionTTL.Std())
	}
	if cfg.Upstreams[0].Operations[0].Method != "GET" {
		t.Errorf("default method = %q", cfg.Upstreams[0].Operations[0].Method)
	}
	if cfg.Upstreams[0].Timeout.Std() != DefaultTimeout {
		t.Errorf("upstream timeout should inherit defaults.timeout")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestDecodeRejectsUnknownField(t *testing.T) {
	if _, err := Decode(strings.NewReader("server:\n  bogus: true\n")); err == nil {
		t.Fatal("expected an error for an unknown field")
	}
}

func TestDecodeRejectsEmptyFile(t *testing.T) {
	if _, err := Decode(strings.NewReader("")); err == nil {
		t.Fatal("expected an error for an empty file")
	}
}

func TestDecodeRejectsBareDuration(t *testing.T) {
	if _, err := Decode(strings.NewReader("defaults:\n  timeout: 15\n")); err == nil {
		t.Fatal("expected a bare duration to be rejected")
	}
	if _, err := Decode(strings.NewReader("defaults:\n  timeout: [1, 2]\n")); err == nil {
		t.Fatal("expected a non-scalar duration to be rejected")
	}
}

func TestLoadReadsAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcpx.yaml")
	if err := os.WriteFile(path, []byte(sampleYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.FindUpstream("demo") == nil {
		t.Fatal("Load lost the upstream")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestApplyDefaultsIsIdempotent(t *testing.T) {
	once := mustDecode(t, sampleYAML)
	twice := mustDecode(t, sampleYAML)
	twice.ApplyDefaults()
	if !reflect.DeepEqual(once, twice) {
		t.Fatal("ApplyDefaults changed an already normalised config")
	}
}

func TestDurationFormatting(t *testing.T) {
	if got := Duration(90 * time.Second).String(); got != "1m30s" {
		t.Errorf("String = %q", got)
	}
	if got := Duration(2 * time.Second).Std(); got != 2*time.Second {
		t.Errorf("Std = %v", got)
	}
	var parsed Duration
	if err := parsed.UnmarshalJSON([]byte(`"15s"`)); err != nil || parsed.Std() != 15*time.Second {
		t.Fatalf("UnmarshalJSON string: %v %v", parsed, err)
	}
	if err := parsed.UnmarshalJSON([]byte(`1000000000`)); err != nil || parsed.Std() != time.Second {
		t.Fatalf("UnmarshalJSON number: %v %v", parsed, err)
	}
	if err := parsed.UnmarshalJSON([]byte(`"nonsense"`)); err == nil {
		t.Fatal("expected an invalid duration error")
	}
	if err := parsed.UnmarshalJSON([]byte(`{}`)); err == nil {
		t.Fatal("expected a non-duration error")
	}
}

func TestValidateSample(t *testing.T) {
	cfg := mustDecode(t, sampleYAML)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name     string
		document string
		want     string
	}{
		{
			name:     "no upstreams",
			document: `server: {listen: "127.0.0.1:1"}`,
			want:     "no upstreams",
		},
		{
			name: "relative base url",
			document: `
upstreams:
  - name: demo
    base_url: example.com
    operations: [{name: ping, path: /ping}]
`,
			want: "http or https",
		},
		{
			name: "credentials in base url",
			document: `
upstreams:
  - name: demo
    base_url: https://user:pass@example.com
    operations: [{name: ping, path: /ping}]
`,
			want: "must not embed credentials",
		},
		{
			name: "query in base url",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com?x=1
    operations: [{name: ping, path: /ping}]
`,
			want: "must not carry a query",
		},
		{
			name: "dot dot in base url",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com/a/../b
    operations: [{name: ping, path: /ping}]
`,
			want: "must not contain ..",
		},
		{
			name: "bad upstream name",
			document: `
upstreams:
  - name: Demo
    base_url: https://example.com
    operations: [{name: ping, path: /ping}]
`,
			want: "name must match",
		},
		{
			name: "bad method",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, method: TRACE, path: /ping}]
`,
			want: "method",
		},
		{
			name: "relative path",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: ping}]
`,
			want: "must start with /",
		},
		{
			name: "query in path",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: "/ping?x=1"}]
`,
			want: "query, fragment or whitespace",
		},
		{
			name: "percent in path",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: "/pi%6eg"}]
`,
			want: "percent escapes",
		},
		{
			name: "dot segment in path",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: /a/../b}]
`,
			want: "..",
		},
		{
			name: "double slash path",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: "//evil"}]
`,
			want: "must not start with //",
		},
		{
			name: "malformed placeholder",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: "/pets/{id"}]
`,
			want: "malformed placeholder",
		},
		{
			name: "placeholder without param",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: "/pets/{id}"}]
`,
			want: "no param",
		},
		{
			name: "placeholder bound to a query param",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations:
      - name: ping
        path: "/pets/{id}"
        params: [{name: id, in: query}]
`,
			want: "must bind a param with in=path",
		},
		{
			name: "path param without placeholder",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations:
      - name: ping
        path: /pets
        params: [{name: id, in: path}]
`,
			want: "no placeholder",
		},
		{
			name: "duplicate tool name",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations:
      - {name: ping, path: /ping}
      - {name: ping, path: /ping2}
`,
			want: "declared by",
		},
		{
			name: "duplicate upstream",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: /ping}]
  - name: demo
    base_url: https://example.org
    operations: [{name: pong, path: /pong}]
`,
			want: "more than once",
		},
		{
			name: "empty token env",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    auth: {scheme: bearer}
    operations: [{name: ping, path: /ping}]
`,
			want: "token_env is required",
		},
		{
			name: "token env is not a variable name",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    auth: {scheme: bearer, token_env: "not-a-var"}
    operations: [{name: ping, path: /ping}]
`,
			want: "environment variable name",
		},
		{
			name: "header scheme without header name",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    auth: {scheme: header, token_env: DEMO_TOKEN}
    operations: [{name: ping, path: /ping}]
`,
			want: "header_name is required",
		},
		{
			name: "unknown auth scheme",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    auth: {scheme: digest, token_env: DEMO_TOKEN}
    operations: [{name: ping, path: /ping}]
`,
			want: "must be none, bearer or header",
		},
		{
			name: "invalid static header name",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    headers: {"Bad Header": x}
    operations: [{name: ping, path: /ping}]
`,
			want: "not a valid header name",
		},
		{
			name: "static header with CRLF",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    headers:
      X-Trace: "a\nb"
    operations: [{name: ping, path: /ping}]
`,
			want: "must not contain CR or LF",
		},
		{
			name: "bad prefix",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    allow_prefixes: ["api"]
    operations: [{name: ping, path: /ping}]
`,
			want: "prefix",
		},
		{
			name: "prefix with placeholder",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    allow_prefixes: ["/api/{id}"]
    operations: [{name: ping, path: /ping}]
`,
			want: "must not contain placeholders",
		},
		{
			name: "duplicate param",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations:
      - name: ping
        path: /ping
        params:
          - {name: q, in: query}
          - {name: q, in: query}
`,
			want: "declared twice",
		},
		{
			name: "unknown param location",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations:
      - name: ping
        path: /ping
        params: [{name: q, in: cookie}]
`,
			want: "expected path/query/header/body",
		},
		{
			name: "hyphen in a non-header param name",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations:
      - name: ping
        path: /ping
        params: [{name: q-1, in: query}]
`,
			want: "must match",
		},
		{
			name: "duplicate header param with different case",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations:
      - name: ping
        path: /ping
        params:
          - {name: X-Trace, in: header}
          - {name: x-trace, in: header}
`,
			want: "declared twice",
		},
		{
			name: "unsupported schema type",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations:
      - name: ping
        path: /ping
        params: [{name: q, in: query, schema: {type: date}}]
`,
			want: "unsupported schema type",
		},
		{
			name: "two body params",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations:
      - name: ping
        method: POST
        path: /ping
        params:
          - {name: a, in: body}
          - {name: b, in: body}
`,
			want: "only one in=body",
		},
		{
			name: "body param on GET",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations:
      - name: ping
        method: GET
        path: /ping
        body_param: payload
        params: [{name: payload, in: body}]
`,
			want: "cannot carry a body",
		},
		{
			name: "body_param not declared",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations:
      - name: ping
        method: POST
        path: /ping
        body_param: payload
`,
			want: "not declared in params",
		},
		{
			name: "body_param bound elsewhere",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations:
      - name: ping
        method: POST
        path: /ping
        body_param: q
        params: [{name: q, in: query}]
`,
			want: "must use in=body",
		},
		{
			name: "input schema is not an object",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations:
      - name: ping
        path: /ping
        input_schema: {type: string}
`,
			want: "must be an object schema",
		},
		{
			name: "upstream without tools",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
`,
			want: "at least one operation",
		},
		{
			name: "open passthrough",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: /ping}]
    passthrough: {enabled: true}
`,
			want: "allow_prefixes",
		},
		{
			name: "passthrough with a bad method",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    allow_prefixes: ["/api"]
    operations: [{name: ping, path: /ping}]
    passthrough: {enabled: true, methods: [TRACE]}
`,
			want: "not allowed",
		},
		{
			name: "passthrough with a bad name",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    allow_prefixes: ["/api"]
    operations: [{name: ping, path: /ping}]
    passthrough: {enabled: true, name: "bad name"}
`,
			want: "passthrough.name",
		},
		{
			name: "passthrough name collides with an operation",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    allow_prefixes: ["/api"]
    operations: [{name: request, path: /ping}]
    passthrough: {enabled: true, name: "demo_request"}
`,
			want: "declared by",
		},
		{
			name: "server auth cannot use the header scheme",
			document: `
server:
  auth: {scheme: header, token_env: MCPX_TOKEN, header_name: X-Token}
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: /ping}]
`,
			want: "must be none or bearer",
		},
		{
			name: "origin with a path",
			document: `
server:
  allowed_origins: ["https://app.example.com/path"]
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: /ping}]
`,
			want: "without a path",
		},
		{
			name: "origin with a bad scheme",
			document: `
server:
  allowed_origins: ["ftp://example.com"]
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: /ping}]
`,
			want: "must be http or https",
		},
		{
			name: "origin without a host",
			document: `
server:
  allowed_origins: ["https://"]
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: /ping}]
`,
			want: "must include a host",
		},
		{
			name: "empty protocol version",
			document: `
server:
  protocol_versions: ["2025-06-18", ""]
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: /ping}]
`,
			want: "empty entry",
		},
		{
			name: "path in server.path",
			document: `
server:
  path: "mcp?x=1"
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: /ping}]
`,
			want: "must be a plain path",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := mustDecode(t, testCase.document)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected an error containing %q", testCase.want)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want it to contain %q", err, testCase.want)
			}
		})
	}
}

// TestValidateDirectConfig covers the guards that only fire when a caller
// builds a Config in code and skips ApplyDefaults.
func TestValidateDirectConfig(t *testing.T) {
	base := func() *Config {
		return &Config{
			Server: Server{Listen: "127.0.0.1:1", Path: "/mcp", MaxRequestBytes: 1, SessionTTL: Duration(time.Second)},
			Defaults: Defaults{
				Timeout:          Duration(time.Second),
				MaxResponseBytes: 1,
				MaxBodyBytes:     1,
			},
			Upstreams: []Upstream{{
				Name:             "demo",
				BaseURL:          "https://example.com",
				Timeout:          Duration(time.Second),
				MaxResponseBytes: 1,
				Operations:       []Operation{{Name: "ping", Method: "GET", Path: "/ping"}},
			}},
		}
	}

	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "empty listen", mutate: func(c *Config) { c.Server.Listen = " " }},
		{name: "path without slash", mutate: func(c *Config) { c.Server.Path = "mcp" }},
		{name: "request limit", mutate: func(c *Config) { c.Server.MaxRequestBytes = 0 }},
		{name: "session ttl", mutate: func(c *Config) { c.Server.SessionTTL = 0 }},
		{name: "default timeout", mutate: func(c *Config) { c.Defaults.Timeout = 0 }},
		{name: "default response limit", mutate: func(c *Config) { c.Defaults.MaxResponseBytes = 0 }},
		{name: "default body limit", mutate: func(c *Config) { c.Defaults.MaxBodyBytes = 0 }},
		{name: "upstream timeout", mutate: func(c *Config) { c.Upstreams[0].Timeout = 0 }},
		{name: "upstream response limit", mutate: func(c *Config) { c.Upstreams[0].MaxResponseBytes = 0 }},
		{name: "bad path syntax", mutate: func(c *Config) { c.Upstreams[0].Operations[0].Path = "/a b" }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := base()
			testCase.mutate(cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}

	var nilConfig *Config
	if err := nilConfig.Validate(); err == nil {
		t.Fatal("a nil config must not validate")
	}
}

func TestAllowedOriginWildcard(t *testing.T) {
	cfg := mustDecode(t, sampleYAML)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the wildcard origin should be accepted: %v", err)
	}
}

func TestServerAuthNoneIsAccepted(t *testing.T) {
	cfg := mustDecode(t, `
server:
  auth: {scheme: none}
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: /ping}]
`)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
