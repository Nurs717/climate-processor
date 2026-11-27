// Package rest serves the JSON API of the climate service (gin) and, around it, the files of the web app.
package rest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"climate-processor/internal/apperr"
	"climate-processor/internal/auth"
	"climate-processor/internal/auth/telegram"
	"climate-processor/internal/domain"
	"climate-processor/internal/ratelimit"
	"climate-processor/internal/service"
	"climate-processor/internal/web"

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

// Sessions is what the REST layer needs from package auth: who a session token belongs to, and the sign-in
// and sign-out of the owner.
type Sessions interface {
	Authenticate(ctx context.Context, token string) (auth.Identity, error)
	Create(ctx context.Context, userID int64, userAgent string) (token string, session domain.Session, err error)
	Revoke(ctx context.Context, token string) error
}

// SignIn is the "Log In With Telegram" flow of package telegram.
type SignIn interface {
	// Begin starts a sign-in: the address to send the browser to, and the state to keep in the browser.
	Begin() (redirectURL, state string, err error)
	// Finish completes a sign-in that Telegram has sent back, and says which Telegram account it was.
	Finish(ctx context.Context, state, browserState, code string) (telegram.User, error)
	// FlowTTL is how long a started sign-in can be finished.
	FlowTTL() time.Duration
}

// UI serves the files of the web app. Has tells whether a URL path is one of them.
type UI interface {
	Has(path string) bool
	http.Handler
}

// The implementations must satisfy the interfaces; checked at compile time.
var (
	_ Reader   = (*service.ClimateService)(nil)
	_ Limiter  = (*ratelimit.Limiter)(nil)
	_ Sessions = (*auth.Service)(nil)
	_ SignIn   = (*telegram.Login)(nil)
	_ UI       = (*web.App)(nil)
)

// Options are the optional settings of the router.
type Options struct {
	Now func() time.Time // clock, replaced in tests (default time.Now)
	// Limiter limits the requests of one client, told apart by IP address. nil = no limit.
	Limiter Limiter
	// OwnerLimiter is the higher limit of the signed-in owner, counted per user and not per address. It also
	// takes one token per request of an IP address that sends a session token, which keeps a flood of made-up
	// tokens away from the session store. It needs Sessions. nil = the owner has the same limit as everybody.
	OwnerLimiter Limiter
	// UI serves the web app: its files answer the GET and HEAD requests that no API route takes. Every client has
	// a budget for them (in Limiter) that is separate from the one of the API. nil = there is no web app.
	UI UI
	// TrustedProxies are the IPs or CIDR ranges of the reverse proxies in front of the service. Their
	// X-Forwarded-For header is believed (that is how the real client address is found); from any other
	// peer it is ignored. Empty = no proxy is trusted, the client is the peer.
	TrustedProxies []string
	// Sessions tells the owner from everybody else by the session token of a request (a cookie, or an
	// "Authorization: Bearer" header for apps). nil = every request is anonymous, with the demo role.
	Sessions Sessions
	// SignIn serves the sign-in routes. It needs Sessions to hand out the session; nil = no sign-in routes.
	SignIn SignIn
	// SecureCookies makes the browser send the cookies only over HTTPS. Set it everywhere but in local
	// development over plain HTTP.
	SecureCookies bool
}

// NewRouter builds the HTTP handler of the API: GET /api/v1/latest, /api/v1/readings and /api/v1/stats, and
// under /api/v1/auth the identity of the client (me) and, when Options.SignIn is set, the Telegram sign-in
// (telegram/login, telegram/callback) and logout.
// Request ids, access logging and panic recovery are installed in this order, so that a panic is still
// logged as a finished request with status 500. Every route then passes the rate limiter (if there is one),
// and a refused request is logged like any other. The endpoints that depend on who the client is pass it twice,
// around the identity check (see limits); the sign-in routes always have the plain limit per IP address, whatever
// token they carry. It fails when TrustedProxies holds an invalid entry or when SignIn or OwnerLimiter is set
// without Sessions.
func NewRouter(log *slog.Logger, reader Reader, opts Options) (http.Handler, error) {
	if log == nil {
		log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.SignIn != nil && opts.Sessions == nil {
		return nil, errors.New("sign-in needs sessions")
	}
	if opts.OwnerLimiter != nil && opts.Sessions == nil {
		return nil, errors.New("the owner limit needs sessions")
	}
	a := &api{
		log:      log,
		reader:   reader,
		now:      opts.Now,
		sessions: opts.Sessions,
		signIn:   opts.SignIn,
		secure:   opts.SecureCookies,
	}

	// Release mode stops gin from printing its own debug output: everything is logged through slog.
	gin.SetMode(gin.ReleaseMode)
	r := gin.New() // not gin.Default(): its logger and recovery write unstructured text
	// Gin trusts every proxy unless told otherwise: always set the list explicitly (nil = trust none).
	if err := r.SetTrustedProxies(opts.TrustedProxies); err != nil {
		return nil, fmt.Errorf("trusted proxies: %w", err)
	}
	r.Use(requestID(), accessLog(log), recovery(log))
	limit := func(cost int) gin.HandlerFunc { return rateLimit(opts.Limiter, opts.Now, cost) }
	lim := limits{demo: opts.Limiter, owner: opts.OwnerLimiter, now: opts.Now}
	r.NoRoute(lim.page(opts.UI), limit(costCheap), wrap(func(*gin.Context) error {
		return apperr.New(apperr.CodeNotFound, "rest.NoRoute", "not found")
	}))

	v1 := r.Group("/api/v1", noStore())
	// Every endpoint passes a rate limit before the identity check, so that a flood of requests with made-up tokens
	// is stopped before it can reach the session store, and another one after it, which charges the owner by user.
	route := func(cost int, h handlerFunc) []gin.HandlerFunc {
		return []gin.HandlerFunc{lim.admit(cost), a.identify, lim.settle(cost), wrap(h)}
	}
	v1.GET("/latest", route(costCheap, a.latest)...)
	v1.GET("/readings", route(costHistory, a.readings)...)
	v1.GET("/stats", route(costHistory, a.stats)...)
	v1.GET("/auth/me", route(costCheap, a.me)...)
	if opts.SignIn != nil {
		// These routes read the token themselves (or none at all): a stale cookie must not stop a sign-in or a sign-out.
		v1.GET("/auth/telegram/login", limit(costSignIn), wrap(a.telegramLogin))
		v1.GET("/auth/telegram/callback", limit(costSignIn), wrap(a.telegramCallback))
		v1.POST("/auth/logout", limit(costCheap), wrap(a.logout))
	}
	return r, nil
}
