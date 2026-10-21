// Command mcpx is an MCP gateway: it publishes configured HTTP services as
// MCP tools over Streamable HTTP or stdio.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"mcpx/internal/config"
	"mcpx/internal/gateway"
	"mcpx/internal/mcp"
	"mcpx/internal/transport/httpsrv"
	stdioTransport "mcpx/internal/transport/stdio"
	"mcpx/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	switch command {
	case "serve":
		return runServe(args)
	case "stdio":
		return runStdio(args)
	case "check":
		return runCheck(args)
	case "version", "-version", "--version":
		fmt.Println(version.Full())
		return 0
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "mcpx: unknown command %q\n\n", command)
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mcpx - MCP gateway for arbitrary HTTP services

Usage:
  mcpx serve   [-c config.yaml] [-v]   serve Streamable HTTP (default command)
  mcpx stdio   [-c config.yaml] [-v]   serve newline-delimited JSON-RPC on stdin/stdout
  mcpx check   [-c config.yaml]        validate the config and list the tools
  mcpx version                         print the build stamp

The config path defaults to $MCPX_CONFIG or ./mcpx.yaml.
`)
}

// options is the flag set every subcommand shares.
type options struct {
	configPath string
	verbose    bool
}

func parseOptions(name string, args []string) (options, error) {
	opts := options{configPath: defaultConfigPath()}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&opts.configPath, "c", opts.configPath, "path to the configuration file")
	flags.StringVar(&opts.configPath, "config", opts.configPath, "path to the configuration file")
	flags.BoolVar(&opts.verbose, "v", false, "debug logging")
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	if flags.NArg() > 0 {
		return opts, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	return opts, nil
}

func defaultConfigPath() string {
	if path := strings.TrimSpace(os.Getenv("MCPX_CONFIG")); path != "" {
		return path
	}
	return "mcpx.yaml"
}

func newLogger(verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// load builds the gateway, which validates the config as a side effect.
func load(path string, logger *slog.Logger) (*config.Config, *gateway.Gateway, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	built, err := gateway.Build(cfg, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, built, nil
}

func newMCPServer(cfg *config.Config, gw *gateway.Gateway, logger *slog.Logger) *mcp.Server {
	return &mcp.Server{
		Name:         "mcpx",
		Version:      version.Short(),
		Protocols:    cfg.Server.ProtocolVersions,
		Instructions: cfg.Server.Instructions,
		Tools:        gw.Registry(),
		Caller:       gw,
		Logger:       logger,
	}
}

func runServe(args []string) int {
	opts, err := parseOptions("serve", args)
	if err != nil {
		return 2
	}
	logger := newLogger(opts.verbose)
	cfg, gw, err := load(opts.configPath, logger)
	if err != nil {
		logger.Error("config rejected", "config", opts.configPath, "error", err)
		return 1
	}

	token, err := serverToken(cfg)
	if err != nil {
		logger.Error("authentication misconfigured", "error", err)
		return 1
	}
	if token == "" {
		logger.Warn("authentication disabled: add server.auth with scheme bearer to require a token")
	}

	handler := httpsrv.New(httpsrv.Options{
		Path:            cfg.Server.Path,
		Server:          newMCPServer(cfg, gw, logger),
		BearerToken:     token,
		AllowedOrigins:  cfg.Server.AllowedOrigins,
		MaxRequestBytes: cfg.Server.MaxRequestBytes,
		SessionTTL:      cfg.Server.SessionTTL.Std(),
		Version:         version.Short(),
		Logger:          logger,
	})
	server := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("MCP endpoint listening",
			"addr", cfg.Server.Listen,
			"path", cfg.Server.Path,
			"tools", gw.Registry().Len(),
			"auth", token != "",
		)
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "error", err)
			return 1
		}
		return 0
	case <-ctx.Done():
		logger.Info("shutting down", "grace", cfg.Server.ShutdownGrace.String())
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownGrace.Std())
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
			_ = server.Close()
			return 1
		}
		return 0
	}
}

// serverToken resolves the MCP endpoint credential. A configured scheme with
// an empty environment variable is fatal: starting an open endpoint because
// of a typo is worse than not starting.
func serverToken(cfg *config.Config) (string, error) {
	auth := cfg.Server.Auth
	if auth == nil || auth.Scheme == "" || auth.Scheme == config.SchemeNone {
		return "", nil
	}
	if auth.Scheme != config.SchemeBearer {
		return "", fmt.Errorf("server.auth.scheme must be none or bearer, got %q", auth.Scheme)
	}
	token := strings.TrimSpace(os.Getenv(auth.TokenEnv))
	if token == "" {
		return "", fmt.Errorf("server.auth.token_env %s is empty", auth.TokenEnv)
	}
	return token, nil
}

func runStdio(args []string) int {
	opts, err := parseOptions("stdio", args)
	if err != nil {
		return 2
	}
	logger := newLogger(opts.verbose)
	cfg, gw, err := load(opts.configPath, logger)
	if err != nil {
		logger.Error("config rejected", "config", opts.configPath, "error", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := newMCPServer(cfg, gw, logger)
	if err := stdioTransport.Run(ctx, os.Stdin, os.Stdout, stdioTransport.Options{
		Server: server,
		Logger: logger,
	}); err != nil {
		logger.Error("stdio transport stopped", "error", err)
		return 1
	}
	return 0
}

func runCheck(args []string) int {
	opts, err := parseOptions("check", args)
	if err != nil {
		return 2
	}
	logger := newLogger(opts.verbose)
	cfg, gw, err := load(opts.configPath, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcpx: %v\n", err)
		return 1
	}
	fmt.Printf("mcpx: configuration OK (%s)\n", opts.configPath)
	fmt.Printf("listen: %s%s\n", cfg.Server.Listen, cfg.Server.Path)
	fmt.Printf("upstreams: %d, tools: %d\n", len(cfg.Upstreams), gw.Registry().Len())
	for _, t := range gw.Registry().All() {
		fmt.Printf("  - %-32s %s\n", t.Name(), t.Description())
	}
	return 0
}
