package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcpx/internal/config"
	"mcpx/internal/tool"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func testConfig(baseURL string) *config.Config {
	cfg := &config.Config{
		Upstreams: []config.Upstream{
			{
				Name:    "demo",
				BaseURL: baseURL,
				Operations: []config.Operation{
					{Name: "ping", Method: "GET", Path: "/ping"},
					{Name: "get_pet", Method: "GET", Path: "/pets/{id}", Params: []config.Param{
						{Name: "id", In: config.ParamInPath, Required: true},
					}},
				},
				Passthrough: &config.Passthrough{
					Enabled:       true,
					Methods:       []string{"GET"},
					AllowPrefixes: []string{"/pets"},
				},
			},
		},
	}
	cfg.ApplyDefaults()
	return cfg
}

func TestBuildRegistersEveryTool(t *testing.T) {
	cfg := testConfig("https://example.com")
	built, err := Build(cfg, discardLogger())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built.Registry().Len() != 3 {
		t.Fatalf("tools = %d, want 3", built.Registry().Len())
	}
	for _, name := range []string{"demo_ping", "demo_get_pet", "demo_request"} {
		if _, ok := built.Registry().Get(name); !ok {
			t.Errorf("tool %s is missing", name)
		}
	}
}

func TestBuildRejectsInvalidConfig(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.Upstream{{Name: "demo", BaseURL: "not-a-url", Operations: []config.Operation{{Name: "ping", Path: "/ping"}}}},
	}
	if _, err := Build(cfg, discardLogger()); err == nil {
		t.Fatal("expected Build to reject an invalid base_url")
	}
}

func TestCallDispatchesToUpstream(t *testing.T) {
	var path string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"42","name":"rex"}`))
	}))
	defer upstream.Close()

	built, err := Build(testConfig(upstream.URL), discardLogger())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	result, err := built.Call(context.Background(), "demo_get_pet", map[string]any{"id": "42"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected isError: %s", result.Text)
	}
	if path != "/pets/42" {
		t.Errorf("upstream path = %q", path)
	}
}

func TestCallUnknownTool(t *testing.T) {
	built, err := Build(testConfig("https://example.com"), discardLogger())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := built.Call(context.Background(), "nope", nil); err == nil {
		t.Fatal("expected an error for an unknown tool")
	}
}

func TestCallMapsInvalidArgumentsToIsError(t *testing.T) {
	built, err := Build(testConfig("https://example.com"), discardLogger())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	result, err := built.Call(context.Background(), "demo_get_pet", map[string]any{})
	if err != nil {
		t.Fatalf("Call returned a protocol error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("result = %+v, want isError", result)
	}
}

type failingTool struct{ err error }

func (f failingTool) Name() string                { return "fail" }
func (f failingTool) Description() string         { return "always fails" }
func (f failingTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (f failingTool) Invoke(context.Context, map[string]any) (tool.Result, error) {
	return tool.Result{}, f.err
}

func TestCallMapsUnexpectedError(t *testing.T) {
	registry, err := tool.NewRegistry(failingTool{err: errors.New("exploded")})
	if err != nil {
		t.Fatal(err)
	}
	built := &Gateway{registry: registry, logger: discardLogger()}
	result, err := built.Call(context.Background(), "fail", nil)
	if err != nil {
		t.Fatalf("Call returned a protocol error: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Text, "exploded") {
		t.Fatalf("result = %+v", result)
	}
}

func TestBuildUsesTheDefaultLogger(t *testing.T) {
	previous := slog.Default()
	slog.SetDefault(discardLogger())
	t.Cleanup(func() { slog.SetDefault(previous) })

	if _, err := Build(testConfig("https://example.com"), nil); err != nil {
		t.Fatalf("Build with a nil logger: %v", err)
	}
}

func TestCallPassthroughPrefix(t *testing.T) {
	var reached []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.URL.Path)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	built, err := Build(testConfig(upstream.URL), discardLogger())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := built.Call(context.Background(), "demo_request", map[string]any{"path": "/pets/1"}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	denied, err := built.Call(context.Background(), "demo_request", map[string]any{"path": "/admin"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !denied.IsError {
		t.Fatal("an out-of-prefix path must be an isError result")
	}
	if len(reached) != 1 {
		t.Fatalf("upstream saw %v, the denied call must not be forwarded", reached)
	}
}
