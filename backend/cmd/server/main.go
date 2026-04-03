package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"

	"github.com/workflow-platform/backend/internal/api"
	"github.com/workflow-platform/backend/internal/metrics"
	"github.com/workflow-platform/backend/internal/orchestrator"
	"github.com/workflow-platform/backend/internal/persistence"
	"github.com/workflow-platform/backend/internal/scheduler"
	"github.com/workflow-platform/backend/internal/worker"
)

var (
	version   = "dev"
	buildTime = "unknown"
)

func main() {
	// ── Logging ──────────────────────────────────────────────────
	logLevel := zerolog.InfoLevel
	if getEnv("LOG_LEVEL", "info") == "debug" {
		logLevel = zerolog.DebugLevel
	}
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
	zerolog.SetGlobalLevel(logLevel)

	log.Info().
		Str("version", version).
		Str("built", buildTime).
		Msg("starting Fluxor Workflow Orchestration Platform")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── Configuration ────────────────────────────────────────────
	postgresURL   := getEnv("POSTGRES_URL",   "postgres://workflow:workflow@localhost:5432/workflow?sslmode=disable")
	redisAddr     := getEnv("REDIS_ADDR",     "localhost:6379")
	redisPassword := getEnv("REDIS_PASSWORD", "")
	httpAddr      := getEnv("HTTP_ADDR",      ":8080")
	grpcAddr      := getEnv("GRPC_ADDR",      ":9090")
	metricsAddr   := getEnv("METRICS_ADDR",   ":9091")
	workerCount   := getEnvInt("WORKER_COUNT",       3)
	workerConc    := getEnvInt("WORKER_CONCURRENCY", 5)

	// ── Prometheus ───────────────────────────────────────────────
	reg := prometheus.NewRegistry()
	prom := metrics.NewMetrics(reg)
	log.Info().Str("addr", metricsAddr).Msg("Prometheus metrics endpoint")

	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", metrics.Handler())
		srv := &http.Server{Addr: metricsAddr, Handler: mux, ReadTimeout: 5 * time.Second}
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("metrics server failed")
		}
	}()
	_ = prom // used via prometheus.MustRegister in metrics package

	// ── Redis ────────────────────────────────────────────────────
	log.Info().Str("addr", redisAddr).Msg("connecting to Redis")
	redisClient := persistence.NewRedisClient(redisAddr, redisPassword, 0)
	if err := redisClient.Ping(ctx); err != nil {
		log.Fatal().Err(err).Msg("Redis connection failed")
	}
	log.Info().Msg("Redis connected")
	defer redisClient.Close()

	// ── PostgreSQL ───────────────────────────────────────────────
	log.Info().Msg("connecting to PostgreSQL")
	store, err := persistence.NewStore(ctx, postgresURL)
	if err != nil {
		log.Fatal().Err(err).Msg("PostgreSQL connection failed")
	}
	log.Info().Msg("PostgreSQL connected")
	defer store.Close()

	// ── Core services ────────────────────────────────────────────
	hub := api.NewHub()
	go hub.Run()

	orch := orchestrator.NewOrchestrator(store, redisClient, hub)

	// ── Crash recovery: reload in-flight executions ───────────────
	log.Info().Msg("running crash recovery...")
	if err := orch.RecoverInFlightExecutions(ctx); err != nil {
		log.Warn().Err(err).Msg("crash recovery encountered errors (non-fatal)")
	}

	// ── Background services ───────────────────────────────────────
	resultProcessor := scheduler.NewResultProcessor(redisClient, orch)
	retryPoller     := scheduler.NewRetryPoller(redisClient, orch)
	workerPool      := worker.NewPool(redisClient, workerCount, workerConc)
	watchdog        := worker.NewTaskWatchdog(redisClient)

	go resultProcessor.Run(ctx)
	go retryPoller.Run(ctx)
	go workerPool.Start(ctx)
	go watchdog.Run(ctx)

	// ── HTTP server ───────────────────────────────────────────────
	handler := api.NewHandler(store, redisClient, orch, hub)
	rawMux  := handler.Routes()

	// Compose middleware chain
	rateLimiter := api.NewRateLimiter(200, time.Minute)
	finalHandler := api.ChainMiddleware(
		rawMux,
		api.RequestIDMiddleware,
		api.RecoveryMiddleware,
		api.LoggingMiddleware,
		api.CORSMiddleware([]string{"*"}),
		rateLimiter.Middleware,
	)

	httpSrv := &http.Server{
		Addr:         httpAddr,
		Handler:      finalHandler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Info().Str("addr", httpAddr).Msg("HTTP server listening")
		if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("HTTP server error")
		}
	}()

	// ── gRPC server ───────────────────────────────────────────────
	grpcSrv := grpc.NewServer(
		grpc.UnaryInterceptor(api.UnaryInterceptor),
	)
	grpcServer := api.NewGRPCServer(store, redisClient, orch, hub)
	grpcServer.Register(grpcSrv)

	go func() {
		lis, err := net.Listen("tcp", grpcAddr)
		if err != nil {
			log.Fatal().Err(err).Str("addr", grpcAddr).Msg("gRPC listener failed")
		}
		log.Info().Str("addr", grpcAddr).Msg("gRPC server listening")
		if err := grpcSrv.Serve(lis); err != nil {
			log.Error().Err(err).Msg("gRPC server error")
		}
	}()

	// ── Graceful shutdown ─────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit

	log.Info().Str("signal", sig.String()).Msg("shutdown signal received")
	cancel() // stop all background goroutines

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	grpcSrv.GracefulStop()
	log.Info().Msg("gRPC server stopped")

	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("HTTP shutdown error")
	}
	log.Info().Msg("HTTP server stopped")

	log.Info().Msg("shutdown complete")
}

// ChainMiddleware is defined here to avoid circular imports
func init() {} // package init placeholder

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// logStartupBanner logs a structured startup summary.
func logStartupBanner(httpAddr, grpcAddr, metricsAddr string, workers, concurrency int) {
	fmt.Printf(`
┌────────────────────────────────────────────┐
│  Fluxor Orchestration Platform             │
│  Version: %-32s│
│  HTTP:    %-32s│
│  gRPC:    %-32s│
│  Metrics: %-32s│
│  Workers: %d × %d concurrency              │
└────────────────────────────────────────────┘
`, version, httpAddr, grpcAddr, metricsAddr, workers, concurrency)
}
