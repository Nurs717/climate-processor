package rest

import (
	"log/slog"
	"net/http"
	"time"

	"climate-processor/internal/apperr"
	"climate-processor/internal/service"

	"github.com/gin-gonic/gin"
)

type api struct {
	log      *slog.Logger
	reader   Reader
	now      func() time.Time
	sessions Sessions // nil = every request is anonymous
	signIn   SignIn   // nil = no sign-in routes
	secure   bool     // mark the cookies Secure
}

// latest: GET /api/v1/latest
func (a *api) latest(c *gin.Context) error {
	l, ok := a.reader.Latest()
	if !ok {
		return apperr.New(apperr.CodeNotFound, "rest.latest", "no readings yet")
	}
	c.JSON(http.StatusOK, newLatest(l))
	return nil
}

// readings: GET /api/v1/readings?range=24h&step=5m (or from=&to=). The history is aggregated into windows
// of step; without step the server picks one that gives a few hundred points.
func (a *api) readings(c *gin.Context) error {
	const op = "rest.readings"
	q := c.Request.URL.Query()

	from, to, err := timeRange(q, a.now(), op)
	if err != nil {
		return err
	}
	step, err := parseStep(q, op)
	if err != nil {
		return err
	}
	if step == 0 {
		step = service.DefaultStep(to.Sub(from))
	}

	buckets, err := a.reader.History(c.Request.Context(), from, to, step)
	if err != nil {
		return err
	}
	c.JSON(http.StatusOK, newReadings(from, to, step, buckets))
	return nil
}

// stats: GET /api/v1/stats?range=24h (or from=&to=): average, minimum and maximum over the interval.
func (a *api) stats(c *gin.Context) error {
	const op = "rest.stats"
	from, to, err := timeRange(c.Request.URL.Query(), a.now(), op)
	if err != nil {
		return err
	}

	sum, err := a.reader.Summary(c.Request.Context(), from, to)
	if err != nil {
		return err
	}
	c.JSON(http.StatusOK, newStats(from, to, sum))
	return nil
}
