// Package gateway ties the declarative config to the tool registry: it is the
// single place that knows both "what the file says" and "what can be called".
package gateway

import (
	"log/slog"

	"mcpx/internal/config"
	"mcpx/internal/tool"
	"mcpx/internal/upstream"
)

// Gateway owns the immutable tool set built from one config.
type Gateway struct {
	registry *tool.Registry
	logger   *slog.Logger
}

// Build compiles every upstream into tools. Validation runs first so a config
// problem is reported as itself instead of as a confusing tool error.
func Build(cfg *config.Config, logger *slog.Logger) (*Gateway, error) {
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}

	tools := make([]tool.Tool, 0, len(cfg.Upstreams))
	for i := range cfg.Upstreams {
		u := &cfg.Upstreams[i]
		client, err := upstream.NewClient(upstream.ClientOptions{
			Name:             u.Name,
			BaseURL:          u.BaseURL,
			Headers:          u.Headers,
			Auth:             u.Auth,
			Timeout:          u.Timeout.Std(),
			MaxResponseBytes: u.MaxResponseBytes,
			MaxBodyBytes:     cfg.Defaults.MaxBodyBytes,
		})
		if err != nil {
			return nil, err
		}
		for _, op := range u.Operations {
			operation, err := upstream.NewOperation(client, op)
			if err != nil {
				return nil, err
			}
			tools = append(tools, operation)
		}
		if u.Passthrough != nil && u.Passthrough.Enabled {
			passthrough, err := upstream.NewPassthrough(client, *u.Passthrough)
			if err != nil {
				return nil, err
			}
			tools = append(tools, passthrough)
		}
	}

	registry, err := tool.NewRegistry(tools...)
	if err != nil {
		return nil, err
	}
	logger.Info("tools compiled", "count", registry.Len(), "upstreams", len(cfg.Upstreams))
	return &Gateway{registry: registry, logger: logger}, nil
}

// Registry is the tool set the MCP layer publishes.
func (g *Gateway) Registry() *tool.Registry { return g.registry }
