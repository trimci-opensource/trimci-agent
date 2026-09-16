// trimci-agent — the TrimCI push agent for CI instances behind VPNs and
// private networks. See README.md for the 60-second quickstart and
// docs/PROTOCOL.md for the wire protocol.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/trimci-opensource/trimci-agent/internal/agentloop"
	"github.com/trimci-opensource/trimci-agent/internal/api"
	"github.com/trimci-opensource/trimci-agent/internal/backoff"
	"github.com/trimci-opensource/trimci-agent/internal/collector/gitlabci"
	"github.com/trimci-opensource/trimci-agent/internal/config"
	"github.com/trimci-opensource/trimci-agent/internal/healthz"
	"github.com/trimci-opensource/trimci-agent/internal/logredact"
	"github.com/trimci-opensource/trimci-agent/internal/version"
)

func main() {
	once := flag.Bool("once", false, "run a single sync cycle and exit (for cron)")
	showVersion := flag.Bool("version", false, "print the agent version and exit")
	healthcheck := flag.Bool("healthcheck", false,
		"probe the local /healthz endpoint (HEALTHZ_ADDR) and exit 0/1 — for container health checks")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.UserAgent())
		return
	}
	if *healthcheck {
		os.Exit(runHealthcheck())
	}
	os.Exit(run(*once))
}

func run(once bool) int {
	cfg, err := config.Load()
	if err != nil {
		// Config errors precede logger construction; they never contain
		// secret VALUES (only variable names), so plain stderr is safe.
		fmt.Fprintln(os.Stderr, "trimci-agent:", err)
		return 2
	}

	logger := buildLogger(cfg)

	gitlab, err := gitlabci.New(cfg.GitLabURL, cfg.GitLabToken, cfg.GitLabCABundle, version.UserAgent(), backoff.Sleep)
	if err != nil {
		logger.Error("collector setup failed", "error", err)
		return 2
	}

	var health *healthz.State
	if cfg.HealthzAddr != "" {
		health = healthz.NewState()
		closeHealthz, err := healthz.Serve(cfg.HealthzAddr, health)
		if err != nil {
			logger.Error("healthz listener failed", "addr", cfg.HealthzAddr, "error", err)
			return 2
		}
		defer func() { _ = closeHealthz() }()
		logger.Info("healthz serving", "addr", cfg.HealthzAddr)
	}

	agent := &agentloop.Agent{
		API:          api.NewClient(cfg.TrimCIURL, cfg.TrimCIToken, version.UserAgent()),
		Collector:    gitlab,
		Log:          logger,
		Sleep:        backoff.Sleep,
		Health:       health,
		DashboardURL: cfg.TrimCIURL + "/dashboard/connectors/",
		Once:         once,
	}

	// Graceful shutdown: SIGINT/SIGTERM cancel the context; a batch killed
	// mid-request is safe — the server's upserts are idempotent and the
	// cursor only ever covers what it accepted.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := agent.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("agent stopped", "error", err)
		return 1
	}
	logger.Info("agent stopped")
	return 0
}

func buildLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	// Redaction wraps the writer itself, underneath the handler: every log
	// line is scrubbed of both configured secrets and anything shaped like
	// a credential, wherever it came from.
	sink := logredact.Writer(os.Stderr, cfg.TrimCIToken, cfg.GitLabToken)
	options := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.LogFormat == "json" {
		handler = slog.NewJSONHandler(sink, options)
	} else {
		handler = slog.NewTextHandler(sink, options)
	}
	return slog.New(handler)
}

func runHealthcheck() int {
	addr := os.Getenv("HEALTHZ_ADDR")
	if addr == "" {
		fmt.Fprintln(os.Stderr, "trimci-agent: -healthcheck needs HEALTHZ_ADDR set")
		return 2
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "trimci-agent: healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
