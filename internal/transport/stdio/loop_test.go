package stdio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"mcpx/internal/mcp"
	"mcpx/internal/tool"
)

type stubTool struct{}

func (stubTool) Name() string                { return "echo" }
func (stubTool) Description() string         { return "echo" }
func (stubTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (stubTool) Invoke(context.Context, map[string]any) (tool.Result, error) {
	return tool.Result{Text: "OK"}, nil
}

type stubCaller struct{}

func (stubCaller) Call(context.Context, string, map[string]any) (tool.Result, error) {
	return tool.Result{Text: "OK"}, nil
}

func newServer(t *testing.T) *mcp.Server {
	t.Helper()
	registry, err := tool.NewRegistry(stubTool{})
	if err != nil {
		t.Fatal(err)
	}
	return &mcp.Server{
		Name:      "mcpx",
		Version:   "test",
		Protocols: []string{"2025-06-18"},
		Tools:     registry,
		Caller:    stubCaller{},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestRunRoundTrip(t *testing.T) {
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()

	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), inReader, outWriter, Options{
			Server: newServer(t),
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()

	scanner := bufio.NewScanner(outReader)
	writeLine(t, inWriter, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	first := scanLine(t, scanner)
	if first["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize response = %v", first)
	}

	writeLine(t, inWriter, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	second := scanLine(t, scanner)
	tools := second["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "echo" {
		t.Fatalf("tools/list = %v", second)
	}

	writeLine(t, inWriter, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo"}}`)
	third := scanLine(t, scanner)
	content := third["result"].(map[string]any)["content"].([]any)
	if content[0].(map[string]any)["text"] != "OK" {
		t.Fatalf("tools/call = %v", third)
	}

	if err := inWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := outReader.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, EOF should be a clean shutdown", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop at EOF")
	}
}

func TestBadJSONDoesNotStopTheLoop(t *testing.T) {
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	defer func() { _ = outReader.Close() }()

	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), inReader, outWriter, Options{
			Server: newServer(t),
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()

	scanner := bufio.NewScanner(outReader)
	writeLine(t, inWriter, `{"jsonrpc":`)
	parseError := scanLine(t, scanner)
	if parseError["error"] == nil {
		t.Fatalf("expected a parse error, got %v", parseError)
	}

	writeLine(t, inWriter, `{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	pong := scanLine(t, scanner)
	if pong["result"] == nil {
		t.Fatalf("the loop stopped after a bad message: %v", pong)
	}

	_ = inWriter.Close()
	_ = outReader.Close()
	<-done
}

func TestLongLineIsRejected(t *testing.T) {
	err := Run(context.Background(), strings.NewReader(strings.Repeat("x", 256)), io.Discard, Options{
		Server:       newServer(t),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		MaxLineBytes: 64,
	})
	if !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("err = %v, want ErrLineTooLong", err)
	}
}

func TestCancellationStopsWhileWaiting(t *testing.T) {
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, inReader, outWriter, Options{
			Server: newServer(t),
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v on cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run ignored cancellation while blocked on stdin")
	}
	_ = inWriter.Close()
	_ = outReader.Close()
	_ = outWriter.Close()
}

func TestTrailingLineWithoutNewlineIsProcessed(t *testing.T) {
	var output bytes.Buffer
	err := Run(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`), &output, Options{
		Server: newServer(t),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(output.String(), `"result"`) {
		t.Fatalf("output = %q", output.String())
	}
}

func TestBlankLinesAreSkipped(t *testing.T) {
	var output bytes.Buffer
	input := "\n\n" + `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n\n"
	if err := Run(context.Background(), strings.NewReader(input), &output, Options{
		Server: newServer(t),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if lines := strings.Count(strings.TrimSpace(output.String()), "\n"); lines != 0 {
		t.Fatalf("blank input produced extra responses: %q", output.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestWriteFailureStopsTheLoop(t *testing.T) {
	err := Run(context.Background(),
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"),
		failingWriter{},
		Options{Server: newServer(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadFailureStopsTheLoop(t *testing.T) {
	err := Run(context.Background(), failingReader{}, io.Discard, Options{
		Server: newServer(t),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestNilServerIsRejected(t *testing.T) {
	if err := Run(context.Background(), strings.NewReader(""), io.Discard, Options{}); err == nil {
		t.Fatal("expected an error for a nil server")
	}
}

func writeLine(t *testing.T, writer io.Writer, line string) {
	t.Helper()
	if _, err := io.WriteString(writer, line+"\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func scanLine(t *testing.T, scanner *bufio.Scanner) map[string]any {
	t.Helper()
	if !scanner.Scan() {
		t.Fatalf("no response line: %v", scanner.Err())
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(scanner.Bytes(), &decoded); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, scanner.Text())
	}
	return decoded
}
