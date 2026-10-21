package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpx/internal/config"
)

const serveConfig = `
server:
  listen: 127.0.0.1:0
  auth:
    scheme: bearer
    token_env: MCPX_TEST_SERVE_TOKEN
upstreams:
  - name: demo
    base_url: https://example.com
    operations:
      - name: ping
        path: /ping
`

func writeConfig(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcpx.yaml")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunCommandDispatch(t *testing.T) {
	cases := []struct {
		args []string
		want int
	}{
		{args: []string{"version"}, want: 0},
		{args: []string{"help"}, want: 0},
		{args: []string{"nope"}, want: 2},
		{args: []string{"serve", "extra-positional"}, want: 2},
		{args: []string{"-c", "x.yaml", "check"}, want: 2},
	}
	for _, testCase := range cases {
		if got := run(testCase.args); got != testCase.want {
			t.Errorf("run(%v) = %d, want %d", testCase.args, got, testCase.want)
		}
	}
}

func TestRunCheck(t *testing.T) {
	good := writeConfig(t, serveConfig)
	if code := run([]string{"check", "-c", good}); code != 0 {
		t.Fatalf("check on a valid config = %d", code)
	}
	if code := run([]string{"check", "-c", filepath.Join(t.TempDir(), "missing.yaml")}); code != 1 {
		t.Fatalf("check on a missing file = %d", code)
	}
	bad := writeConfig(t, `
upstreams:
  - name: demo
    base_url: not-a-url
    operations: [{name: ping, path: /ping}]
`)
	if code := run([]string{"check", "-c", bad}); code != 1 {
		t.Fatalf("check on an invalid config = %d", code)
	}
}

func TestRunServeFailsWithoutCredential(t *testing.T) {
	path := writeConfig(t, serveConfig)
	t.Setenv("MCPX_TEST_SERVE_TOKEN", "")
	if code := run([]string{"serve", "-c", path}); code != 1 {
		t.Fatalf("serve without a token = %d, want 1", code)
	}
}

func TestRunServeFailsOnBadListenAddress(t *testing.T) {
	path := writeConfig(t, `
server:
  listen: not-an-address
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: /ping}]
`)
	if code := run([]string{"serve", "-c", path}); code != 1 {
		t.Fatalf("serve with an unusable address = %d, want 1", code)
	}
}

func TestRunStdioConsumesStdin(t *testing.T) {
	path := writeConfig(t, `
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: /ping}]
`)

	stdin, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	if _, err := stdin.WriteString(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdout.Close() }()

	originalIn, originalOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = stdin, stdout
	defer func() { os.Stdin, os.Stdout = originalIn, originalOut }()

	if code := run([]string{"stdio", "-c", path}); code != 0 {
		t.Fatalf("stdio = %d, want 0 on EOF", code)
	}
	written, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), `"result"`) {
		t.Fatalf("stdout = %q", written)
	}
}

func TestServerToken(t *testing.T) {
	if token, err := serverToken(&config.Config{}); token != "" || err != nil {
		t.Fatalf("no auth: %q %v", token, err)
	}
	if token, err := serverToken(&config.Config{Server: config.Server{Auth: &config.Auth{Scheme: config.SchemeNone}}}); token != "" || err != nil {
		t.Fatalf("scheme none: %q %v", token, err)
	}

	bearer := &config.Config{Server: config.Server{Auth: &config.Auth{Scheme: config.SchemeBearer, TokenEnv: "MCPX_TEST_TOKEN_VALUE"}}}
	t.Setenv("MCPX_TEST_TOKEN_VALUE", "")
	if _, err := serverToken(bearer); err == nil {
		t.Fatal("an empty token variable must fail")
	}
	t.Setenv("MCPX_TEST_TOKEN_VALUE", " token-value ")
	if token, err := serverToken(bearer); err != nil || token != "token-value" {
		t.Fatalf("token = %q %v", token, err)
	}

	header := &config.Config{Server: config.Server{Auth: &config.Auth{Scheme: config.SchemeHeader, TokenEnv: "X", HeaderName: "X-Token"}}}
	if _, err := serverToken(header); err == nil {
		t.Fatal("the header scheme is not valid for the MCP endpoint")
	}
}

func TestDefaultConfigPath(t *testing.T) {
	t.Setenv("MCPX_CONFIG", "/tmp/custom.yaml")
	if got := defaultConfigPath(); got != "/tmp/custom.yaml" {
		t.Errorf("path = %q", got)
	}
	t.Setenv("MCPX_CONFIG", "  ")
	if got := defaultConfigPath(); got != "mcpx.yaml" {
		t.Errorf("fallback = %q", got)
	}
}

func TestNewLogger(t *testing.T) {
	if newLogger(true) == nil || newLogger(false) == nil {
		t.Fatal("newLogger returned nil")
	}
}
