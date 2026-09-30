package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/a-matson/workflow-orchestrator/backend/internal/api"
	"github.com/a-matson/workflow-orchestrator/backend/internal/egress"
	"github.com/a-matson/workflow-orchestrator/backend/internal/metrics"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/internal/scheduler"
	"github.com/a-matson/workflow-orchestrator/backend/internal/storage"
	"github.com/a-matson/workflow-orchestrator/backend/internal/worker"
	"github.com/a-matson/workflow-orchestrator/backend/migrations"
)

var (
	version   = "dev"
	buildTime = "unknown"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "apikey" {
		os.Exit(runAPIKey(os.Args[2:], os.Stdout, os.Stderr))
	}

	// ── Logging ──────────────────────────────────────────────────
	logLevel := zerolog.InfoLevel
	if getEnv("LOG_LEVEL", "info") == "debug" {
		logLevel = zerolog.DebugLevel
	}
	// JSON by default so log shippers can parse request_id; console is for local dev only.
	if getEnv("LOG_FORMAT", "json") == "console" {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
	}
	zerolog.SetGlobalLevel(logLevel)

	log.Info().
		Str("version", version).
		Str("built", buildTime).
		Msg("starting Fluxor Workflow Orchestration Platform")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Configuration
	postgresURL := getEnv("POSTGRES_URL", defaultPostgresURL)
	redisAddr := getEnv("REDIS_ADDR", "localhost:6379")
	redisPassword := getEnv("REDIS_PASSWORD", "")
	httpAddr := getEnv("HTTP_ADDR", ":8080")
	metricsAddr := getEnv("METRICS_ADDR", ":9091")
	workerCount := getEnvInt("WORKER_COUNT", 3)
	workerConc := getEnvInt("WORKER_CONCURRENCY", 5)
	// MinIO / artifact storage
	minioEndpoint := getEnv("MINIO_ENDPOINT", "localhost:9000")
	minioAccessKey := getEnv("MINIO_ACCESS_KEY", "minioadmin")
	minioSecretKey := getEnv("MINIO_SECRET_KEY", "minioadmin")
	minioBucket := getEnv("MINIO_BUCKET", "fluxor-artifacts")
	minioSSL := getEnv("MINIO_USE_SSL", "false") == "true"

	// Redis
	log.Info().Str("addr", redisAddr).Msg("connecting to Redis")
	redisClient := persistence.NewRedisClient(redisAddr, redisPassword, 0)
	if err := redisClient.Ping(ctx); err != nil {
		log.Fatal().Err(err).Msg("Redis connection failed")
	}
	log.Info().Msg("Redis connected")
	// Managed Redis often disables CONFIG, so an unreadable policy is not fatal.
	if policy, err := redisClient.EvictionPolicy(ctx); err != nil {
		log.Warn().Err(err).Msg("could not verify Redis maxmemory-policy")
	} else if err := persistence.CheckEvictionPolicy(policy); err != nil {
		log.Fatal().Err(err).Msg("unsafe Redis configuration")
	}
	defer func() { _ = redisClient.Close() }()

	// MinIO (artifact storage)
	log.Info().Str("endpoint", minioEndpoint).Msg("connecting to MinIO")
	minioClient, err := storage.NewClient(ctx, minioEndpoint, minioAccessKey, minioSecretKey, minioBucket, minioSSL)
	if err != nil {
		log.Warn().Err(err).Msg("MinIO unavailable — artifact storage disabled")
		minioClient = nil
	}

	// PostgreSQL
	log.Info().Msg("connecting to PostgreSQL")
	store, err := persistence.NewStore(ctx, postgresURL)
	if err != nil {
		log.Fatal().Err(err).Msg("PostgreSQL connection failed")
	}
	log.Info().Msg("PostgreSQL connected")
	defer store.Close()

	if err := persistence.Migrate(ctx, store.Pool(), migrations.FS); err != nil {
		log.Fatal().Err(err).Msg("database migration failed")
	}
	if err := bootstrapAPIKeys(ctx, store); err != nil {
		log.Fatal().Err(err).Msg("API key bootstrap failed")
	}

	// Core services
	allowedOrigins := strings.Split(getEnv("FLUXOR_ALLOWED_ORIGINS", ""), ",")
	trustedProxies, err := api.ParseTrustedProxies(os.Getenv("FLUXOR_TRUSTED_PROXIES"))
	if err != nil {
		log.Fatal().Err(err).Msg("invalid FLUXOR_TRUSTED_PROXIES")
	}
	generalLimit, err := rateLimitFromEnv("FLUXOR_RATE_LIMIT_PER_MIN", 200)
	if err != nil {
		log.Fatal().Err(err).Msg("invalid rate limit")
	}
	loginLimit, err := rateLimitFromEnv("FLUXOR_LOGIN_RATE_LIMIT_PER_MIN", 10)
	if err != nil {
		log.Fatal().Err(err).Msg("invalid rate limit")
	}
	maxBody, err := maxBodyFromEnv("FLUXOR_MAX_BODY_BYTES", api.DefaultMaxBody)
	if err != nil {
		log.Fatal().Err(err).Msg("invalid request body limit")
	}
	hub := api.NewHub(
		api.WithAllowedOrigins(allowedOrigins),
		// Bounds how long a revoked key keeps an open /ws stream (README "Authentication").
		api.WithPrincipalRecheck(api.NewAuthenticator(store).StillValid, 30*time.Second),
	)
	go hub.Run()

	orch := orchestrator.NewOrchestrator(store, redisClient, hub)

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	orch.SetTaskDurationObserver(metrics.Register(reg, metrics.Sources{
		Snapshot:   orch.GetMetrics,
		QueueDepth: redisClient.QueueDepth,
		WSClients:  hub.ConnectedClients,
	}))
	go func() {
		log.Info().Str("addr", metricsAddr).Msg("Prometheus metrics endpoint")
		mux := http.NewServeMux()
		mux.Handle("/metrics", metrics.Handler(reg))
		srv := &http.Server{Addr: metricsAddr, Handler: mux, ReadTimeout: 5 * time.Second}
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("metrics server failed")
		}
	}()

	// Must finish before the result processor and the worker pool start below:
	// recovery moves every running task back to pending, which would pull a
	// live pickup out from under its worker.
	log.Info().Msg("running crash recovery...")
	if err := orch.RecoverInFlightExecutions(ctx); err != nil {
		log.Warn().Err(err).Msg("crash recovery encountered errors (non-fatal)")
	}

	// Background services
	resultProcessor := scheduler.NewResultProcessor(redisClient, orch)
	retryPoller := scheduler.NewRetryPoller(orch)
	egressGuard, err := egress.New(os.Getenv("FLUXOR_EGRESS_ALLOW"))
	if err != nil {
		log.Fatal().Err(err).Msg("FLUXOR_EGRESS_ALLOW is invalid")
	}
	workerPool, err := worker.NewPool(redisClient, workerCount, workerConc, orch, minioClient, worker.Workspace{
		Root:   os.Getenv("FLUXOR_WORKSPACE_ROOT"),
		Volume: os.Getenv("FLUXOR_WORKSPACE_VOLUME"),
	}, egressGuard)
	if err != nil {
		log.Fatal().Err(err).Msg("worker pool: task workspace setup failed")
	}
	orch.SetTaskCanceller(workerPool)

	go resultProcessor.Run(ctx)
	go retryPoller.Run(ctx)
	// Workers stop before everything else, so the result processor is still
	// consuming while they drain. Docker's stop timeout must exceed the grace
	// (compose sets stop_grace_period).
	workerCtx, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()
	workersDone := make(chan struct{})
	go func() {
		workerPool.Start(workerCtx, shutdownGrace)
		close(workersDone)
	}()

	// HTTP server
	session, err := sessionConfig()
	if err != nil {
		log.Fatal().Err(err).Msg("session configuration failed")
	}
	handler := api.NewHandler(store, redisClient, orch, hub, minioClient).WithSession(session).WithTrustedProxies(trustedProxies).WithRateLimits(generalLimit, loginLimit).WithMaxBody(maxBody)

	httpSrv := &http.Server{
		Addr:         httpAddr,
		Handler:      handler.Server(allowedOrigins),
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

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit

	log.Info().Str("signal", sig.String()).Msg("shutdown signal received")
	stopWorkers()
	<-workersDone
	cancel() // stop all background goroutines

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("HTTP shutdown error")
	}
	log.Info().Msg("HTTP server stopped")

	log.Info().Msg("shutdown complete")
}

// shutdownGrace is how long running tasks get to finish after SIGTERM.
const shutdownGrace = 25 * time.Second

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

// rateLimitFromEnv rejects zero, negative and non-numeric values: a limit that
// silently falls back to the default would hide a typo in a security setting.
func rateLimitFromEnv(key string, def int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s=%q: want a positive integer", key, v)
	}
	return n, nil
}

func maxBodyFromEnv(key string, def int64) (int64, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s=%q: want a positive integer", key, v)
	}
	return n, nil
}
