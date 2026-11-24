package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata" // embed the timezone database: the alpine runtime image has none

	"climate-processor/internal/access"
	"climate-processor/internal/config"
	"climate-processor/internal/handler"
	"climate-processor/internal/metrics"
	"climate-processor/internal/service"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	log.Println("[INFO] Starting Go Climate Processor with TELEGRAM...")

	// Load configuration
	cfg := config.Load()

	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		log.Printf("[WARN] Unknown TIMEZONE %q (%v), using UTC", cfg.Timezone, err)
		loc = time.UTC
	}

	// Canceled on Ctrl+C / SIGTERM (docker stop)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 1. Initialize metrics
	climateMetrics := metrics.NewClimateMetrics(prometheus.DefaultRegisterer)

	// 2. Initialize the service and inject metrics into it
	climateService := service.NewClimateService(climateMetrics, service.Options{
		Location:   loc,
		StaleAfter: cfg.StaleAfter,
	})

	mqttHandler, err := handler.NewMQTTHandler(handler.MQTTConfig{
		BrokerURL:     cfg.BrokerURL,
		ClientID:      cfg.ClientID,
		Topic:         cfg.Topic,
		Username:      cfg.MQTTUsername,
		Password:      cfg.MQTTPassword,
		CAFile:        cfg.MQTTCAFile,
		TLSServerName: cfg.MQTTTLSServerName,
	}, climateService)
	if err != nil {
		log.Fatalf("[FATAL] MQTT setup error: %v", err)
	}
	mqttHandler.Connect() // connects (and subscribes) in the background, retrying until the broker is up
	defer mqttHandler.Disconnect()

	var wg sync.WaitGroup

	// Who may read the data: admins from the config plus users approved in the bot (kept in ACCESS_FILE).
	accessStore, err := access.NewStore(cfg.AccessFile, cfg.AdminChatIDs)
	if err != nil {
		log.Fatalf("[FATAL] Access list error: %v", err)
	}
	if cfg.AccessFile == "" {
		log.Println("[WARN] ACCESS_FILE is not set: users approved in the bot are lost on restart.")
	}

	// Initialize and start the Telegram bot in a goroutine
	notify := func(string) {}
	if tgHandler := handler.NewTGHandler(cfg.TGToken, accessStore, climateService); tgHandler != nil {
		notify = tgHandler.Notify
		wg.Go(func() {
			tgHandler.Start(ctx)
		})
		log.Println("[INFO] Telegram bot worker started successfully in the background.")
	}

	// Alert (in Telegram) when the sensor goes silent and when it comes back
	wg.Go(func() {
		climateService.WatchStaleness(ctx, 30*time.Second, notify)
	})

	// === 3. START THE HTTP SERVER (Prometheus metrics + health check) ===
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
		log.Printf("[INFO] HTTP server started on %s (/metrics, /healthz)\n", cfg.MetricsPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[ERROR] HTTP server failed: %v", err)
			stop() // trigger a graceful shutdown
		}
	}()

	log.Println("[INFO] Worker started successfully. Press Ctrl+C to exit.")

	<-ctx.Done()
	log.Println("[INFO] Shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[WARN] HTTP server shutdown: %v", err)
	}
	waitTimeout(&wg, 5*time.Second)
}

// waitTimeout waits for wg, but not longer than d.
func waitTimeout(wg *sync.WaitGroup, d time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		log.Println("[WARN] Timed out waiting for workers to stop")
	}
}
