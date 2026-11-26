// Package rest serves the JSON API of the climate service (gin).
package rest

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"climate-processor/internal/apperr"
	"climate-processor/internal/domain"
	"climate-processor/internal/service"

	"github.com/gin-gonic/gin"
)

// Reader is what the REST layer needs from the service layer.
type Reader interface {
	Latest() (service.LatestReading, bool)
	History(ctx context.Context, from, to time.Time, step time.Duration) ([]domain.Bucket, error)
	Summary(ctx context.Context, from, to time.Time) (domain.Summary, error)
}

// The service must satisfy the interface; checked at compile time.
var _ Reader = (*service.ClimateService)(nil)

// Options are the optional settings of the router.
type Options struct {
	Now func() time.Time // clock, replaced in tests (default time.Now)
}

// NewRouter builds the HTTP handler of the API: GET /api/v1/latest, /api/v1/readings and /api/v1/stats.
// Request ids, access logging and panic recovery are installed in this order, so that a panic is still
// logged as a finished request with status 500.
func NewRouter(log *slog.Logger, reader Reader, opts Options) http.Handler {
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
	// Until the service sits behind a trusted reverse proxy, the client address is the peer address.
	_ = r.SetTrustedProxies(nil)
	r.Use(requestID(), accessLog(log), recovery(log))
	r.NoRoute(wrap(func(*gin.Context) error {
		return apperr.New(apperr.CodeNotFound, "rest.NoRoute", "not found")
	}))

	v1 := r.Group("/api/v1", noStore())
	v1.GET("/latest", wrap(a.latest))
	v1.GET("/readings", wrap(a.readings))
	v1.GET("/stats", wrap(a.stats))
	return r
}
