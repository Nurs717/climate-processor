package rest

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"climate-processor/internal/auth"
	"climate-processor/internal/domain"

	"github.com/gin-gonic/gin"
)

// limits are the rate limits of the API for its two kinds of clients.
//
// An anonymous client has the demo limit, counted per IP address. A signed-in owner has the owner limit: it is
// higher and counted per user, so it does not depend on the network the owner is on. Who a client is becomes known
// only when its session is checked, and that is a database query which a flood of made-up tokens must not reach.
// So a request with a token first pays one token, per IP address, to the owner limit (admit), and pays its whole
// cost, per user, only after the session has proved valid (settle).
type limits struct {
	demo  Limiter // nil = anonymous clients are not limited
	owner Limiter // nil = every client has the demo limit
	now   func() time.Time
}

// admit is the first limit of a request: the one in front of the session check.
func (l limits) admit(cost int) gin.HandlerFunc {
	return func(c *gin.Context) {
		switch {
		case l.owner != nil && hasToken(c.Request):
			charge(c, l.owner, l.now(), "ip:"+c.ClientIP(), 1)
		case l.demo != nil:
			charge(c, l.demo, l.now(), c.ClientIP(), cost)
		}
	}
}

// settle is the second limit of a request: the one after the session check. An anonymous client has paid in full
// by now; an owner pays the cost of the request to the owner limit.
func (l limits) settle(cost int) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := identityOf(c)
		if l.owner == nil || id == auth.Anonymous {
			return
		}
		switch {
		case id.Role == domain.RoleOwner:
			charge(c, l.owner, l.now(), "user:"+strconv.FormatInt(id.UserID, 10), cost)
		case l.demo != nil:
			// A role without a limit of its own gets the strictest one.
			charge(c, l.demo, l.now(), c.ClientIP(), cost)
		}
	}
}

// page serves the files of the web app to the requests that no route of the API has taken. Opening the page takes a
// handful of files, so they have a budget of their own, per IP address: it must not eat the one of the API. Anything
// that is not a file of the app falls through to the handlers after this one, which answer 404.
func (l limits) page(ui UI) gin.HandlerFunc {
	if ui == nil {
		return func(*gin.Context) {}
	}
	return func(c *gin.Context) {
		r := c.Request
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) ||
			strings.HasPrefix(r.URL.Path, "/api/") || // an API error stays JSON, whatever the app holds
			!ui.Has(r.URL.Path) {
			return
		}
		if l.demo != nil {
			charge(c, l.demo, l.now(), "static:"+c.ClientIP(), 1)
		}
		if !c.IsAborted() {
			// Gin starts the answer to an unknown route as a 404, and keeps that status for a handler that writes a
			// body, or nothing (HEAD), without setting one. The app may still choose another status.
			c.Status(http.StatusOK)
			ui.ServeHTTP(c.Writer, r)
		}
		c.Abort()
	}
}

// hasToken tells whether the request carries a session token, valid or not.
func hasToken(r *http.Request) bool {
	token, _ := tokenOf(r)
	return token != ""
}
