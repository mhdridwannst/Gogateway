package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/mhdridwannst/Gogateway/internal/admin"
	"github.com/mhdridwannst/Gogateway/internal/config"
	"github.com/mhdridwannst/Gogateway/internal/health"
	"github.com/mhdridwannst/Gogateway/internal/middleware"
	"github.com/mhdridwannst/Gogateway/internal/proxy"
	"github.com/mhdridwannst/Gogateway/internal/telemetry"
)

func main() {
	configPath := flag.String("config", "configs/gateway.yaml", "path to YAML configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load configuration", slog.Any("error", err))
		os.Exit(1)
	}

	logger := telemetry.NewLogger(os.Stdout, cfg.Logging.Level, cfg.Logging.Format)
	slog.SetDefault(logger)

	metrics := telemetry.NewMetrics()

	gw, err := proxy.New(cfg)
	if err != nil {
		logger.Error("failed to initialize gateway proxy", slog.Any("error", err))
		os.Exit(1)
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	checker := health.NewChecker(gw.HealthTargetGroups())
	checker.Start(sigCtx)

	// Build HTTP handler chain (inside -> out):
	// Gateway -> RateLimiter -> IPFilter -> Observe (slog + metrics) -> RequestID
	var handler http.Handler = gw

	if cfg.RateLimit.Enabled {
		rl := middleware.NewRateLimiter(
			sigCtx,
			cfg.RateLimit.RequestsPerSec,
			cfg.RateLimit.Burst,
			cfg.RateLimit.CleanupInterval,
			cfg.RateLimit.ClientTTL,
		)
		handler = rl.Middleware(handler)
	}

	ipFilter, err := middleware.NewIPFilter(cfg.IPFilter.AllowList, cfg.IPFilter.DenyList)
	if err != nil {
		logger.Error("failed to initialize IP filter", slog.Any("error", err))
		os.Exit(1)
	}
	handler = ipFilter.Middleware(handler)
	handler = middleware.Observe(logger, metrics, handler)
	handler = middleware.RequestID(handler)

	srv := &http.Server{
		Addr:              cfg.Server.ListenAddr,
		Handler:           handler,
		ReadTimeout:       cfg.Server.ReadTimeout,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
		MaxHeaderBytes:    cfg.Server.MaxHeaderBytes,
	}

	var adminSrv *http.Server
	if cfg.Admin.Enabled {
		adminSrv = &http.Server{
			Addr:              cfg.Admin.ListenAddr,
			Handler:           admin.NewHandler(gw, metrics),
			ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
			ReadTimeout:       cfg.Server.ReadTimeout,
			WriteTimeout:      cfg.Server.WriteTimeout,
		}
		go func() {
			logger.Info("starting admin server", slog.String("addr", cfg.Admin.ListenAddr))
			if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("admin server error", slog.Any("error", err))
			}
		}()
	}

	serverErr := make(chan error, 1)

	go func() {
		logger.Info("starting gateway server", slog.String("addr", cfg.Server.ListenAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		logger.Error("server failed to start", slog.Any("error", err))
		os.Exit(1)
	case <-sigCtx.Done():
		logger.Info("shutdown signal received, draining connections",
			slog.Duration("timeout", cfg.Server.ShutdownTimeout))
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()

	if adminSrv != nil {
		_ = adminSrv.Shutdown(shutdownCtx)
	}

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", slog.Any("error", err))
		os.Exit(1)
	}

	logger.Info("gateway stopped gracefully")
}
