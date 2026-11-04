package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mcpx/internal/config"
	stdioTransport "mcpx/internal/transport/stdio"
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

// plainConfig has no auth block, so the serve tests do not depend on the
// environment.
const plainConfig = `
server:
  listen: 127.0.0.1:0
upstreams:
  - name: demo
    base_url: https://example.com
    operations: [{name: ping, path: /ping}]
`

func TestMainUsesTheExitSeam(t *testing.T) {
	previousArgs, previousExit := os.Args, exitProcess
	t.Cleanup(func() { os.Args, exitProcess = previousArgs, previousExit })

	code := -1
	exitProcess = func(got int) { code = got }
	os.Args = []string{"mcpx", "version"}
	main()
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

func TestFlagErrorsReturnUsage(t *testing.T) {
	for _, command := range []string{"serve", "stdio", "check"} {
		if code := run([]string{command, "-no-such-flag"}); code != 2 {
			t.Errorf("%s with a bad flag = %d, want 2", command, code)
		}
	}
}

func TestRunServeReportsConfigurationFailures(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	if code := run([]string{"serve", "-c", missing}); code != 1 {
		t.Fatalf("serve on a missing config = %d, want 1", code)
	}
}

func TestRunServeListenerOutcome(t *testing.T) {
	path := writeConfig(t, plainConfig)
	cases := []struct {
		name     string
		listen   error
		wantCode int
	}{
		{name: "clean shutdown", listen: nil, wantCode: 0},
		{name: "already closed", listen: http.ErrServerClosed, wantCode: 0},
		{name: "bind failure", listen: errors.New("address already in use"), wantCode: 1},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			previous := listenAndServe
			t.Cleanup(func() { listenAndServe = previous })
			listenAndServe = func(*http.Server) error { return testCase.listen }

			if code := run([]string{"serve", "-c", path}); code != testCase.wantCode {
				t.Fatalf("code = %d, want %d", code, testCase.wantCode)
			}
		})
	}
}

func TestRunServeGracefulShutdown(t *testing.T) {
	path := writeConfig(t, plainConfig)
	cases := []struct {
		name     string
		shutdown error
		wantCode int
	}{
		{name: "shutdown succeeds", shutdown: nil, wantCode: 0},
		{name: "shutdown fails", shutdown: errors.New("shutdown stalled"), wantCode: 1},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			previousListen, previousNotify, previousShutdown := listenAndServe, notifyContext, shutdownServer
			t.Cleanup(func() {
				listenAndServe, notifyContext, shutdownServer = previousListen, previousNotify, previousShutdown
			})

			release := make(chan struct{})
			finished := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)

			listenAndServe = func(*http.Server) error {
				<-release
				close(finished)
				return nil
			}
			notifyContext = func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			}
			shutdownServer = func(*http.Server, context.Context) error {
				unblock()
				return testCase.shutdown
			}

			if code := run([]string{"serve", "-c", path}); code != testCase.wantCode {
				t.Fatalf("code = %d, want %d", code, testCase.wantCode)
			}
			// The listener goroutine reads the seam variables, so it must be
			// finished before cleanup restores them.
			unblock()
			<-finished
		})
	}
}

func TestRunServeUsesTheRealShutdown(t *testing.T) {
	path := writeConfig(t, plainConfig)
	previousListen, previousNotify := listenAndServe, notifyContext
	t.Cleanup(func() { listenAndServe, notifyContext = previousListen, previousNotify })

	release := make(chan struct{})
	finished := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	listenAndServe = func(*http.Server) error {
		<-release
		close(finished)
		return nil
	}
	notifyContext = func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx, func() {}
	}

	if code := run([]string{"serve", "-c", path}); code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	unblock()
	<-finished
}

func TestRunStdioFailures(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	if code := run([]string{"stdio", "-c", missing}); code != 1 {
		t.Fatalf("stdio on a missing config = %d, want 1", code)
	}

	path := writeConfig(t, plainConfig)
	previous := stdioRun
	t.Cleanup(func() { stdioRun = previous })
	stdioRun = func(context.Context, io.Reader, io.Writer, stdioTransport.Options) error {
		return errors.New("stdin broke")
	}
	if code := run([]string{"stdio", "-c", path}); code != 1 {
		t.Fatalf("stdio with a failing transport = %d, want 1", code)
	}
}
