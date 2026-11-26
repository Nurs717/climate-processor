package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata" // embed the timezone database: the alpine runtime image has none

	"climate-processor/internal/access"
	"climate-processor/internal/auth"
	"climate-processor/internal/auth/telegram"
	"climate-processor/internal/config"
	"climate-processor/internal/handler"
	"climate-processor/internal/handler/rest"
	"climate-processor/internal/logger"
	"climate-processor/internal/metrics"
	"climate-processor/internal/ratelimit"
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
	repo, sessions, closeRepo, err := openRepository(ctx, log, cfg.DBPath)
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

	// Owner sign-in with Telegram: the sessions live in the database, the owners are the admins of the bot.
	owner, err := newOwnerAuth(ctx, log, cfg, sessions, accessStore)
	if err != nil {
		return err
	}
	if owner != nil {
		wg.Go(func() {
			owner.service.RunCleanup(ctx, sessionCleanupEvery)
		})
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

	// === 4. START THE HTTP SERVERS ===
	// Internal server: Prometheus metrics and the health check. It must never be exposed to the internet.
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	internalSrv := newHTTPServer(cfg.MetricsPort, mux)
	servers := []*http.Server{internalSrv}
	startHTTPServer(log, internalSrv, "/metrics, /healthz", stop)

	// REST API for the apps (the PWA, ...). An empty HTTP_ADDR disables it.
	if cfg.HTTPAddr != "" {
		apiSrv, err := newAPIServer(log, cfg, climateService, owner)
		if err != nil {
			return err
		}
		servers = append(servers, apiSrv)
		startHTTPServer(log, apiSrv, "/api/v1/*", stop)
	}

	log.Info("worker started, press Ctrl+C to exit")

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, srv := range servers {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Warn("HTTP server shutdown failed", "addr", srv.Addr, "error", err)
		}
	}
	waitTimeout(log, &wg, 5*time.Second)
	return nil
}

// maxTrackedClients bounds the memory of the rate limiter: it remembers at most this many client addresses.
const maxTrackedClients = 10_000

// sessionCleanupEvery is how often the sessions that have expired are deleted from the database.
const sessionCleanupEvery = time.Hour

// ownerAuth is the owner sign-in: the sessions of the owner and the Telegram flow that starts them.
type ownerAuth struct {
	service *auth.Service
	login   *telegram.Login
}

// newOwnerAuth sets up the owner sign-in. It returns nil, and every client stays anonymous with the read-only demo
// role, when the sign-in is not configured or is configured halfway: that deserves a warning, but not stopping the
// sensor's service. The sign-in needs PUBLIC_URL, the client id and secret of the bot's OpenID Connect settings and
// a database for the sessions (sessions is nil without one).
func newOwnerAuth(ctx context.Context, log *slog.Logger, cfg *config.Config, sessions auth.SessionStore, owners auth.Owners) (*ownerAuth, error) {
	if cfg.PublicURL == "" && cfg.TGLoginClientID == "" && cfg.TGLoginClientSecret == "" {
		log.Info("owner sign-in is off: PUBLIC_URL and TELEGRAM_OIDC_CLIENT_ID/SECRET are not set, everybody is a demo client")
		return nil, nil
	}
	var missing []string
	for _, setting := range []struct {
		name string
		set  bool
	}{
		{"PUBLIC_URL", cfg.PublicURL != ""},
		{"TELEGRAM_OIDC_CLIENT_ID", cfg.TGLoginClientID != ""},
		{"TELEGRAM_OIDC_CLIENT_SECRET", cfg.TGLoginClientSecret != ""},
		{"DB_PATH", sessions != nil},
	} {
		if !setting.set {
			missing = append(missing, setting.name)
		}
	}
	if len(missing) > 0 {
		log.Warn("owner sign-in is off: some of its settings are missing, everybody is a demo client", "missing", missing)
		return nil, nil
	}

	redirectURL := cfg.PublicURL + rest.TelegramCallbackPath
	provider, err := telegram.NewProvider(ctx, cfg.TGLoginClientID, cfg.TGLoginClientSecret, redirectURL)
	if err != nil {
		return nil, fmt.Errorf("telegram sign-in: %w", err)
	}
	if len(cfg.AdminChatIDs) == 0 {
		log.Warn("TELEGRAM_ADMIN_CHAT_IDS is empty: nobody can sign in as the owner")
	}
	// The redirect address is logged because it must match, character for character, the one registered in @BotFather.
	log.Info("owner sign-in is on", "redirect_uri", redirectURL, "session_ttl", cfg.SessionTTL.String())
	return &ownerAuth{
		service: auth.NewService(log.With("component", "auth"), sessions, owners, auth.Options{TTL: cfg.SessionTTL}),
		login:   telegram.NewLogin(log.With("component", "telegram-login"), provider, telegram.Options{}),
	}, nil
}

// newAPIServer builds the public REST API server: its trusted proxies (to find the real client address), the
// per-client rate limit and the owner sign-in (nil = off) come from the configuration.
func newAPIServer(log *slog.Logger, cfg *config.Config, reader rest.Reader, owner *ownerAuth) (*http.Server, error) {
	opts := rest.Options{TrustedProxies: cfg.TrustedProxies}
	if cfg.APIRatePerMin > 0 {
		limiter, err := ratelimit.New(cfg.APIRatePerMin, cfg.APIRateBurst, maxTrackedClients)
		if err != nil {
			return nil, fmt.Errorf("rate limiter: %w", err)
		}
		opts.Limiter = limiter // assigned only here: a nil *Limiter in the interface would not be "no limit"
	} else {
		log.Warn("API_RATE_PER_MIN is 0: the API is not rate limited")
	}
	if owner != nil {
		// Assigned only here, for the same reason as the limiter: a nil pointer in an interface is not "off".
		opts.Sessions = owner.service
		opts.SignIn = owner.login
		// Secure cookies, unless the service is on plain http (local development: PUBLIC_URL allows it only there).
		opts.SecureCookies = strings.HasPrefix(cfg.PublicURL, "https://")
	}
	log.Info("REST API settings",
		"trusted_proxies", cfg.TrustedProxies,
		"rate_per_minute", cfg.APIRatePerMin,
		"rate_burst", cfg.APIRateBurst,
		"owner_sign_in", owner != nil)

	router, err := rest.NewRouter(log.With("component", "rest"), reader, opts)
	if err != nil {
		return nil, fmt.Errorf("REST API: %w", err)
	}
	return newHTTPServer(cfg.HTTPAddr, router), nil
}

// newHTTPServer returns a server with timeouts that protect it from slow clients.
func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// startHTTPServer serves srv in the background. A failure other than a normal shutdown stops the whole service.
func startHTTPServer(log *slog.Logger, srv *http.Server, endpoints string, stop func()) {
	go func() {
		log.Info("HTTP server started", "addr", srv.Addr, "endpoints", endpoints)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP server failed", "addr", srv.Addr, "error", err)
			stop() // trigger a graceful shutdown
		}
	}()
}

// openRepository returns the reading storage, the session storage (the same database) and a function that closes
// them. Without a path readings are dropped (NopRepository) and there are no sessions (nil): nobody can sign in.
func openRepository(ctx context.Context, log *slog.Logger, path string) (service.ReadingRepository, auth.SessionStore, func() error, error) {
	if path == "" {
		log.Warn("DB_PATH is not set: readings are not stored")
		return service.NopRepository{}, nil, func() error { return nil }, nil
	}
	r, err := sqlite.New(ctx, path)
	if err != nil {
		return nil, nil, nil, err
	}
	log.Info("readings are stored", "path", path)
	return r, r, r.Close, nil
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
