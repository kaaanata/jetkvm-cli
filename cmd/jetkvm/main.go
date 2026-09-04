package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	charmlog "charm.land/log/v2"
	"github.com/kaaanata/jetkvm-cli/internal/app"
	"github.com/kaaanata/jetkvm-cli/internal/buildinfo"
	"github.com/kaaanata/jetkvm-cli/internal/cli"
	"github.com/kaaanata/jetkvm-cli/internal/mcpserver"
)

const mcpBearerTokenEnvironment = "JETKVM_MCP_BEARER_TOKEN"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := charmlog.NewWithOptions(os.Stderr, charmlog.Options{
		ReportTimestamp: true,
		TimeFormat:      time.RFC3339,
	})
	build := buildinfo.Current()
	application := cli.New(cli.Dependencies{
		Version:    build,
		ConfigPath: defaultConfigPath(),
		Logger:     logger,
		Setup:      newLazySetupService(),
		Updater:    newLazyUpdateService(build),
		Loader: cli.RuntimeLoaderFunc(func(ctx context.Context, path string) (cli.Runtime, error) {
			runtime, err := app.Load(ctx, path, build.Version)
			if err != nil {
				return cli.Runtime{}, err
			}
			return cli.Runtime{
				Devices:       runtime.Devices,
				Automation:    runtime.Automation,
				Releaser:      runtime.Automation,
				Confirmations: cliConfirmationIssuer{authority: runtime.Confirmation, input: os.Stdin, output: os.Stderr},
				MCP:           &mcpRunner{server: runtime.MCP, logger: logger},
				OutputMode:    string(runtime.Config.Output.Default),
				Close:         runtime.Close,
			}, nil
		}),
	})
	return application.Execute(ctx, args)
}

func defaultConfigPath() string {
	directory, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(directory, "jetkvm", "config.json")
}

type mcpRunner struct {
	server *mcpserver.Server
	logger *charmlog.Logger
}

func (r *mcpRunner) Serve(ctx context.Context, options cli.MCPServeOptions) error {
	switch options.Transport {
	case "stdio":
		err := r.server.RunStdio(ctx)
		if errors.Is(err, io.EOF) || strings.HasSuffix(errString(err), "server is closing: EOF") || (ctx.Err() != nil && errors.Is(err, context.Canceled)) {
			return nil
		}
		return err
	case "http":
		return r.serveHTTP(ctx, options.Listen)
	default:
		return errors.New("unsupported MCP transport")
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (r *mcpRunner) serveHTTP(ctx context.Context, address string) error {
	token := os.Getenv(mcpBearerTokenEnvironment)
	server, err := r.server.NewStatelessHTTPServer(mcpserver.HTTPConfig{
		ListenAddress: address,
		BearerToken:   token,
	})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	r.logger.Info("MCP HTTP server listening", "address", listener.Addr().String())

	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()

	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeoutCause(context.Background(), 5*time.Second, errors.New("MCP HTTP shutdown timed out"))
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		serveErr := <-serveResult
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr)
	}
}
