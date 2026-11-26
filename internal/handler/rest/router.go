// Package rest serves the JSON API of the climate service (gin).
package rest

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"climate-processor/internal/apperr"
	"climate-processor/internal/domain"
	"climate-processor/internal/ratelimit"
	"climate-processor/internal/service"

	"github.com/gin-gonic/gin"
)

// Reader is what the REST layer needs from the service layer.
type Reader interface {
	Latest() (service.LatestReading, bool)
	History(ctx context.Context, from, to time.Time, step time.Duration) ([]domain.Bucket, error)
	Summary(ctx context.Context, from, to time.Time) (domain.Summary, error)
}

// Limiter decides whether a client may make a request that costs the given number of tokens. When it says
// no, retryAfter is how long the client should wait.
type Limiter interface {
	Allow(key string, cost int, now time.Time) (ok bool, retryAfter time.Duration)
}

// The implementations must satisfy the interfaces; checked at compile time.
var (
	_ Reader  = (*service.ClimateService)(nil)
	_ Limiter = (*ratelimit.Limiter)(nil)
)

// Options are the optional settings of the router.
type Options struct {
	Now func() time.Time // clock, replaced in tests (default time.Now)
	// Limiter limits the requests of one client, told apart by IP address. nil = no limit.
	Limiter Limiter
	// TrustedProxies are the IPs or CIDR ranges of the reverse proxies in front of the service. Their
	// X-Forwarded-For header is believed (that is how the real client address is found); from any other
	// peer it is ignored. Empty = no proxy is trusted, the client is the peer.
	TrustedProxies []string
}

// NewRouter builds the HTTP handler of the API: GET /api/v1/latest, /api/v1/readings and /api/v1/stats.
// Request ids, access logging and panic recovery are installed in this order, so that a panic is still
// logged as a finished request with status 500. Every route then passes the rate limiter (if there is one),
// and a refused request is logged like any other. It fails only when TrustedProxies holds an invalid entry.
func NewRouter(log *slog.Logger, reader Reader, opts Options) (http.Handler, error) {
	if log == nil {
		log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	a := &api{reader: reader, now: opts.Now}

	// Release mode stops gin from printing its own debug output: everything is logged through slog.
	gin.SetMode(gin.ReleaseMode)
	r := gin.New() // not gin.Default(): its logger and recovery write unstructured text
	// Gin trusts every proxy unless told otherwise: always set the list explicitly (nil = trust none).
	if err := r.SetTrustedProxies(opts.TrustedProxies); err != nil {
		return nil, fmt.Errorf("trusted proxies: %w", err)
	}
	r.Use(requestID(), accessLog(log), recovery(log))
	limit := func(cost int) gin.HandlerFunc { return rateLimit(opts.Limiter, opts.Now, cost) }
	r.NoRoute(limit(costCheap), wrap(func(*gin.Context) error {
		return apperr.New(apperr.CodeNotFound, "rest.NoRoute", "not found")
	}))

	v1 := r.Group("/api/v1", noStore())
	v1.GET("/latest", limit(costCheap), wrap(a.latest))
	v1.GET("/readings", limit(costHistory), wrap(a.readings))
	v1.GET("/stats", limit(costHistory), wrap(a.stats))
	return r, nil
}
