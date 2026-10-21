package config

import (
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
	if got := cfg.Defaults.Timeout.Std(); got != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", got)
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
}

func TestDecodeJSON(t *testing.T) {
	document := `{
	  "server": {"listen": "127.0.0.1:1234", "session_ttl": "1m"},
	  "upstreams": [{
	    "name": "demo",
	    "base_url": "https://example.com",
	    "operations": [{
	      "name": "ping",
	      "path": "/ping"
	    }]
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
	_, err := Decode(strings.NewReader("server:\n  bogus: true\n"))
	if err == nil {
		t.Fatal("expected an error for an unknown field")
	}
}

func TestDecodeRejectsEmptyFile(t *testing.T) {
	if _, err := Decode(strings.NewReader("")); err == nil {
		t.Fatal("expected an error for an empty file")
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
			name: "no upstreams",
			document: `
server: {listen: "127.0.0.1:1"}
`,
			want: "no upstreams",
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
			name: "dot dot path",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: /a/../b}]
`,
			want: "..",
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
			name: "no tools on an upstream",
			document: `
upstreams:
  - name: demo
    base_url: https://example.com
`,
			want: "at least one operation",
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

func TestDurationRejectsBareNumber(t *testing.T) {
	if _, err := Decode(strings.NewReader("defaults:\n  timeout: 15\n")); err == nil {
		t.Fatal("expected a bare duration to be rejected")
	}
}
