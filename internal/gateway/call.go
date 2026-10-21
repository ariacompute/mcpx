package gateway

import (
	"context"
	"errors"
	"fmt"

	"mcpx/internal/tool"
)

// Call dispatches one tools/call. Every failure becomes an isError result:
// a model that cannot reach an upstream should read why and try something
// else, not receive a transport-level exception.
func (g *Gateway) Call(ctx context.Context, name string, args map[string]any) (tool.Result, error) {
	registered, ok := g.registry.Get(name)
	if !ok {
		return tool.Result{}, fmt.Errorf("gateway: unknown tool %q", name)
	}
	result, err := registered.Invoke(ctx, args)
	if err == nil {
		return result, nil
	}
	if errors.Is(err, tool.ErrInvalidArguments) {
		return tool.Result{Text: err.Error(), IsError: true}, nil
	}
	g.logger.Error("tool call failed", "tool", registered.Name(), "error", err)
	return tool.Result{
		Text:    fmt.Sprintf("tool %s failed: %v", registered.Name(), err),
		IsError: true,
	}, nil
}
