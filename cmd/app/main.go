package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata" // embed the timezone database: the alpine runtime image has none

	"climate-processor/internal/access"
	"climate-processor/internal/config"
	"climate-processor/internal/handler"
	"climate-processor/internal/logger"
	"climate-processor/internal/metrics"
	"climate-processor/internal/repository/sqlite"
	"climate-processor/internal/service"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	// A bootstrap logger covers the time until the configuration (LOG_LEVEL, LOG_FORMAT) is read.
	slog.SetDefault(logger.New(os.Stdout, slog.LevelInfo, logger.FormatJSON))

	cfg := config.Load()
	log := newLogger(cfg)
	slog.SetDefault(log)

	if err := run(log, cfg); err != nil {
		log.Error("service stopped with an error", "error", err)
		os.Exit(1)
	}
}

// newLogger builds the logger from LOG_LEVEL and LOG_FORMAT; an invalid level falls back to info.
func newLogger(cfg *config.Config) *slog.Logger {
	level, err := logger.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = slog.LevelInfo
	}
	log := logger.New(os.Stdout, level, cfg.LogFormat)
	if err != nil {
		log.Warn("using the default log level", "level", "info", "error", err)
	}
	return log
}

func run(log *slog.Logger, cfg *config.Config) error {
	log.Info("starting climate processor")

	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		log.Warn("unknown TIMEZONE, using UTC", "timezone", cfg.Timezone, "error", err)
		loc = time.UTC
	}

	// Canceled on Ctrl+C / SIGTERM (docker stop)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 1. Initialize metrics
	climateMetrics := metrics.NewClimateMetrics(prometheus.DefaultRegisterer)

	// 2. Reading history (optional)
	repo, closeRepo, err := openRepository(ctx, log, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	// Registered before the MQTT disconnect, so the database is closed after the last message is handled.
	defer func() {
		if err := closeRepo(); err != nil {
			log.Warn("closing the database failed", "error", err)
		}
	}()

	// 3. The service gets its dependencies through the constructor
	climateService := service.NewClimateService(log.With("component", "service"), climateMetrics, repo, service.Options{
		Location:   loc,
		StaleAfter: cfg.StaleAfter,
		DeviceID:   cfg.DeviceID,
	})

	mqttHandler, err := handler.NewMQTTHandler(log.With("component", "mqtt"), handler.MQTTConfig{
		BrokerURL:     cfg.BrokerURL,
		ClientID:      cfg.ClientID,
		Topic:         cfg.Topic,
		Username:      cfg.MQTTUsername,
		Password:      cfg.MQTTPassword,
		CAFile:        cfg.MQTTCAFile,
		TLSServerName: cfg.MQTTTLSServerName,
	}, climateService)
	if err != nil {
		return fmt.Errorf("MQTT setup: %w", err)
	}
	mqttHandler.Connect() // connects (and subscribes) in the background, retrying until the broker is up
	defer mqttHandler.Disconnect()

	var wg sync.WaitGroup

	// Who may read the data: admins from the config plus users approved in the bot (kept in ACCESS_FILE).
	accessStore, err := access.NewStore(cfg.AccessFile, cfg.AdminChatIDs)
	if err != nil {
		return fmt.Errorf("access list: %w", err)
	}
	if cfg.AccessFile == "" {
		log.Warn("ACCESS_FILE is not set: users approved in the bot are lost on restart")
	}

	// Initialize and start the Telegram bot in a goroutine
	notify := func(string) {}
	if tgHandler := handler.NewTGHandler(log.With("component", "telegram"), cfg.TGToken, accessStore, climateService); tgHandler != nil {
		notify = tgHandler.Notify
		wg.Go(func() {
			tgHandler.Start(ctx)
		})
		log.Info("Telegram bot worker started")
	}

	// Alert (in Telegram) when the sensor goes silent and when it comes back
	wg.Go(func() {
		climateService.WatchStaleness(ctx, 30*time.Second, notify)
	})

	// === 4. START THE HTTP SERVER (Prometheus metrics + health check) ===
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	srv := &http.Server{
		Addr:              cfg.MetricsPort,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		log.Info("HTTP server started", "addr", cfg.MetricsPort, "endpoints", "/metrics, /healthz")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP server failed", "error", err)
			stop() // trigger a graceful shutdown
		}
	}()

	log.Info("worker started, press Ctrl+C to exit")

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("HTTP server shutdown failed", "error", err)
	}
	waitTimeout(log, &wg, 5*time.Second)
	return nil
}

// openRepository returns the reading storage and a function that closes it.
// Without a path readings are dropped (NopRepository).
func openRepository(ctx context.Context, log *slog.Logger, path string) (service.ReadingRepository, func() error, error) {
	if path == "" {
		log.Warn("DB_PATH is not set: readings are not stored")
		return service.NopRepository{}, func() error { return nil }, nil
	}
	r, err := sqlite.New(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	log.Info("readings are stored", "path", path)
	return r, r.Close, nil
}

// waitTimeout waits for wg, but not longer than d.
func waitTimeout(log *slog.Logger, wg *sync.WaitGroup, d time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		log.Warn("timed out waiting for workers to stop")
	}
}
