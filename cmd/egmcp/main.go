// Package main is the entrypoint of the egmcp platform.
//
// egmcp is a management plane for the Model Context Protocol (MCP).
// It exposes a single HTTP server that:
//   - serves the admin console (REST API + bundled static frontend),
//   - hosts per-instance MCP endpoints under /mcp/{slug},
//   - dispatches tool/resource/prompt calls to the configured connectors.
//
// This file is intentionally small: it wires up config, logging, the core
// router and the HTTP server. The bulk of the logic lives in the internal/
// packages.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/processcrash/egmcp/internal/config"
	"github.com/processcrash/egmcp/internal/connectors/builtin/filesystem"
	"github.com/processcrash/egmcp/internal/connectors/builtin/mysql"
	"github.com/processcrash/egmcp/internal/connectors/builtin/oss"
	"github.com/processcrash/egmcp/internal/connectors/builtin/postgres"
	"github.com/processcrash/egmcp/internal/connectors/builtin/s3"
	"github.com/processcrash/egmcp/internal/connectors/builtin/swagger"
	"github.com/processcrash/egmcp/internal/core"
	"github.com/processcrash/egmcp/internal/log"
	egmcpplugin "github.com/processcrash/egmcp/internal/plugin"
	"github.com/processcrash/egmcp/internal/server"
	"github.com/processcrash/egmcp/pkg/connector"
)

// version is set at build time via -ldflags.
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "egmcp: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. Locate the config file. Default is ./configs/admin.yaml relative
	//    to either the working directory or the executable's directory.
	cfgPath, err := resolveConfigPath()
	if err != nil {
		return fmt.Errorf("resolve config: %w", err)
	}

	// 2. Load + validate config. First-run bootstraps the file with sane
	//    defaults and a random admin password printed to stdout.
	cfg, firstBoot, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// 3. Initialise structured logging. Output goes to stdout in JSON so
	//    container log collectors can pick it up uniformly.
	logger, err := log.New(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer func() { _ = logger.Sync() }()

	if firstBoot {
		logger.Info("first boot — generated default config",
			log.String("config_path", cfgPath),
			log.String("admin_password", cfg.FirstBootPassword), // printed only on first boot
		)
	}

	logger.Info("egmcp starting",
		log.String("version", version),
		log.String("listen", cfg.Server.Listen),
		log.String("config_path", cfgPath),
	)

	// 4. Build the core router. In M0 the router is a stub that knows
	//    about /healthz; richer wiring lands in later milestones.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg := connector.NewRegistry()
	// Built-in connectors register here. Each one ships in
	// internal/connectors/builtin/<name>.
	reg.MustRegister("filesystem", func() connector.Connector {
		return filesystem.New()
	})
	reg.MustRegister("mysql", func() connector.Connector {
		return mysql.New()
	})
	reg.MustRegister("postgres", func() connector.Connector {
		return postgres.New()
	})
	reg.MustRegister("s3", func() connector.Connector {
		return s3.New()
	})
	reg.MustRegister("oss", func() connector.Connector {
		return oss.New()
	})
	reg.MustRegister("swagger", func() connector.Connector {
		return swagger.New()
	})

	// Plugin system: load .so/.dll from data/plugins/ and register
	// their Connectors under their declared names.
	pluginMgr, err := egmcpplugin.NewManager(cfg.PluginsDir)
	if err != nil {
		logger.Warn("plugin manager init failed", log.Err(err))
	} else {
		if err := pluginMgr.LoadAll(); err != nil {
			logger.Warn("some plugins failed to load", log.Err(err))
		}
		for name, c := range pluginMgr.Connectors() {
			reg.MustRegister(name, func() connector.Connector { return c })
			logger.Info("plugin connector registered",
				log.String("name", name),
				log.String("version", c.Manifest().Version),
			)
		}
		defer func() { _ = os.RemoveAll(cfg.PluginsDir + "/.lock") }()
	}

	router, err := core.New(ctx, cfg, logger, reg)
	if err != nil {
		return fmt.Errorf("init core: %w", err)
	}
	defer func() { _ = router.Close() }()

	// 5. Build the HTTP server. The handler composition is intentionally
	//    explicit so middleware ordering stays reviewable.
	mux := server.NewMux(router, cfg, logger)

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // streaming endpoints (SSE) need no write timeout
		IdleTimeout:       120 * time.Second,
		ErrorLog:          log.NewStdLogger(logger),
	}

	// 6. Run the server with graceful shutdown on SIGINT/SIGTERM.
	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", log.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-stop:
		logger.Info("shutdown signal received", log.String("signal", sig.String()))
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("egmcp stopped cleanly")
	return nil
}

// resolveConfigPath picks the config file path in priority order:
//  1. --config / -c flag
//  2. EG_MCP_CONFIG env var
//  3. ./configs/admin.yaml relative to CWD
//  4. ./configs/admin.yaml relative to the executable's directory
func resolveConfigPath() (string, error) {
	flagPath := ""
	for i, a := range os.Args {
		if a == "--config" || a == "-c" {
			if i+1 < len(os.Args) {
				flagPath = os.Args[i+1]
			}
			break
		}
		if v, ok := stringsCutPrefix(a, "--config="); ok {
			flagPath = v
			break
		}
	}
	if flagPath == "" {
		flagPath = os.Getenv("EGMCP_CONFIG")
	}
	if flagPath != "" {
		return filepath.Abs(flagPath)
	}

	if wd, err := os.Getwd(); err == nil {
		p := filepath.Join(wd, "configs", "admin.yaml")
		if _, statErr := os.Stat(p); statErr == nil {
			return p, nil
		}
	}

	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "configs", "admin.yaml")
		if _, statErr := os.Stat(p); statErr == nil {
			return p, nil
		}
	}

	// Fall back to the CWD-relative default; Load() will create it on first run.
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, "configs", "admin.yaml"), nil
	}
	return "", errors.New("no config path could be resolved")
}

// stringsCutPrefix is a tiny replacement for strings.CutPrefix for older Go
// toolchains; behaviour mirrors strings.CutPrefix exactly.
func stringsCutPrefix(s, prefix string) (string, bool) {
	if len(s) < len(prefix) {
		return "", false
	}
	if s[:len(prefix)] != prefix {
		return "", false
	}
	return s[len(prefix):], true
}
