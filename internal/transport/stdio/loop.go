// Package stdio serves the MCP stdio transport: newline-delimited JSON-RPC on
// stdin and stdout. stdout belongs to the protocol, so every log line in this
// package goes to stderr through the configured logger.
package stdio

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"

	"mcpx/internal/mcp"
)

// ErrLineTooLong protects the process from a client that never sends a
// newline.
var ErrLineTooLong = errors.New("stdio: message exceeds the line limit")

// DefaultMaxLineBytes is generous for a tool call and far below a runaway
// stream.
const DefaultMaxLineBytes = 8 << 20

// Options configures the loop.
type Options struct {
	Server       *mcp.Server
	Logger       *slog.Logger
	MaxLineBytes int
}

// Run reads messages until EOF. EOF is a normal shutdown, not an error.
func Run(ctx context.Context, in io.Reader, out io.Writer, opts Options) error {
	if opts.Server == nil {
		return errors.New("stdio: nil MCP server")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	maxLine := opts.MaxLineBytes
	if maxLine <= 0 {
		maxLine = DefaultMaxLineBytes
	}
	logger.Debug("stdio transport started", "max_line_bytes", maxLine)

	// The loop runs in its own goroutine so a signal can stop the process even
	// while it is blocked waiting for the next stdin line.
	result := make(chan error, 1)
	go func() { result <- loop(ctx, in, out, opts.Server, maxLine) }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return nil
	}
}

func loop(ctx context.Context, in io.Reader, out io.Writer, server *mcp.Server, maxLine int) error {
	reader := bufio.NewReaderSize(in, 64<<10)
	writer := bufio.NewWriter(out)
	defer func() { _ = writer.Flush() }()

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		line, err := readLine(reader, maxLine)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		response := server.Handle(ctx, line)
		if response == nil {
			continue
		}
		if _, err := writer.Write(response); err != nil {
			return err
		}
		if err := writer.WriteByte('\n'); err != nil {
			return err
		}
		if err := writer.Flush(); err != nil {
			return err
		}
	}
}

// readLine assembles one newline-terminated message while enforcing the limit
// before the buffer can grow without bound.
func readLine(reader *bufio.Reader, max int) ([]byte, error) {
	buffer := make([]byte, 0, 4096)
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(buffer)+len(chunk) > max {
			return nil, ErrLineTooLong
		}
		buffer = append(buffer, chunk...)
		switch {
		case err == nil:
			return buffer, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(buffer) > 0 {
				return buffer, nil
			}
			return nil, io.EOF
		default:
			return nil, err
		}
	}
}
